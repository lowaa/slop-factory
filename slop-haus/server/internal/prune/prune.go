// Package prune keeps slop-haus's database from growing without bound.
//
// These tables grow with *agent activity* rather than human intent, which
// is the distinction that decides what is safe to delete:
//
//   - sessions: one row per `claude` run, and restarting an agent is cheap
//     and frequent.
//   - leases: one row per claim, so a busy fleet churns them.
//   - scope_blocks, scope_extension_denials, scope_narrowings and
//     lock_holds: the history behind the scope and lock reports, on the
//     lease clock.
//   - github_deliveries and intake_deliveries: one row per webhook or intake delivery, however
//     busy the repos behind them are.
//   - llm_usage: a row per model for every transcript an agent reports.
//     Kept far longer than the rest, since it is what a ticket cost; the
//     llm_* metrics are counted at ingest and never read it back, so
//     pruning it leaves them alone.
//   - pending_actions: what console agents propose for a person to
//     approve. Undecided ones are closed once they expire, and decided ones
//     deleted after PendingActionRetention.
//   - conductor_conversations: a conductor's conversations and their
//     transcripts and queued work, once ended (stopped or failed) and
//     untouched for ConversationRetention. A live one is never pruned.
//
// The one-time credentials of signing in -- activation and reset links,
// invitations, OAuth codes, device codes and tokens -- also pile up with
// use, and are worthless once expired, so they go a day after that.
//
// Everything else is bounded by a human doing something. Tickets, their
// comments, scopes and dependencies are the *work record* -- the thing the
// human is actually keeping -- and are deliberately never pruned here. If
// they ever need it, that is an archival decision someone makes on purpose,
// not a timer quietly deleting the project's history.
package prune

import (
	"time"
)

// Defaults chosen so an unconfigured server still behaves sensibly. Zero
// disables a given prune entirely, which is how you turn one off without
// having to invent a large number.
const (
	DefaultSessionRetention = 30 * 24 * time.Hour
	DefaultLeaseRetention   = 30 * 24 * time.Hour
	// DefaultDeliveryRetention is how long a GitHub webhook delivery's
	// record is kept: long enough to debug a webhook set up last week,
	// and to dedupe a redelivery, which GitHub offers for a few days.
	DefaultDeliveryRetention = 30 * 24 * time.Hour
	// DefaultLLMUsageRetention is how long a transcript's usage is kept
	// after its last report: a year of what each ticket cost.
	DefaultLLMUsageRetention = 365 * 24 * time.Hour
	// DefaultPendingActionRetention is how long a decided proposal of a
	// console agent is kept: the record of what a person was asked and what
	// they answered.
	DefaultPendingActionRetention = 90 * 24 * time.Hour
	// DefaultConversationRetention is how long an ended conductor
	// conversation, with its transcript, is kept after its last activity: the
	// record of what a person and the model said, which is personal text.
	DefaultConversationRetention = 90 * 24 * time.Hour
	DefaultInterval              = time.Hour

	// ExpiredCredentialGrace is how long an expired credential is kept
	// before it is deleted. Not zero: a rotated refresh token presented
	// again is recognised as a stolen copy -- and its grant revoked --
	// only while its row still exists, so it outlives its expiry a little.
	ExpiredCredentialGrace = 24 * time.Hour

	// UnclaimedClientGrace is how long a client registered through the open
	// registration endpoint may go without being authorized. Claude Code
	// registers and authorizes in one go, and an authorization request
	// lives ten minutes, so an hour-old client with no grant was abandoned
	// -- or is one of a flood of them.
	UnclaimedClientGrace = time.Hour

	// batchSize bounds how many rows one DELETE touches. A single
	// statement over a very large backlog holds locks for as long as it
	// takes; looping in batches keeps each one short and lets the run be
	// cancelled between them.
	batchSize = 1000
)

// Config is the retention policy. A zero Retention disables that prune.
type Config struct {
	SessionRetention time.Duration
	LeaseRetention   time.Duration
	// DeliveryRetention is how long GitHub webhook deliveries are kept.
	DeliveryRetention time.Duration
	// LLMUsageRetention is how long a transcript's usage is kept after its
	// last report.
	LLMUsageRetention time.Duration
	// PendingActionRetention is how long a decided proposal is kept. Expiry
	// of undecided ones is not governed by it: a proposal past its time is
	// always closed.
	PendingActionRetention time.Duration
	// ConversationRetention is how long an ended conductor conversation is
	// kept after its last activity. A live one is kept whatever its age.
	ConversationRetention time.Duration
	// Interval is how often the background loop runs.
	Interval time.Duration
}

// Result reports what one pass deleted, for the log line that makes this
// visible rather than mysterious.
type Result struct {
	Sessions int64
	Leases   int64
	// LeaseActivity is scope and integration lock history rows deleted.
	LeaseActivity int64
	Deliveries    int64
	LLMUsage      int64
	Credentials   int64
	// PendingActions is decided proposals deleted.
	PendingActions int64
	// Conversations is ended conductor conversations deleted.
	Conversations int64
}

// Empty reports whether the pass deleted nothing, so a quiet server does
// not log an hourly line saying so.
func (r Result) Empty() bool {
	return r.Sessions == 0 && r.Leases == 0 && r.LeaseActivity == 0 && r.Deliveries == 0 && r.LLMUsage == 0 && r.Credentials == 0 && r.PendingActions == 0 && r.Conversations == 0
}
