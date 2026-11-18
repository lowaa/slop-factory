// Package landing lands a finished ticket's change on the server's authority:
// it takes the head of the tenant's landing queue as the server agent, merges
// the base into the change, waits for CI and merges through the same gate as an
// agent's change_merge. Anything else hands the ticket back with guidance.
package landing

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Tickets is what the runner needs of the ticket side, as servermerge's does.
type Tickets interface {
	EnsureServerAgent(ctx context.Context) (uuid.UUID, error)
	ServerClaimLanding(ctx context.Context, server uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ticket.ClaimResult, error)
	ServerHandBack(ctx context.Context, server uuid.UUID, ticketID int64, guidance string, affinity time.Duration) error
	// ServerReleaseLanding ends the server's lease on a landing without handing
	// the ticket back: it stays queued for the next claim.
	ServerReleaseLanding(ctx context.Context, server uuid.UUID, ticketID int64) error
	// RepairMergeQueue puts right a ticket whose status and landing row disagree.
	RepairMergeQueue(ctx context.Context, grace time.Duration) ([]int64, error)
	// Discuss moves the ticket the server holds into discussion, for a human.
	Discuss(ctx context.Context, server uuid.UUID, ticketID int64, question, handoff string) (*ticket.Ticket, error)
	Heartbeat(ctx context.Context, agent uuid.UUID, ttl time.Duration) error
	AcquireIntegrationLock(ctx context.Context, agent uuid.UUID) error
	ChangeGateFor(ctx context.Context, agent uuid.UUID) (*ticket.ChangeGate, error)
	Done(ctx context.Context, agent uuid.UUID, p *ticket.DoneParams) (*ticket.Ticket, error)
}

// Changes is the change flow: the same service the agents' change tools call,
// so the merge rules are not copied here.
type Changes interface {
	Checks(ctx context.Context, tenant, agent uuid.UUID, expectedSHA string) (*change.Report, error)
	Merge(ctx context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error)
}

// ChangeState is a change as the code host and the store show it.
type ChangeState struct {
	URL     string
	HeadSHA string
	// Merged is whether the change's head is in its base branch on the host,
	// or the store already records the merge.
	Merged bool
}

// Host is what the runner does to the change on the code host, beyond the gate.
// It reads the host rather than the runner's own memory, so that a restart
// reconciles from what is true.
type Host interface {
	State(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) (*ChangeState, error)
	// UpdateBranch merges the change's base into its head for headSHA.
	UpdateBranch(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int, headSHA string) (*codehost.BranchUpdate, error)
	// MarkMerged records in the store a merge found on the host.
	MarkMerged(ctx context.Context, ticketID int64, attempt int, by uuid.UUID) error
}

// PipelineState is a CI pipeline's state, as the runner needs to tell it.
type PipelineState string

const (
	PipelinePending PipelineState = "pending"
	PipelineRunning PipelineState = "running"
	PipelineSuccess PipelineState = "success"
	PipelineFailure PipelineState = "failure"
	// PipelineCancelled is a pipeline stopped by something other than its own
	// result: the woodpecker-watchdog (stuck past the repo timeout, orphaned by
	// a Woodpecker restart) or a person.
	PipelineCancelled PipelineState = "cancelled"
)

// Pipeline is one CI pipeline on a commit.
type Pipeline struct {
	// ID grows with each pipeline on the repo, so the newest on a head is the
	// highest: the watchdog restarts a pipeline as a new one on the same commit.
	ID      int64
	State   PipelineState
	Created time.Time
	// Finished is zero while it runs.
	Finished time.Time
}

// CheckVerdict is what the CI system itself says of one check's workflow.
type CheckVerdict string

const (
	CIPassed  CheckVerdict = "passed"
	CIFailed  CheckVerdict = "failed"
	CIRunning CheckVerdict = "running"
)

// Pipelines is the CI system behind the checks. The checks the host shows say
// whether the head is green; the pipelines say whether CI is still going to say
// something, which the watchdog's cancel-and-restart makes hard to tell.
type Pipelines interface {
	// Newest is the highest-numbered pipeline on the head; nil when none.
	Newest(ctx context.Context, headSHA string) (*Pipeline, error)
	// RepoTimeout is the repo's pipeline timeout in CI.
	RepoTimeout(ctx context.Context) (time.Duration, error)
	// Retry runs the head's failed pipeline again.
	Retry(ctx context.Context, headSHA string) error
	// Restart cancels the pipeline with the given id on the head and runs it
	// again as a new pipeline, for one that is lost: still reported running
	// though its workflows are dead.
	Restart(ctx context.Context, headSHA string, pipelineID int64) error
	// PassedOnBase is whether every one of the named checks passed on the base
	// branch's tip, which clears a failure as not this change's.
	PassedOnBase(ctx context.Context, checks []string) (bool, error)
	// PassedOnCommit is whether every one of the named checks passed on the
	// commit: with PassedOnBase, it says whether a red after a clean merge of the
	// base is the change's own or an interaction with what landed.
	PassedOnCommit(ctx context.Context, sha string, checks []string) (bool, error)
	// CheckVerdict is what the CI says of the workflow a check's target_url
	// names on headSHA, for a check the host still shows pending. ErrUnknown
	// where the URL is not this CI's: the host's status then stands alone.
	CheckVerdict(ctx context.Context, headSHA, targetURL string) (CheckVerdict, error)
}

// Queue is the tenant's landing queue.
type Queue interface {
	// Head is the next row to land, skipping the ticket ids in skip; nil when
	// there is none. A row that has begun (it has a step) comes first.
	Head(ctx context.Context, skip []int64) (*LandingRow, error)
	SetStep(ctx context.Context, ticketID int64, step Step) error
	HandBack(ctx context.Context, ticketID int64, reason Outcome, keepsPlace bool) error
	// Remove drops the row as a move to a terminal phase: PhaseMerged, or
	// PhaseDequeued for a ticket taken out of the queue unmerged.
	Remove(ctx context.Context, ticketID int64, to Phase) error
}

// Outcomes records what each attempt came to.
type Outcomes interface {
	Record(ctx context.Context, r *Record) error
}

// Mode says whether the tenant in the context is served: its merge_method is
// merge_queue and the server-wide MERGE_QUEUE_ENABLED kill switch is on. It is
// read on every take, never cached, so a switch to the worker flow is seen at
// the next step.
type Mode interface {
	Enabled(ctx context.Context) (bool, error)
}

// ModeFunc is a Mode that is a function.
type ModeFunc func(ctx context.Context) (bool, error)

func (f ModeFunc) Enabled(ctx context.Context) (bool, error) { return f(ctx) }

// Observer is told of each landing outcome once it is recorded; *metrics.Metrics
// is one. queueDepth is negative where it was not known and batchSize is 1
// outside a batch.
type Observer interface {
	LandingOutcome(outcome string, queueDepth int, enqueuedFor time.Duration, batchSize int)
	// LandingBatch is told once when a batch ends, with one of the Batch*
	// results of the metrics package.
	LandingBatch(result string)
}

// FlakyObserver is an Observer that is also told of the known-flaky retries'
// results. It is separate so an Observer that does not count them need not
// say so.
type FlakyObserver interface {
	// LandingFlakyRetry is told once per retried workflow when the retry ends,
	// as FlakyRescued or FlakyConfirmed; expired is whether the workflow's
	// entry was past its review date.
	LandingFlakyRetry(workflow, result string, expired bool)
}

// Results of a known-flaky retry.
const (
	FlakyRescued   = "rescued"
	FlakyConfirmed = "confirmed"
)

// BatchHost is what the batch builder does on the code host. Each call is for
// the repository of the change the row names; the rows of one batch share it.
type BatchHost interface {
	ChangedFiles
	// BaseTip reads the tip of the row's change's base branch.
	BaseTip(ctx context.Context, row *LandingRow) (string, error)
	CreateBranch(ctx context.Context, row *LandingRow, branch, sha string) error
	// UpdateMember merges the row's change's base into its head, if the head is
	// still headSHA. A conflict wraps codehost.ErrMergeConflict; an update the
	// host has not applied yet is codehost.ErrUpdatePending.
	UpdateMember(ctx context.Context, row *LandingRow, headSHA string) (*codehost.BranchUpdate, error)
	// MergeHead merges the row's change's head branch into branch, if the head
	// is still headSHA (codehost.ErrHeadMoved otherwise). A conflict wraps
	// codehost.ErrMergeConflict.
	MergeHead(ctx context.Context, row *LandingRow, branch, headSHA string) (*codehost.BranchMerge, error)
	// OpenBatchChange opens a draft change from the batch branch onto the row's
	// change's base. CI runs on pull requests only, so nothing tests the branch
	// until it has one.
	OpenBatchChange(ctx context.Context, row *LandingRow, branch string) (codehost.ChangeRef, error)
	// CloseBatchChange closes a change OpenBatchChange opened; one already
	// closed is success.
	CloseBatchChange(ctx context.Context, row *LandingRow, ref codehost.ChangeRef) error
	// DeleteBranch deletes a branch; one that is gone is success.
	DeleteBranch(ctx context.Context, row *LandingRow, branch string) error
}

// MemberHandBack hands back a batch member that holds no lease of its own:
// only the batch's first member does.
type MemberHandBack interface {
	// Comment leaves the guidance on the member's attempt for its author.
	Comment(ctx context.Context, server uuid.UUID, row *LandingRow, guidance string) error
	// Requeue moves the member's attempt out of the merge queue, once its
	// landings row is handed back.
	Requeue(ctx context.Context, server uuid.UUID, row *LandingRow) error
}
