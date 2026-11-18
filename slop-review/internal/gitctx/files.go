package gitctx

import (
	"path"
	"regexp"
	"strings"
)

// FileDiff is one file's slice of the scope's unified diff, plus what a
// reviewer (or the chunk planner) needs to know about it without reading
// the hunks: how big the change is, and whether it is worth a reviewer's
// attention at all.
type FileDiff struct {
	// Path is the post-change path, or the pre-change path for a deletion.
	Path string
	// OldPath is set only for a rename.
	OldPath string
	Added   int
	Deleted int
	// Noise marks a lockfile or generated file: still reviewable on
	// request, but never inlined or planned into a chunk, since on a big
	// change set these routinely outweigh the code that produced them.
	//
	// Generated status is only ever taken from the base revision (see
	// markGenerated), never from the change under review: otherwise the
	// change itself could label any file generated and take it out of
	// the review.
	Noise bool
	// Redacted marks a secret-shaped file whose content was replaced by a
	// placeholder (see RedactSecretDiff).
	Redacted bool
	// Block is the file's "diff --git" block, already redacted.
	Block string
}

// Size is what the file costs to show a reviewer, which is what budgets
// here are measured in.
func (f *FileDiff) Size() int {
	return len(f.Block)
}

// Reviewable reports whether a reviewer should be expected to look at f.
// Noise and redacted files are listed but not owed a review.
func (f *FileDiff) Reviewable() bool {
	return !f.Noise && !f.Redacted
}

// lockfileNames are dependency lockfiles: machine-written, frequently
// enormous, and not something a code reviewer can usefully judge line by
// line.
var lockfileNames = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"uv.lock":           true,
	"poetry.lock":       true,
	"Cargo.lock":        true,
	"Gemfile.lock":      true,
	"composer.lock":     true,
	"go.work.sum":       true,
}

// generatedMarkerRe is the convention Go uses to mark a generated file
// (https://go.dev/s/generatedcode), plus the "@generated" comment tag
// used across other ecosystems -- anchored as whole comment lines, so
// code that merely mentions either (this file, say) is not itself
// mistaken for generated output. POSIX ERE, for git grep.
const generatedMarkerRe = `^// Code generated .* DO NOT EDIT\.$|^[[:space:]]*(//|#|/?\*)[[:space:]]*@generated`

// diffGitHeaderRe is the fallback path source for a block with no
// "---"/"+++" lines (a binary file, a mode-only change, a redaction
// placeholder). Only used when the paths are unambiguous, i.e. identical.
var diffGitHeaderRe = regexp.MustCompile(`(?m)^diff --git a/(.+) b/(.+)$`)

// parseFileDiff builds a FileDiff from one "diff --git" block.
func parseFileDiff(block string) FileDiff {
	f := FileDiff{Block: block}

	oldPath := extractDiffPath(diffOldPathRe, block, "a/")
	newPath := extractDiffPath(diffNewPathRe, block, "b/")
	switch {
	case newPath != "":
		f.Path = newPath
		if oldPath != "" && oldPath != newPath {
			f.OldPath = oldPath
		}
	case oldPath != "":
		f.Path = oldPath
	default:
		f.Path = headerPath(block)
	}
	// A pure rename (100% similarity) has no ---/+++ lines at all, only
	// "rename from"/"rename to".
	if from, to := renameLine(block, "rename from "), renameLine(block, "rename to "); from != "" && to != "" {
		f.Path, f.OldPath = to, from
	}

	inHunks := false
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunks = true
			continue
		}
		if !inHunks {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			f.Added++
		case strings.HasPrefix(line, "-"):
			f.Deleted++
		}
	}

	f.Noise = lockfileNames[path.Base(f.Path)]
	return f
}

func headerPath(block string) string {
	m := diffGitHeaderRe.FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	if m[1] == m[2] {
		return m[2]
	}
	// Ambiguous when a path contains " b/"; the b-side is still the best
	// guess and only feeds a label.
	return gitUnquotePath(m[2])
}

func renameLine(block, prefix string) string {
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "@@") {
			return ""
		}
		if strings.HasPrefix(line, prefix) {
			return gitUnquotePath(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// splitFileDiffs parses a whole unified diff into per-file FileDiffs,
// redacting secret-shaped files the same way RedactSecretDiff does.
func splitFileDiffs(diff string, allowSecrets bool) ([]FileDiff, []string) {
	_, blocks := splitDiffPreambleAndBlocks(diff)
	files := make([]FileDiff, 0, len(blocks))
	var warnings []string
	for _, block := range blocks {
		redacted, secretPath, warning := redactBlock(block, allowSecrets)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		f := parseFileDiff(block)
		if secretPath != "" && redacted != block {
			f = FileDiff{Path: secretPath, Redacted: true, Block: redacted}
		}
		f.Block = redacted
		files = append(files, f)
	}
	return files, warnings
}

// markGenerated flags files that were already generated in the base
// revision: by a generated-code marker, or by the linguist-generated
// attribute GitHub also honours. Both are read from baseRef rather than
// the working tree, so the change under review can't mark its own files
// generated -- a newly added generated file is simply reviewed.
func markGenerated(toplevel, baseRef, scopePath string, files []FileDiff) error {
	if len(files) == 0 {
		return nil
	}
	generated, err := generatedByAttribute(toplevel, baseRef, files)
	if err != nil {
		return err
	}
	byMarker, err := generatedByMarker(toplevel, baseRef, scopePath)
	if err != nil {
		return err
	}
	for p := range byMarker {
		generated[p] = true
	}
	for i := range files {
		basePath := files[i].Path
		if files[i].OldPath != "" {
			basePath = files[i].OldPath
		}
		if generated[basePath] {
			files[i].Noise = true
		}
	}
	return nil
}

func generatedByAttribute(toplevel, baseRef string, files []FileDiff) (map[string]bool, error) {
	// --source needs git 2.40+ (see version.go); older git's own failure
	// here doesn't say why, so this is checked explicitly rather than left
	// for check-attr to reject below.
	if err := ensureCheckAttrSourceSupported(toplevel); err != nil {
		return nil, err
	}
	// Paths go in on stdin rather than as arguments: a big enough change
	// set would otherwise exceed the OS's argument-length limit. -z on
	// both sides, so no path needs quoting or unquoting.
	var stdin strings.Builder
	for i := range files {
		for _, p := range []string{files[i].Path, files[i].OldPath} {
			if p != "" {
				stdin.WriteString(p)
				stdin.WriteByte(0)
			}
		}
	}
	out, err := runGitWithStdin(toplevel, stdin.String(), "check-attr", "--source="+baseRef, "-z", "--stdin", "linguist-generated")
	if err != nil {
		return nil, err
	}
	// Output is <path> NUL <attribute> NUL <value> NUL, repeated.
	fields := strings.Split(out, "\x00")
	generated := map[string]bool{}
	for i := 0; i+2 < len(fields); i += 3 {
		if value := fields[i+2]; value == "set" || value == "true" {
			generated[fields[i]] = true
		}
	}
	return generated, nil
}

func generatedByMarker(toplevel, baseRef, scopePath string) (map[string]bool, error) {
	// Exit code 1 is git grep's "no matches".
	out, err := runGitAllowExitCode(toplevel, 1, withPathspec([]string{"grep", "-l", "-z", "-I", "-E", generatedMarkerRe, baseRef}, scopePath)...)
	if err != nil {
		return nil, err
	}
	generated := map[string]bool{}
	for _, entry := range strings.Split(out, "\x00") {
		// Each entry is "<baseRef>:<path>".
		if p, ok := strings.CutPrefix(entry, baseRef+":"); ok {
			generated[p] = true
		}
	}
	return generated, nil
}
