package codehost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DiffStatus is what a change does to one file. Only the three that a patch
// alone describes are comparable; a rename, a copy and a mode-only change are
// told apart for callers that can reason about them, and anything else is
// DiffOther. None of those has a fingerprint made of it.
type DiffStatus string

const (
	DiffAdded    DiffStatus = "added"
	DiffModified DiffStatus = "modified"
	DiffRemoved  DiffStatus = "removed"
	DiffRenamed  DiffStatus = "renamed"
	DiffCopied   DiffStatus = "copied"
	// DiffModeChanged is a change of mode with no change of content.
	DiffModeChanged DiffStatus = "mode_changed"
	DiffOther       DiffStatus = "other"
)

// DescribedByPatch is whether a patch alone says what happened to the file.
func (s DiffStatus) DescribedByPatch() bool {
	return s == DiffAdded || s == DiffModified || s == DiffRemoved
}

// FileDiff is one file of a Diff, with its unified-diff patch as the host
// gives it. An empty Patch is a file the host gave no patch for (binary, or
// too large).
type FileDiff struct {
	Path   string
	Status DiffStatus
	Patch  string
	// PreviousPath is where a renamed or copied file came from, empty for
	// every other status.
	PreviousPath string
}

// Diff is what a change would bring to its base branch: its head against the
// merge base of the two.
type Diff struct {
	// Base is the branch the change targets on the host.
	Base string
	// BaseSHA is the tip of Base the diff was read against, MergeBaseSHA the
	// common ancestor it was taken from. They are equal exactly when the
	// head contains the tip, which is when the head's tree is the tip's tree
	// plus these files.
	BaseSHA, MergeBaseSHA string
	Files                 []FileDiff
}

// ContainsTip is whether the head had taken in the base's tip.
func (d *Diff) ContainsTip() bool {
	return d.BaseSHA != "" && d.BaseSHA == d.MergeBaseSHA
}

var (
	// ErrDiffUnsupported is a diff that cannot be compared safely: truncated
	// or oversized, a file with no patch, a rename, or a patch that does not
	// parse. Callers treat it as "not the same diff", never as "no diff".
	ErrDiffUnsupported = errors.New("codehost: the diff cannot be compared")
	// ErrDiffChanged is a diff read while its base moved under the reader, so
	// its pages do not add up to one diff. Read it again.
	ErrDiffChanged = errors.New("codehost: the diff changed while it was being read")
)

const noNewlineMarker = `\ No newline at end of file`

// ChangeDetectionStrategy decides what "the same change" means: it reduces a
// diff to a fingerprint, and two diffs are the same change when their
// fingerprints are equal. It fails closed (ErrDiffUnsupported) on a diff it
// cannot vouch for.
//
// Prefix names the strategy and its version. Every fingerprint starts with it,
// so a fingerprint recorded by one strategy can never be compared with one
// made by another: callers refuse a recorded fingerprint without the active
// strategy's Prefix.
type ChangeDetectionStrategy interface {
	Prefix() string
	Fingerprint(d *Diff) (string, error)
}

// LineNumberChangeDetectionStrategy fingerprints the files with their
// statuses and, per file, every hunk with its line numbers (@@ old and new
// start and count) and body lines. Only the trailing function-context text
// after the second @@ is dropped. A base change that touches the files only
// below or above the hunks without moving them leaves the fingerprint alone;
// one that shifts a hunk, or comes within its context, changes it and needs a
// fresh review. Keeping the line numbers is also what stops a hunk being
// applied at another place in the file that happens to have the same text.
//
// It fails closed on anything it cannot vouch for: no files, a repeated path,
// a status other than added, modified or removed, a file with no patch, or a
// patch whose hunks do not match their own counts. Mode and type changes
// that come with a content change are not visible in a patch and are not
// covered.
type LineNumberChangeDetectionStrategy struct{}

const lineNumberPrefix = "lines-v1:"

func (LineNumberChangeDetectionStrategy) Prefix() string { return lineNumberPrefix }

func (LineNumberChangeDetectionStrategy) Fingerprint(d *Diff) (string, error) {
	if d == nil || len(d.Files) == 0 {
		return "", fmt.Errorf("%w: it has no files", ErrDiffUnsupported)
	}
	files := append([]FileDiff(nil), d.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var canon strings.Builder
	for i := range files {
		f := &files[i]
		if f.Path == "" || (i > 0 && files[i-1].Path == f.Path) {
			return "", fmt.Errorf("%w: a file path is empty or repeated", ErrDiffUnsupported)
		}
		switch f.Status {
		case DiffAdded, DiffModified, DiffRemoved:
		default:
			return "", fmt.Errorf("%w: %s is %q, which a patch alone does not describe", ErrDiffUnsupported, f.Path, f.Status)
		}
		hunks, err := parseHunks(f.Patch)
		if err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrDiffUnsupported, f.Path, err)
		}
		canon.WriteString("F " + strconv.Quote(f.Path) + " " + string(f.Status) + "\n")
		for _, h := range hunks {
			fmt.Fprintf(&canon, "@@ -%d,%d +%d,%d\n", h.oldStart, h.oldN, h.newStart, h.newN)
			for _, line := range h.lines {
				canon.WriteString(line + "\n")
			}
		}
	}
	sum := sha256.Sum256([]byte(canon.String()))
	return lineNumberPrefix + hex.EncodeToString(sum[:]), nil
}

// ChangedLines is every line a unified patch adds or removes, without its
// + or - marker, in order. The patch is checked as a fingerprint checks it,
// so one that does not parse, or is empty (the host gave none), is an error
// wrapping ErrDiffUnsupported rather than "no lines changed".
func ChangedLines(patch string) ([]string, error) {
	hunks, err := parseHunks(patch)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiffUnsupported, err)
	}
	var out []string
	for _, h := range hunks {
		for _, line := range h.lines {
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				out = append(out, line[1:])
			}
		}
	}
	return out, nil
}

type hunk struct {
	oldStart, oldN, newStart, newN int
	lines                          []string
}

// parseHunks splits a unified patch into its hunks' lines, each checked
// against the counts its own @@ header gives. Lines are kept with their
// leading marker (space, + or -); "\ No newline" markers are kept too, as
// they are part of what changed.
func parseHunks(patch string) ([]hunk, error) {
	if patch == "" {
		return nil, errors.New("the host gave no patch (binary or oversized)")
	}
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	var hunks []hunk
	for i := 0; i < len(lines); {
		h, ok := parseHunkHeader(lines[i])
		if !ok {
			return nil, fmt.Errorf("expected a hunk header, got %q", truncate(lines[i]))
		}
		i++
		oldN, newN := h.oldN, h.newN
		var body []string
		for (oldN > 0 || newN > 0) || (i < len(lines) && lines[i] == noNewlineMarker) {
			if i >= len(lines) {
				return nil, errors.New("a hunk ends before its line counts are met")
			}
			line := lines[i]
			switch {
			case strings.HasPrefix(line, " "):
				oldN--
				newN--
			case strings.HasPrefix(line, "-"):
				oldN--
			case strings.HasPrefix(line, "+"):
				newN--
			case line == noNewlineMarker && len(body) > 0 && !strings.HasPrefix(body[len(body)-1], `\`):
			default:
				return nil, fmt.Errorf("unexpected line %q in a hunk", truncate(line))
			}
			if oldN < 0 || newN < 0 {
				return nil, errors.New("a hunk has more lines than its header counts")
			}
			body = append(body, line)
			i++
		}
		h.lines = body
		hunks = append(hunks, h)
	}
	if len(hunks) == 0 {
		return nil, errors.New("the patch has no hunks")
	}
	return hunks, nil
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@ ..." into its line numbers and
// counts; a missing count is 1. The text after the second @@ is ignored.
func parseHunkHeader(line string) (h hunk, ok bool) {
	rest, found := strings.CutPrefix(line, "@@ -")
	if !found {
		return h, false
	}
	spec, _, found := strings.Cut(rest, " @@")
	if !found {
		return h, false
	}
	oldSpec, newSpec, found := strings.Cut(spec, " +")
	if !found {
		return h, false
	}
	if h.oldStart, h.oldN, ok = rangeOf(oldSpec); !ok {
		return h, false
	}
	h.newStart, h.newN, ok = rangeOf(newSpec)
	return h, ok
}

// rangeOf is the start and line count of "start[,count]".
func rangeOf(spec string) (start, n int, ok bool) {
	startS, count, hasCount := strings.Cut(spec, ",")
	start, err := strconv.Atoi(startS)
	if err != nil || start < 0 || strconv.Itoa(start) != startS {
		return 0, 0, false
	}
	if !hasCount {
		return start, 1, true
	}
	n, err = strconv.Atoi(count)
	return start, n, err == nil && n >= 0 && strconv.Itoa(n) == count
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}
