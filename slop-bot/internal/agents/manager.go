// Package agents runs slop-bot's agents: one per ticket, for the ticket's
// whole life. The Manager spawns an agent when a ticket becomes eligible,
// turns its harness's events into agent states, delivers messages, nudges an
// agent that stops without saying why, and finishes the agent when it says
// the ticket is done.
package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// Harnesses finds the implementation configured for a harness name.
type Harnesses interface {
	Harness(name string) (harness.AIHarness, bool)
}

// Worktrees creates and cleans up a ticket's worktrees.
type Worktrees interface {
	Create(ctx context.Context, ticket ident.TicketID, repos []string, primary string) ([]*worktree.Worktree, error)
	Cleanup(ctx context.Context, ticket ident.TicketID, wts []*worktree.Worktree) []*worktree.CleanupResult
	// Check is what Cleanup would keep a worktree for, removing nothing.
	Check(ctx context.Context, ticket ident.TicketID, wt *worktree.Worktree) ([]worktree.Reason, error)
	// ForceCleanup discards a worktree whatever it holds, on the user's
	// say-so.
	ForceCleanup(ctx context.Context, ticket ident.TicketID, wt *worktree.Worktree) error
}

// Options are the Manager's dependencies.
type Options struct {
	Config    *config.Config
	Paths     *paths.Paths
	State     *state.State
	Tickets   tickets.TicketingSystem
	Harnesses Harnesses
	Worktrees Worktrees
	// User is who slop-bot runs for: eligible tickets are assigned to them.
	User   tickets.UserID
	Logger *slog.Logger
	Clock  clock.Clock
	// CLI is the absolute path agents run slop-bot by.
	CLI string
	// CLIReference is the briefing's command list, generated from the CLI's
	// command tree so it can't drift from it.
	CLIReference string
	// StartTimeout bounds starting or resuming one agent, git and the
	// harness's handshake included; zero means DefaultStartTimeout.
	StartTimeout time.Duration
	// Transcripts keeps an agent's transcript once it ends.
	Transcripts Retainer
}

// Retainer copies an ended agent's transcript to where slop-bot keeps it
// (transcript.Library).
type Retainer interface {
	Retain(ctx context.Context, a *state.Agent) error
}

// DefaultStartTimeout is Options.StartTimeout's default: generous, since a
// harness's first start may install or update itself.
const DefaultStartTimeout = 2 * time.Minute

// Manager runs every agent. It is a lifecycle component: Start resumes the
// agents that were running and starts scheduling; Stop drains them.
type Manager struct {
	opts *Options

	mu      sync.Mutex
	runners map[ident.AgentID]*runner
	// wantSlot are idle agents with a message to deliver and no free slot,
	// in the order they started waiting: an answered agent goes ahead of
	// new work.
	wantSlot []ident.AgentID
	// starting are tickets whose agent is being started or resumed off the
	// scheduler goroutine, with the pool it starts in: each holds a slot and
	// a live place until its start settles, so starts in flight can't
	// overfill a pool.
	starting map[ident.TicketID]state.Pool
	// recoverAfter are agents whose harness died while a start for their
	// ticket was still in flight (a resume, still delivering): each is
	// recovered once that start settles, rather than lost.
	recoverAfter map[ident.TicketID]*runner
	stopping     bool
	// streams counts the attached CLIs' open streams per agent.
	streams map[ident.AgentID]int

	limits *limits
	// wake fires when the earliest held harness's window resets, at
	// wakeTime; only the scheduler goroutine touches them.
	wake     <-chan time.Time
	wakeTime time.Time
	// guardTold is when the user was last told the loop guard stopped a
	// pair, so a refused retry doesn't tell them again within the window.
	guardTold map[string]time.Time

	// syncMu serialises keeping an agent in step with its ticket: the
	// scheduler does it on every ticket event, and a finished start does it
	// once, and two at once would both send the same notice.
	syncMu sync.Mutex

	// base is cancelled by Stop, so a start in flight gives up rather than
	// holding the stop.
	base       context.Context
	cancelBase context.CancelFunc
	// starts are the goroutines starting or resuming agents; ends, those
	// stopping and cleaning up finished ones. Stop waits for both. Each Add
	// happens before Stop waits: starts under mu while not stopping, ends
	// from the scheduler, a start or a runner, which Stop waits out first.
	starts sync.WaitGroup
	ends   sync.WaitGroup

	kick chan struct{}
	stop chan struct{}
	done chan struct{}
	// swept is closed when the lease sweeper has stopped.
	swept chan struct{}
}

// New makes a Manager; nothing runs until Start.
func New(opts *Options) (*Manager, error) {
	if opts == nil || opts.Config == nil || opts.Paths == nil || opts.State == nil || opts.Tickets == nil ||
		opts.Harnesses == nil || opts.Worktrees == nil || opts.Logger == nil || opts.Clock == nil || opts.Transcripts == nil {
		return nil, errors.New("agents: New is missing a dependency")
	}
	base, cancel := context.WithCancel(context.Background())
	return &Manager{
		opts:         opts,
		runners:      map[ident.AgentID]*runner{},
		starting:     map[ident.TicketID]state.Pool{},
		recoverAfter: map[ident.TicketID]*runner{},
		streams:      map[ident.AgentID]int{},
		limits:       newLimits(),
		guardTold:    map[string]time.Time{},
		base:         base,
		cancelBase:   cancel,
		kick:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		swept:        make(chan struct{}),
	}, nil
}

// Start resumes live agents, each on its own goroutine, and starts the
// scheduler.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.loadLimits(ctx); err != nil {
		return err
	}
	if err := m.resumeAll(ctx); err != nil {
		return err
	}
	go m.schedule() // #nosec G118 -- the scheduler outlives Start's ctx; Stop ends it
	go m.sweeper()  // #nosec G118 -- like the scheduler, it runs until Stop
	m.Kick()
	return nil
}

// Stop drains: each agent finishes its current turn and stops, so it can be
// resumed by id when the daemon starts again. Starts in flight are
// abandoned. ctx bounds the wait: past it, agents are killed and Stop
// returns ctx's error without waiting for the rest.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return nil
	}
	m.stopping = true
	// Marked now, so a turn that ends from here on starts nothing new;
	// adopt marks the runners that starts in flight bring.
	for _, r := range m.runners {
		r.mu.Lock()
		r.stopping = true
		r.mu.Unlock()
	}
	m.mu.Unlock()
	close(m.stop)
	m.cancelBase()
	if err := waitCtx(ctx, m.done); err != nil {
		return err
	}
	if err := waitCtx(ctx, m.swept); err != nil {
		return err
	}
	if err := waitGroup(ctx, &m.starts); err != nil {
		return err
	}
	// Only now is the set of runners final: a start that was in flight has
	// adopted its runner, or given up.
	m.mu.Lock()
	runners := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		runners = append(runners, r)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Go(func() { r.shutdown(ctx) })
	}
	wg.Wait()
	// An agent that finished during the drain is cleaned up before the
	// daemon closes the databases under it.
	return waitGroup(ctx, &m.ends)
}

// startTimeout is Options.StartTimeout, defaulted.
func (m *Manager) startTimeout() time.Duration {
	if m.opts.StartTimeout > 0 {
		return m.opts.StartTimeout
	}
	return DefaultStartTimeout
}

// goStart runs start on its own goroutine, holding a place for ticket's
// agent in pool until it returns, with a context Stop cancels and
// startTimeout bounds. When admit is set it first checks the pool has room
// (see admits), and reports false, starting nothing, when it hasn't, or
// when the manager is stopping.
func (m *Manager) goStart(ticket ident.TicketID, pool state.Pool, admit bool, start func(context.Context)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.goStartLocked(ticket, pool, admit, start)
}

// goStartLocked is goStart, holding mu.
func (m *Manager) goStartLocked(ticket ident.TicketID, pool state.Pool, admit bool, start func(context.Context)) bool {
	if m.stopping {
		return false
	}
	if _, busy := m.starting[ticket]; busy || (admit && !m.admitsLocked(pool)) {
		return false
	}
	m.starting[ticket] = pool
	m.starts.Go(func() {
		defer m.settled(ticket)
		ctx, cancel := context.WithTimeout(m.base, m.startTimeout())
		defer cancel()
		start(ctx)
	})
	return true
}

func (m *Manager) settled(ticket ident.TicketID) {
	m.mu.Lock()
	delete(m.starting, ticket)
	if r, ok := m.recoverAfter[ticket]; ok {
		delete(m.recoverAfter, ticket)
		m.goStartLocked(ticket, r.pool, false, func(ctx context.Context) { m.recover(ctx, r.id) })
	}
	m.mu.Unlock()
	m.Kick()
}

// goRecover recovers r, whose harness died, on its own goroutine: now, or
// once a start in flight for its ticket settles. A daemon stopping leaves
// it to the next start.
func (m *Manager) goRecover(r *runner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.goStartLocked(r.ticket, r.pool, false, func(ctx context.Context) { m.recover(ctx, r.id) }) || m.stopping {
		return
	}
	m.recoverAfter[r.ticket] = r
}

func (m *Manager) isStarting(ticket ident.TicketID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.starting[ticket]
	return ok
}

func waitCtx(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitGroup(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return waitCtx(ctx, done)
}

// Kick asks the scheduler to look again.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) runner(id ident.AgentID) (*runner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[id]
	return r, ok
}

func (m *Manager) forget(id ident.AgentID) {
	m.mu.Lock()
	delete(m.runners, id)
	// A gone agent mustn't hold its pool's queue.
	m.wantSlot = slices.DeleteFunc(m.wantSlot, func(w ident.AgentID) bool { return w == id })
	m.mu.Unlock()
	m.Kick()
}

// event records something for `slop-bot events` and the tests that follow it.
func (m *Manager) event(ctx context.Context, typ string, agent ident.AgentID, ticket ident.TicketID, payload any) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err == nil {
			raw = b
		}
	}
	if _, err := m.opts.State.Events.Append(ctx, &state.NewEvent{Type: state.EventType(typ), Agent: agent, Ticket: ticket, Payload: raw}); err != nil {
		m.opts.Logger.Warn("agents: recording an event", "type", typ, "err", err)
	}
}

// retain keeps an ended agent's transcript, because the harness prunes its
// own. A failure costs the copy, not the ending, so it's told, not
// returned.
func (m *Manager) retain(ctx context.Context, id ident.AgentID) {
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err == nil {
		err = m.opts.Transcripts.Retain(ctx, a)
	}
	// An agent that never got a conversation going has nothing to keep;
	// one that did and has no transcript is worth knowing about.
	if errors.Is(err, transcript.ErrNotFound) && a != nil && a.ConversationID == "" {
		return
	}
	if err != nil {
		m.opts.Logger.Warn("agents: retaining a transcript", "agent", id, "err", err)
		m.tellUser(ctx, id, fmt.Sprintf("agent %s's transcript couldn't be kept: %v", id, err))
		return
	}
	m.event(ctx, EventRetained, id, a.Ticket, nil)
}

// TellUser puts a system message from slop-bot in the user's inbox.
func (m *Manager) TellUser(ctx context.Context, body string) { m.tellUser(ctx, "", body) }

// tellUser puts a system message in the user's inbox.
func (m *Manager) tellUser(ctx context.Context, from ident.AgentID, body string) {
	sender := state.UserParty
	if from != "" {
		sender = state.AgentParty(from)
	}
	if _, err := m.opts.State.Messages.Enqueue(ctx, &state.NewMessage{From: sender, To: state.UserParty, Kind: state.KindSystem, Body: body}); err != nil {
		m.opts.Logger.Warn("agents: telling the user", "err", err)
	}
	m.event(ctx, EventUserTold, from, "", map[string]string{"body": body})
}

func newAgentID(ticket ident.TicketID) (ident.AgentID, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ident.AgentID(fmt.Sprintf("t%s-%s", ticket, hex.EncodeToString(b))), nil
}

// Event types the Manager records.
const (
	EventSpawned      = "agent.spawned"
	EventResumed      = "agent.resumed"
	EventState        = "agent.state"
	EventDelivered    = "message.delivered"
	EventQueued       = "message.queued"
	EventNudged       = "agent.nudged"
	EventUserTold     = "user.told"
	EventFinished     = "agent.finished"
	EventReleased     = "agent.released"
	EventSpawnFailed  = "agent.spawn_failed"
	EventMisconfigure = "ticket.misconfigured"
	// EventHeld is a ticket waiting for its harness's plan window to reset.
	EventHeld = "ticket.held"
	// EventRetained is an ended agent's transcript, kept.
	EventRetained = "transcript.retained"
	// EventHarness prefixes the conversation's own events: harness.text,
	// harness.tool_started, and so on, which `slop-bot watch` shows.
	EventHarness = "harness."
)
