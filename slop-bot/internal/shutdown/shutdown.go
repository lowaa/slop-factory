// Package shutdown is the daemon's stop budget, in one place: the daemon
// bounds its stop by these, and the service manager's kill timeout is
// derived from their sum, so the supervisor never kills a daemon that is
// still closing its stores. It imports nothing of slop-bot's, so service
// can read it without importing the daemon.
package shutdown

import "time"

const (
	// Drain bounds the components' Stop: past it they give up waiting
	// (the agents are forced to stop).
	Drain = 30 * time.Second
	// Grace is what the agents' drain gives, past Drain, to what it forced
	// or still waits for: a forced harness's exit, starts, cleanups.
	Grace = 10 * time.Second
	// ExitWait bounds the wait, after the components' Stop, for those that
	// outlived it (lifecycle.Group.BoundExits).
	ExitWait = 10 * time.Second
)

// Worst is the longest a daemon's stop can take: the drain, its grace,
// and the exit wait, one after another.
const Worst = Drain + Grace + ExitWait
