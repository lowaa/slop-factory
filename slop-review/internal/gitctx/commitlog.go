package gitctx

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// CommitLogMaxBytes bounds the commit log carried in every session's
// prompt: the messages say what the author meant, but a long-lived branch
// must not crowd out the diff.
const CommitLogMaxBytes = 8 * 1024

// commitSep and the record format below delimit commits unambiguously:
// a message may contain any text, including blank lines.
const commitSep = "\x1e"

// CommitLog renders the commit messages from baseRef to HEAD, oldest
// first, merge commits dropped (their messages are bookkeeping, and a
// merge of main into the branch would list main's commits' noise).
//
// A log git can't produce is a warning, not an error. It happened for real
// in a sibling deployment: a background gc racing the read made git answer
// "Invalid revision range". The diff, which is what the review needs, was
// fine.
//
// When the log exceeds maxBytes the oldest commits are dropped, since the
// newest messages describe where the branch ended up, and a line says so.
func CommitLog(repoRoot, baseRef string, maxBytes int) (log, warning string) {
	out, err := runGit(repoRoot, "log", "--no-merges", "--reverse", "--format=%h %s%n%n%b"+commitSep, baseRef+"..HEAD")
	if err != nil {
		return "", fmt.Sprintf("commit log unavailable, so the reviewer sees the diff without it: %v", err)
	}

	var msgs []string
	for _, m := range strings.Split(out, commitSep) {
		if m = strings.TrimSpace(m); m != "" {
			msgs = append(msgs, m)
		}
	}

	if len(strings.Join(msgs, "\n\n")) <= maxBytes {
		return strings.Join(msgs, "\n\n"), ""
	}

	// Keep the newest messages that fit alongside the omission note, so the
	// cap holds for the text as rendered, not just for the messages.
	for first := 1; first < len(msgs); first++ {
		out := fmt.Sprintf("[%d older commit(s) omitted]\n\n%s", first, strings.Join(msgs[first:], "\n\n"))
		if len(out) <= maxBytes {
			return out, ""
		}
	}
	// Even the newest message alone is over the cap.
	note := fmt.Sprintf("[%d older commit(s) omitted]\n\n", len(msgs)-1)
	return note + truncateUTF8(msgs[len(msgs)-1], max(maxBytes-len(note)-len(truncationMark), 0)), ""
}

const truncationMark = " …"

func truncateUTF8(s string, n int) string {
	if len(s) <= n+len(truncationMark) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + truncationMark
}
