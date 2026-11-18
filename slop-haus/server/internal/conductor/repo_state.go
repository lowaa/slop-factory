package conductor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// HostReport is what a host says of itself with each poll.
type HostReport struct {
	Capabilities []string
	Version      string
}

// HostStatus is what a person looking at a remote control needs to know of its
// host.
type HostStatus struct {
	// Online is whether a host has polled within the host TTL.
	Online bool
	// Capabilities and Version are what the host that polled last said of
	// itself; empty for a host that predates the report.
	Capabilities []string
	Version      string
	// CurrentVersion is the build id of the host package the server ships now,
	// to tell a host that is behind; empty when the server ships none.
	CurrentVersion string
}

// Supports is whether the host reported the capability.
func (s HostStatus) Supports(capability string) bool {
	return slices.Contains(s.Capabilities, capability)
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// Limits on a repo state report. Sized so a report with every field full
// stays inside a transcript event's size.
const (
	MaxRepoPaths        = 200
	MaxRepoPathBytes    = 200
	MaxRepoCommits      = 20
	MaxRepoPushes       = 20
	MaxRepoSubjectBytes = 200
	MaxRepoRefBytes     = 255
	// maxRepoCount bounds the counters, which the host computes from git.
	maxRepoCount = 10_000_000
	// maxEventBodyBytes is the transcript's limit on one event's body.
	maxEventBodyBytes = 64 * 1024
)

// RepoState is where the working copy stands after a turn,
// as the host reports it.
type RepoState struct {
	// Branch is empty when HEAD is detached.
	Branch string `json:"branch"`
	Head   string `json:"head"`
	// Dirty says there are uncommitted changes (not counting what setup adds).
	Dirty      bool   `json:"dirty"`
	DirtyCount int    `json:"dirty_count"`
	Upstream   string `json:"upstream"`
	// Ahead and Behind are against the upstream; without one, Ahead is the
	// commits no remote branch holds and Behind is 0.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Commits and Pushes are what the turn made, newest first.
	Commits []RepoCommit `json:"commits"`
	Pushes  []Push       `json:"pushes"`
	// ChangedPaths are the paths the conversation has changed (at most
	// MaxRepoPaths of ChangedTotal), which the board compares with the scopes
	// agents hold.
	ChangedPaths []string `json:"changed_paths"`
	ChangedTotal int      `json:"changed_total"`
}

type RepoCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// Push is a push the conversation made: a remote-tracking branch moved by it.
type Push struct {
	// Ref is the remote branch as the host's remote names it ("origin/x").
	Ref string `json:"ref"`
	// From is empty for a branch the push created.
	From string `json:"from"`
	To   string `json:"to"`
}

func badText(s string, limit int) bool {
	if len(s) > limit || !utf8.ValidString(s) {
		return true
	}
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// Validate refuses a report the board must not store, and makes the lists
// non-nil so that they are arrays in what is stored.
func (r *RepoState) Validate() error {
	if badText(r.Branch, MaxRepoRefBytes) || badText(r.Upstream, MaxRepoRefBytes) {
		return invalid("repo state: branch and upstream are text of at most %d bytes", MaxRepoRefBytes)
	}
	if !commitPattern.MatchString(r.Head) {
		return invalid("repo state needs the head commit")
	}
	for _, n := range []int{r.DirtyCount, r.Ahead, r.Behind, r.ChangedTotal} {
		if n < 0 || n > maxRepoCount {
			return invalid("repo state: a count is out of range")
		}
	}
	if len(r.Commits) > MaxRepoCommits || len(r.Pushes) > MaxRepoPushes || len(r.ChangedPaths) > MaxRepoPaths {
		return invalid("repo state: at most %d commits, %d pushes and %d paths", MaxRepoCommits, MaxRepoPushes, MaxRepoPaths)
	}
	if r.ChangedTotal < len(r.ChangedPaths) {
		return invalid("repo state: changed_total is less than the paths listed")
	}
	for _, c := range r.Commits {
		if !commitPattern.MatchString(c.SHA) || badText(c.Subject, MaxRepoSubjectBytes) {
			return invalid("repo state: a commit needs a sha and a subject of at most %d bytes", MaxRepoSubjectBytes)
		}
	}
	for _, p := range r.Pushes {
		if p.Ref == "" || badText(p.Ref, MaxRepoRefBytes) || !commitPattern.MatchString(p.To) ||
			(p.From != "" && !commitPattern.MatchString(p.From)) {
			return invalid("repo state: a push needs a ref and the commit it moved to")
		}
	}
	for _, p := range r.ChangedPaths {
		if p == "" || badText(p, MaxRepoPathBytes) || strings.HasPrefix(p, "/") {
			return invalid("repo state: a changed path is a repo-relative path of at most %d bytes", MaxRepoPathBytes)
		}
	}
	if r.Commits == nil {
		r.Commits = []RepoCommit{}
	}
	if r.Pushes == nil {
		r.Pushes = []Push{}
	}
	if r.ChangedPaths == nil {
		r.ChangedPaths = []string{}
	}
	return nil
}

// encode is the repo state as the transcript event's body. Not HTML-escaped:
// the escapes would make a path with a "<" six times its size, past what the
// limits above allow for.
func (r *RepoState) encode() (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return "", fmt.Errorf("conductor: encode repo state: %w", err)
	}
	body := strings.TrimSuffix(buf.String(), "\n")
	if len(body) > maxEventBodyBytes {
		return "", invalid("repo state is larger than %d bytes", maxEventBodyBytes)
	}
	return body, nil
}
