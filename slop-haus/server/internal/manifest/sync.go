package manifest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// ErrSyncBlocked is a sync refused because an agent holds the base branch's
// integration lock; the error is a *SyncBlockedError naming the holder.
var ErrSyncBlocked = errors.New("manifest: the base branch is being merged into")

// SyncBlockedError says who holds the lock.
type SyncBlockedError struct {
	Branch    string
	AgentName string
	TicketID  int64
}

func (e *SyncBlockedError) Error() string {
	return fmt.Sprintf("%v: %s's integration lock is held by %s for #%d; try again when it is released",
		ErrSyncBlocked, e.Branch, e.AgentName, e.TicketID)
}

func (e *SyncBlockedError) Unwrap() error { return ErrSyncBlocked }

// ErrSyncUnavailable is a sync or comparison slop-haus cannot make because
// the repo's code host is unknown to it or did not answer.
var ErrSyncUnavailable = errors.New("manifest: the code host cannot be reached for this repo")

// Locks reads who holds an integration lock.
type Locks interface {
	LockState(ctx context.Context, name string) (*ticket.LockState, error)
}

// Tickets files the ticket a sync conflict is resolved through.
type Tickets interface {
	Create(ctx context.Context, params *ticket.CreateParams) (*ticket.Ticket, error)
}

// WithSync lets an owner sync a base branch from the default branch.
func WithSync(locks Locks, tickets Tickets) Option {
	return func(s *Service) { s.locks, s.tickets = locks, tickets }
}

const syncTimeout = 30 * time.Second

// divergenceBudget bounds reading every repo's divergence for a page load.
const divergenceBudget = 10 * time.Second

// maxConflictPaths bounds the scopes of the ticket a conflict files. More
// files than this are not a narrow scope any more, so the repo stands in.
const maxConflictPaths = 50

// Divergence is where a repo's base branch stands against its default branch.
type Divergence struct {
	Path          string `json:"path" doc:"The manifest repo's path."`
	BaseBranch    string `json:"base_branch"`
	DefaultBranch string `json:"default_branch" doc:"Empty when the code host could not be read."`
	Behind        int    `json:"behind" doc:"Commits of the default branch the base branch lacks."`
	Ahead         int    `json:"ahead" doc:"Commits of the base branch the default branch lacks."`
	Error         string `json:"error,omitempty" doc:"Why the counts could not be read; they are zero then."`
}

// Divergences compares each repo that has a base branch of its own with its
// default branch, on the code host as it is now. A repo without a base
// branch, or on its default branch, is not listed. A repo the host cannot
// answer for is listed with Error, so one unreachable repo hides no other.
func (s *Service) Divergences(ctx context.Context, m *Manifest) []Divergence {
	out := []Divergence{}
	if m == nil {
		return out
	}
	// Every page load waits on this, so the repos are read together and the
	// whole read has one deadline: a slow host costs the page divergenceBudget
	// at most, however many repos the manifest has.
	ctx, cancel := context.WithTimeout(ctx, divergenceBudget)
	defer cancel()
	var repos []*Repo
	for i := range m.Repos {
		if m.Repos[i].BaseBranch != "" {
			repos = append(repos, &m.Repos[i])
		}
	}
	read := make([]*Divergence, len(repos))
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := Divergence{Path: r.Path, BaseBranch: r.BaseBranch}
			def, err := s.divergence(ctx, r)
			if def != nil {
				d.DefaultBranch = def.defaultBranch
				d.Ahead, d.Behind = def.cmp.Ahead, def.cmp.Behind
			}
			if err != nil {
				d.Error = err.Error()
			}
			if def == nil || def.defaultBranch != r.BaseBranch {
				read[i] = &d
			}
		}()
	}
	wg.Wait()
	for _, d := range read {
		if d != nil {
			out = append(out, *d)
		}
	}
	return out
}

type divergence struct {
	defaultBranch string
	cmp           codehost.BranchComparison
}

func (s *Service) divergence(ctx context.Context, r *Repo) (*divergence, error) {
	h, err := s.hostFor(ctx, r)
	if err != nil {
		return nil, err
	}
	head, err := h.changes.DefaultBranchHead(ctx, h.tenant, h.repo)
	if err != nil {
		return nil, fmt.Errorf("could not read the default branch of %s: %w", r.Remote, err)
	}
	if head.Branch == r.BaseBranch {
		return &divergence{defaultBranch: head.Branch}, nil
	}
	cmp, err := h.changes.CompareBranches(ctx, h.tenant, h.repo, head.Branch, r.BaseBranch)
	if err != nil {
		return &divergence{defaultBranch: head.Branch}, fmt.Errorf("could not compare %s with %s: %w", r.BaseBranch, head.Branch, err)
	}
	return &divergence{defaultBranch: head.Branch, cmp: *cmp}, nil
}

type repoHost struct {
	tenant  uuid.UUID
	host    string
	repo    codehost.Repo
	changes codehost.Changes
}

func (s *Service) hostFor(ctx context.Context, r *Repo) (*repoHost, error) {
	if s.hosts == nil {
		return nil, fmt.Errorf("%w: slop-haus has no code host", ErrSyncUnavailable)
	}
	host, repo, ok := s.hosts.ParseRemote(r.Remote)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not a repository on a code host slop-haus knows", ErrSyncUnavailable, r.Remote)
	}
	changes, ok := s.hosts.Changes(host)
	if !ok {
		return nil, fmt.Errorf("%w: slop-haus cannot act on %s", ErrSyncUnavailable, host)
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	return &repoHost{tenant: tenant, host: host, repo: repo, changes: changes}, nil
}

// Outcomes of a sync.
const (
	SyncMerged   = "merged"
	SyncUpToDate = "up_to_date"
	SyncConflict = "conflict"
)

// SyncResult is what syncing one repo's base branch came to.
type SyncResult struct {
	Path          string `json:"path"`
	BaseBranch    string `json:"base_branch"`
	DefaultBranch string `json:"default_branch"`
	Outcome       string `json:"outcome" enum:"merged,up_to_date,conflict" doc:"merged: the default branch was merged in, Head is the merge commit. up_to_date: there was nothing to merge. conflict: nothing changed and TicketID was filed to resolve it."`
	Head          string `json:"head,omitempty" doc:"The base branch's head after the sync."`
	TicketID      int64  `json:"ticket_id,omitempty" doc:"The ticket filed for a conflict, in preparing."`
}

// Sync merges the repo's default branch into its base branch on the code
// host, for an owner. The operations name a (repo, branch) pair, not the
// manifest's base branch, so a superticket's branch can be synced from its
// parent's base by the same code (see syncBranch).
//
// The integration lock is checked, not taken. A lock row needs a holder agent
// and a lease on a ticket, which an owner clicking a button has neither of.
// What the lock protects is that a landing merge and this one do not race:
// the host's merge is atomic per branch, and the merge gate sends an agent
// whose head fell behind the new tip back to merge the base again, so an
// agent taking the lock between the check and the merge costs it one more
// base merge, not a wrong landing. Do not turn this into a hold without
// reworking how lock holders are stored (a merge queue keyed by ticket is
// planned for that).
func (s *Service) Sync(ctx context.Context, who *authn.User, path string) (*SyncResult, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	m, err := s.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	var r *Repo
	if m != nil {
		for i := range m.Repos {
			if m.Repos[i].Path == path {
				r = &m.Repos[i]
			}
		}
	}
	if r == nil {
		return nil, invalid("no repo at %q in the manifest", path)
	}
	if r.BaseBranch == "" {
		return nil, invalid("%s has no base branch: it merges into the default branch already", path)
	}
	h, err := s.hostFor(ctx, r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*syncTimeout)
	defer cancel()
	def, err := h.changes.DefaultBranchHead(ctx, h.tenant, h.repo)
	if err != nil {
		return nil, fmt.Errorf("%w: read the default branch of %s: %w", ErrSyncUnavailable, r.Remote, err)
	}
	if def.Branch == r.BaseBranch {
		return nil, invalid("%s's base branch is its default branch", path)
	}
	res, err := s.syncBranch(ctx, who, h, r, r.BaseBranch, def.Branch)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) syncBranch(ctx context.Context, who *authn.User, h *repoHost, r *Repo, into, from string) (*SyncResult, error) {
	if s.locks == nil || s.tickets == nil {
		return nil, fmt.Errorf("%w: syncing is not set up", ErrSyncUnavailable)
	}
	state, err := s.locks.LockState(ctx, ticket.BranchLockName(r.Path, into))
	if err != nil {
		return nil, fmt.Errorf("read the integration lock: %w", err)
	}
	if hold := state.Holder; hold != nil {
		return nil, &SyncBlockedError{Branch: into, AgentName: hold.AgentName, TicketID: hold.TicketID}
	}

	res := &SyncResult{Path: r.Path, BaseBranch: into, DefaultBranch: from}
	merged, err := h.changes.MergeBranch(ctx, h.tenant, h.repo, into, from)
	switch {
	case errors.Is(err, codehost.ErrMergeConflict):
		id, ferr := s.fileConflict(ctx, who, h, r, into, from)
		if ferr != nil {
			return nil, ferr
		}
		res.Outcome, res.TicketID = SyncConflict, id
	case err != nil:
		return nil, fmt.Errorf("%w: merge %s into %s: %w", ErrSyncUnavailable, from, into, err)
	case merged.UpToDate:
		res.Outcome, res.Head = SyncUpToDate, merged.SHA
	default:
		res.Outcome, res.Head = SyncMerged, merged.SHA
	}
	if s.events != nil {
		s.events.Record(ctx, audit.RepoSynced, "user_id", who.ID, "tenant", who.TenantID,
			"path", r.Path, "base_branch", into, "default_branch", from, "outcome", res.Outcome,
			"head", res.Head, "ticket", res.TicketID)
	}
	return res, nil
}

// fileConflict files the ticket someone resolves the conflict through, on a
// branch of their own and the normal flow. Its scopes are the files both
// branches changed since they diverged: the narrowest scope that is honest.
func (s *Service) fileConflict(ctx context.Context, who *authn.User, h *repoHost, r *Repo, into, from string) (int64, error) {
	var note string
	intoHead, fromHead, files, err := s.conflictFiles(ctx, h, into, from)
	switch {
	case err != nil:
		note = fmt.Sprintf("The conflicting files could not be worked out (%v), so the scope is the whole repo.", err)
		files = nil
	case len(files) == 0:
		note = "No file was changed on both branches (a rename or delete conflict, perhaps), so the scope is the whole repo."
	case len(files) > maxConflictPaths:
		note = fmt.Sprintf("%d files were changed on both branches, so the scope is the whole repo.", len(files))
		files = nil
	}
	paths := make([]model.Path, 0, len(files))
	for _, f := range files {
		paths = append(paths, model.Path{Path: scopeIn(r, f), Source: model.PathSourceFiled})
	}
	if len(paths) == 0 {
		paths = append(paths, model.Path{Path: scopeIn(r, ""), Source: model.PathSourceFiled})
	}

	title := fmt.Sprintf("Resolve the conflict merging %s into %s", from, into)
	if len(files) > 0 {
		title += " (" + strings.Join(files, ", ") + ")"
	}
	if len(title) > 200 {
		title = title[:197] + "..."
	}
	body := fmt.Sprintf("An owner synced `%s` into the base branch `%s` of %s (%s) and the merge conflicts, so nothing changed.\n\n"+
		"- `%s` was at `%s`\n- `%s` was at `%s`\n\n"+
		"Resolve it on a branch of this ticket: merge `%s` into it, fix the conflicts, then merge through the normal flow into `%s`.",
		from, into, r.Path, r.Remote, into, short(intoHead), from, short(fromHead), from, into)
	if len(files) > 0 {
		body += "\n\nFiles changed on both branches:\n- " + strings.Join(files, "\n- ")
	}
	if note != "" {
		body += "\n\n" + note
	}
	t, err := s.tickets.Create(ctx, &ticket.CreateParams{
		Title: title, Body: body, Paths: paths, Status: ticket.StatusPreparing, CreatedByUserID: &who.ID,
	})
	if err != nil {
		return 0, fmt.Errorf("file the conflict ticket: %w", err)
	}
	return t.ID, nil
}

// conflictFiles are the files changed on both branches since their merge
// base, with the two tips they were read at.
func (s *Service) conflictFiles(ctx context.Context, h *repoHost, into, from string) (intoSHA, fromSHA string, files []string, err error) {
	ih, err := h.changes.BranchHead(ctx, h.tenant, h.repo, into)
	if err != nil {
		return "", "", nil, err
	}
	fh, err := h.changes.BranchHead(ctx, h.tenant, h.repo, from)
	if err != nil {
		return ih.SHA, "", nil, err
	}
	ref := codehost.ChangeRef{Host: h.host, Owner: h.repo.Owner, Repo: h.repo.Name}
	onInto, err := h.changes.CompareCommits(ctx, h.tenant, ref, fh.SHA, ih.SHA)
	if err != nil {
		return ih.SHA, fh.SHA, nil, err
	}
	onFrom, err := h.changes.CompareCommits(ctx, h.tenant, ref, ih.SHA, fh.SHA)
	if err != nil {
		return ih.SHA, fh.SHA, nil, err
	}
	changedInto := map[string]bool{}
	for _, f := range onInto.Files {
		changedInto[f.Path] = true
	}
	for _, f := range onFrom.Files {
		if changedInto[f.Path] {
			files = append(files, f.Path)
		}
	}
	sort.Strings(files)
	return ih.SHA, fh.SHA, files, nil
}

// scopeIn is a file of the repo as a ticket scope (workspace-relative); ""
// is the whole repo.
func scopeIn(r *Repo, file string) string {
	if file == "" {
		return r.Path + "/"
	}
	return r.Path + "/" + file
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
