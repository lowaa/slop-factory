// Package codehosttest is a second codehost.Host and codehost.Changes, for
// tests of the layers above the interfaces.
package codehosttest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// ParsedEvent is one ParseEvent call.
type ParsedEvent struct {
	Name string
	Body []byte
}

// Fake decides nothing: it answers each call with what the test configured
// and records what it was asked. Set the maps before use; the fake only
// reads them.
type Fake struct {
	Domain string
	// Changes is what ParseChangeURL answers, by link; any other link is
	// not this host's.
	Changes map[string]codehost.ChangeRef
	// Events is what ParseEvent answers, by event name; any other name is
	// an ignored event.
	Events map[string]*codehost.Event
	// Remotes is what ParseRemote answers, by remote; any other remote is
	// not this host's.
	Remotes map[string]codehost.Repo

	// ParseURLFunc, ChangeURLFunc and ParseRemoteFunc, when set, answer in
	// place of the maps above, for a test with more links than it wants to
	// list. See NewLinkHost.
	ParseURLFunc    func(raw string) (codehost.ChangeRef, bool)
	ChangeURLFunc   func(ref codehost.ChangeRef) string
	ParseRemoteFunc func(remote string) (codehost.Repo, bool)

	// Opened is what OpenChange answers; OpenErr, if set, fails it instead.
	Opened  codehost.ChangeRef
	OpenErr error
	// OpenedFor, when set, answers OpenChange in place of Opened, for a test
	// that opens several changes and needs each to have its own reference.
	OpenedFor func(req *codehost.OpenRequest) codehost.ChangeRef
	// ReadyErr fails MarkReady.
	ReadyErr error
	// DraftErr fails MarkDraft.
	DraftErr error
	// CloseErr fails CloseChange.
	CloseErr error
	// RetargetErr, RetitleErr and DeleteErr fail RetargetChange,
	// RetitleChange and DeleteBranch.
	RetargetErr, RetitleErr, DeleteErr error
	// CI, when set, decides when checks exist (see CI); ChecksOf and
	// BranchChecksOf then say what a finished pipeline reports, not whether
	// there is one. A change or branch with no pipeline has no checks.
	CI *CI
	// ChecksOf is what Checks answers, by change; a change not in it is
	// ErrChangeNotFound. ChecksErr, if set, fails every Checks instead.
	ChecksOf map[codehost.ChangeRef]*codehost.Checks
	// BranchChecksOf is what BranchChecks answers, by branch.
	BranchChecksOf map[BranchKey]*codehost.Checks
	ChecksErr      error
	// StateOf is what ChangeState answers, by change; a change not in it is
	// not on the host. StateErr, if set, fails every call instead.
	StateOf  map[codehost.ChangeRef]*codehost.ChangeState
	StateErr error
	// OnBranch is what BranchContains answers, by branch and commit; a pair
	// not in it is a commit the host does not know (ErrChangeNotFound), so a
	// commit known not to be on the branch is an entry set to false.
	// ContainsErr, if set,
	// fails every call instead.
	OnBranch    map[BranchCommit]bool
	ContainsErr error
	// CompareOf is what CompareBase answers, by change; a change not in it
	// is ErrChangeNotFound. CompareErr, if set, fails every call instead.
	CompareOf  map[codehost.ChangeRef]*codehost.BaseComparison
	CompareErr error

	// DiffOf is what Diff answers, by the head commit asked about; a head not
	// in it is ErrChangeNotFound. DiffErr, if set, fails every Diff instead.
	DiffOf  map[string]*codehost.Diff
	DiffErr error
	// CommitsOf is what CompareCommits answers, by CommitRange; a range not
	// in it is ErrChangeNotFound. CommitsErr, if set, fails every call
	// instead.
	CommitsOf  map[CommitRange]*codehost.Diff
	CommitsErr error
	// AfterChecks, when set, runs once each Checks call has answered, before
	// it returns: where a test makes the world change between the read and
	// whatever follows it, such as a push moving the head (set MergeErr to
	// ErrHeadMoved), without any timing.
	AfterChecks func()
	// HeadOf is what DefaultBranchHead answers, by repository; a repository
	// not in it is ErrChangeNotFound. HeadErr, if set, fails every call
	// instead.
	HeadOf  map[codehost.Repo]*codehost.BranchHead
	HeadErr error
	// BeforeHead, when set, runs at the start of each DefaultBranchHead
	// call, with no lock held: where a test holds the read at a barrier.
	BeforeHead func()
	// BranchAt is where each branch is, by repository and name. CreateBranch
	// puts a missing one there and answers like the host for one that
	// exists. BranchErr, if set, fails every call instead.
	BranchAt  map[BranchKey]string
	BranchErr error
	// BeforeBranch, when set, runs at the start of each CreateBranch call,
	// with no lock held: where a test holds the create at a barrier.
	BeforeBranch func()
	// RefOf is what ResolveRef answers, by repository and ref as asked; a ref
	// not in it is ErrChangeNotFound. A branch in BranchAt is refused as a
	// branch. RefErr, if set, fails every call instead.
	RefOf  map[RefKey]codehost.ResolvedRef
	RefErr error
	// TreeAt is the tree each commit has, by repository and commit; a commit
	// not in it is ErrChangeNotFound. TreeErr, if set, fails every TreeOf
	// instead.
	TreeAt  map[RefKey]string
	TreeErr error
	// TagAt is where each tag is. CreateTag puts a missing one there and
	// answers like the host for one that exists. TagErr, if set, fails every
	// CreateTag instead.
	TagAt  map[TagKey]string
	TagErr error
	// TookInMain is what TakesInBranchPast answers, by the head it was asked
	// about (a branch or a commit); a head not in it took in nothing.
	// TakesErr, if set, fails every call instead.
	TookInMain map[string]bool
	TakesErr   error
	// AheadOf is what CommitsAhead answers, by branch; a branch not in it is
	// not on the host. AheadErr, if set, fails every call instead.
	AheadOf  map[string]int
	AheadErr error
	// ComparisonOf is what CompareBranches answers, by the compared branch; a
	// branch not in it is not on the host. ComparisonErr, if set, fails every
	// call instead.
	ComparisonOf  map[BranchKey]*codehost.BranchComparison
	ComparisonErr error
	// BranchMerged is what a MergeBranch answers; BranchMergeErr, if set,
	// fails it instead -- ErrMergeConflict to play a conflict.
	BranchMerged   codehost.BranchMerge
	BranchMergeErr error
	// BranchUpdated is what an UpdateBranch answers; BranchUpdateErr, if set,
	// fails it instead -- ErrMergeConflict, ErrHeadMoved or ErrUpdatePending.
	BranchUpdated   codehost.BranchUpdate
	BranchUpdateErr error
	// MergedWith is what a Merge answers; MergeErr, if set, fails it
	// instead -- ErrHeadMoved to play a head that moved since the checks.
	MergedWith codehost.Merged
	MergeErr   error

	mu           sync.Mutex
	branchHeads  []BranchKey
	parsedEvents []ParsedEvent
	opens        []OpenCall
	readied      []OperationCall
	closed       []OperationCall
	drafted      []OperationCall
	retargets    []RetargetCall
	retitles     []RetitleCall
	deletes      []BranchKey
	checked      []OperationCall
	stateReads   []codehost.ChangeRef
	compared     []OperationCall

	diffed         []OperationCall
	commitCompares []CommitRange
	heads          []codehost.Repo
	tagCalls       []TagCall
	// branchCalls is every CreateBranch call.
	branchCalls []BranchCall
	merges      []OperationCall
	branchMerge []BranchMergeCall
	updates     []OperationCall
	aheadSince  []string
	takes       []TakesCall
}

// TakesCall is one TakesInBranchPast call.
type TakesCall struct {
	Tenant              uuid.UUID
	Repo                codehost.Repo
	Branch, Since, Head string
}

// BranchMergeCall is one MergeBranch call.
type BranchMergeCall struct {
	Tenant     uuid.UUID
	Repo       codehost.Repo
	Into, From string
}

// BranchKey names a branch of a repository.
type BranchKey struct {
	Repo   codehost.Repo
	Branch string
}

// RetargetCall is one RetargetChange call.
type RetargetCall struct {
	Tenant uuid.UUID
	Change codehost.ChangeRef
	Base   string
}

// RetitleCall is one RetitleChange call.
type RetitleCall struct {
	Tenant uuid.UUID
	Change codehost.ChangeRef
	Title  string
}

// RefKey names a ref in a repository.
type RefKey struct {
	Repo codehost.Repo
	Ref  string
}

// TagKey names a tag in a repository.
type TagKey struct {
	Repo codehost.Repo
	Tag  string
}

// TagCall is one CreateTag call.
type TagCall struct {
	Tenant uuid.UUID
	TagKey
	SHA string
}

// BranchCall is one CreateBranch or EnsureBranch call.
type BranchCall struct {
	Tenant uuid.UUID
	BranchKey
	SHA string
	// Ensure is true for an EnsureBranch call.
	Ensure bool
}

// OpenCall is one OpenChange call.
type OpenCall struct {
	Tenant  uuid.UUID
	Request codehost.OpenRequest
}

// OperationCall is one call to MarkReady, Checks, ContainsBase or Merge. For Merge,
// Merge holds what was asked.
type OperationCall struct {
	Tenant uuid.UUID
	Change codehost.ChangeRef
	Merge  codehost.MergeRequest
	// Update holds what an UpdateBranch call asked.
	Update codehost.UpdateBranchRequest
	// HeadSHA is the commit a CompareBase call was asked about.
	HeadSHA string
}

var (
	_ codehost.Host        = (*Fake)(nil)
	_ codehost.Changes     = (*Fake)(nil)
	_ codehost.DraftMarker = (*Fake)(nil)
)

func (f *Fake) Name() string { return f.Domain }

func (f *Fake) ParseChangeURL(raw string) (codehost.ChangeRef, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ParseURLFunc != nil {
		return f.ParseURLFunc(raw)
	}
	ref, ok := f.Changes[raw]
	return ref, ok
}

// ChangeURL is the link the test registered for ref, or "".
func (f *Fake) ChangeURL(ref codehost.ChangeRef) string {
	if f.ChangeURLFunc != nil {
		return f.ChangeURLFunc(ref)
	}
	for raw, r := range f.Changes {
		if r == ref {
			return raw
		}
	}
	return ""
}

func (f *Fake) ParseEvent(name string, body []byte) *codehost.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parsedEvents = append(f.parsedEvents, ParsedEvent{Name: name, Body: append([]byte(nil), body...)})
	if ev, ok := f.Events[name]; ok {
		return ev
	}
	return &codehost.Event{Kind: codehost.EventIgnored, Ignore: "fake: no event configured for " + name}
}

// ParsedEvents is every ParseEvent call, in order.
func (f *Fake) ParsedEvents() []ParsedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ParsedEvent(nil), f.parsedEvents...)
}

func (f *Fake) ParseRemote(remote string) (codehost.Repo, bool) {
	if f.ParseRemoteFunc != nil {
		return f.ParseRemoteFunc(remote)
	}
	repo, ok := f.Remotes[remote]
	return repo, ok
}

// OpenChange records the request and answers Opened.
func (f *Fake) OpenChange(_ context.Context, tenant uuid.UUID, req *codehost.OpenRequest) (codehost.ChangeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, OpenCall{Tenant: tenant, Request: *req})
	if f.OpenErr != nil {
		return codehost.ChangeRef{}, f.OpenErr
	}
	ref := f.Opened
	if f.OpenedFor != nil {
		ref = f.OpenedFor(req)
	}
	if f.CI != nil {
		if f.CI.Strict {
			var err error
			if ref, err = f.strictOpen(req, ref); err != nil {
				return codehost.ChangeRef{}, err
			}
		}
		f.CI.opened(ref, req)
	}
	return ref, nil
}

func (f *Fake) MarkReady(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readied = append(f.readied, OperationCall{Tenant: tenant, Change: ref})
	return f.ReadyErr
}

func (f *Fake) MarkDraft(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drafted = append(f.drafted, OperationCall{Tenant: tenant, Change: ref})
	return f.DraftErr
}

func (f *Fake) CloseChange(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, OperationCall{Tenant: tenant, Change: ref})
	if f.CloseErr == nil && f.CI != nil && f.CI.Strict {
		if _, ok := f.CI.change(ref); !ok {
			return codehost.ErrChangeNotFound
		}
	}
	if f.CloseErr == nil && f.CI != nil {
		f.CI.closed(ref)
	}
	return f.CloseErr
}

// Closed is every CloseChange call, in order.
func (f *Fake) Closed() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.closed...)
}

func (f *Fake) RetargetChange(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef, base string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retargets = append(f.retargets, RetargetCall{Tenant: tenant, Change: ref, Base: base})
	return f.RetargetErr
}

// Retargeted is every RetargetChange call, in order.
func (f *Fake) Retargeted() []RetargetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RetargetCall(nil), f.retargets...)
}

func (f *Fake) RetitleChange(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retitles = append(f.retitles, RetitleCall{Tenant: tenant, Change: ref, Title: title})
	return f.RetitleErr
}

// Retitled is every RetitleChange call, in order.
func (f *Fake) Retitled() []RetitleCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RetitleCall(nil), f.retitles...)
}

// DeleteBranch records the call and forgets the branch, as the host would.
func (f *Fake) DeleteBranch(_ context.Context, _ uuid.UUID, repo codehost.Repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := BranchKey{Repo: repo, Branch: branch}
	f.deletes = append(f.deletes, key)
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	delete(f.BranchAt, key)
	return nil
}

// BranchDeletes is every DeleteBranch call, in order.
func (f *Fake) BranchDeletes() []BranchKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BranchKey(nil), f.deletes...)
}

func (f *Fake) Checks(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef) (*codehost.Checks, error) {
	checks, err := f.readChecks(tenant, ref)
	if err == nil && f.AfterChecks != nil {
		f.AfterChecks()
	}
	return checks, err
}

func (f *Fake) readChecks(tenant uuid.UUID, ref codehost.ChangeRef) (*codehost.Checks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, OperationCall{Tenant: tenant, Change: ref})
	if f.ChecksErr != nil {
		return nil, f.ChecksErr
	}
	checks, ok := f.ChecksOf[ref]
	set := ok
	if !ok {
		checks, ok = f.strictView(ref)
	}
	if !ok {
		return nil, codehost.ErrChangeNotFound
	}
	if f.CI != nil {
		out := *checks
		return withChecks(&out, f.CI.changeChecks(ref, checks.HeadSHA, configured{set, checks.Checks})), nil
	}
	return checks, nil
}

// strictOpen is the refusals of OpenChange under CI.Strict, and the change's
// reference. The caller holds f.mu.
func (f *Fake) strictOpen(req *codehost.OpenRequest, ref codehost.ChangeRef) (codehost.ChangeRef, error) {
	head, ok := f.BranchAt[BranchKey{Repo: req.Repo, Branch: req.Head}]
	if !ok {
		return ref, fmt.Errorf("%w: head branch %q does not exist", codehost.ErrRejected, req.Head)
	}
	base, ok := f.BranchAt[BranchKey{Repo: req.Repo, Branch: req.Base}]
	if !ok {
		return ref, fmt.Errorf("%w: base branch %q does not exist", codehost.ErrRejected, req.Base)
	}
	if head == base {
		return ref, fmt.Errorf("%w: no commits between %s and %s", codehost.ErrRejected, req.Base, req.Head)
	}
	if f.CI.hasOpen(req.Repo, req.Head, req.Base) {
		return ref, fmt.Errorf("%w: a pull request already exists for %s", codehost.ErrRejected, req.Head)
	}
	if ref == (codehost.ChangeRef{}) {
		ref = codehost.ChangeRef{Host: f.Domain, Owner: strings.ToLower(req.Repo.Owner), Repo: strings.ToLower(req.Repo.Name), Number: f.CI.next()}
	}
	return ref, nil
}

// strictView is the change as the branches have it, for a change the test
// configured nothing for.
func (f *Fake) strictView(ref codehost.ChangeRef) (*codehost.Checks, bool) {
	if f.CI == nil || !f.CI.Strict {
		return nil, false
	}
	ch, ok := f.CI.change(ref)
	if !ok {
		return nil, false
	}
	sha, ok := f.BranchAt[BranchKey{Repo: ch.repo, Branch: ch.head}]
	if !ok {
		return nil, false
	}
	return &codehost.Checks{HeadSHA: sha, Base: ch.base}, true
}

// withChecks is c with its checks (and so its state) replaced.
func withChecks(c *codehost.Checks, checks []codehost.Check) *codehost.Checks {
	out := codehost.NewChecks(c.HeadSHA, c.Base, checks)
	out.Title = c.Title
	return out
}

// BranchChecks answers from BranchChecksOf, by branch; a branch not in it is
// not found.
func (f *Fake) BranchChecks(_ context.Context, _ uuid.UUID, repo codehost.Repo, branch string) (*codehost.Checks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ChecksErr != nil {
		return nil, f.ChecksErr
	}
	if f.CI != nil {
		sha, ok := f.BranchAt[BranchKey{Repo: repo, Branch: branch}]
		if !ok {
			return nil, fmt.Errorf("%w: no branch %q", codehost.ErrChangeNotFound, branch)
		}
		var set configured
		if c, ok := f.BranchChecksOf[BranchKey{Repo: repo, Branch: branch}]; ok {
			set = configured{true, c.Checks}
		}
		return codehost.NewChecks(sha, "", f.CI.branchChecks(repo, branch, sha, set)), nil
	}
	checks, ok := f.BranchChecksOf[BranchKey{Repo: repo, Branch: branch}]
	if !ok {
		return nil, fmt.Errorf("%w: no branch %q", codehost.ErrChangeNotFound, branch)
	}
	return checks, nil
}

// BranchCommit is a commit asked of a branch.
type BranchCommit struct {
	Repo   codehost.Repo
	Branch string
	SHA    string
}

func (f *Fake) ChangeState(_ context.Context, _ uuid.UUID, ref codehost.ChangeRef) (*codehost.ChangeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateReads = append(f.stateReads, ref)
	if f.StateErr != nil {
		return nil, f.StateErr
	}
	st, ok := f.StateOf[ref]
	if !ok {
		view, found := f.strictView(ref)
		if !found {
			return nil, codehost.ErrChangeNotFound
		}
		st = &codehost.ChangeState{Base: view.Base, HeadSHA: view.HeadSHA}
	}
	out := *st
	return &out, nil
}

// StateReads is every ChangeState call's change, in order.
func (f *Fake) StateReads() []codehost.ChangeRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codehost.ChangeRef(nil), f.stateReads...)
}

func (f *Fake) BranchContains(_ context.Context, _ uuid.UUID, repo codehost.Repo, branch, sha string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ContainsErr != nil {
		return false, f.ContainsErr
	}
	on, ok := f.OnBranch[BranchCommit{Repo: repo, Branch: branch, SHA: sha}]
	if !ok {
		return false, fmt.Errorf("%w: commit %s on %s", codehost.ErrChangeNotFound, sha, branch)
	}
	return on, nil
}

func (f *Fake) CompareBase(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef, headSHA string) (*codehost.BaseComparison, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compared = append(f.compared, OperationCall{Tenant: tenant, Change: ref, HeadSHA: headSHA})
	if f.CompareErr != nil {
		return nil, f.CompareErr
	}
	cmp, ok := f.CompareOf[ref]
	if !ok {
		return nil, codehost.ErrChangeNotFound
	}
	return cmp, nil
}

func (f *Fake) Diff(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef, headSHA string) (*codehost.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffed = append(f.diffed, OperationCall{Tenant: tenant, Change: ref, HeadSHA: headSHA})
	if f.DiffErr != nil {
		return nil, f.DiffErr
	}
	d, ok := f.DiffOf[headSHA]
	if !ok {
		return nil, codehost.ErrChangeNotFound
	}
	return d, nil
}

// CommitRange is a CompareCommits call's two commits.
type CommitRange struct{ From, To string }

func (f *Fake) CompareCommits(_ context.Context, tenant uuid.UUID, ref codehost.ChangeRef, fromSHA, toSHA string) (*codehost.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitCompares = append(f.commitCompares, CommitRange{From: fromSHA, To: toSHA})
	if f.CommitsErr != nil {
		return nil, f.CommitsErr
	}
	d, ok := f.CommitsOf[CommitRange{From: fromSHA, To: toSHA}]
	if !ok {
		return nil, codehost.ErrChangeNotFound
	}
	return d, nil
}

// CommitCompares is every CompareCommits call, in order.
func (f *Fake) CommitCompares() []CommitRange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CommitRange(nil), f.commitCompares...)
}

func (f *Fake) DefaultBranchHead(_ context.Context, _ uuid.UUID, repo codehost.Repo) (*codehost.BranchHead, error) {
	if f.BeforeHead != nil {
		f.BeforeHead()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, repo)
	if f.HeadErr != nil {
		return nil, f.HeadErr
	}
	head, ok := f.HeadOf[repo]
	if !ok {
		return nil, codehost.ErrChangeNotFound
	}
	out := *head
	return &out, nil
}

// BranchHead answers from BranchAt, and for the repository's default branch
// from HeadOf; any other branch is not on the host. HeadErr fails it too.
func (f *Fake) BranchHead(_ context.Context, _ uuid.UUID, repo codehost.Repo, branch string) (*codehost.BranchHead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branchHeads = append(f.branchHeads, BranchKey{Repo: repo, Branch: branch})
	if f.HeadErr != nil {
		return nil, f.HeadErr
	}
	if sha, ok := f.BranchAt[BranchKey{Repo: repo, Branch: branch}]; ok {
		return &codehost.BranchHead{Branch: branch, SHA: sha}, nil
	}
	if head, ok := f.HeadOf[repo]; ok && head.Branch == branch {
		out := *head
		return &out, nil
	}
	return nil, codehost.ErrChangeNotFound
}

// BranchHeadReads is every branch BranchHead was asked about, in order.
func (f *Fake) BranchHeadReads() []BranchKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BranchKey(nil), f.branchHeads...)
}

// HeadReads is every repository DefaultBranchHead was asked about, in order.
func (f *Fake) HeadReads() []codehost.Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codehost.Repo(nil), f.heads...)
}

func (f *Fake) CreateBranch(_ context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) error {
	if f.BeforeBranch != nil {
		f.BeforeBranch()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := BranchKey{Repo: repo, Branch: branch}
	f.branchCalls = append(f.branchCalls, BranchCall{Tenant: tenant, BranchKey: key, SHA: sha})
	if f.BranchErr != nil {
		return f.BranchErr
	}
	have, ok := f.BranchAt[key]
	if !ok {
		if f.BranchAt == nil {
			f.BranchAt = map[BranchKey]string{}
		}
		f.BranchAt[key] = sha
		return nil
	}
	if have != sha {
		return &codehost.BranchConflictError{Branch: branch, Have: have, Want: sha}
	}
	return nil
}

func (f *Fake) TakesInBranchPast(_ context.Context, tenant uuid.UUID, repo codehost.Repo, branch, since, head string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes = append(f.takes, TakesCall{Tenant: tenant, Repo: repo, Branch: branch, Since: since, Head: head})
	if f.TakesErr != nil {
		return false, f.TakesErr
	}
	return f.TookInMain[head], nil
}

// AheadSinces is the since commit of every CommitsAhead call, in order.
func (f *Fake) AheadSinces() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.aheadSince)
}

func (f *Fake) CommitsAhead(_ context.Context, _ uuid.UUID, _ codehost.Repo, since, branch string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aheadSince = append(f.aheadSince, since)
	if f.AheadErr != nil {
		return 0, f.AheadErr
	}
	n, ok := f.AheadOf[branch]
	if !ok {
		return 0, fmt.Errorf("%w: branch %s", codehost.ErrChangeNotFound, branch)
	}
	return n, nil
}

func (f *Fake) CompareBranches(_ context.Context, _ uuid.UUID, repo codehost.Repo, _, branch string) (*codehost.BranchComparison, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ComparisonErr != nil {
		return nil, f.ComparisonErr
	}
	c, ok := f.ComparisonOf[BranchKey{Repo: repo, Branch: branch}]
	if !ok {
		return nil, fmt.Errorf("%w: branch %s", codehost.ErrChangeNotFound, branch)
	}
	out := *c
	return &out, nil
}

func (f *Fake) MergeBranch(_ context.Context, tenant uuid.UUID, repo codehost.Repo, into, from string) (*codehost.BranchMerge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branchMerge = append(f.branchMerge, BranchMergeCall{Tenant: tenant, Repo: repo, Into: into, From: from})
	if f.BranchMergeErr != nil {
		return nil, f.BranchMergeErr
	}
	out := f.BranchMerged
	return &out, nil
}

func (f *Fake) UpdateBranch(_ context.Context, tenant uuid.UUID, req *codehost.UpdateBranchRequest) (*codehost.BranchUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, OperationCall{Tenant: tenant, Change: req.Change, Update: *req})
	if f.BranchUpdateErr != nil {
		return nil, f.BranchUpdateErr
	}
	out := f.BranchUpdated
	return &out, nil
}

// BranchUpdates is every UpdateBranch call, in order.
func (f *Fake) BranchUpdates() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.updates...)
}

// BranchMerges is every MergeBranch call, in order.
func (f *Fake) BranchMerges() []BranchMergeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BranchMergeCall(nil), f.branchMerge...)
}

// TakesCalls is every TakesInBranchPast call, in order.
func (f *Fake) TakesCalls() []TakesCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TakesCall(nil), f.takes...)
}

// BranchCreates is every CreateBranch call, in order.
// EnsureBranch records the call with CreateBranch's and puts a missing branch
// at sha; an existing one stays where it is.
func (f *Fake) EnsureBranch(_ context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) error {
	if f.BeforeBranch != nil {
		f.BeforeBranch()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := BranchKey{Repo: repo, Branch: branch}
	f.branchCalls = append(f.branchCalls, BranchCall{Tenant: tenant, BranchKey: key, SHA: sha, Ensure: true})
	if f.BranchErr != nil {
		return f.BranchErr
	}
	if _, ok := f.BranchAt[key]; !ok {
		if f.BranchAt == nil {
			f.BranchAt = map[BranchKey]string{}
		}
		f.BranchAt[key] = sha
	}
	return nil
}

func (f *Fake) ResolveRef(_ context.Context, _ uuid.UUID, repo codehost.Repo, ref string) (*codehost.ResolvedRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RefErr != nil {
		return nil, f.RefErr
	}
	if r, ok := f.RefOf[RefKey{Repo: repo, Ref: ref}]; ok {
		return &r, nil
	}
	if _, ok := f.BranchAt[BranchKey{Repo: repo, Branch: ref}]; ok {
		return nil, fmt.Errorf("%w: %s", codehost.ErrRefIsBranch, ref)
	}
	return nil, fmt.Errorf("%w: no tag or commit %q", codehost.ErrChangeNotFound, ref)
}

func (f *Fake) TreeOf(_ context.Context, _ uuid.UUID, repo codehost.Repo, sha string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.TreeErr != nil {
		return "", f.TreeErr
	}
	if tree, ok := f.TreeAt[RefKey{Repo: repo, Ref: sha}]; ok {
		return tree, nil
	}
	return "", fmt.Errorf("%w: no commit %q", codehost.ErrChangeNotFound, sha)
}

func (f *Fake) CreateTag(_ context.Context, tenant uuid.UUID, repo codehost.Repo, name, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := TagKey{Repo: repo, Tag: name}
	f.tagCalls = append(f.tagCalls, TagCall{Tenant: tenant, TagKey: key, SHA: sha})
	if f.TagErr != nil {
		return f.TagErr
	}
	have, ok := f.TagAt[key]
	if !ok {
		if f.TagAt == nil {
			f.TagAt = map[TagKey]string{}
		}
		f.TagAt[key] = sha
		return nil
	}
	if have != sha {
		return &codehost.TagConflictError{Tag: name, Have: have, Want: sha}
	}
	return nil
}

// TagCreates is every CreateTag call, in order.
func (f *Fake) TagCreates() []TagCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TagCall(nil), f.tagCalls...)
}

// BranchCreates is every CreateBranch and EnsureBranch call, in order.
func (f *Fake) BranchCreates() []BranchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BranchCall(nil), f.branchCalls...)
}

func (f *Fake) Merge(_ context.Context, tenant uuid.UUID, req *codehost.MergeRequest) (*codehost.Merged, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merges = append(f.merges, OperationCall{Tenant: tenant, Change: req.Change, Merge: *req})
	if f.MergeErr != nil {
		return nil, f.MergeErr
	}
	if view, ok := f.strictView(req.Change); ok && view.HeadSHA != req.HeadSHA {
		return nil, codehost.ErrHeadMoved
	}
	merged := f.MergedWith
	return &merged, nil
}

// Opens is every OpenChange call, in order.
func (f *Fake) Opens() []OpenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OpenCall(nil), f.opens...)
}

// Readied is every MarkReady call, in order.
func (f *Fake) Readied() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.readied...)
}

// Drafted is every MarkDraft call, in order.
func (f *Fake) Drafted() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.drafted...)
}

// Checked is every Checks call, in order.
func (f *Fake) Checked() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.checked...)
}

// Merges is every Merge call, in order.
func (f *Fake) Merges() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.merges...)
}

// Compared is every CompareBase call, in order.
func (f *Fake) Compared() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.compared...)
}

// Diffed is every Diff call, in order.
func (f *Fake) Diffed() []OperationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OperationCall(nil), f.diffed...)
}
