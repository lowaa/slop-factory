// Package agy is the agy/tui harness: Google's Antigravity CLI, its own TUI
// run in tmux under the tuidriver kit.
//
// It runs on the user's own agy login and settings (~/.gemini), adding only
// session-only flags and a sidecar folder beside the worktree for the
// session's hooks and briefing (see sidecar). On a consumer Google sign-in,
// driving agy from another program is against Google's terms; that is the
// user's call, for doctor to raise (see the design doc's terms caveat).
package agy

import (
	"context"
	"log/slog"
	"os"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// Name is the harness, as the config and tickets spell it.
const Name = "agy"

// sessionPrefix names agy's sessions on the host, so Adopt finds its own.
const sessionPrefix = "agy-"

// Harness runs agy.
type Harness struct {
	tuidriver.Sessions
	// Binary is the agy executable; "" finds it on PATH.
	Binary string
	// Environ is the base environment; nil means os.Environ.
	Environ func() []string
}

// Name implements harness.AIHarness.
func (h *Harness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *Harness) Driver() string { return tuidriver.Driver }

// Capabilities implements harness.AIHarness: PreInvocation injects before
// every model call. agy picks its own conversation ids.
func (h *Harness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true}
}

// Start implements harness.AIHarness.
func (h *Harness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	return h.Sessions.Start(ctx, h.Profile(), sessionPrefix, ws, resume)
}

// AdoptAgent implements harness.Adopter.
func (h *Harness) AdoptAgent(ctx context.Context, agent ident.AgentID) (harness.Conversation, error) {
	return h.Adopt(ctx, h.Profile(), sessionPrefix, agent)
}

// ForgetWorkspace removes what Start wrote for ws, its sidecar folder, once
// the agent is done with the workspace.
func (h *Harness) ForgetWorkspace(ws *harness.Workspace) error {
	return os.RemoveAll(sidecar(ws))
}

func (h *Harness) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return h.Logger
}
