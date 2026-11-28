// Package api holds the wire types the daemon and the CLI exchange over the
// control socket. Both sides import it, so a field renamed on one side is a
// compile error on the other.
package api

import "time"

// Health answers GET /v1/health.
type Health struct {
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Socket    string    `json:"socket"`
}

// Route paths.
const (
	PathHealth = "/v1/health"
)
