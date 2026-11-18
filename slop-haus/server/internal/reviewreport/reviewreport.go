// Package reviewreport is the report server slop-review posts to: the runs a
// reviewer reports (slop-review/report), the labels its agent gives their
// findings, and the tenant's central provider config the reviewer runs with.
// The wire contract is the client's; this package imports it rather than
// restating it, so the two cannot drift.
//
// Runs are posted by a supervisor, which runs slop-review for the agents it
// runs, or by an agent for itself (standalone use). A run is filed under a
// ticket's attempt only when its branch names that attempt (ticket.BranchName)
// and, as it is posted, the attempt is held by the posting agent or by an
// agent the posting supervisor runs. That is the trust boundary: an
// attributed run is evidence the steward reads as "this attempt was
// reviewed", so nobody may file reviews against another's work. Every other
// run is kept unattributed. Each run records who posted it, since the steward
// trusts only a supervisor's: an agent could post a review it never ran.
package reviewreport

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalid is a report or label the contract refuses.
	ErrInvalid = errors.New("reviewreport: invalid")
	// ErrRateLimited is a poster past its limit.
	ErrRateLimited = errors.New("reviewreport: too many reports from this caller")
	// ErrRunTaken is a run id another poster already posted.
	ErrRunTaken = errors.New("reviewreport: the run id is already another poster's")
	// ErrFindingTaken is a finding id another run already has.
	ErrFindingTaken = errors.New("reviewreport: a finding id is already another run's")
	// ErrNoFinding is a finding the caller cannot label: there is none, or
	// the caller neither holds its run's attempt nor posted the run. The two
	// are one answer so that finding ids do not leak across agents.
	ErrNoFinding = errors.New("reviewreport: no such finding")
	// ErrNoProviderConfig is a tenant with no provider config set.
	ErrNoProviderConfig = errors.New("reviewreport: the tenant has no provider config")
)

// PosterKind is what posted a run.
type PosterKind string

const (
	PostedByAgent      PosterKind = "agent"
	PostedBySupervisor PosterKind = "supervisor"
)

// Poster is who posted a run: a supervisor or an agent, by its agent id.
type Poster struct {
	Kind PosterKind
	ID   uuid.UUID
}

// Attribution is the attempt a run is filed under.
type Attribution struct {
	TicketID int64
	Attempt  int
}

// Inserted is what storing a run did.
type Inserted struct {
	// Created is false for a run already stored: a retry, which changed
	// nothing.
	Created bool
	// Attribution is nil for an unattributed run.
	Attribution *Attribution
}

// LabelVerdicts are what an agent may say of a finding (slop-review label).
var LabelVerdicts = []string{"confirmed", "false_positive", "fabricated", "duplicate", "wont_fix"}

// Label is an agent's verdict on a finding.
type Label struct {
	Verdict string
	Reason  string
	// FixedIn is the commit that fixed a confirmed finding; empty for none.
	FixedIn string
}

// CurrentLabel is a finding's newest label.
type CurrentLabel struct {
	Label
	LabelledAt time.Time
}

// AttemptRun is one run filed under an attempt, as the UI shows it.
type AttemptRun struct {
	ID     uuid.UUID
	Poster Poster
	// PosterName is the posting supervisor's or agent's name.
	PosterName string
	Kind       string
	// Status is completed or failed.
	Status string
	// FailureClass and FailureMessage are set on a failed run.
	FailureClass   string
	FailureMessage string
	Branch         string
	HeadSHA        string
	Personas       []string
	Verdict        string
	Summary        string
	Incomplete     bool
	Provider       string
	Model          string
	StartedAt      time.Time
	FinishedAt     time.Time
	ReceivedAt     time.Time
	Findings       []AttemptFinding
}

// AttemptFinding is one finding of an AttemptRun.
type AttemptFinding struct {
	ID       uuid.UUID
	File     string
	Line     int
	Severity string
	Summary  string
	Detail   string
	Personas []string
	// Label is nil for a finding nobody has labelled.
	Label *CurrentLabel
}

// HeadRun is a completed review of one head of an attempt, as the steward
// weighs it.
type HeadRun struct {
	RunID uuid.UUID
	// Poster says who posted the run; only a supervisor's is evidence the
	// review ran, an agent's is the agent's word.
	Poster     Poster
	HeadSHA    string
	Personas   []string
	Verdict    string
	Incomplete bool
	FinishedAt time.Time
	// Blocking counts the blocker and major findings the run raised, each
	// once, whichever persona it names: the run's own passes (blast-radius,
	// seams) name personas it was not asked for, and theirs block too.
	// Labels are not consulted: they are the reviewed agent's own say, so a
	// finding only goes away when a later review of a new head no longer
	// raises it.
	Blocking int
}

// PostedBy narrows RunsForAttemptHead by who posted a run.
type PostedBy int

const (
	// PostedByAnyone is every run, the agents' own included.
	PostedByAnyone PostedBy = iota
	// PostedBySupervisorOnly is the runs a supervisor posted: the ones the
	// steward may take as evidence.
	PostedBySupervisorOnly
)

// HeadRunReader is what the steward reads an attempt's reviews through.
type HeadRunReader interface {
	// RunsForAttemptHead is the attempt's completed review runs of headSHA
	// that postedBy admits, newest first. A head matches by its full sha or
	// by a prefix of at least 7 characters either way round, ignoring case; a
	// headSHA shorter than that matches nothing.
	RunsForAttemptHead(ctx context.Context, ticketID int64, attempt int, headSHA string, postedBy PostedBy) ([]HeadRun, error)
}
