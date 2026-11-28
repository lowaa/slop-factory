// Package servicetest has a fake service.Runner, so tests drive a Manager
// without touching the real systemd or launchd.
package servicetest

import (
	"context"
	"strings"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// Runner records every command and answers with what the test set. A
// command with no answer succeeds with no output; tests assert the call
// list, so an unexpected command still shows.
type Runner struct {
	mu      sync.Mutex
	calls   []string
	answers map[string]answer
}

type answer struct {
	res *service.Result
	err error
}

// NewRunner returns an empty fake.
func NewRunner() *Runner { return &Runner{answers: map[string]answer{}} }

// Answer sets what the command line (name and args, space-joined) returns.
func (r *Runner) Answer(cmdline string, res *service.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[cmdline] = answer{res: res}
}

// Fail makes the command line fail to run at all.
func (r *Runner) Fail(cmdline string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[cmdline] = answer{err: err}
}

// Run records the call and returns its answer.
func (r *Runner) Run(_ context.Context, name string, args ...string) (*service.Result, error) {
	cmdline := strings.Join(append([]string{name}, args...), " ")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, cmdline)
	a, ok := r.answers[cmdline]
	if !ok {
		return &service.Result{}, nil
	}
	if a.err != nil {
		return nil, a.err
	}
	res := *a.res
	return &res, nil
}

// Calls returns the command lines run so far, in order.
func (r *Runner) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// Reset forgets the calls, keeping the answers.
func (r *Runner) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}
