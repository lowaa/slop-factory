package tuidriver

import (
	"context"
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
	// PollInterval and ConfirmTimeout are Options'; zero is the kit's
	// default.
	PollInterval   time.Duration
	ConfirmTimeout time.Duration
}

// Options are p's for the session named session.
func (s *Sessions) Options(p *Profile, session string) *Options {
	return &Options{
		Profile: p, Host: s.Host, Hub: s.Hub, Session: session,
		Pointer: s.Pointer, Logger: s.Logger, Clock: s.Clock,
		PollInterval: s.PollInterval, ConfirmTimeout: s.ConfirmTimeout,
	}
}

// jscpd:ignore-start -- fake/tuiharness.go has its own copy of AdoptAll and
// SessionName from before these were shared; moving fake/tui onto Sessions
// is left to its own change.

// AdoptAll adopts every session on the host whose name starts with prefix,
// the harness's own: those that outlived a daemon.
func (s *Sessions) AdoptAll(ctx context.Context, p *Profile, prefix string) ([]harness.Conversation, error) {
	names, err := s.Host.List(ctx)
	if err != nil {
		return nil, err
	}
	var convs []harness.Conversation
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		conv, err := Adopt(ctx, s.Options(p, name))
		if err != nil {
			return convs, fmt.Errorf("%s: adopting %s: %w", p.Harness, name, err)
		}
		convs = append(convs, conv)
	}
	return convs, nil
}

// SessionName is an agent's session: prefix, then the agent's id with
// anything tmux would read specially replaced.
func SessionName(prefix string, agent ident.AgentID) string {
	return prefix + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, string(agent))
}

// jscpd:ignore-end
