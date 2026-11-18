package main

import (
	"context"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
)

// closers are what the api has opened and must close on the way out,
// closed in the reverse of the order they were pushed: whatever is opened
// later may use what was opened before it, so it has to stop first.
type closers struct {
	stack []namedCloser
	// closing, if set, is told each closer's name just before it runs. It
	// is how a test sees the shutdown's order; nothing else sets it.
	closing func(name string)
}

type namedCloser struct {
	name  string
	close func()
}

func (c *closers) push(name string, close func()) {
	c.stack = append(c.stack, namedCloser{name: name, close: close})
}

// close runs every closer, last pushed first, and empties the stack. Like
// the defers it stands in for, a closer that panics still leaves the rest
// to run: the database is still closed and the logs still flushed.
func (c *closers) close() {
	if len(c.stack) == 0 {
		return
	}
	top := c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
	defer c.close()
	if c.closing != nil {
		c.closing(top.name)
	}
	top.close()
}

// startJob starts job on sched and pushes its stop under the job's name. The
// stop cancels the job and waits for its run, so whatever it uses, pushed
// before it, cannot be closed under a run still going. Stopped by the closer
// rather than left to ctx: the servers also stop on a listen failure, which
// never cancels ctx. A nil job (one that is configured off) is skipped.
func (c *closers) startJob(ctx context.Context, sched *jobs.Scheduler, job *jobs.Job) error {
	if job == nil {
		return nil
	}
	stop, err := sched.Start(ctx, job)
	if err != nil {
		return err
	}
	c.push(job.Name, stop)
	return nil
}
