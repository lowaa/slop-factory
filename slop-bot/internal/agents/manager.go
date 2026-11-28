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
	"sync"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
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
}

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
	stopping bool

	kick chan struct{}
	stop chan struct{}
	done chan struct{}
}

// New makes a Manager; nothing runs until Start.
func New(opts *Options) (*Manager, error) {
	if opts == nil || opts.Config == nil || opts.Paths == nil || opts.State == nil || opts.Tickets == nil ||
		opts.Harnesses == nil || opts.Worktrees == nil || opts.Logger == nil || opts.Clock == nil {
		return nil, errors.New("agents: New is missing a dependency")
	}
	return &Manager{
		opts:    opts,
		runners: map[ident.AgentID]*runner{},
		kick:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// Name implements lifecycle.Component.
func (m *Manager) Name() string { return "agents" }

// Start resumes live agents and starts the scheduler.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.resumeAll(ctx); err != nil {
		return err
	}
	go m.schedule()
	m.Kick()
	return nil
}

// Stop drains: each agent finishes its current turn and stops, so it can be
// resumed by id when the daemon starts again. ctx bounds the wait; past it,
// agents are killed.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return nil
	}
	m.stopping = true
	runners := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		runners = append(runners, r)
	}
	m.mu.Unlock()
	close(m.stop)
	<-m.done
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Go(func() { r.shutdown(ctx) })
	}
	wg.Wait()
	return nil
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
	EventNudged       = "agent.nudged"
	EventUserTold     = "user.told"
	EventFinished     = "agent.finished"
	EventSpawnFailed  = "agent.spawn_failed"
	EventMisconfigure = "ticket.misconfigured"
)
