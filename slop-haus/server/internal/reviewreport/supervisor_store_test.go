package reviewreport_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// supervised is a tenant with two supervisors, each running one slot, and an
// agent run by hand.
type supervised struct {
	*fixture
	sup, otherSup     uuid.UUID
	slot, otherSlot   uuid.UUID
	standalone        uuid.UUID
	spareSlot         uuid.UUID
	ticketID          int64
	slotLease         int64
	otherSlotAttempt  int
	standaloneTicket  int64
	spareSlotsAttempt int
}

func newSupervised(t *testing.T) *supervised {
	t.Helper()
	f := newFixture(t)
	s := &supervised{fixture: f}
	s.sup, s.otherSup = f.supervisor(t, f.ctx, "sup"), f.supervisor(t, f.ctx, "other-sup")
	s.slot, s.otherSlot = f.slot(t, f.ctx, s.sup, "slot"), f.slot(t, f.ctx, s.otherSup, "other-slot")
	s.spareSlot = f.slot(t, f.ctx, s.sup, "spare-slot")
	s.standalone = f.agent(t, f.ctx, "standalone")
	s.ticketID = f.ticket(t, f.ctx)
	s.otherSlotAttempt = f.addAttempt(t, f.ctx, s.ticketID)
	s.spareSlotsAttempt = f.addAttempt(t, f.ctx, s.ticketID)
	s.slotLease = f.hold(t, f.ctx, s.slot, s.ticketID, 1)
	f.hold(t, f.ctx, s.otherSlot, s.ticketID, s.otherSlotAttempt)
	released := f.hold(t, f.ctx, s.spareSlot, s.ticketID, s.spareSlotsAttempt)
	f.release(t, f.ctx, released)
	s.standaloneTicket = f.ticket(t, f.ctx)
	f.hold(t, f.ctx, s.standalone, s.standaloneTicket, 1)
	return s
}

func TestInsertRun_ASupervisorFilesARunUnderTheAttemptItsOwnAgentHolds(t *testing.T) {
	s := newSupervised(t)
	run := validRun(ticket.BranchName(s.ticketID, 1))
	got := s.insertBy(t, s.ctx, bySupervisor(s.sup), run)
	assert.True(t, got.Created)
	assert.Equal(t, &reviewreport.Attribution{TicketID: s.ticketID, Attempt: 1}, got.Attribution)

	runs, err := s.store.AttemptRuns(s.ctx, attempt(s.ticketID, 1), 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, reviewreport.Poster{Kind: reviewreport.PostedBySupervisor, ID: s.sup}, runs[0].Poster)
	assert.Equal(t, "sup", runs[0].PosterName)
}

// The supervisor's half of the trust boundary: it vouches only for attempts
// its own agents hold.
func TestInsertRun_NoSupervisorCanFileAReviewAgainstAnotherSupervisorsAgent(t *testing.T) {
	s := newSupervised(t)
	for name, c := range map[string]struct {
		poster *reviewreport.Poster
		branch string
	}{
		"another supervisor's agent holds the attempt": {bySupervisor(s.otherSup), ticket.BranchName(s.ticketID, 1)},
		"an agent posting as a supervisor":             {bySupervisor(s.slot), ticket.BranchName(s.ticketID, s.otherSlotAttempt)},
		"an agent run by hand holds the attempt":       {bySupervisor(s.sup), ticket.BranchName(s.standaloneTicket, 1)},
		"its agent's lease on the attempt has ended":   {bySupervisor(s.sup), ticket.BranchName(s.ticketID, s.spareSlotsAttempt)},
		"nobody holds the attempt it names":            {bySupervisor(s.sup), ticket.BranchName(s.ticketID+1000, 1)},
		// A supervisor is not its agents: posting as an agent, it holds
		// nothing itself.
		"a supervisor id posting as an agent": {byAgent(s.sup), ticket.BranchName(s.ticketID, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			got := s.insertBy(t, s.ctx, c.poster, validRun(c.branch))
			assert.True(t, got.Created)
			assert.Nil(t, got.Attribution)
		})
	}
	// The other supervisor's own agent's attempt is still its to file under.
	got := s.insertBy(t, s.ctx, bySupervisor(s.otherSup), validRun(ticket.BranchName(s.ticketID, s.otherSlotAttempt)))
	assert.NotNil(t, got.Attribution)
}

func TestInsertRun_ARunIDIsOnePostersOnly(t *testing.T) {
	s := newSupervised(t)
	run := validRun(ticket.BranchName(s.ticketID, 1))
	s.insertBy(t, s.ctx, bySupervisor(s.sup), run)

	assert.False(t, s.insertBy(t, s.ctx, bySupervisor(s.sup), run).Created, "the supervisor's own retry")
	for name, poster := range map[string]*reviewreport.Poster{
		"its agent":                   byAgent(s.slot),
		"another supervisor":          bySupervisor(s.otherSup),
		"the same id as another kind": byAgent(s.sup),
	} {
		_, err := s.store.InsertRun(s.ctx, poster, run)
		assert.ErrorIs(t, err, reviewreport.ErrRunTaken, name)
	}
}

// The agent whose attempt a supervisor reviewed labels the findings; so does
// an agent that posted its own run. Nobody else may.
func TestLabelFinding_TheAttemptsHolderOrTheRunsAgentLabels(t *testing.T) {
	s := newSupervised(t)
	supervisedRun := validRun(ticket.BranchName(s.ticketID, 1), "standard")
	s.insertBy(t, s.ctx, bySupervisor(s.sup), supervisedRun)
	ownRun := validRun("main", "standard")
	s.insert(t, s.ctx, s.standalone, ownRun)
	label := &reviewreport.Label{Verdict: "false_positive"}
	finding := supervisedRun.Findings[0].ID

	for name, agentID := range map[string]uuid.UUID{
		"the supervisor that posted it":          s.sup,
		"another agent of the same supervisor":   s.spareSlot,
		"another supervisor's agent":             s.otherSlot,
		"an agent that holds some other attempt": s.standalone,
	} {
		assert.ErrorIs(t, s.store.LabelFinding(s.ctx, agentID, finding, label), reviewreport.ErrNoFinding, name)
	}
	assert.ErrorIs(t, s.store.LabelFinding(s.ctx, s.slot, ownRun.Findings[0].ID, label), reviewreport.ErrNoFinding,
		"the holder of an attempt labels only that attempt's runs")

	require.NoError(t, s.store.LabelFinding(s.ctx, s.slot, finding, label), "the attempt's holder")
	require.NoError(t, s.store.LabelFinding(s.ctx, s.standalone, ownRun.Findings[0].ID, label), "the run's own agent")

	s.release(t, s.ctx, s.slotLease)
	assert.ErrorIs(t, s.store.LabelFinding(s.ctx, s.slot, finding, label), reviewreport.ErrNoFinding,
		"once the attempt is no longer held, its former holder is nobody in particular")
}

func TestRunsForAttemptHead_CanBeNarrowedToSupervisorPostedRuns(t *testing.T) {
	s := newSupervised(t)
	f := s.fixture
	ownTicket := s.ticketID
	byItsAgent := validRun(ticket.BranchName(ownTicket, 1))
	bySup := validRun(ticket.BranchName(ownTicket, 1))
	bySup.FinishedAt = byItsAgent.FinishedAt.Add(time.Minute)
	f.insert(t, f.ctx, s.slot, byItsAgent)
	f.insertBy(t, f.ctx, bySupervisor(s.sup), bySup)

	all, err := f.store.RunsForAttemptHead(f.ctx, ownTicket, 1, headSHA, reviewreport.PostedByAnyone)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, reviewreport.Poster{Kind: reviewreport.PostedBySupervisor, ID: s.sup}, all[0].Poster)
	assert.Equal(t, reviewreport.Poster{Kind: reviewreport.PostedByAgent, ID: s.slot}, all[1].Poster)

	trusted, err := f.store.RunsForAttemptHead(f.ctx, ownTicket, 1, headSHA, reviewreport.PostedBySupervisorOnly)
	require.NoError(t, err)
	require.Len(t, trusted, 1)
	assert.Equal(t, bySup.ID, trusted[0].RunID)
}
