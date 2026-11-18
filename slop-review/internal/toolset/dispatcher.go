// Package toolset is the review session's read-only navigation toolset:
// read_file, list_dir, grep, get_diff, git_show, git_log, git_blame. Every tool here
// only ever reads -- see CLAUDE.md's security note this design is built
// around: the whole point of slop-review is letting a model explore the
// repo unattended, so nothing in this package may mutate it.
package toolset

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/secrets"
)

const (
	toolReadFile = "read_file"
	toolListDir  = "list_dir"
	toolGrep     = "grep"
	toolGetDiff  = "get_diff"
	toolGitShow  = "git_show"
	toolGitLog   = "git_log"
	toolGitBlame = "git_blame"

	defaultGrepMaxResults = 200
	maxGrepMaxResults     = 500
	defaultLogMaxCount    = 50
	maxLogMaxCount        = 200
)

// Dispatcher implements the review session's toolset against repoRoot.
// AllowSecrets and the accumulated Warnings mirror the config decision
// described in internal/config: read_file/grep refuse secret-shaped paths
// by default, and record a warning (surfaced in the final review) on the
// occasions --allow-secrets lets one through instead.
//
// A Dispatcher is owned by one session and is not safe for concurrent use.
type Dispatcher struct {
	repoRoot     string
	outputCap    int
	allowSecrets bool
	warnings     []string

	diffs map[string]*gitctx.FileDiff
	// owned is the files this session is answerable for, in order;
	// shown is, per file, which lines of its diff the session has
	// actually been given -- inline in its prompt, or through get_diff.
	// Per line rather than per file, so fetching the first screenful of a
	// long diff doesn't count as having read all of it.
	owned []string
	shown map[string][]bool
}

type Options struct {
	RepoRoot       string
	OutputCapBytes int
	AllowSecrets   bool
	// Files is the whole change under review, which get_diff serves --
	// all of it, not just Owned, so a session reviewing one chunk can
	// still follow a call into another.
	Files []gitctx.FileDiff
	// Owned is the paths (a subset of Files) this session is responsible
	// for reviewing.
	Owned []string
}

func New(opts *Options) *Dispatcher {
	d := &Dispatcher{
		repoRoot:     opts.RepoRoot,
		outputCap:    opts.OutputCapBytes,
		allowSecrets: opts.AllowSecrets,
		diffs:        map[string]*gitctx.FileDiff{},
		owned:        opts.Owned,
		shown:        map[string][]bool{},
	}
	for i := range opts.Files {
		f := &opts.Files[i]
		d.diffs[f.Path] = f
		if f.OldPath != "" {
			d.diffs[f.OldPath] = f
		}
	}
	return d
}

// MarkSeen records that these files' diffs were shown to the model by
// some other route than get_diff -- i.e. inline in its prompt.
func (d *Dispatcher) MarkSeen(paths ...string) {
	for _, p := range paths {
		f, ok := d.diffs[p]
		if !ok {
			continue
		}
		lines := d.shownLines(f)
		for i := range lines {
			lines[i] = true
		}
	}
}

// shownLines returns f's per-line record, creating it on first use.
func (d *Dispatcher) shownLines(f *gitctx.FileDiff) []bool {
	lines, ok := d.shown[f.Path]
	if !ok {
		lines = make([]bool, len(diffLines(f.Block)))
		d.shown[f.Path] = lines
	}
	return lines
}

func (d *Dispatcher) fullyShown(f *gitctx.FileDiff) bool {
	lines, ok := d.shown[f.Path]
	if !ok {
		return false
	}
	for _, seen := range lines {
		if !seen {
			return false
		}
	}
	return true
}

func diffLines(block string) []string {
	return strings.Split(strings.TrimSuffix(block, "\n"), "\n")
}

// Warnings returns every secret-access warning recorded so far, plus one
// naming any owned file the session was never shown, for internal/session
// to fold into the final Review. The latter is what keeps a review that
// silently skipped part of a big change from reading as a clean approval.
func (d *Dispatcher) Warnings() []string {
	warnings := append([]string(nil), d.warnings...)
	if unseen := d.Unseen(); len(unseen) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d changed file(s) were never looked at by this review: %s", len(unseen), strings.Join(unseen, ", ")))
	}
	return warnings
}

// Unseen returns the owned, reviewable files whose diff the session was
// never shown in full.
func (d *Dispatcher) Unseen() []string {
	var unseen []string
	for _, p := range d.owned {
		f, ok := d.diffs[p]
		if !ok || !f.Reviewable() || d.fullyShown(f) {
			continue
		}
		unseen = append(unseen, p)
	}
	return unseen
}

func (d *Dispatcher) Definitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{
			Name:        toolReadFile,
			Description: "Read a file (or a line range of it) from the repository, relative to the repo root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string"},
					"start_line": map[string]any{"type": "integer", "description": "1-based, inclusive. Omit for start of file."},
					"end_line":   map[string]any{"type": "integer", "description": "1-based, inclusive. Omit for end of file."},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        toolListDir,
			Description: "List a directory's immediate contents, relative to the repo root. Directories are suffixed with \"/\".",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string", "description": "Omit or \".\" for the repo root."}},
			},
		},
		{
			Name:        toolGrep,
			Description: "Search file contents by regular expression across the repository (or a path glob within it).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern":     map[string]any{"type": "string", "description": "RE2 regular expression."},
					"path_glob":   map[string]any{"type": "string", "description": "Optional glob to restrict the search, matched via Go's filepath.Match (no recursive \"**\"): \"*.go\" matches that extension at any depth (matched against the basename), while a glob containing \"/\" (e.g. \"internal/toolset/*.go\") matches only that exact directory depth, not deeper subdirectories."},
					"max_results": map[string]any{"type": "integer", "description": fmt.Sprintf("Defaults to %d, capped at %d.", defaultGrepMaxResults, maxGrepMaxResults)},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        toolGetDiff,
			Description: "Show the diff for one changed file in the change under review (any file in the manifest), optionally a line range of it.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "The file's path as the manifest lists it."},
					"start_line": map[string]any{"type": "integer", "description": "1-based, inclusive line of the diff. Omit for the start."},
					"end_line":   map[string]any{"type": "integer", "description": "1-based, inclusive line of the diff. Omit for the end."},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        toolGitShow,
			Description: "Show a commit, tag, or <ref>:<path> blob via `git show`.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"ref": map[string]any{"type": "string"}},
				"required":   []string{"ref"},
			},
		},
		{
			Name:        toolGitLog,
			Description: "List commits via `git log --oneline`, optionally over a revision range and/or scoped to a path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"revision_range": map[string]any{"type": "string", "description": "e.g. \"abc123..HEAD\". Omit for HEAD's history."},
					"max_count":      map[string]any{"type": "integer", "description": fmt.Sprintf("Defaults to %d, capped at %d.", defaultLogMaxCount, maxLogMaxCount)},
					"path":           map[string]any{"type": "string", "description": "Restrict to commits touching this path."},
				},
			},
		},
		{
			Name:        toolGitBlame,
			Description: "Show `git blame` for a file, optionally restricted to a line range.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string"},
					"start_line": map[string]any{"type": "integer"},
					"end_line":   map[string]any{"type": "integer"},
				},
				"required": []string{"path"},
			},
		},
	}
}

// Dispatch runs tool name with argumentsJSON (the model's raw tool-call
// arguments) and returns the text to send back as that tool call's result.
// Errors -- bad arguments, a path escaping the repo, a refused secret read
// -- are returned as "error: ..." text rather than a Go error, since the
// model is the caller here and needs to see and adapt to them, not crash
// the session.
func (d *Dispatcher) Dispatch(name, argumentsJSON string) string {
	switch name {
	case toolReadFile:
		return d.readFile(argumentsJSON)
	case toolListDir:
		return d.listDir(argumentsJSON)
	case toolGrep:
		return d.grep(argumentsJSON)
	case toolGetDiff:
		return d.getDiff(argumentsJSON)
	case toolGitShow:
		return d.gitShow(argumentsJSON)
	case toolGitLog:
		return d.gitLog(argumentsJSON)
	case toolGitBlame:
		return d.gitBlame(argumentsJSON)
	default:
		return fmt.Sprintf("error: unknown tool %q", name)
	}
}

func (d *Dispatcher) readFile(argumentsJSON string) string {
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}
	if args.StartLine > 0 && args.EndLine > 0 && args.StartLine > args.EndLine {
		return fmt.Sprintf("error: start_line (%d) is after end_line (%d)", args.StartLine, args.EndLine)
	}

	resolved, err := d.resolvePath(args.Path)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	if secrets.LooksLikeSecret(args.Path) {
		if !d.allowSecrets {
			return fmt.Sprintf("error: refusing to read %q: matches a secret-shaped filename pattern (rerun with --allow-secrets to override)", args.Path)
		}
		d.warnings = append(d.warnings, fmt.Sprintf("read %q despite it matching a secret-shaped filename pattern (--allow-secrets was set)", args.Path))
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Sprintf("error: read %q: %v", args.Path, err)
	}

	text := string(content)
	if args.StartLine > 0 || args.EndLine > 0 {
		text = sliceLines(text, args.StartLine, args.EndLine)
	}
	return d.truncate(text)
}

func (d *Dispatcher) listDir(argumentsJSON string) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}

	resolved, err := d.resolvePath(args.Path)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Sprintf("error: list %q: %v", args.Path, err)
	}

	var b strings.Builder
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
		} else {
			fmt.Fprintf(&b, "%s\n", e.Name())
		}
	}
	if b.Len() == 0 {
		return "(empty directory)"
	}
	return d.truncate(b.String())
}

func (d *Dispatcher) grep(argumentsJSON string) string {
	var args struct {
		Pattern    string `json:"pattern"`
		PathGlob   string `json:"path_glob"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}
	if args.Pattern == "" {
		return "error: pattern is required"
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return fmt.Sprintf("error: invalid pattern: %v", err)
	}
	maxResults := args.MaxResults
	if maxResults <= 0 || maxResults > maxGrepMaxResults {
		maxResults = defaultGrepMaxResults
	}

	absRoot, err := filepath.Abs(d.repoRoot)
	if err != nil {
		return fmt.Sprintf("error: resolve repo root: %v", err)
	}

	var results []string
	secretHit := false
	_ = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		// Unlike read_file/list_dir (which go through resolvePath's
		// symlink-escape check), grep walks the tree directly -- skip
		// symlinks entirely rather than risk os.ReadFile following one to
		// a target outside the repo root (and past the secret-name filter
		// below, which only ever sees the symlink's own name).
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		if args.PathGlob != "" {
			if ok, _ := filepath.Match(args.PathGlob, rel); !ok {
				if ok, _ := filepath.Match(args.PathGlob, filepath.Base(rel)); !ok {
					return nil
				}
			}
		}
		if secrets.LooksLikeSecret(rel) {
			if !d.allowSecrets {
				return nil
			}
			secretHit = true
		}
		content, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(content) {
			return nil
		}
		for i, line := range strings.Split(string(content), "\n") {
			if len(results) >= maxResults {
				return filepath.SkipAll
			}
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", rel, i+1, line))
			}
		}
		return nil
	})

	if secretHit {
		d.warnings = append(d.warnings, "grep matched inside a secret-shaped file (--allow-secrets was set)")
	}
	if len(results) == 0 {
		return "no matches"
	}
	return d.truncate(strings.Join(results, "\n"))
}

// getDiff serves a changed file's diff out of the already-resolved (and
// already-redacted) scope, rather than re-running git: the model sees
// exactly the change every other session is reviewing, including
// uncommitted work, and redaction can't be bypassed by asking this way.
func (d *Dispatcher) getDiff(argumentsJSON string) string {
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}
	if args.StartLine > 0 && args.EndLine > 0 && args.StartLine > args.EndLine {
		return fmt.Sprintf("error: start_line (%d) is after end_line (%d)", args.StartLine, args.EndLine)
	}
	f, ok := d.diffs[args.Path]
	if !ok {
		return fmt.Sprintf("error: %q is not a changed file in this review; see the manifest for the list", args.Path)
	}

	lines := diffLines(f.Block)
	total := len(lines)
	start, end := max(args.StartLine, 1), args.EndLine
	if end <= 0 || end > total {
		end = total
	}
	if start > end {
		return fmt.Sprintf("error: start_line %d is past the end of the %d-line diff of %s", start, total, f.Path)
	}

	// Paged by whole lines rather than cut by d.truncate, so the result
	// says exactly which lines were delivered -- to the model, which can
	// ask for the rest, and to the coverage record, which must only count
	// what was actually shown.
	shown := d.shownLines(f)
	var b strings.Builder
	fmt.Fprintf(&b, "[diff of %s: lines %d-%d of %d]\n", f.Path, start, end, total)
	budget := d.outputCap - pagingNoteReserve
	last := start - 1
	for i := start; i <= end; i++ {
		line := lines[i-1]
		if b.Len()+len(line)+1 > budget {
			if i == start {
				// A single line longer than the whole budget (minified
				// output, say) can never be delivered in one piece; show
				// what fits and count it, or the file could never be
				// fully seen at all.
				b.WriteString(line[:max(budget-b.Len(), 0)])
				b.WriteString("\n")
				last = i
			}
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		last = i
	}
	for i := start; i <= last; i++ {
		shown[i-1] = true
	}
	if last < end {
		fmt.Fprintf(&b, "... [output capped after line %d of %d: call get_diff again with start_line=%d for the rest]", last, total, last+1)
	}
	return b.String()
}

// pagingNoteReserve keeps room under the output cap for get_diff's
// "call again" note.
const pagingNoteReserve = 160

func (d *Dispatcher) gitShow(argumentsJSON string) string {
	var args struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}

	// git's <rev>:<path> blob syntax shows one file's raw content directly
	// -- refuse it up front for a secret-shaped path, the same as
	// read_file, rather than relying on the diff-block redaction below
	// (which only applies to unified-diff output, not raw blob content).
	if path, ok := blobPath(args.Ref); ok && secrets.LooksLikeSecret(path) {
		if !d.allowSecrets {
			return fmt.Sprintf("error: refusing to show %q: matches a secret-shaped filename pattern (rerun with --allow-secrets to override)", path)
		}
		d.warnings = append(d.warnings, fmt.Sprintf("showed %q despite it matching a secret-shaped filename pattern (--allow-secrets was set)", path))
	}

	out, err := gitctx.Show(d.repoRoot, args.Ref)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	// A commit-ish ref renders as a unified diff spanning however many
	// files that commit touched -- apply the same per-file redaction
	// ResolveScope applies to the initial diff, so `git show` can't
	// surface a secret file's historical content either.
	redacted, warnings := gitctx.RedactSecretDiff(out, d.allowSecrets)
	d.warnings = append(d.warnings, warnings...)
	return d.truncate(redacted)
}

// blobPath extracts the path portion of git's <rev>:<path> blob syntax
// (e.g. "HEAD:.env" -> (".env", true)), splitting on the first ":" since
// ref names conventionally don't contain one. ok is false for a plain
// commit-ish ref.
func blobPath(ref string) (string, bool) {
	i := strings.Index(ref, ":")
	if i < 0 {
		return "", false
	}
	return ref[i+1:], true
}

func (d *Dispatcher) gitLog(argumentsJSON string) string {
	var args struct {
		RevisionRange string `json:"revision_range"`
		MaxCount      int    `json:"max_count"`
		Path          string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}
	maxCount := args.MaxCount
	if maxCount <= 0 || maxCount > maxLogMaxCount {
		maxCount = defaultLogMaxCount
	}
	out, err := gitctx.Log(d.repoRoot, args.RevisionRange, maxCount, args.Path)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return d.truncate(out)
}

func (d *Dispatcher) gitBlame(argumentsJSON string) string {
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}

	if secrets.LooksLikeSecret(args.Path) {
		if !d.allowSecrets {
			return fmt.Sprintf("error: refusing to blame %q: matches a secret-shaped filename pattern (rerun with --allow-secrets to override)", args.Path)
		}
		d.warnings = append(d.warnings, fmt.Sprintf("blamed %q despite it matching a secret-shaped filename pattern (--allow-secrets was set)", args.Path))
	}

	out, err := gitctx.Blame(d.repoRoot, args.Path, args.StartLine, args.EndLine)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return d.truncate(out)
}

// resolvePath joins relPath onto the repo root and rejects anything that
// escapes it -- either directly (a leading "../" chain) or via a symlink
// that resolves outside the root once followed.
func (d *Dispatcher) resolvePath(relPath string) (string, error) {
	if relPath == "" {
		relPath = "."
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q must be relative to the repo root", relPath)
	}

	absRoot, err := filepath.Abs(d.repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	joined := filepath.Clean(filepath.Join(absRoot, relPath))
	if joined != absRoot && !strings.HasPrefix(joined, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository root", relPath)
	}

	if real, err := filepath.EvalSymlinks(joined); err == nil {
		if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
			if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
				return "", fmt.Errorf("path %q resolves outside the repository root via a symlink", relPath)
			}
		}
	}

	return joined, nil
}

func sliceLines(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	// Defense in depth: readFile already rejects start > end up front, but
	// this stays safe against any other/future caller passing a bad range
	// instead of panicking on the slice below.
	if start > len(lines) || start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// truncate caps s at d.outputCap bytes total, note included -- not
// d.outputCap bytes of content plus however much the note itself adds on
// top, which would make outputCap a soft rather than actual bound.
func (d *Dispatcher) truncate(s string) string {
	if len(s) <= d.outputCap {
		return s
	}
	note := fmt.Sprintf("\n... [truncated: %d of %d bytes shown; ask for a narrower range]", d.outputCap, len(s))
	contentCap := d.outputCap - len(note)
	if contentCap < 0 {
		contentCap = 0
	}
	return s[:contentCap] + note
}
