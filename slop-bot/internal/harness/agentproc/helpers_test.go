package agentproc

import (
	"fmt"
	"time"
)

// The grandchild's exit is asynchronous to Kill's return (init reaps it), so
// this is a bounded wait on a condition, not a sleep.
const (
	testWait = 5 * time.Second
	testTick = 5 * time.Millisecond
)

func fmtSscan(s string, v *int) (int, error) { return fmt.Sscan(s, v) }
