package tuidriver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// Sessions is what a TUI harness runs its sessions with: all of Options but
// its profile and each session's name. A harness embeds it.
type Sessions struct {
	Host sessionhost.SessionHost
	Hub  HookHub
	// Pointer is Options.Pointer.
	Pointer func(msgs []*harness.Message) string
	Logger  *slog.Logger
	Clock   clock.Clock
	// PollInterval, HookTrust, ConfirmTimeout and RenderTimeout are
	// Options'; zero is the kit's default.
	PollInterval   time.Duration
	HookTrust      time.Duration
	ConfirmTimeout time.Duration
	RenderTimeout  time.Duration
	// Guard is Options.Guard.
	Guard Guard
}

// Options are p's for the session named session.
func (s *Sessions) Options(p *Profile, session string) *Options {
	return &Options{
		Profile: p, Host: s.Host, Hub: s.Hub, Session: session,
		Pointer: s.Pointer, Logger: s.Logger, Clock: s.Clock,
		PollInterval: s.PollInterval, HookTrust: s.HookTrust, ConfirmTimeout: s.ConfirmTimeout, RenderTimeout: s.RenderTimeout,
		Guard: s.Guard,
	}
}

// Start launches p's session for ws's agent, named with prefix (see
// SessionName).
func (s *Sessions) Start(ctx context.Context, p *Profile, prefix string, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	conv, err := Start(ctx, s.Options(p, SessionName(prefix, ws.Agent)), ws, resume)
	if err != nil {
		return nil, err
	}
	return conv, nil
}

// Adopt takes over agent's session, p's, named with prefix, which outlived
// the daemon that started it: harness.ErrNoSession if there is none.
func (s *Sessions) Adopt(ctx context.Context, p *Profile, prefix string, agent ident.AgentID) (harness.Conversation, error) {
	session := SessionName(prefix, agent)
	conv, err := Adopt(ctx, s.Options(p, session))
	if errors.Is(err, sessionhost.ErrNoSession) || errors.Is(err, sessionhost.ErrNoServer) {
		return nil, fmt.Errorf("%w: %s: %w", harness.ErrNoSession, session, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: adopting %s: %w", p.Harness, session, err)
	}
	return conv, nil
}

// SessionName is an agent's session: prefix, then the agent's id with
// anything tmux would read specially replaced. A prefix is the harness's
// own, lowercase letters then a dash, so that a name says whose session it
// is (see OwnsSession).
func SessionName(prefix string, agent ident.AgentID) string {
	return prefix + sanitise(agent)
}

// OwnsSession says whether session is agent's, under any harness's prefix:
// a hook relayed for a session must come from that session's own agent.
func OwnsSession(session string, agent ident.AgentID) bool {
	prefix, rest, ok := strings.Cut(session, "-")
	if !ok || prefix == "" || strings.Trim(prefix, "abcdefghijklmnopqrstuvwxyz") != "" {
		return false
	}
	return rest == sanitise(agent)
}

func sanitise(agent ident.AgentID) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, string(agent))
}
