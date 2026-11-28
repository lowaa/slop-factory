package fake

import (
	"context"
	"log/slog"
	"os"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// Name and Driver of the fake, as tests register it.
const (
	Name   = "fake"
	Driver = "rpc"
)

// Harness runs the fakeharness binary as an rpc-driven harness.
type Harness struct {
	// Binary is the built fakeharness.
	Binary string
	// Control is the Controller's socket, or "" for an autonomous fake.
	Control string
	Logger  *slog.Logger
	Clock   clock.Clock
}

// Name implements harness.AIHarness.
func (h *Harness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *Harness) Driver() string { return Driver }

// Capabilities implements harness.AIHarness.
func (h *Harness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true}
}

// Start implements harness.AIHarness.
func (h *Harness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	set := map[string]string{}
	for k, v := range ws.Env {
		set[k] = v
	}
	if h.Control != "" {
		set[EnvControl] = h.Control
	}
	var args []string
	if resume != nil {
		args = []string{"--resume", resume.ID}
	}
	spec := &harness.LaunchSpec{
		Path: h.Binary,
		Args: args,
		Env:  agentproc.AgentEnv(&agentproc.EnvOptions{Base: os.Environ(), APIKeyOptIn: ws.APIKeyOptIn, Set: set}),
		Dir:  ws.Dir,
	}
	return rpcdriver.Start(ctx, &rpcdriver.Options{
		Harness: Name, Driver: Driver, Codec: codec{}, Spec: spec,
		Logger: h.Logger, Clock: h.Clock,
	})
}
