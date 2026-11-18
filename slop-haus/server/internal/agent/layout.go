package agent

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Layout is what an agent's workspace says it looks like on disk: its root
// and the git repos directly under it. Self-asserted by the agent's machine
// -- fit for showing a human, never for deciding what the agent may do.
type Layout struct {
	Root  LayoutEntry   `json:"root"`
	Repos []LayoutEntry `json:"repos"`
}

// LayoutEntry is one directory of the workspace. Path is relative to the
// workspace root: "." for the root itself, otherwise a single name. Origin
// is the remote with any credential removed, "" when there is none.
type LayoutEntry struct {
	Path   string `json:"path"`
	Git    bool   `json:"git"`
	Origin string `json:"origin"`
}

// ReportedLayout is an agent's latest report and when it arrived.
type ReportedLayout struct {
	Layout     Layout
	ReportedAt time.Time
}

// Limits on a report, generous for anything real: a workspace is a handful
// of repos and a remote a URL.
const (
	MaxLayoutRepos  = 50
	MaxLayoutPath   = 255
	MaxLayoutOrigin = 500
)

// ParseLayout reads the layout out of a hook payload's slop_haus object.
//
// Lenient like session.ParseDescription, for the same reason: the report is
// decoration on a hook whose real job is the agent loop, so anything
// malformed is dropped rather than rejected -- a Stop that failed would let
// the agent quit. Nil means the payload carried no usable layout, which is
// also what a workspace that has not updated sends.
//
// Nothing here trusts the sender to have stripped credentials from a
// remote: the workspace's script does, and this does it again, so a script
// that is out of date or wrong still cannot put a token in the database.
func ParseLayout(raw json.RawMessage) *Layout {
	if len(raw) == 0 {
		return nil
	}
	var doc struct {
		Layout *struct {
			Root  json.RawMessage   `json:"root"`
			Repos []json.RawMessage `json:"repos"`
		} `json:"layout"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Layout == nil {
		return nil
	}
	root, ok := parseEntry(doc.Layout.Root)
	if !ok || root.Path != "." {
		return nil
	}
	layout := &Layout{Root: root, Repos: []LayoutEntry{}}
	seen := map[string]bool{}
	for _, r := range doc.Layout.Repos {
		if len(layout.Repos) == MaxLayoutRepos {
			break
		}
		entry, ok := parseEntry(r)
		// Only the root's direct children, once each: a path that climbs
		// or nests names somewhere this was never meant to look.
		if !ok || entry.Path == "." || entry.Path == ".." || strings.ContainsAny(entry.Path, `/\`) || seen[entry.Path] {
			continue
		}
		seen[entry.Path] = true
		layout.Repos = append(layout.Repos, entry)
	}
	return layout
}

func parseEntry(raw json.RawMessage) (LayoutEntry, bool) {
	var e struct {
		Path   string `json:"path"`
		Git    bool   `json:"git"`
		Origin string `json:"origin"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return LayoutEntry{}, false
	}
	path := cleanText(e.Path, MaxLayoutPath)
	if path == "" {
		return LayoutEntry{}, false
	}
	origin := ""
	if e.Git {
		origin = cleanText(StripUserinfo(e.Origin), MaxLayoutOrigin)
	}
	return LayoutEntry{Path: path, Git: e.Git, Origin: origin}, true
}

// StripUserinfo removes everything from a git remote that could be a
// credential, and its scheme, so `https://user:token@host/a/b`,
// `ssh://git@host/a/b` and `git@host:a/b` leave only what names the
// repository.
//
// Everything up to the last `@` goes, not only the URL's userinfo: a
// password may hold an unescaped `/`, `?` or `#`, which would end the
// authority early and leave part of it behind. Over-stripping a path that holds an `@` is
// the safe way to be wrong. A query or fragment goes too. Keep in step with
// strip_userinfo in the workspace's describe-session.py.
func StripUserinfo(remote string) string {
	remote = strings.TrimSpace(remote)
	// Before the query is cut: a password may hold an unescaped `?` or `#`.
	if i := strings.LastIndex(remote, "@"); i >= 0 {
		remote = remote[i+1:]
	}
	if i := strings.IndexAny(remote, "?#"); i >= 0 {
		remote = remote[:i]
	}
	if scheme, rest, ok := strings.Cut(remote, "://"); ok && isScheme(scheme) {
		remote = rest
	}
	return remote
}

func isScheme(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("+-.", r) {
			return false
		}
	}
	return true
}

// cleanText strips control and format characters -- a terminal escape or a
// bidi override has no business in a path shown to a human -- and caps the
// result at max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > max {
		s = strings.TrimSpace(string(runes[:max]))
	}
	return s
}
