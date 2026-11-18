package manifest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// Owners answers who may change the manifest. Checked against the database
// on every write, not read from the session, which is up to one token
// lifetime stale.
type Owners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Agents is the slice of agent.Service the service reads.
type Agents interface {
	List(ctx context.Context, includeArchived bool) ([]agent.Agent, error)
}

// Service is the manifest's rules: who may change it, what is valid, and
// what an agent's report comes to against it.
type Service struct {
	store   Store
	gate    *authn.OwnerGate
	agents  Agents
	layouts agent.LayoutStore
	logger  *slog.Logger
	// hosts and events are optional: without hosts no base branch is made,
	// without events nothing is audited.
	hosts  Hosts
	events *audit.Recorder
	// locks and tickets are optional, set by WithSync: without them a sync
	// is refused.
	locks   Locks
	tickets Tickets
}

// Option configures the optional parts of a Service at construction.
type Option func(*Service)

// WithHosts lets saving a base branch create it on the repo's code host.
func WithHosts(h Hosts) Option { return func(s *Service) { s.hosts = h } }

// WithAudit records the security-relevant saves.
func WithAudit(events *audit.Recorder) Option { return func(s *Service) { s.events = events } }

func NewService(store Store, owners Owners, agents Agents, layouts agent.LayoutStore, logHandler slog.Handler, opts ...Option) *Service {
	s := &Service{store: store, gate: authn.NewOwnerGate(owners, authn.WithRefusal(ErrNotOwner)), agents: agents, layouts: layouts, logger: slog.New(logHandler)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get is the tenant's manifest, nil while it has declared none.
func (s *Service) Get(ctx context.Context) (*Manifest, error) { return s.store.Get(ctx) }

// Revision is the tenant's current revision, also while it has declared none.
func (s *Service) Revision(ctx context.Context) (int64, error) { return s.store.Revision(ctx) }

// ScriptRepos is the manifest as the setup and update scripts take it,
// empty while none is declared.
func (s *Service) ScriptRepos(ctx context.Context) ([]workspace.Repo, error) {
	m, err := s.store.Get(ctx)
	if err != nil || m == nil {
		return nil, err
	}
	repos := make([]workspace.Repo, 0, len(m.Repos))
	for _, r := range m.Repos {
		repos = append(repos, workspace.Repo{Path: r.Path, Remote: r.Remote})
	}
	return repos, nil
}

// CanEdit reports whether who may change the manifest.
func (s *Service) CanEdit(ctx context.Context, who *authn.User) (bool, error) {
	return s.gate.IsOwner(ctx, who)
}

// Set adopts or replaces the manifest. Nothing else changes with it: open
// tickets keep their scopes and status, and are not re-validated.
func (s *Service) Set(ctx context.Context, who *authn.User, repos []Repo) (*Manifest, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	clean, err := Normalise(repos)
	if err != nil {
		return nil, err
	}
	m, err := s.store.Put(ctx, clean, who.ID)
	if err != nil {
		return nil, err
	}
	s.auditBaseBranches(ctx, who, m)
	return m, nil
}

func (s *Service) auditBaseBranches(ctx context.Context, who *authn.User, next *Manifest) {
	if s.events == nil {
		return
	}
	for _, c := range Diff(next.Replaced, next.Repos) {
		if c.BaseBranch == nil {
			continue
		}
		remote := c.NewRemote
		if remote == "" {
			remote = c.OldRemote
		}
		if remote == "" {
			if r := RepoFor(next.Repos, c.Path); r != nil {
				remote = r.Remote
			}
		}
		s.events.Record(ctx, audit.BaseBranchChanged, "user_id", who.ID, "tenant", who.TenantID,
			"path", c.Path, "remote", remote, "old", c.BaseBranch.Old, "new", c.BaseBranch.New)
	}
}

// Clear takes the manifest away, back to nothing being enforced.
func (s *Service) Clear(ctx context.Context, who *authn.User) error {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return err
	}
	return s.store.Delete(ctx)
}

// AgentState is one enabled agent's workspace against the manifest.
type AgentState struct {
	Agent      agent.Agent
	Report     *agent.ReportedLayout
	Status     Status
	Mismatches []Mismatch
}

// States is every enabled agent's state, by name. A disabled agent runs
// nothing, so it is not part of the fleet the board is about.
func (s *Service) States(ctx context.Context, m *Manifest) ([]AgentState, error) {
	agents, err := s.agents.List(ctx, false)
	if err != nil {
		return nil, err
	}
	reports, err := s.layouts.Layouts(ctx)
	if err != nil {
		return nil, err
	}
	out := []AgentState{}
	for _, a := range agents {
		if a.DisabledAt != nil {
			continue
		}
		st := AgentState{Agent: a, Status: StatusUnknown}
		if r, ok := reports[a.ID]; ok {
			st.Report = &r
			st.Status = StatusReported
			if m != nil {
				st.Mismatches = Check(m.Repos, &r.Layout)
				st.Status = StatusMatches
				if len(st.Mismatches) > 0 {
					st.Status = StatusMismatch
				}
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// Proposal is what the enabled agents' reports suggest, for a tenant with
// no manifest.
func (s *Service) Proposal(ctx context.Context, states []AgentState) *Proposal {
	var reports []AgentLayout
	for _, st := range states {
		if st.Report != nil {
			reports = append(reports, AgentLayout{AgentID: st.Agent.ID, AgentName: st.Agent.Name, Layout: st.Report.Layout})
		}
	}
	return Propose(reports)
}

// ClaimNotice is what an agent that has just been granted a ticket with
// these scopes is told about its workspace, or "" for nothing to say.
//
// Only a mismatch in the repo a scope falls under counts: the agent is told
// to give the ticket back, because the work would land somewhere other than
// where the ticket means. Everything else -- no manifest, no report
// (unknown), another repo wrong -- is silent here; the board shows it.
// Failing to find out is silent too: unknown never blocks.
func (s *Service) ClaimNotice(ctx context.Context, agentID uuid.UUID, scopes []string) string {
	m, err := s.store.Get(ctx)
	if err != nil || m == nil {
		if err != nil {
			s.logger.Error("manifest claim check", "err", err)
		}
		return ""
	}
	reports, err := s.layouts.Layouts(ctx)
	if err != nil {
		s.logger.Error("manifest claim check", "err", err)
		return ""
	}
	r, ok := reports[agentID]
	if !ok {
		return ""
	}
	needed := map[string]bool{}
	for _, scope := range scopes {
		if repo := RepoFor(m.Repos, scope); repo != nil {
			needed[repo.Path] = true
		}
	}
	var lines []string
	for _, mm := range Check(m.Repos, &r.Layout) {
		if needed[mm.Path] {
			lines = append(lines, "  - "+mm.Describe())
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("WORKSPACE MISMATCH: the board's workspace manifest and what your workspace last reported disagree "+
		"in a repo this ticket's scopes need:\n%s\nDo not start work. Call ticket_release saying what you found: "+
		"a person has to fix this workspace (or the manifest), and taking the ticket again before then "+
		"would only repeat this.",
		strings.Join(lines, "\n"))
}

// Hosts is the slice of the code host registry the service reads.
type Hosts interface {
	ParseRemote(remote string) (host string, repo codehost.Repo, ok bool)
	Changes(host string) (codehost.Changes, bool)
}

// Outcomes of making a base branch.
const (
	// BranchReady: the branch exists on the host, made now or already there.
	BranchReady = "ready"
	// BranchIsDefault: the base branch is the repo's default branch, which
	// is what a repo without a base branch uses anyway.
	BranchIsDefault = "default_branch"
	// BranchUnavailable: slop-haus cannot act on this repo's host, so the
	// branch has to be made by hand.
	BranchUnavailable = "unavailable"
	BranchFailed      = "failed"
)

// BaseBranchResult is what making one repo's base branch came to. A failure
// never undoes the saved setting: the owner sees it and saves again.
type BaseBranchResult struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Status  string `json:"status" enum:"ready,default_branch,unavailable,failed"`
	Message string `json:"message"`
}

// baseBranchTimeout bounds each repo's code host calls, which a save waits on.
const baseBranchTimeout = 15 * time.Second

// EnsureBaseBranches makes each repo's base branch on its code host when it
// is missing, from the tip of the repo's default branch. It never moves a
// branch that exists, so saving again is harmless.
func (s *Service) EnsureBaseBranches(ctx context.Context, m *Manifest) []BaseBranchResult {
	var out []BaseBranchResult
	for i := range m.Repos {
		r := &m.Repos[i]
		if r.BaseBranch == "" {
			continue
		}
		res := BaseBranchResult{Path: r.Path, Branch: r.BaseBranch}
		res.Status, res.Message = s.ensureBaseBranch(ctx, r)
		if res.Status == BranchFailed {
			s.logger.Warn("manifest: base branch not made", "path", r.Path, "branch", r.BaseBranch, "err", res.Message)
		}
		out = append(out, res)
	}
	return out
}

func (s *Service) ensureBaseBranch(ctx context.Context, r *Repo) (status, message string) {
	if s.hosts == nil {
		return BranchUnavailable, "slop-haus has no code host to make the branch on: make " + r.BaseBranch + " yourself"
	}
	host, repo, ok := s.hosts.ParseRemote(r.Remote)
	if !ok {
		return BranchUnavailable, r.Remote + " is not a repository on a code host slop-haus knows: make " + r.BaseBranch + " yourself"
	}
	changes, ok := s.hosts.Changes(host)
	if !ok {
		return BranchUnavailable, "slop-haus cannot act on " + host + ": make " + r.BaseBranch + " yourself"
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return BranchFailed, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, baseBranchTimeout)
	defer cancel()
	head, err := changes.DefaultBranchHead(ctx, tenant, repo)
	if err != nil {
		return BranchFailed, fmt.Sprintf("could not read the default branch of %s: %v", r.Remote, err)
	}
	if head.Branch == r.BaseBranch {
		return BranchIsDefault, r.BaseBranch + " is the repo's default branch, which is what a repo without a base branch uses"
	}
	if err := changes.EnsureBranch(ctx, tenant, repo, r.BaseBranch, head.SHA); err != nil {
		return BranchFailed, fmt.Sprintf("could not make %s on %s: %v", r.BaseBranch, r.Remote, err)
	}
	return BranchReady, r.BaseBranch + " exists on " + r.Remote
}
