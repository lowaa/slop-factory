package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// AgentState is where an agent is in its life. See the design doc, "States".
type AgentState string

const (
	StateStarting     AgentState = "starting"
	StateRefining     AgentState = "refining"
	StateWorking      AgentState = "working"
	StateIdle         AgentState = "idle"
	StateWaitingUser  AgentState = "waiting_user"
	StateDiscussion   AgentState = "discussion"
	StateWaitingMerge AgentState = "waiting_merge"
	StateAttached     AgentState = "attached"
	StateReleased     AgentState = "released"
	StateDone         AgentState = "done"
	StateStalled      AgentState = "stalled"
	StateExited       AgentState = "exited"
	StateCrashed      AgentState = "crashed"
)

// Ends reports whether entering s ends the agent: slop-bot no longer runs
// it, and it can't change state again. exited and crashed don't: the daemon
// may resume a harness that died.
func (s AgentState) Ends() bool { return s == StateDone || s == StateReleased }

// Mode is how the agent's harness is being driven.
type Mode string

const (
	ModeProgrammatic Mode = "programmatic"
	// ModeAttached is a person's TUI holding the attach lease.
	ModeAttached Mode = "attached"
)

// Pool is the concurrency pool an agent's slot comes from.
type Pool string

const (
	PoolNormal Pool = "normal"
	PoolUrgent Pool = "urgent"
)

// Worktree is one of an agent's git worktrees.
type Worktree struct {
	Repo   string
	Path   string
	Branch string
	// Primary is the harness's cwd; an agent has at most one.
	Primary bool
}

// Lease is an attach: who holds the agent's TUI hand-over, and until when.
type Lease struct {
	Holder    string
	ExpiresAt time.Time
}

// Agent is one agent's record.
type Agent struct {
	ID             ident.AgentID
	Ticket         ident.TicketID
	Harness        string
	Driver         string
	Model          string
	Effort         string
	ConversationID string
	Mode           Mode
	// Lease is set exactly when Mode is ModeAttached.
	Lease *Lease
	// TUISession is the tmux session name, for TUI drivers.
	TUISession   string
	Pool         Pool
	State        AgentState
	StateSince   time.Time
	ProgressNote string
	Nudges       int
	Worktrees    []Worktree
	CreatedAt    time.Time
	// EndedAt is set once the agent entered a state that Ends.
	EndedAt *time.Time
}

// NewAgent is what Create needs.
type NewAgent struct {
	ID         ident.AgentID
	Ticket     ident.TicketID
	Harness    string
	Driver     string
	Model      string
	Effort     string
	TUISession string
	Pool       Pool
	// State is the first state, usually starting.
	State  AgentState
	Reason string
	// TokenHash is control.HashToken of the agent's token: state.db stores
	// only the hash, so a leaked file leaks no live credential.
	TokenHash string
	Worktrees []Worktree
}

// Transition is a state change SetState makes.
type Transition struct {
	Agent ident.AgentID
	// From, when set, makes the change conditional: it fails with
	// ErrConflict unless the agent is in From. Two goroutines racing to move
	// the same agent then can't both win.
	From   AgentState
	To     AgentState
	Reason string
}

// StateChange is one entry in an agent's state history.
type StateChange struct {
	ID    int64
	Agent ident.AgentID
	// From is empty for the agent's first state.
	From   AgentState
	To     AgentState
	Reason string
	At     time.Time
}

// LeaseHeldError is AcquireLease refusing because someone else holds the
// lease. It matches ErrConflict.
type LeaseHeldError struct {
	Holder    string
	ExpiresAt time.Time
}

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("state: attach lease held by %s until %s", e.Holder, e.ExpiresAt.Format(time.RFC3339))
}

// Is makes errors.Is(err, ErrConflict) hold.
func (e *LeaseHeldError) Is(target error) bool { return target == ErrConflict }

type agentRow struct {
	bun.BaseModel `bun:"table:agents"`

	ID             string `bun:"id,pk"`
	TicketID       string `bun:"ticket_id"`
	Harness        string `bun:"harness"`
	Driver         string `bun:"driver"`
	Model          string `bun:"model"`
	Effort         string `bun:"effort"`
	ConversationID string `bun:"conversation_id"`
	Mode           string `bun:"mode"`
	LeaseHolder    string `bun:"lease_holder"`
	LeaseExpiresAt *int64 `bun:"lease_expires_at"`
	TUISession     string `bun:"tui_session"`
	Pool           string `bun:"pool"`
	State          string `bun:"state"`
	StateSince     int64  `bun:"state_since"`
	ProgressNote   string `bun:"progress_note"`
	NudgeCount     int    `bun:"nudge_count"`
	TokenHash      string `bun:"token_hash"`
	CreatedAt      int64  `bun:"created_at"`
	EndedAt        *int64 `bun:"ended_at"`
}

func (r *agentRow) agent() *Agent {
	a := &Agent{
		ID:             ident.AgentID(r.ID),
		Ticket:         ident.TicketID(r.TicketID),
		Harness:        r.Harness,
		Driver:         r.Driver,
		Model:          r.Model,
		Effort:         r.Effort,
		ConversationID: r.ConversationID,
		Mode:           Mode(r.Mode),
		TUISession:     r.TUISession,
		Pool:           Pool(r.Pool),
		State:          AgentState(r.State),
		StateSince:     fromMillis(r.StateSince),
		ProgressNote:   r.ProgressNote,
		Nudges:         r.NudgeCount,
		CreatedAt:      fromMillis(r.CreatedAt),
		EndedAt:        fromMillisPtr(r.EndedAt),
	}
	if r.LeaseExpiresAt != nil {
		a.Lease = &Lease{Holder: r.LeaseHolder, ExpiresAt: fromMillis(*r.LeaseExpiresAt)}
	}
	return a
}

type worktreeRow struct {
	bun.BaseModel `bun:"table:agent_worktrees"`

	AgentID   string `bun:"agent_id,pk"`
	Repo      string `bun:"repo,pk"`
	Path      string `bun:"path"`
	Branch    string `bun:"branch"`
	IsPrimary bool   `bun:"is_primary"`
}

type stateChangeRow struct {
	bun.BaseModel `bun:"table:agent_state_changes"`

	ID        int64   `bun:"id,pk,autoincrement"`
	AgentID   string  `bun:"agent_id"`
	FromState *string `bun:"from_state"`
	ToState   string  `bun:"to_state"`
	Reason    string  `bun:"reason"`
	At        int64   `bun:"at"`
}

// Agents is the agents table, with its worktrees and state history.
type Agents struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// Create records a new agent, its worktrees and its first state change in
// one transaction. A ticket that already has a live agent, a taken id, or a
// reused token hash is ErrConflict.
func (a *Agents) Create(ctx context.Context, n *NewAgent) (*Agent, error) {
	if n.State.Ends() {
		return nil, fmt.Errorf("state: an agent can't start in %s", n.State)
	}
	at := now(a.clock)
	row := &agentRow{
		ID: string(n.ID), TicketID: string(n.Ticket),
		Harness: n.Harness, Driver: n.Driver, Model: n.Model, Effort: n.Effort,
		Mode: string(ModeProgrammatic), TUISession: n.TUISession,
		Pool: string(n.Pool), State: string(n.State), StateSince: toMillis(at),
		TokenHash: n.TokenHash, CreatedAt: toMillis(at),
	}
	var created *Agent
	err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
			if sqlitex.IsUniqueViolation(err) {
				return fmt.Errorf("%w: creating agent %s for ticket %s: %w", ErrConflict, n.ID, n.Ticket, err)
			}
			return fmt.Errorf("state: creating agent %s: %w", n.ID, err)
		}
		for i := range n.Worktrees {
			if err := insertWorktree(ctx, tx, n.ID, &n.Worktrees[i]); err != nil {
				return err
			}
		}
		if err := insertStateChange(ctx, tx, &stateChangeRow{
			AgentID: row.ID, ToState: row.State, Reason: n.Reason, At: row.CreatedAt,
		}); err != nil {
			return err
		}
		// Read back in the transaction, so the worktrees come in the order
		// every read gives them, and a failed read-back can't report failure
		// for an agent that was committed.
		var err error
		created, err = a.one(ctx, tx, func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("id = ?", row.ID) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Get returns the agent with id, ended or not.
func (a *Agents) Get(ctx context.Context, id ident.AgentID) (*Agent, error) {
	return a.one(ctx, a.db, func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("id = ?", string(id)) })
}

// AgentForTokenHash returns who holds the token whose control.HashToken is
// hash: socket auth (control.TokenResolver). Only a live agent's token
// identifies anyone; an ended agent's is ErrNotFound.
func (a *Agents) AgentForTokenHash(ctx context.Context, hash string) (ident.Principal, error) {
	ag, err := a.one(ctx, a.db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("token_hash = ?", hash).Where("ended_at IS NULL")
	})
	if err != nil {
		return ident.Principal{}, err
	}
	return ident.AgentPrincipal(ag.ID, ag.Ticket), nil
}

// LiveForTicket returns the ticket's live agent.
func (a *Agents) LiveForTicket(ctx context.Context, ticket ident.TicketID) (*Agent, error) {
	return a.one(ctx, a.db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("ticket_id = ?", string(ticket)).Where("ended_at IS NULL")
	})
}

// ReleasedForTicket reports whether the ticket's agent was released to a
// person (`attach --no-resume`). The ticket stays assigned, so without this
// the scheduler would start a new agent on work a person has taken over.
func (a *Agents) ReleasedForTicket(ctx context.Context, ticket ident.TicketID) (bool, error) {
	n, err := a.db.NewSelect().Table("agents").
		Where("ticket_id = ?", string(ticket)).Where("state = ?", string(StateReleased)).Count(ctx)
	if err != nil {
		return false, fmt.Errorf("state: checking ticket %s for a released agent: %w", ticket, err)
	}
	return n > 0, nil
}

// ListLive returns every agent that hasn't ended, oldest first: the ones the
// daemon resumes on start.
func (a *Agents) ListLive(ctx context.Context) ([]*Agent, error) {
	var rows []agentRow
	if err := a.db.NewSelect().Model(&rows).Where("ended_at IS NULL").Order("created_at", "id").Scan(ctx); err != nil {
		return nil, fmt.Errorf("state: listing live agents: %w", err)
	}
	return a.withWorktrees(ctx, a.db, rows)
}

// ExpiredLeases returns the attached agents whose lease expired at or
// before at, for the daemon to hand back.
func (a *Agents) ExpiredLeases(ctx context.Context, at time.Time) ([]*Agent, error) {
	var rows []agentRow
	err := a.db.NewSelect().Model(&rows).
		Where("mode = ?", string(ModeAttached)).Where("lease_expires_at <= ?", toMillis(at)).
		Order("lease_expires_at", "id").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("state: listing expired leases: %w", err)
	}
	return a.withWorktrees(ctx, a.db, rows)
}

// SetState moves an agent to t.To and appends the change to its history, in
// one transaction. It reports false, writing nothing, when the agent is
// already in t.To, so StateSince keeps meaning "in this state since". An
// ended agent is ErrEnded; a t.From that doesn't match is ErrConflict.
func (a *Agents) SetState(ctx context.Context, t *Transition) (bool, error) {
	changed := false
	err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var cur agentRow
		if err := tx.NewSelect().Model(&cur).Column("state", "ended_at").Where("id = ?", string(t.Agent)).Scan(ctx); err != nil {
			return notFound(err, "agent %s", t.Agent)
		}
		switch {
		case cur.EndedAt != nil:
			return fmt.Errorf("%w: agent %s is %s", ErrEnded, t.Agent, cur.State)
		case t.From != "" && AgentState(cur.State) != t.From:
			return fmt.Errorf("%w: agent %s is %s, not %s", ErrConflict, t.Agent, cur.State, t.From)
		case AgentState(cur.State) == t.To:
			return nil
		}
		at := toMillis(now(a.clock))
		q := tx.NewUpdate().Table("agents").Set("state = ?", string(t.To)).Set("state_since = ?", at).Where("id = ?", string(t.Agent))
		if t.To.Ends() {
			q = q.Set("ended_at = ?", at)
		}
		if _, err := q.Exec(ctx); err != nil {
			return fmt.Errorf("state: setting agent %s to %s: %w", t.Agent, t.To, err)
		}
		if t.To.Ends() {
			if _, err := closeAsksOf(ctx, tx, AgentParty(t.Agent), at, nil); err != nil {
				return err
			}
		}
		if err := insertStateChange(ctx, tx, &stateChangeRow{
			AgentID: string(t.Agent), FromState: &cur.State, ToState: string(t.To), Reason: t.Reason, At: at,
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// History returns an agent's state changes, oldest first.
func (a *Agents) History(ctx context.Context, id ident.AgentID) ([]StateChange, error) {
	var rows []stateChangeRow
	if err := a.db.NewSelect().Model(&rows).Where("agent_id = ?", string(id)).Order("id").Scan(ctx); err != nil {
		return nil, fmt.Errorf("state: reading history of agent %s: %w", id, err)
	}
	out := make([]StateChange, len(rows))
	for i, r := range rows {
		out[i] = StateChange{ID: r.ID, Agent: id, To: AgentState(r.ToState), Reason: r.Reason, At: fromMillis(r.At)}
		if r.FromState != nil {
			out[i].From = AgentState(*r.FromState)
		}
	}
	return out, nil
}

// SetConversationID records the harness's conversation id, which resumes
// the agent after a restart or an attach.
func (a *Agents) SetConversationID(ctx context.Context, id ident.AgentID, conversation string) error {
	return a.updateLive(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("conversation_id = ?", conversation)
	})
}

// RotateTokenHash replaces the agent's token hash. state.db keeps only
// hashes, so an agent resumed after a daemon restart gets a fresh token,
// and the old one stops resolving.
func (a *Agents) RotateTokenHash(ctx context.Context, id ident.AgentID, hash string) error {
	return a.updateLive(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("token_hash = ?", hash)
	})
}

// SetProgressNote records the agent's own one-line note.
func (a *Agents) SetProgressNote(ctx context.Context, id ident.AgentID, note string) error {
	return a.updateLive(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("progress_note = ?", note)
	})
}

// AddNudge counts one more nudge and returns the new count, so the caller
// can tell when the agent has had enough to be called stalled.
func (a *Agents) AddNudge(ctx context.Context, id ident.AgentID) (int, error) {
	var n int
	err := a.db.NewRaw(
		"UPDATE agents SET nudge_count = nudge_count + 1 WHERE id = ? AND ended_at IS NULL RETURNING nudge_count",
		string(id)).Scan(ctx, &n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, a.missingOrEnded(ctx, id)
	}
	if err != nil {
		return 0, fmt.Errorf("state: nudging agent %s: %w", id, err)
	}
	return n, nil
}

// ResetNudges zeroes the nudge count, once the agent shows it is working.
func (a *Agents) ResetNudges(ctx context.Context, id ident.AgentID) error {
	return a.updateLive(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("nudge_count = 0")
	})
}

// AddWorktree records one more worktree for a live agent. A repo or path it
// already has, or a second primary, is ErrConflict.
func (a *Agents) AddWorktree(ctx context.Context, id ident.AgentID, w *Worktree) error {
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := a.requireLive(ctx, tx, id); err != nil {
			return err
		}
		return insertWorktree(ctx, tx, id, w)
	})
}

// RemoveWorktree forgets a worktree once it is removed from disk. It works
// on ended agents too, since cleanup runs after the ticket ends, and an
// ended agent's row is pruned only once it has no worktrees left.
func (a *Agents) RemoveWorktree(ctx context.Context, id ident.AgentID, repo string) error {
	res, err := a.db.NewDelete().Model((*worktreeRow)(nil)).
		Where("agent_id = ?", string(id)).Where("repo = ?", repo).Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: removing worktree %s of agent %s: %w", repo, id, err)
	}
	return requireRow(res, "worktree %s of agent %s", repo, id)
}

// AcquireLease attaches the agent for l.Holder. It is refused with a
// *LeaseHeldError while another holder's lease is unexpired; an expired one
// is taken over, and the same holder acquiring again just extends it.
func (a *Agents) AcquireLease(ctx context.Context, id ident.AgentID, l *Lease) error {
	return a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var cur agentRow
		if err := tx.NewSelect().Model(&cur).Column("lease_holder", "lease_expires_at", "ended_at").
			Where("id = ?", string(id)).Scan(ctx); err != nil {
			return notFound(err, "agent %s", id)
		}
		if cur.EndedAt != nil {
			return fmt.Errorf("%w: agent %s", ErrEnded, id)
		}
		held := cur.LeaseExpiresAt != nil && *cur.LeaseExpiresAt > toMillis(a.clock.Now())
		if held && cur.LeaseHolder != l.Holder {
			return &LeaseHeldError{Holder: cur.LeaseHolder, ExpiresAt: fromMillis(*cur.LeaseExpiresAt)}
		}
		_, err := tx.NewUpdate().Table("agents").
			Set("mode = ?", string(ModeAttached)).Set("lease_holder = ?", l.Holder).
			Set("lease_expires_at = ?", toMillis(l.ExpiresAt)).Where("id = ?", string(id)).Exec(ctx)
		if err != nil {
			return fmt.Errorf("state: attaching agent %s: %w", id, err)
		}
		return nil
	})
}

// RenewLease extends an unexpired lease its holder has on a live agent: the
// heartbeat. A lease that has expired, been released or been taken over is
// ErrConflict, so a late heartbeat can neither re-attach an agent the
// daemon handed back nor race the expiry sweep; the CLI must re-acquire.
// An ended agent is ErrEnded, so the heartbeat learns it ended.
func (a *Agents) RenewLease(ctx context.Context, id ident.AgentID, l *Lease) error {
	res, err := a.db.NewUpdate().Table("agents").Set("lease_expires_at = ?", toMillis(l.ExpiresAt)).
		Where("id = ?", string(id)).Where("ended_at IS NULL").
		Where("mode = ?", string(ModeAttached)).Where("lease_holder = ?", l.Holder).
		Where("lease_expires_at > ?", toMillis(a.clock.Now())).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: renewing lease on agent %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if err := a.missingOrEnded(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s holds no unexpired lease on agent %s", ErrConflict, l.Holder, id)
}

// ReleaseLease hands the agent back to programmatic mode, if holder holds
// its lease, expired or not: detach and the CLI's own hand-back. An agent
// that ended while attached (a person called done from the TUI) can still be
// released.
func (a *Agents) ReleaseLease(ctx context.Context, id ident.AgentID, holder string) error {
	return a.release(ctx, id, holder, nil)
}

// ReleaseExpiredLease is the expiry sweep's ReleaseLease: it releases only
// if the lease is still expired at at, so a heartbeat or re-acquire that
// lands between ExpiredLeases and this call wins, with ErrConflict here.
func (a *Agents) ReleaseExpiredLease(ctx context.Context, id ident.AgentID, holder string, at time.Time) error {
	return a.release(ctx, id, holder, &at)
}

func (a *Agents) release(ctx context.Context, id ident.AgentID, holder string, expiredAt *time.Time) error {
	q := a.db.NewUpdate().Table("agents").
		Set("mode = ?", string(ModeProgrammatic)).Set("lease_holder = ''").Set("lease_expires_at = NULL").
		Where("id = ?", string(id)).Where("mode = ?", string(ModeAttached)).Where("lease_holder = ?", holder)
	if expiredAt != nil {
		q = q.Where("lease_expires_at <= ?", toMillis(*expiredAt))
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: releasing lease on agent %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s holds no releasable lease on agent %s", ErrConflict, holder, id)
	}
	return nil
}

func (a *Agents) one(ctx context.Context, db bun.IDB, where func(*bun.SelectQuery) *bun.SelectQuery) (*Agent, error) {
	var row agentRow
	if err := db.NewSelect().Model(&row).Apply(where).Limit(1).Scan(ctx); err != nil {
		return nil, notFound(err, "agent")
	}
	agents, err := a.withWorktrees(ctx, db, []agentRow{row})
	if err != nil {
		return nil, err
	}
	return agents[0], nil
}

// withWorktrees converts rows and loads all their worktrees in one query.
func (a *Agents) withWorktrees(ctx context.Context, db bun.IDB, rows []agentRow) ([]*Agent, error) {
	out := make([]*Agent, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	byID := make(map[string]*Agent, len(rows))
	ids := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].agent()
		byID[rows[i].ID] = out[i]
		ids[i] = rows[i].ID
	}
	var wts []worktreeRow
	err := db.NewSelect().Model(&wts).Where("agent_id IN (?)", bun.List(ids)).
		OrderExpr("agent_id, is_primary DESC, repo").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("state: loading worktrees: %w", err)
	}
	for _, w := range wts {
		ag := byID[w.AgentID]
		ag.Worktrees = append(ag.Worktrees, Worktree{Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.IsPrimary})
	}
	return out, nil
}

func (a *Agents) updateLive(ctx context.Context, id ident.AgentID, set func(*bun.UpdateQuery) *bun.UpdateQuery) error {
	res, err := a.db.NewUpdate().Table("agents").Apply(set).
		Where("id = ?", string(id)).Where("ended_at IS NULL").Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: updating agent %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return a.missingOrEnded(ctx, id)
	}
	return nil
}

// missingOrEnded explains why a live-only write matched no row.
func (a *Agents) missingOrEnded(ctx context.Context, id ident.AgentID) error {
	return a.requireLive(ctx, a.db, id)
}

func (a *Agents) requireLive(ctx context.Context, db bun.IDB, id ident.AgentID) error {
	var cur agentRow
	if err := db.NewSelect().Model(&cur).Column("ended_at").Where("id = ?", string(id)).Scan(ctx); err != nil {
		return notFound(err, "agent %s", id)
	}
	if cur.EndedAt != nil {
		return fmt.Errorf("%w: agent %s", ErrEnded, id)
	}
	return nil
}

func insertWorktree(ctx context.Context, db bun.IDB, id ident.AgentID, w *Worktree) error {
	row := &worktreeRow{AgentID: string(id), Repo: w.Repo, Path: w.Path, Branch: w.Branch, IsPrimary: w.Primary}
	if _, err := db.NewInsert().Model(row).Exec(ctx); err != nil {
		if sqlitex.IsUniqueViolation(err) {
			return fmt.Errorf("%w: worktree %s (%s) for agent %s: %w", ErrConflict, w.Repo, w.Path, id, err)
		}
		return fmt.Errorf("state: adding worktree %s for agent %s: %w", w.Repo, id, err)
	}
	return nil
}

func insertStateChange(ctx context.Context, db bun.IDB, row *stateChangeRow) error {
	if _, err := db.NewInsert().Model(row).Exec(ctx); err != nil {
		return fmt.Errorf("state: recording state change of agent %s: %w", row.AgentID, err)
	}
	return nil
}

// notFound turns sql.ErrNoRows into ErrNotFound, naming what was missing.
func notFound(err error, format string, args ...any) error {
	what := fmt.Sprintf(format, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return fmt.Errorf("state: reading %s: %w", what, err)
}

func requireRow(res sql.Result, format string, args ...any) error {
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, fmt.Sprintf(format, args...))
	}
	return nil
}
