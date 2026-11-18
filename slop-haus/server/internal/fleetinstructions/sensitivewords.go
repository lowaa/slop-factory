package fleetinstructions

import (
	"regexp"
	"strings"
)

// Match finds the first word or phrase of a sensitive word list (one per
// line; blank lines and surrounding spaces ignored) that text contains as
// whole words, ignoring case. It returns the entry as the list spells it
// (trimmed). "prod" matches "the prod db" but not "product".
func Match(list, text string) (word string, ok bool) {
	for _, line := range strings.Split(list, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		if wholeWord(entry).MatchString(text) {
			return entry, true
		}
	}
	return "", false
}

// wholeWord matches entry bounded by anything that is not a letter, digit or
// underscore on both sides; runs of spaces inside a phrase match any run of
// whitespace.
func wholeWord(entry string) *regexp.Regexp {
	parts := strings.Fields(entry)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])` + strings.Join(parts, `\s+`) + `(?:$|[^\p{L}\p{N}_])`)
}
