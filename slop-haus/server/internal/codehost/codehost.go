// Package codehost is what slop-haus needs from the place a ticket's change
// is reviewed and merged -- today GitHub, later others -- behind interfaces,
// so the ticket rules, the webhook door and the merge gate never name a
// host. Code outside this package tree (and the wiring in cmd/api) relies
// on these interfaces only, never on codehost/github or codehost/githubapp;
// a test enforces it.
//
// The interfaces are the intersection of GitHub and GitLab, designed against
// both:
//
//	slop-haus says     GitHub                      GitLab
//	change             pull request                merge request
//	change link        {owner}/{repo}/pull/{n}     {group}/.../{project}/-/merge_requests/{iid}
//	remote             github.com/{owner}/{repo}   gitlab.com/{group}/.../{project}
//	owner              user or org (one segment)   namespace, possibly nested groups
//	number             PR number, per repo         MR iid, per project
//	review verdict     review state approved /     approval is a separate act; "request
//	                   changes_requested           changes" is a review state only on
//	                                               newer tiers -- so a Host reports the
//	                                               verdicts it has, and none is invented
//	merge              pull_request closed+merged  merge_request state "merged"
//	actor              numeric user id + login     numeric user id + username
//	webhook auth       HMAC of the body            a shared token header
//
// The operations on a change, which Changes carries (see below for why it is
// not part of Host):
//
//	slop-haus says     GitHub                      GitLab
//	open, draft        POST /pulls, draft:true     POST /merge_requests, title "Draft: ..."
//	open, ready        POST /pulls                 POST /merge_requests
//	mark ready         GraphQL                     PUT /merge_requests/{iid}, the title
//	                   markPullRequestReadyFor-    without its "Draft: " prefix
//	                   Review (REST cannot)
//	head SHA           pull.head.sha               merge_request.sha
//	checks             check runs + commit         the head pipeline + external commit
//	                   statuses of the head SHA    statuses of the head SHA
//	merge only if      PUT /pulls/{n}/merge        PUT /merge_requests/{iid}/merge
//	the head is SHA    {sha, merge_method:merge,   {sha, merge_commit_message}
//	                   commit_title}
//	head moved         409 (or 405 with a new head) 409
//	cannot merge       405 (conflict, blocked)     405 or 406
//
// Left out because only one host has it, or because nothing needs it yet:
// closing a change (add it with the ticket that closes changes), GitHub's
// auto-merge and merge queue, GitLab's merge-when-pipeline-succeeds, squash
// and rebase merges (slop-haus merges with a merge commit, as the repo's
// history does), reviewers and labels, and GitHub's check annotations.
//
// Changes is its own interface, not methods on Host. Host is pure: it
// parses text and holds no credential, so the webhook door and the ticket
// rules, which need only that, cannot act on a repository by accident.
// Changes calls the host's API, so each of its methods names the tenant it
// acts for (a Host acts for nobody) and can fail on the network or on a
// missing credential. A host registers both under one name in a Registry.
//
// Checks are one list whatever made them: GitHub's implementation merges
// check runs with commit statuses, since CI reports either (this repo's
// Woodpecker reports only statuses) and a reader of one kind would take
// "no checks" for green. See Checks for what the overall state means,
// in particular that no checks at all is not a pass.
//
// The Owner of a ChangeRef is the whole namespace path so a nested GitLab
// group fits. A host-specific notion stays out until a ticket needs it,
// with the fake implementing it first.
//
// Request authentication stays with the transport that receives the
// delivery (internal/githubhook): the Host only turns an authenticated
// payload into an Event.
package codehost

import "time"

// ChangeRef names one change on one host. Two refs are equal when they name
// the same change.
type ChangeRef struct {
	// Host is the Name of the Host that parsed it, which is how a Registry
	// finds that host's Changes for it.
	Host string
	// Owner is the namespace the repository lives in, lower-cased: hosts
	// treat names case-insensitively, and the link an agent reported may
	// not use the case a webhook does.
	Owner  string
	Repo   string
	Number int64
}

// Host is one code host.
type Host interface {
	// Name is the host's domain, as a repository remote starts
	// ("github.com").
	Name() string
	// ParseChangeURL reads a link to a change on this host, reporting false
	// for anything else.
	ParseChangeURL(raw string) (ChangeRef, bool)
	// ChangeURL is a change's canonical link.
	ChangeURL(ref ChangeRef) string
	// ParseRemote reads a repository remote as a manifest records it,
	// without scheme or .git: "github.com/acme/app" is {acme, app}; a
	// GitLab "gitlab.com/group/sub/app" is {group/sub, app}. It reports
	// false for a remote that is not on this host or not a repository.
	ParseRemote(remote string) (Repo, bool)
	// ParseEvent turns an authenticated webhook delivery -- the host's own
	// name for the event, and its body -- into what happened. It never
	// fails: a delivery that is not one slop-haus acts on, including a
	// malformed one, is an Event of kind EventIgnored with the reason.
	ParseEvent(name string, body []byte) *Event
}

// EventKind is what a delivery means to slop-haus.
type EventKind int

const (
	// EventIgnored changes nothing; Event.Ignore says why.
	EventIgnored EventKind = iota
	// EventReview is a person's verdict on a change.
	EventReview
	// EventMerged is a person having merged a change.
	EventMerged
)

// Verdict is a review's outcome, for the verdicts that move a ticket.
type Verdict int

const (
	VerdictApproved Verdict = iota + 1
	VerdictChangesRequested
)

// Actor is a person on the host. ID identifies them: a login can be
// renamed and then taken by someone else.
type Actor struct {
	ID    int64
	Login string
}

// Event is one delivery, understood. Which fields are set depends on Kind.
type Event struct {
	Kind EventKind
	// Ignore is why an EventIgnored does nothing. It may carry text from
	// the payload, already length-bounded.
	Ignore string

	Change ChangeRef
	// Actor is the reviewer of an EventReview, the merger of an
	// EventMerged.
	Actor Actor

	// Base is the branch an EventMerged change was merged into, as the
	// delivery names it; empty when the payload does not say.
	Base string
	// MergeSHA is the merge commit of an EventMerged, when the payload names
	// one.
	MergeSHA string

	// Verdict, SubmittedAt, Body and URL are an EventReview's: what the
	// reviewer said, when, and where to read it.
	Verdict     Verdict
	SubmittedAt time.Time
	Body        string
	URL         string
}

// InRepo is the repository the change is in.
func (r ChangeRef) InRepo() Repo { return Repo{Owner: r.Owner, Name: r.Repo} }
