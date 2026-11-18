package reviewreport_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const otherHead = "0123456789abcdef0123456789abcdef01234567"

// personaRun is a completed review of the slot's attempt at head, by the
// given personas.
func personaRun(s *supervised, head string, personas ...string) *report.Run {
	r := validRun(ticket.BranchName(s.ticketID, 1))
	r.HeadSHA, r.Personas, r.Sessions = head, personas, nil
	for _, p := range personas {
		r.Sessions = append(r.Sessions, report.Session{Persona: p, Part: 1, Parts: 1, Files: []string{"a.go"}})
	}
	return r
}

func failedRun(s *supervised, head string, personas ...string) *report.Run {
	r := personaRun(s, head, personas...)
	r.SchemaVersion, r.Status, r.Verdict, r.Summary = report.FailedSchemaVersion, report.StatusFailed, "", ""
	r.Failure = &report.Failure{Class: report.FailureUsageLimit, Message: "out of credits"}
	return r
}

func (s *supervised) request(t *testing.T, head string) *reviewreport.Request {
	t.Helper()
	req, err := s.store.RequestReview(s.ctx, s.slot, head, "make the widget spin")
	require.NoError(t, err)
	return req
}

func (s *supervised) open(t *testing.T) *reviewreport.Request {
	t.Helper()
	req, err := s.store.OpenRequest(s.ctx, s.slot, s.ticketID, 1)
	require.NoError(t, err)
	return req
}

func TestRequestReview_OnlyASupervisorsAgentHoldingAnAttemptMayAsk(t *testing.T) {
	s := newSupervised(t)
	_, err := s.store.RequestReview(s.ctx, s.standalone, headSHA, "x")
	assert.ErrorIs(t, err, reviewreport.ErrNotSupervised, "an agent run by hand: no supervisor would act on it")
	_, err = s.store.RequestReview(s.ctx, s.spareSlot, headSHA, "x")
	assert.ErrorIs(t, err, reviewreport.ErrNoHeldAttempt, "a slot whose lease ended")
	_, err = s.store.RequestReview(s.ctx, s.sup, headSHA, "x")
	assert.ErrorIs(t, err, reviewreport.ErrNoHeldAttempt, "a supervisor holds nothing itself")

	got := s.request(t, headSHA)
	assert.Equal(t, s.ticketID, got.TicketID)
	assert.Equal(t, 1, got.Attempt)
	assert.Equal(t, headSHA, got.HeadSHA)
	assert.False(t, got.RequestedAt.IsZero())
}

func TestRequestReview_ANewRequestReplacesTheOpenOne(t *testing.T) {
	s := newSupervised(t)
	first := s.request(t, headSHA)
	// The supervisor tells requests apart by head and time: asking again for
	// the same head (say, after a push that had not gone through) is a new
	// request it must act on.
	again := s.request(t, headSHA)
	assert.True(t, again.RequestedAt.After(first.RequestedAt), "every request is stamped anew")
	open := s.open(t)
	require.NotNil(t, open)
	assert.True(t, open.RequestedAt.Equal(again.RequestedAt))

	s.request(t, otherHead)
	open = s.open(t)
	require.NotNil(t, open)
	assert.Equal(t, otherHead, open.HeadSHA)
	var n int
	require.NoError(t, s.database.QueryRow(s.ctx, `SELECT count(*) FROM review_requests`).Scan(&n))
	assert.Equal(t, 1, n)
}

// Closure is the supervisor's runs of the requested head covering every
// required persona (the one pass, standard), whatever became of them.
func TestOpenRequest_ClosesWhenSupervisorRunsOfTheHeadCoverEveryPersona(t *testing.T) {
	s := newSupervised(t)
	assert.Nil(t, s.open(t), "nothing requested")
	s.request(t, headSHA)
	require.NotNil(t, s.open(t))

	s.insertBy(t, s.ctx, bySupervisor(s.sup), personaRun(s, otherHead, "standard"))
	s.insert(t, s.ctx, s.slot, personaRun(s, headSHA, "standard"))
	bench := personaRun(s, headSHA, "standard")
	bench.Kind = report.KindBench
	s.insertBy(t, s.ctx, bySupervisor(s.sup), bench)
	require.NotNil(t, s.open(t), "another head, the agent's own runs and a bench run cover nothing")

	s.insertBy(t, s.ctx, bySupervisor(s.sup), personaRun(s, headSHA, "security", "concurrency", "test-efficacy"))
	require.NotNil(t, s.open(t), "other personas do not stand in for the required one")

	s.insertBy(t, s.ctx, bySupervisor(s.sup), failedRun(s, headSHA[:12], "standard"))
	assert.Nil(t, s.open(t), "a failed run is an answer too, and an abbreviated head is the same head")

	// The agent asks again after reading a failed round: the old runs answered
	// the old request.
	s.request(t, headSHA)
	assert.NotNil(t, s.open(t))
}

func TestOpenRequest_IsTheRequestersOwn(t *testing.T) {
	s := newSupervised(t)
	s.request(t, headSHA)
	got, err := s.store.OpenRequest(s.ctx, s.otherSlot, s.ticketID, 1)
	require.NoError(t, err)
	assert.Nil(t, got, "a later holder of the attempt is not shown a request it did not make")
}

// insertRunsInTx stores supervisor-posted runs of every persona at head in
// a transaction of its own that stays open until release is closed, so the
// test decides what commits first.
func insertRunsInTx(t *testing.T, s *supervised, head string, started chan<- struct{}, release <-chan struct{}) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- s.database.InTx(s.ctx, func(q db.Querier) error {
			for _, p := range reviewreport.RequiredPersonas {
				if _, err := q.Exec(s.ctx, `
					INSERT INTO review_runs (id, posted_by_kind, posted_by_id, ticket_id, attempt_number, kind, status, branch, head_sha,
						personas, verdict, incomplete, provider, model, started_at, finished_at, report)
					VALUES ($1, 'supervisor', $2, $3, 1, 'review', 'completed', $4, $5, $6, 'approve', false, 'claude', 'm', $7, $7, '{}')`,
					uuid.New(), s.sup, s.ticketID, ticket.BranchName(s.ticketID, 1), head, []string{p}, t0); err != nil {
					return err
				}
			}
			close(started)
			<-release
			return nil
		})
	}()
	return done
}

// The race between a request and the runs: runs whose posting began before
// the request was made answer an earlier round, even when they commit after
// it, so they leave it open; runs posted once the request is there close it.
func TestOpenRequest_RunsPostedAcrossARequestDoNotCloseIt(t *testing.T) {
	s := newSupervised(t)
	started, release := make(chan struct{}), make(chan struct{})
	done := insertRunsInTx(t, s, headSHA, started, release)
	// The posting transaction has begun, and inserted its runs, before the
	// request's begins.
	<-started
	s.request(t, headSHA)
	close(release)
	require.NoError(t, <-done)
	assert.NotNil(t, s.open(t), "the runs began before the request")

	started, release = make(chan struct{}), make(chan struct{})
	done = insertRunsInTx(t, s, headSHA, started, release)
	<-started
	close(release)
	require.NoError(t, <-done)
	assert.Nil(t, s.open(t), "runs posted after the request close it")
}

// The other order: the request's transaction began before the runs were
// posted, and commits after them. Those runs answered the earlier round, so
// they leave it open.
func TestOpenRequest_RunsPostedWhileARequestIsBeingMadeDoNotCloseIt(t *testing.T) {
	s := newSupervised(t)
	reached, release := make(chan struct{}), make(chan struct{})
	restore := reviewreport.SetRequestBarrier(func() {
		close(reached)
		<-release
	})
	defer restore()
	done := make(chan error, 1)
	go func() {
		_, err := s.store.RequestReview(s.ctx, s.slot, headSHA, "again")
		done <- err
	}()
	<-reached
	all := personaRun(s, headSHA, reviewreport.RequiredPersonas...)
	s.insertBy(t, s.ctx, bySupervisor(s.sup), all)
	close(release)
	require.NoError(t, <-done)
	restore()
	assert.NotNil(t, s.open(t), "the runs were posted before the request was written")
}

func TestHeldAttemptRuns_AreTheHeldAttemptsAtTheRequestedHead(t *testing.T) {
	s := newSupervised(t)
	_, err := s.store.HeldAttemptRuns(s.ctx, s.slot, "", 10)
	assert.ErrorIs(t, err, reviewreport.ErrNoRequest)
	_, err = s.store.HeldAttemptRuns(s.ctx, s.spareSlot, headSHA, 10)
	assert.ErrorIs(t, err, reviewreport.ErrNoHeldAttempt)

	atHead := personaRun(s, headSHA, "standard")
	atHead.Findings = []report.Finding{{ID: uuid.New(), File: "a.go", Line: 3, Severity: "major", Summary: "s", Detail: "d", Personas: []string{"standard"}}}
	s.insertBy(t, s.ctx, bySupervisor(s.sup), atHead)
	failed := failedRun(s, headSHA, "security")
	s.insertBy(t, s.ctx, bySupervisor(s.sup), failed)
	s.insertBy(t, s.ctx, bySupervisor(s.sup), personaRun(s, otherHead, "standard"))
	require.NoError(t, s.store.LabelFinding(s.ctx, s.slot, atHead.Findings[0].ID, &reviewreport.Label{Verdict: "wont_fix", Reason: "later"}))
	s.request(t, headSHA)

	got, err := s.store.HeldAttemptRuns(s.ctx, s.slot, "", 10)
	require.NoError(t, err)
	assert.Equal(t, headSHA, got.HeadSHA)
	require.Len(t, got.Runs, 2, "the requested head's runs, the failed one included")
	ids := []uuid.UUID{got.Runs[0].ID, got.Runs[1].ID}
	assert.ElementsMatch(t, []uuid.UUID{atHead.ID, failed.ID}, ids)
	for _, r := range got.Runs {
		if r.ID == atHead.ID {
			require.Len(t, r.Findings, 1)
			require.NotNil(t, r.Findings[0].Label)
			assert.Equal(t, "wont_fix", r.Findings[0].Label.Verdict)
		}
	}

	other, err := s.store.HeldAttemptRuns(s.ctx, s.slot, otherHead[:7], 10)
	require.NoError(t, err)
	assert.Len(t, other.Runs, 1, "a head named is read instead")
	assert.False(t, other.Truncated)

	cut, err := s.store.HeldAttemptRuns(s.ctx, s.slot, "", 1)
	require.NoError(t, err)
	assert.Len(t, cut.Runs, 1)
	assert.True(t, cut.Truncated, "the runs left out are said to be")

	// The slot hands the attempt on; its next holder has asked for nothing.
	s.release(t, s.ctx, s.slotLease)
	s.hold(t, s.ctx, s.spareSlot, s.ticketID, 1)
	_, err = s.store.HeldAttemptRuns(s.ctx, s.spareSlot, "", 10)
	assert.ErrorIs(t, err, reviewreport.ErrNoRequest, "the head last requested is the caller's own")
}
