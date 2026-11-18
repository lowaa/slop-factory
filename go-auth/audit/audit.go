// Package audit records security events. An event is a log line -- message
// "audit", with a stable action field that log queries key on, and the
// client address -- and a count, which alerts key on.
//
// Each app names its own events (its action set) and counts them however
// it counts things: the Counter it passes in owns the metric's name, so an
// app's dashboards and alert rules stay its own. The actions go-auth's own
// packages record are declared here.
package audit

import (
	"context"
	"log/slog"

	"github.com/lowaa/slop-factory/go-auth/clientip"
)

// Action names an event. Its values are the log's action field and the
// counter's action label, so they are part of what dashboards and alerts
// are written against: add new ones freely, but rename none.
type Action string

// The actions go-auth records itself. Both are warnings: see Config.
const (
	// CrossOriginRefused and CSRFRefused are browser writes refused by
	// authn.BrowserGuard.
	CrossOriginRefused Action = "cross_origin_refused"
	CSRFRefused        Action = "csrf_refused"
)

// CoreActions is every action go-auth records itself, for an app to
// include in the set it pre-creates series for.
func CoreActions() []Action {
	return []Action{CrossOriginRefused, CSRFRefused}
}

// Counter counts events.
type Counter interface {
	AuditEvent(action, method string)
}

type nopCounter struct{}

func (nopCounter) AuditEvent(string, string) {}

// Config configures a Recorder.
type Config struct {
	// Logger is where each event's line goes.
	Logger slog.Handler
	// Counter counts each event; nil counts nothing.
	Counter Counter
	// Warnings are the app's actions logged at warn rather than info: each
	// one means something is wrong, not just that something happened.
	// go-auth's own refusals (CoreActions) are always warnings.
	Warnings []Action
}

// Recorder records events. Safe for concurrent use.
type Recorder struct {
	logger   *slog.Logger
	counter  Counter
	warnings map[Action]bool
}

// New builds a Recorder from cfg.
func New(cfg *Config) *Recorder {
	counter := cfg.Counter
	if counter == nil {
		counter = nopCounter{}
	}
	warnings := map[Action]bool{}
	for _, a := range CoreActions() {
		warnings[a] = true
	}
	for _, a := range cfg.Warnings {
		warnings[a] = true
	}
	return &Recorder{logger: slog.New(cfg.Logger), counter: counter, warnings: warnings}
}

// Record records action with attrs, slog-style key/value pairs, and the
// client address from ctx (see clientip). A "method" attr -- how someone
// signed in -- is also the counter's method label.
func (r *Recorder) Record(ctx context.Context, action Action, attrs ...any) {
	level := slog.LevelInfo
	if r.warnings[action] {
		level = slog.LevelWarn
	}
	r.logger.Log(ctx, level, "audit",
		append([]any{"action", string(action), "client_ip", clientip.FromContext(ctx).String()}, attrs...)...)
	r.counter.AuditEvent(string(action), methodOf(attrs))
}

func methodOf(attrs []any) string {
	for i := 0; i+1 < len(attrs); i += 2 {
		if k, ok := attrs[i].(string); ok && k == "method" {
			if v, ok := attrs[i+1].(string); ok {
				return v
			}
		}
	}
	return ""
}
