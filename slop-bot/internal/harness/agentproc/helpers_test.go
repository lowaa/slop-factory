package agentproc

import (
	"fmt"
	"time"
)

// The grandchild's exit is asynchronous to Kill's return (init reaps it), so
// this is a bounded wait on a condition, not a sleep. The bound is generous:
// a loaded machine can take seconds to reap.
const (
	testWait = 30 * time.Second
	testTick = 5 * time.Millisecond
)

func fmtSscan(s string, v *int) (int, error) { return fmt.Sscan(s, v) }
