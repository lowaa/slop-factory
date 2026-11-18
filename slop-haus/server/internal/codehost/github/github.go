// Package github is the GitHub implementation of codehost.Host.
package github

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// maxEcho bounds payload text repeated in an Event's Ignore reason, which
// ends up in the delivery's record.
const maxEcho = 100

// Host is github.com.
type Host struct{}

func New() *Host { return &Host{} }

var _ codehost.Host = (*Host)(nil)

const hostName = "github.com"

func (*Host) Name() string { return hostName }

// ParseRemote reads "github.com/{owner}/{repo}": exactly those three
// segments, since GitHub has no nested namespaces.
func (*Host) ParseRemote(remote string) (codehost.Repo, bool) {
	segs := strings.Split(remote, "/")
	if len(segs) != 3 || !strings.EqualFold(segs[0], hostName) || segs[1] == "" || segs[2] == "" {
		return codehost.Repo{}, false
	}
	return codehost.Repo{Owner: strings.ToLower(segs[1]), Name: strings.ToLower(segs[2])}, true
}

// ChangeURL is the PR's canonical link.
func (*Host) ChangeURL(r codehost.ChangeRef) string {
	return fmt.Sprintf("https://"+hostName+"/%s/%s/pull/%d", r.Owner, r.Repo, r.Number)
}

// ParseChangeURL reads a link to a GitHub PR -- https://github.com/{owner}/
// {repo}/pull/{n}, with anything after the number (/files, a fragment)
// ignored -- and reports false for anything else.
func (*Host) ParseChangeURL(raw string) (codehost.ChangeRef, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, hostName) || u.User != nil {
		return codehost.ChangeRef{}, false
	}
	segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(segs) < 4 || segs[0] == "" || segs[1] == "" || segs[2] != "pull" {
		return codehost.ChangeRef{}, false
	}
	n, err := strconv.ParseInt(segs[3], 10, 64)
	// Digits only: ParseInt would take "+7", which is not a PR number
	// GitHub ever links to.
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != segs[3] {
		return codehost.ChangeRef{}, false
	}
	return codehost.ChangeRef{Host: hostName, Owner: strings.ToLower(segs[0]), Repo: strings.ToLower(segs[1]), Number: n}, true
}

// The few fields of GitHub's webhook payloads this package reads. See
// https://docs.github.com/en/webhooks/webhook-events-and-payloads.

type user struct {
	// ID is the user's numeric id, which is what identifies them: a login
	// can be renamed and then taken by someone else.
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type pullRequest struct {
	HTMLURL  string `json:"html_url"`
	Merged   bool   `json:"merged"`
	MergedBy *user  `json:"merged_by"`
	// MergeCommitSHA is the merge commit once merged.
	MergeCommitSHA string `json:"merge_commit_sha"`
	Base           struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// reviewEvent is X-GitHub-Event: pull_request_review.
type reviewEvent struct {
	Action string `json:"action"`
	Review struct {
		// State is approved, changes_requested, commented or dismissed.
		State       string     `json:"state"`
		Body        string     `json:"body"`
		HTMLURL     string     `json:"html_url"`
		SubmittedAt *time.Time `json:"submitted_at"`
		User        *user      `json:"user"`
	} `json:"review"`
	PullRequest pullRequest `json:"pull_request"`
}

// pullRequestEvent is X-GitHub-Event: pull_request.
type pullRequestEvent struct {
	Action      string      `json:"action"`
	PullRequest pullRequest `json:"pull_request"`
}

func ignored(format string, args ...any) *codehost.Event {
	return &codehost.Event{Kind: codehost.EventIgnored, Ignore: fmt.Sprintf(format, args...)}
}

// ParseEvent understands X-GitHub-Event's pull_request_review and
// pull_request.
func (h *Host) ParseEvent(name string, body []byte) *codehost.Event {
	switch name {
	case "pull_request_review":
		return h.review(body)
	case "pull_request":
		return h.pullRequest(body)
	}
	return ignored("slop-haus does not act on %q events", name)
}

func (h *Host) review(body []byte) *codehost.Event {
	var ev reviewEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return ignored("malformed pull_request_review payload")
	}
	if ev.Action != "submitted" {
		return ignored("a review %s changes nothing", hookkit.Clean(ev.Action, maxEcho))
	}
	state := strings.ToLower(ev.Review.State)
	var verdict codehost.Verdict
	switch state {
	case "approved":
		verdict = codehost.VerdictApproved
	case "changes_requested":
		verdict = codehost.VerdictChangesRequested
	default:
		return ignored("a review that is %s changes nothing", hookkit.Clean(state, maxEcho))
	}
	if ev.Review.User == nil || ev.Review.SubmittedAt == nil {
		return ignored("the review names no reviewer or submission time")
	}
	ref, ok := h.ParseChangeURL(ev.PullRequest.HTMLURL)
	if !ok {
		return ignored("not a github.com pull request")
	}
	return &codehost.Event{
		Kind:        codehost.EventReview,
		Change:      ref,
		Actor:       codehost.Actor{ID: ev.Review.User.ID, Login: ev.Review.User.Login},
		Verdict:     verdict,
		SubmittedAt: *ev.Review.SubmittedAt,
		Body:        ev.Review.Body,
		URL:         ev.Review.HTMLURL,
	}
}

func (h *Host) pullRequest(body []byte) *codehost.Event {
	var ev pullRequestEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return ignored("malformed pull_request payload")
	}
	if ev.Action != "closed" || !ev.PullRequest.Merged {
		return ignored("only a merged pull request changes anything")
	}
	if ev.PullRequest.MergedBy == nil {
		return ignored("the merge names nobody who merged it")
	}
	ref, ok := h.ParseChangeURL(ev.PullRequest.HTMLURL)
	if !ok {
		return ignored("not a github.com pull request")
	}
	return &codehost.Event{
		Kind:     codehost.EventMerged,
		Change:   ref,
		Actor:    codehost.Actor{ID: ev.PullRequest.MergedBy.ID, Login: ev.PullRequest.MergedBy.Login},
		Base:     ev.PullRequest.Base.Ref,
		MergeSHA: ev.PullRequest.MergeCommitSHA,
	}
}
