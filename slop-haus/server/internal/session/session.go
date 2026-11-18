// Package session tracks one `claude` run per agent: the liveness signal
// and the loop-protection counters the Stop hook decides on.
//
// Leases are deliberately *not* session-scoped (see internal/ticket): a new
// run is the same agent, so it simply still holds its ticket. That makes
// restart adoption a non-event rather than a race between two sessions that
// both appear to own the work, and it is why nothing here needs to reach
// into a lease.
package session

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// MaxStopBlocks is the loop breaker. After this many consecutive stop
// blocks with no progress in between, the server stops blocking and lets
// the agent stop.
//
// There is no documented stop_hook_active flag to lean on, and leaning on
// one would be weaker anyway: a flag catches "blocked twice in a row" but
// not "agent insists it is done while its ticket is still open". Counting
// blocks against *progress* catches both, and doubles as the stuck-agent
// detector.
const MaxStopBlocks = 3

type Session struct {
	ID              int64
	AgentID         uuid.UUID
	ClaudeSessionID string
	CWD             string
	StartedAt       time.Time
	LastSeenAt      time.Time
	EndedAt         *time.Time
	LastProgressAt  time.Time
	LastBlockAt     *time.Time
	StopBlockCount  int
	// Stuck is set when the loop breaker let this run stop for want of
	// progress, and cleared by the agent's next progress.
	Stuck *Stuck
	// Description is what the run says about itself (see Description).
	Description Description
}

// Stuck is why the loop breaker let a run stop, and when.
type Stuck struct {
	Reason string
	At     time.Time
}

// Current reports whether this is the agent's live session.
func (s *Session) Current() bool { return s.EndedAt == nil }

// Arrival describes how a session-start was resolved, which is what makes
// the hook idempotent.
type Arrival string

const (
	// ArrivalNew is a genuinely new run: the previous session was ended and
	// this one opened.
	ArrivalNew Arrival = "new"
	// ArrivalResumed is the same session delivered twice -- a hook retry
	// after a client timeout. Nothing is written; in particular the
	// loop-protection counters are left alone, which a close-then-insert
	// would have reset.
	ArrivalResumed Arrival = "resumed"
	// ArrivalStale is a retry that lost to a newer session. It must not
	// resurrect itself over the live one, so it is told what the current
	// session is and nothing is written.
	ArrivalStale Arrival = "stale"
)

// StartResult is what the session-start hook acts on.
type StartResult struct {
	Session *Session
	Arrival Arrival
	// ConcurrentRun is set when the session being replaced was still within
	// its liveness window, which means two `claude` processes are live in
	// one workspace.
	//
	// The server detects this rather than preventing it. Fencing would mean
	// putting a session id into every agent request, reintroducing exactly
	// the propagation problem agent-owned leases removed -- and it would not
	// help anyway, since two live processes in one checkout are already
	// fighting over the same working tree, which no server-side lock can
	// fix. It is a human-level mistake with a human-level fix: close one of
	// the terminals.
	ConcurrentRun bool
}

// State is what a hook callback's session id turned out to be.
//
// "Not the current session" is two different situations and they need
// opposite answers, which an earlier version conflated into a bool:
//
//   - Superseded: a *newer* session has replaced this one. Its callbacks
//     must not touch the live session's counters, and it should stop.
//   - Unknown: this agent has no current session at all, because
//     SessionStart never ran -- the hook was misconfigured, or the run
//     predates it. Treating that as superseded silently disables the loop
//     for a perfectly live agent, and because the loop breaker counts
//     against a session row that does not exist, it could not even stop
//     the agent blocking forever. It needs binding, not dismissing.
type State string

const (
	StateLive       State = "live"
	StateSuperseded State = "superseded"
	StateUnknown    State = "unknown"
)

var ErrNotFound = errors.New("session: not found")

// LivenessWindow is how recently a session must have been seen for a
// replacement to count as a concurrent run rather than an ordinary restart.
const LivenessWindow = 5 * time.Minute
