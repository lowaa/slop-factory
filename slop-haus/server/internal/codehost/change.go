package codehost

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Changes is what slop-haus does to a change on a code host, on behalf of a
// tenant. An implementation gets its credential from Credentials, per
// operation; no caller holds a token.
//
// The merge gate is built from Checks and Merge: read the checks, require
// Checks.Passed, then Merge with the HeadSHA those checks were read for.
// Merge refuses if the head has moved since, so a push between the two
// cannot be merged on the strength of checks that never ran on it.
//
// What Merge pins is the head commit, not the checks on it: a check on the
// same commit that turns to failure between Checks and Merge is not seen by
// Merge, on any host. The gate narrows that by calling Checks immediately
// before Merge; closing it needs the host's own branch protection.
type Changes interface {
	// OpenChange opens a change from a pushed branch onto a base branch.
	OpenChange(ctx context.Context, tenant uuid.UUID, req *OpenRequest) (ChangeRef, error)
	// MarkReady takes a draft change out of draft. It is a no-op on a
	// change that is not a draft.
	MarkReady(ctx context.Context, tenant uuid.UUID, ref ChangeRef) error
	// Checks reads the change's checks on its head as of now.
	Checks(ctx context.Context, tenant uuid.UUID, ref ChangeRef) (*Checks, error)
	// BranchChecks reads the checks on the head of branch as of now, for a
	// branch that has no change: the checks of the commit it points at, with
	// Base empty. A branch the host does not have is an error wrapping
	// ErrChangeNotFound.
	BranchChecks(ctx context.Context, tenant uuid.UUID, repo Repo, branch string) (*Checks, error)
	// CompareBase compares the commit headSHA, which must be the change's
	// head, with the tip of the change's base branch as it is now, and says
	// which base that was: the host's answer at the time of the comparison,
	// so a retarget after Checks was read is seen.
	CompareBase(ctx context.Context, tenant uuid.UUID, ref ChangeRef, headSHA string) (*BaseComparison, error)

	// ChangeState reads whether the change is merged, into which branch and
	// by whom, as the host has it now. A change the host does not know is an
	// error wrapping ErrChangeNotFound.
	ChangeState(ctx context.Context, tenant uuid.UUID, ref ChangeRef) (*ChangeState, error)
	// BranchContains says whether the commit sha is in the history of branch
	// as it is now. A commit or branch the host does not have is an error
	// wrapping ErrChangeNotFound; any failure to answer is an error, never
	// "no".
	BranchContains(ctx context.Context, tenant uuid.UUID, repo Repo, branch, sha string) (bool, error)

	// Diff reads what the change's head headSHA, which must be the change's
	// head, would bring to the tip of its base branch as it is now. An error
	// wrapping ErrDiffUnsupported says the diff is too large, or holds
	// something a patch does not describe, for a host to hand over whole.
	Diff(ctx context.Context, tenant uuid.UUID, ref ChangeRef, headSHA string) (*Diff, error)
	// CompareCommits reads what the commit toSHA changes against fromSHA, in
	// the change's repository: the files of toSHA that differ from the merge
	// base of the two, which is fromSHA when fromSHA is an ancestor of toSHA.
	// The returned Diff's BaseSHA is fromSHA as the host resolved it and its
	// MergeBaseSHA that merge base; Base is empty. It fails as Diff does on a
	// comparison too large to hand over whole.
	CompareCommits(ctx context.Context, tenant uuid.UUID, ref ChangeRef, fromSHA, toSHA string) (*Diff, error)
	// CloseChange closes a change without merging it. It is idempotent: an
	// already closed change is success. A merged one is ErrAlreadyMerged, so a
	// caller never mistakes landed work for abandoned work.
	CloseChange(ctx context.Context, tenant uuid.UUID, ref ChangeRef) error
	// RetargetChange changes a change's base branch. It is idempotent: a
	// change already on base is success.
	RetargetChange(ctx context.Context, tenant uuid.UUID, ref ChangeRef, base string) error
	// RetitleChange changes a change's title. It is idempotent: a change
	// already so titled is success.
	RetitleChange(ctx context.Context, tenant uuid.UUID, ref ChangeRef, title string) error
	// Merge merges with a merge commit, only if the change's head is still
	// req.HeadSHA; otherwise it returns ErrHeadMoved and merges nothing.
	Merge(ctx context.Context, tenant uuid.UUID, req *MergeRequest) (*Merged, error)

	// DefaultBranchHead reads the head commit of the repository's default
	// branch as it is now. It is a network call: callers must not hold a
	// lock other work waits on across it.
	DefaultBranchHead(ctx context.Context, tenant uuid.UUID, repo Repo) (*BranchHead, error)

	// BranchHead reads the head commit of branch as it is now. A branch the
	// host does not have is an error wrapping ErrChangeNotFound. It is a
	// network call, like DefaultBranchHead.
	BranchHead(ctx context.Context, tenant uuid.UUID, repo Repo, branch string) (*BranchHead, error)

	// TreeOf reads the hash of the tree the commit sha has, so two commits can
	// be compared by their content rather than their history. sha is a full
	// commit hash; one the host does not have is an error wrapping
	// ErrChangeNotFound. It is a read-only network call.
	TreeOf(ctx context.Context, tenant uuid.UUID, repo Repo, sha string) (string, error)

	// CreateBranch creates branch at the commit sha. It is idempotent: a
	// branch already at sha is success, and one at another commit is a
	// *BranchConflictError naming both, which wraps ErrBranchConflict. It is
	// a network call: callers must not hold a lock other work waits on across
	// it.
	CreateBranch(ctx context.Context, tenant uuid.UUID, repo Repo, branch, sha string) error

	// DeleteBranch deletes branch. It is idempotent: a branch that is
	// already missing is success. Hosts close a change whose base branch is
	// deleted, so a caller deletes a branch that was a base only once no open
	// change targets it (retarget those first). It is a network call:
	// callers must not hold a lock other work waits on across it.
	DeleteBranch(ctx context.Context, tenant uuid.UUID, repo Repo, branch string) error

	// TakesInBranchPast says whether the commit or branch head contains a
	// commit of branch that is not in the history of since: for a ticket's
	// shadow, whether main moved into it after the ticket's base commit. since
	// must be a commit of branch. A head branched earlier than since, or from
	// since itself, takes in nothing past it. It is a network call, and any
	// failure to answer is an error, never "no": callers must not read an
	// error as a pass.
	TakesInBranchPast(ctx context.Context, tenant uuid.UUID, repo Repo, branch, since, head string) (bool, error)

	// CommitsAhead counts the commits of branch that are not in the history of
	// the commit since: zero for a branch still at since (or behind it). A
	// branch the host does not have is an error wrapping ErrChangeNotFound,
	// never zero. It is a network call, and any failure to answer is an error,
	// never "none": callers must not read an error as a pass.
	CommitsAhead(ctx context.Context, tenant uuid.UUID, repo Repo, since, branch string) (int, error)

	// CompareBranches counts the commits of branch that base lacks (Ahead) and
	// the commits of base that branch lacks (Behind). A branch the host does
	// not have is an error wrapping ErrChangeNotFound. It is a network call,
	// and any failure to answer is an error, never "in step".
	CompareBranches(ctx context.Context, tenant uuid.UUID, repo Repo, base, branch string) (*BranchComparison, error)

	// MergeBranch merges the branch from into the branch into with a merge
	// commit, on the host, without a change. A branch already containing from
	// is success with UpToDate set. A conflict changes nothing and is an error
	// wrapping ErrMergeConflict; a missing branch wraps ErrChangeNotFound. It
	// is a network call: callers must not hold a lock other work waits on
	// across it.
	MergeBranch(ctx context.Context, tenant uuid.UUID, repo Repo, into, from string) (*BranchMerge, error)

	// UpdateBranch merges the change's base branch into its head branch
	// with a merge commit, on the host, as the host's "update branch" does,
	// so the push triggers the head's checks like any other. The head must
	// still be req.HeadSHA, otherwise it returns ErrHeadMoved and changes
	// nothing. A head that already contains its base is success with
	// UpToDate set; a conflict changes nothing and wraps ErrMergeConflict.
	// A host that applies the update later than it accepts it may not show
	// the new head in time, which is ErrUpdatePending: the update may still
	// land, so read the head again rather than retrying blindly. It is a
	// network call: callers must not hold a lock other work waits on across
	// it.
	UpdateBranch(ctx context.Context, tenant uuid.UUID, req *UpdateBranchRequest) (*BranchUpdate, error)

	// EnsureBranch makes sure branch exists: it creates it at the commit sha
	// when missing, and a branch that exists is success wherever it is. It
	// never moves a branch. For a branch that gathers work after it is made
	// (an attempt's), where CreateBranch's "same commit or refuse" would
	// refuse every re-claim. Like CreateBranch it is a network call.
	EnsureBranch(ctx context.Context, tenant uuid.UUID, repo Repo, branch, sha string) error

	// ResolveRef turns a full or short commit hash, or a tag name, into the
	// full hash of the commit it names (an annotated tag is followed to its
	// commit). A branch name is refused with ErrRefIsBranch, because a base
	// must not move; a ref the host does not have wraps ErrChangeNotFound and
	// an ambiguous short hash wraps ErrRejected. It is a network call.
	ResolveRef(ctx context.Context, tenant uuid.UUID, repo Repo, ref string) (*ResolvedRef, error)

	// CreateTag creates the tag name at the commit sha. It is idempotent: a
	// tag already at sha is success, and one at another commit is a
	// *TagConflictError, which wraps ErrTagConflict. A tag is never moved or
	// deleted. It is a network call, like CreateBranch.
	CreateTag(ctx context.Context, tenant uuid.UUID, repo Repo, name, sha string) error
}

// RefKind says what a ref named when it was resolved.
type RefKind string

const (
	RefCommit RefKind = "commit"
	RefTag    RefKind = "tag"
)

// ResolvedRef is a ref as the host resolved it.
type ResolvedRef struct {
	// SHA is the full hash of the commit the ref names.
	SHA  string
	Kind RefKind
}

// TagConflictError is a tag that already exists at another commit than the
// one it was to be created at.
type TagConflictError struct {
	Tag string
	// Have is the commit the tag is at, Want the one it was asked to be at.
	Have, Want string
}

func (e *TagConflictError) Error() string {
	return fmt.Sprintf("codehost: tag %s is at %s, not %s", e.Tag, e.Have, e.Want)
}

func (e *TagConflictError) Unwrap() error { return ErrTagConflict }

// BranchConflictError is a branch that already exists at another commit than
// the one it was to be created at.
type BranchConflictError struct {
	Branch string
	// Have is the commit the branch is at, Want the one it was asked to be at.
	Have, Want string
}

func (e *BranchConflictError) Error() string {
	return fmt.Sprintf("codehost: branch %s is at %s, not %s", e.Branch, e.Have, e.Want)
}

func (e *BranchConflictError) Unwrap() error { return ErrBranchConflict }

// BranchHead is a branch and the commit at its head.
type BranchHead struct {
	Branch string
	SHA    string
}

// BranchComparison is one branch set against another.
type BranchComparison struct {
	// Ahead is the commits the branch has that the base lacks, Behind the
	// commits the base has that the branch lacks.
	Ahead, Behind int
}

// BranchMerge is MergeBranch's result.
type BranchMerge struct {
	// SHA is the merge commit, or the branch's unchanged head when UpToDate.
	SHA string
	// UpToDate is true when there was nothing to merge.
	UpToDate bool
}

// UpdateBranchRequest is a base-into-head update to make.
type UpdateBranchRequest struct {
	Change ChangeRef
	// HeadSHA is the head commit the update is for. It is required, as
	// MergeRequest's is.
	HeadSHA string
}

// BranchUpdate is UpdateBranch's result.
type BranchUpdate struct {
	// SHA is the head after the update: the new merge commit, or the
	// unchanged head when UpToDate.
	SHA string
	// UpToDate is true when the head already contained its base.
	UpToDate bool
}

// BaseComparison is a head commit set against the change's base branch.
type BaseComparison struct {
	// Base is the branch the change targets on the host, as the host said
	// when the comparison was made.
	Base string
	// ContainsTip is whether the head contains the tip of Base: nothing has
	// landed on it that the head has not taken in. A head that is behind
	// was tested against an older base than the one it will merge into.
	ContainsTip bool
}

// ChangeState is a change as the host has it now.
type ChangeState struct {
	Merged bool
	// Base is the branch the change targets, which for a merged change is the
	// one it was merged into.
	Base string
	// HeadSHA is the change's head commit.
	HeadSHA string
	// MergeSHA and MergedBy (the merger's login) are set when Merged.
	MergeSHA string
	MergedBy string
}

// OpenRequest is a change to open.
type OpenRequest struct {
	Repo Repo
	// Head is the pushed branch, Base the branch it merges into.
	Head, Base  string
	Title, Body string
	// Draft opens the change not ready for review.
	Draft bool
}

// MergeRequest is a merge to make.
type MergeRequest struct {
	Change ChangeRef
	// HeadSHA is the head commit the merge is for. It is required: an empty
	// one is refused rather than read as "whatever the head is".
	HeadSHA string
	// Subject is the merge commit's subject line, Body the rest of its
	// message; empty leaves the message to the host's default.
	Subject, Body string
}

// Merged is a merge that happened.
type Merged struct {
	// SHA is the merge commit.
	SHA string
}

var (
	// ErrChangeNotFound is a change, or its repository, the host does not
	// know, or the credential cannot see.
	ErrChangeNotFound = errors.New("codehost: change not found")
	// ErrHeadMoved is a merge refused because the change's head is no longer
	// the commit it was asked to merge.
	ErrHeadMoved = errors.New("codehost: the change's head has moved")
	// ErrNotMergeable is a merge the host refuses on the change's own
	// state: a conflict, a draft, a required check or review missing, or
	// already merged or closed.
	ErrNotMergeable = errors.New("codehost: the change cannot be merged")
	// ErrAlreadyMerged is a change that cannot be closed because it was merged.
	ErrAlreadyMerged = errors.New("codehost: the change is already merged")
	// ErrRejected is any other request the host refuses as invalid, such
	// as opening a change whose branch is not pushed or already has one.
	// The error's text says why, in the host's words.
	ErrRejected = errors.New("codehost: the host refused the request")
	// ErrBranchConflict is a branch that exists at a commit other than the
	// one it was to be created at.
	ErrBranchConflict = errors.New("codehost: the branch exists at another commit")
	// ErrRefIsBranch is a ref that names a branch where a commit or tag was
	// needed: a branch moves.
	ErrRefIsBranch = errors.New("codehost: the ref is a branch")
	// ErrTagConflict is a tag that exists at a commit other than the one it
	// was to be created at.
	ErrTagConflict = errors.New("codehost: the tag exists at another commit")
	// ErrMergeConflict is a branch merge the host refuses because the
	// branches change the same lines. Nothing was merged.
	ErrMergeConflict = errors.New("codehost: the branches conflict")
	// ErrUpdatePending is a branch update the host accepted but whose new
	// head was not visible yet.
	ErrUpdatePending = errors.New("codehost: the branch update has not landed yet")
	// ErrUnreachable is the host not answering.
	ErrUnreachable = errors.New("codehost: could not reach the host")
)

// DraftMarker is a host that can put a change back into draft. It is separate
// from Changes because not every host can: a caller that finds a host without
// it leaves the change as it is.
type DraftMarker interface {
	// MarkDraft puts a change back into draft. It is idempotent: a change
	// already a draft is success.
	MarkDraft(ctx context.Context, tenant uuid.UUID, ref ChangeRef) error
}
