package codehosttest

import (
	"sync"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// CI makes a Fake report checks the way GitHub with this repository's
// Woodpecker does, instead of answering whatever ChecksOf holds. Set Fake.CI
// to opt in; a Fake without one keeps answering from ChecksOf and
// BranchChecksOf for any change or branch, which is more generous than the
// real thing (batch branches got no CI in prod because CI only runs
// for pull requests, and the generous fake reported checks for them anyway).
//
// Modelled behaviours, and where each comes from:
//
//   - A pipeline exists only for a commit that has an open change (draft or
//     not) pointing at it. A bare branch has no checks. Source: Woodpecker's
//     workflows trigger on pull_request, cron and manual only.
//   - Checks are attached to the head SHA, never to a branch name or the
//     merge ref: a commit that gets a pipeline shows its checks to a branch
//     read and a change read alike, and a head that moved shows none until
//     the new head has its own pipeline. Source: GitHub's commit status and
//     check run APIs are keyed by SHA.
//   - A pipeline is not instant: until Complete (or at once, when Manual is
//     false) its commit has no checks, and "no checks" is not a pass.
//   - A pipeline can fail before it starts (Script returning
//     ConfigErrorChecks): no checks, then one failed check. Source:
//     Woodpecker answering "could not load config from forge".
//   - Closing a change starts no new pipelines. A pipeline already started
//     keeps its result, and closing never completes a pending one: a
//     pull_request_closed delivery does not make checks pass. Source:
//     Woodpecker's pull_request trigger, GitHub's closed pull requests.
//   - With Strict, the fake also keeps the changes it opened and refuses what
//     GitHub refuses (see Strict), reading a change's head, base and state
//     from them when the test configured none. Source: the contract suite,
//     which runs the same cases against GitHub.
//   - A change is only registered by OpenChange. The fake observes a head
//     moving only when it reads the change, because tests move the head by
//     editing ChecksOf or BranchAt, so the pipeline for a new head starts at
//     the first read of it, while the change is open.
//
// Unlike the rest of the Fake this is a rule the fake owns, which is why the
// contract suite pins it to the real adapter where the real host shows it.
type CI struct {
	// Manual makes pipelines wait for Complete. Otherwise a pipeline is
	// complete as soon as it starts.
	Manual bool
	// Strict makes the Fake behave like a repository, for the contract suite:
	// OpenChange refuses a head or base branch the Fake does not have, a head
	// that is at the base's commit (GitHub: "No commits between"), and a second
	// open change for the same head and base; it numbers the changes itself
	// when neither Opened nor OpenedFor says; Checks and ChangeState read
	// the change's head and base from the branches; CloseChange knows only
	// the changes it opened; and Merge refuses a head that is not the
	// branch's (ErrHeadMoved). Without Strict a Fake answers what the test
	// set.
	Strict bool
	// Script is what a pipeline reports when it completes, for the commits it
	// answers for (ok); for any other, and when nil, the pipeline reports what
	// the test configured (ChecksOf's checks for a change read,
	// BranchChecksOf's for a branch read). Nothing configured is no checks:
	// a test that wants a pass says so, for instance with DefaultChecks.
	Script func(repo codehost.Repo, sha string) (checks []codehost.Check, ok bool)

	mu      sync.Mutex
	changes map[codehost.ChangeRef]*ciChange
	pipes   map[pipeKey]*pipeline
}

// ConfigErrorChecks is what Woodpecker leaves on a commit whose pipeline could
// not be loaded: one failed status, posted after the commit had none.
func ConfigErrorChecks() []codehost.Check {
	return []codehost.Check{{Name: "ci/woodpecker", State: codehost.CheckFailure}}
}

// DefaultChecks is one passing check, for a test that wants a pipeline to pass.
func DefaultChecks() []codehost.Check {
	return []codehost.Check{{Name: "ci/build", State: codehost.CheckSuccess}}
}

type pipeKey struct {
	repo codehost.Repo
	sha  string
}

type pipeline struct {
	done bool
	// configured is what the test set for the commit, as the last read that knew it saw, so a change read and a branch read of one commit agree.
	configured []codehost.Check
}

// configured is what a test set for a commit's checks, when it set anything: a
// set but empty list is a CI that reports nothing, not an unset one.
type configured struct {
	known  bool
	checks []codehost.Check
}

type ciChange struct {
	repo       codehost.Repo
	head, base string
	closed     bool
}

// NewCI is a CI whose pipelines complete as they start.
func NewCI() *CI {
	return &CI{changes: map[codehost.ChangeRef]*ciChange{}, pipes: map[pipeKey]*pipeline{}}
}

func (c *CI) opened(ref codehost.ChangeRef, req *codehost.OpenRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changes[ref] = &ciChange{repo: req.Repo, head: req.Head, base: req.Base}
}

// hasOpen is whether a change from head onto base is open.
func (c *CI) hasOpen(repo codehost.Repo, head, base string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.changes {
		if !ch.closed && ch.repo == repo && ch.head == head && ch.base == base {
			return true
		}
	}
	return false
}

// next is the number of the next change opened, counting from 1.
func (c *CI) next() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.changes)) + 1
}

// change is a copy of the change the fake opened as ref.
func (c *CI) change(ref codehost.ChangeRef) (ciChange, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.changes[ref]
	if !ok {
		return ciChange{}, false
	}
	return *ch, true
}

func (c *CI) closed(ref codehost.ChangeRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.changes[ref]; ok {
		ch.closed = true
	}
}

// start begins sha's pipeline when there is none.
func (c *CI) start(repo codehost.Repo, sha string) {
	key := pipeKey{repo, sha}
	if _, ok := c.pipes[key]; !ok {
		c.pipes[key] = &pipeline{done: !c.Manual}
	}
}

// Complete finishes the pipeline of sha, so its checks show. It does nothing
// for a commit with no pipeline: CI cannot finish what never started.
func (c *CI) Complete(repo codehost.Repo, sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pipes[pipeKey{repo, sha}]; ok {
		p.done = true
	}
}

// Started is whether sha has a pipeline, finished or not.
func (c *CI) Started(repo codehost.Repo, sha string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.pipes[pipeKey{repo, sha}]
	return ok
}

// changeChecks is the checks of the change ref at head sha: the head's
// pipeline is started now if the change is open, and its checks show once it
// is done. fallback is what the test configured for them.
func (c *CI) changeChecks(ref codehost.ChangeRef, sha string, fallback configured) []codehost.Check {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.changes[ref]; ok && !ch.closed {
		c.start(ch.repo, sha)
	}
	return c.report(ref.InRepo(), sha, fallback)
}

// branchChecks is the checks of the commit sha at the head of branch: an open
// change from the branch starts the pipeline, a bare branch does not.
func (c *CI) branchChecks(repo codehost.Repo, branch, sha string, fallback configured) []codehost.Check {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.changes {
		if !ch.closed && ch.repo == repo && ch.head == branch {
			c.start(repo, sha)
		}
	}
	return c.report(repo, sha, fallback)
}

func (c *CI) report(repo codehost.Repo, sha string, fallback configured) []codehost.Check {
	p, ok := c.pipes[pipeKey{repo, sha}]
	if !ok {
		return nil
	}
	// A read that knows what the test set (a change's ChecksOf) wins, so
	// changes sharing a commit can still be given different checks; a read
	// that does not know (a branch's) sees what the last such read did.
	if fallback.known {
		p.configured = fallback.checks
	}
	if !p.done {
		return nil
	}
	if c.Script != nil {
		if checks, ok := c.Script(repo, sha); ok {
			return checks
		}
	}
	return p.configured
}
