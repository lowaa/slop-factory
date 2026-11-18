// Package attemptsignals computes, stores and serves what an attempt's change
// and run show about the work: cheating checks, size, CI outcome, own-test
// classes, cost, time and autonomy. Every signal is unknown, never zero or
// clean, when the inputs for it were not there.
package attemptsignals

import (
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/diffcheck"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/owntests"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/regressions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/usage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// CI is the CI evidence for an attempt's change head.
type CI struct {
	Runs []regressions.Run
	// Complete is whether Runs is every run and names the failed tests. When
	// false (a host that reports only the head's checks) only whether the
	// checks passed is known: reruns and failed tests stay unknown.
	Complete bool
}

// Inputs is what the signals are computed from. A zero field is a missing
// input.
type Inputs struct {
	// Final is whether the attempt is over: its change closed or merged, or
	// the attempt itself closed or done.
	Final   bool
	HeadSHA string
	// Diff is the change's git-style unified diff against its base; empty when
	// the attempt has no change or the host could not give one.
	Diff      string
	Scopes    []string
	Generated *diffcheck.Generated
	CI        *CI
	// OwnTests are the test files the attempt added or changed with their
	// results at the base and the head. Nil when not measured.
	OwnTests         []owntests.FileRun
	TouchedTestFiles []string
}

// Computed is the wire form of the signals with the scalar figures reports
// aggregate over. A nil figure is unknown.
type Computed struct {
	Body *ticket.AttemptSignalsBody

	TestsDeleted, SkipsAdded, AssertionsRemoved, OutOfScopeFiles *int
	LinesAdded, LinesRemoved, FilesTouched                       *int
	ChecksPassed                                                 string
	Reruns, Regressions, OwnTestFailures, OwnTestsFlagged        *int
	Usage                                                        *usage.Signals
}

// Compute is pure: the same inputs and usage give the same signals.
func Compute(in *Inputs, u *usage.Signals, now time.Time) *Computed {
	if u == nil {
		u = &usage.Signals{}
	}
	c := &Computed{Usage: u, ChecksPassed: string(regressions.Unknown)}
	body := &ticket.AttemptSignalsBody{
		Final: in.Final, ComputedAt: now, HeadSHA: in.HeadSHA,
		OwnTests: []ticket.SignalOwnTestBody{},
	}
	c.Body = body

	d := diffcheck.Check(diffcheck.Input{Diff: in.Diff, Scopes: in.Scopes, Generated: in.Generated})
	body.Cheating = ticket.SignalCheatingBody{
		TestsDeleted:      filesBody(d.TestsDeleted),
		SkipsAdded:        filesBody(d.SkipsAdded),
		AssertionsRemoved: filesBody(d.AssertionsRemoved),
		OutOfScope:        filesBody(d.OutOfScope),
	}
	c.TestsDeleted, c.SkipsAdded = countOf(d.TestsDeleted), countOf(d.SkipsAdded)
	c.AssertionsRemoved, c.OutOfScopeFiles = countOf(d.AssertionsRemoved), countOf(d.OutOfScope)
	body.Size = ticket.SignalSizeBody{
		Known: d.Size.Known, LinesAdded: d.Size.LinesAdded, LinesRemoved: d.Size.LinesRemoved,
		FilesTouched: d.Size.FilesTouched, GeneratedSkipped: d.Size.GeneratedSkipped,
		GeneratedLines: d.Size.GeneratedLines, GeneratedRulesApplied: d.Size.GeneratedRulesApplied,
		BinaryFiles: d.Size.BinaryFiles,
	}
	if d.Size.Known {
		c.LinesAdded, c.LinesRemoved, c.FilesTouched = &d.Size.LinesAdded, &d.Size.LinesRemoved, &d.Size.FilesTouched
	}

	body.Checks = ticket.SignalChecksBody{Passed: string(regressions.Unknown)}
	if in.CI != nil && len(in.CI.Runs) > 0 {
		r := regressions.Classify(&regressions.Input{Runs: in.CI.Runs, TouchedTestFiles: in.TouchedTestFiles})
		body.Checks.Passed, c.ChecksPassed = string(r.ChecksPassed), string(r.ChecksPassed)
		if in.CI.Complete {
			body.Checks.Reruns = &r.Reruns
			reg, own := failedBodies(r.Regressions), failedBodies(r.OwnTestFailures)
			body.Checks.Regressions, body.Checks.OwnTestFailures = &reg, &own
			body.Checks.Unattributed = r.Unattributed
			nReg, nOwn := len(reg), len(own)
			c.Reruns, c.Regressions, c.OwnTestFailures = &r.Reruns, &nReg, &nOwn
		}
	}

	if in.OwnTests != nil {
		flagged := 0
		for _, k := range owntests.Classify(in.OwnTests) {
			body.OwnTests = append(body.OwnTests, ticket.SignalOwnTestBody{Path: k.Path, Class: string(k.Class), Flagged: k.Flagged})
			if k.Flagged {
				flagged++
			}
		}
		c.OwnTestsFlagged = &flagged
	}

	body.Autonomy = ticket.SignalAutonomyBody{
		Discussions: u.Discussions, ScopeExtensionDenials: u.ScopeExtensionDenials, BlockedHandbacks: u.BlockedHandbacks,
	}
	body.Time = ticket.SignalTimeBody{
		Runs: u.Runs, Turns: u.Turns, FirstClaimAt: u.FirstClaimAt, LastRunEndedAt: u.LastRunEndedAt,
	}
	if u.WallClock != nil {
		secs := int64(u.WallClock.Seconds())
		body.Time.WallClockSeconds = &secs
	}
	return c
}

func filesBody(s diffcheck.Signal) ticket.SignalFilesBody {
	files := s.Files
	if files == nil {
		files = []string{}
	}
	return ticket.SignalFilesBody{Known: s.Known, Files: files}
}

func countOf(s diffcheck.Signal) *int {
	if !s.Known {
		return nil
	}
	n := s.Count()
	return &n
}

func failedBodies(in []regressions.FailedTest) []ticket.SignalFailedTestBody {
	out := make([]ticket.SignalFailedTestBody, len(in))
	for i, f := range in {
		out[i] = ticket.SignalFailedTestBody{Name: f.Name, File: f.File}
	}
	return out
}
