package ticket

import (
	"context"
	"time"
)

// AttemptSignals reads the stored signals of attempts, refreshing the ones
// still moving. It lives behind an interface because the package computing
// them reads tickets itself.
type AttemptSignals interface {
	// AttemptSignals returns each attempt's signals; an attempt whose signals
	// could not be computed or read has no entry, which the wire form shows as
	// null.
	AttemptSignals(ctx context.Context, refs []AttemptRef) (map[AttemptRef]*AttemptSignalsBody, error)
}

// AttemptSignalsBody is what an attempt's change and run show about the work.
// Every figure is either known or says it is not: unknown is never zero.
type AttemptSignalsBody struct {
	// Final is true once the attempt's change closed or merged: the figures
	// no longer move.
	Final      bool      `json:"final"`
	ComputedAt time.Time `json:"computed_at"`
	// HeadSHA is the change head the figures from the diff and CI are for;
	// empty when the attempt has no change.
	HeadSHA  string              `json:"head_sha"`
	Cheating SignalCheatingBody  `json:"cheating"`
	Size     SignalSizeBody      `json:"size"`
	Checks   SignalChecksBody    `json:"checks"`
	OwnTests []SignalOwnTestBody `json:"own_tests" doc:"The test files the attempt added or changed, each classed by its result at the base and at the head; empty when none were classified."`
	Autonomy SignalAutonomyBody  `json:"autonomy"`
	Time     SignalTimeBody      `json:"time"`
}

// SignalFilesBody is a list-valued finding. When known is false the list is
// empty and must not be read as "none".
type SignalFilesBody struct {
	Known bool     `json:"known"`
	Files []string `json:"files"`
}

type SignalCheatingBody struct {
	TestsDeleted      SignalFilesBody `json:"tests_deleted"`
	SkipsAdded        SignalFilesBody `json:"skips_added"`
	AssertionsRemoved SignalFilesBody `json:"assertions_removed"`
	OutOfScope        SignalFilesBody `json:"out_of_scope"`
}

type SignalSizeBody struct {
	Known            bool `json:"known"`
	LinesAdded       int  `json:"lines_added"`
	LinesRemoved     int  `json:"lines_removed"`
	FilesTouched     int  `json:"files_touched"`
	GeneratedSkipped int  `json:"generated_skipped"`
	GeneratedLines   int  `json:"generated_lines"`
	// GeneratedRulesApplied is false when no generated-file rules were
	// available, so generated files count as hand-written.
	GeneratedRulesApplied bool `json:"generated_rules_applied"`
	BinaryFiles           int  `json:"binary_files"`
}

type SignalFailedTestBody struct {
	Name string `json:"name"`
	File string `json:"file"`
}

type SignalChecksBody struct {
	Passed string `json:"passed" enum:"yes,no,unknown"`
	// Reruns is the CI runs after the first; null when the runs are not known.
	Reruns *int `json:"reruns"`
	// Regressions and OwnTestFailures are null when the failed tests are not
	// known.
	Regressions     *[]SignalFailedTestBody `json:"regressions"`
	OwnTestFailures *[]SignalFailedTestBody `json:"own_test_failures"`
	// Unattributed is true when the latest run failed without naming a test.
	Unattributed bool `json:"unattributed"`
}

type SignalOwnTestBody struct {
	Path    string `json:"path"`
	Class   string `json:"class" enum:"fails-on-base,passes-on-base,does-not-run,unknown"`
	Flagged bool   `json:"flagged"`
}

// SignalAutonomyBody counts the hand-holding an attempt needed; each is null
// for an attempt that never ran.
type SignalAutonomyBody struct {
	Discussions           *int64 `json:"discussions"`
	ScopeExtensionDenials *int64 `json:"scope_extension_denials"`
	BlockedHandbacks      *int64 `json:"blocked_handbacks"`
}

// SignalTimeBody is the attempt's runs and elapsed time; each is null when
// unknown.
type SignalTimeBody struct {
	Runs             *int64     `json:"runs"`
	Turns            *int64     `json:"turns"`
	FirstClaimAt     *time.Time `json:"first_claim_at"`
	LastRunEndedAt   *time.Time `json:"last_run_ended_at"`
	WallClockSeconds *int64     `json:"wall_clock_seconds"`
}

// fillAttemptSignals sets the signals of a ticket's attempts.
func fillAttemptSignals(ctx context.Context, signals AttemptSignals, ticketID int64, attempts []AttemptBody) error {
	if signals == nil || len(attempts) == 0 {
		return nil
	}
	refs := make([]AttemptRef, len(attempts))
	for i := range attempts {
		refs[i] = AttemptRef{TicketID: ticketID, Number: attempts[i].Number}
	}
	got, err := signals.AttemptSignals(ctx, refs)
	if err != nil {
		return err
	}
	for i := range attempts {
		attempts[i].Signals = got[refs[i]]
	}
	return nil
}
