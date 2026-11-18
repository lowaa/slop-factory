package mcpserver_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const reviewHead = "89abcdef0123456789abcdef0123456789abcdef"

// slotRig is a real server with the review tools on, and a supervisor's slot
// holding a ticket's attempt.
type slotRig struct {
	*realRig
	sup     uuid.UUID
	slot    *consoleSession
	held    *ticket.Ticket
	attempt int
}

func newSlotRig(t *testing.T) *slotRig {
	t.Helper()
	r := newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{reviews: true})
	s := &slotRig{realRig: r, sup: uuid.New()}
	_, err := r.database.Exec(r.ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'sup', 'supervisor', 4)`, s.sup)
	require.NoError(t, err)
	slotID := uuid.New()
	_, err = r.database.Exec(r.ctx, `INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, 'slot-1', 'worker', $2)`, slotID, s.sup)
	require.NoError(t, err)
	s.slot = r.connectAs("slot-1", slotID)
	s.held = r.openTicket("Spin the widget")
	claimed := s.slot.call(t, "ticket_next", map[string]any{"ticket_id": s.held.ID})
	require.False(t, claimed.IsError, resultText(t, claimed))
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT attempt_number FROM leases WHERE agent_id = $1 AND released_at IS NULL`,
		slotID).Scan(&s.attempt))
	return s
}

// postRun stores a run of the slot's attempt as its supervisor posted it.
func (s *slotRig) postRun(t *testing.T, persona string, findings ...report.Finding) *report.Run {
	t.Helper()
	at := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	run := &report.Run{
		SchemaVersion: report.CompletedSchemaVersion, ID: uuid.New(), Kind: report.KindReview,
		Branch: ticket.BranchName(s.held.ID, s.attempt), HeadSHA: reviewHead, Provider: "claude", Model: "m",
		Personas: []string{persona}, Verdict: "request_changes", Summary: persona + " summary",
		StartedAt: at, FinishedAt: at.Add(time.Minute), Findings: findings,
	}
	inserted, err := reviewreport.NewPostgresStore(s.database).InsertRun(s.ctx,
		&reviewreport.Poster{Kind: reviewreport.PostedBySupervisor, ID: s.sup}, run)
	require.NoError(t, err)
	require.NotNil(t, inserted.Attribution)
	return run
}

func TestReviewRequest_IsForASupervisorsAgentHoldingATicket(t *testing.T) {
	s := newSlotRig(t)

	// The rig's own agent is run by hand.
	mine := s.openTicket("By hand")
	require.False(t, s.call("ticket_next", map[string]any{"ticket_id": mine.ID}).IsError)
	res := s.call("review_request", map[string]any{"head_sha": reviewHead, "intent": "x"})
	assert.False(t, res.IsError, "an answer, not a failure to retry")
	assert.Equal(t, false, structured(t, res)["ok"])
	assert.Contains(t, resultText(t, res), "Run slop-review yourself, as the repo's CLAUDE.md")

	res = s.slot.call(t, "review_request", map[string]any{"head_sha": reviewHead[:12], "intent": "x"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "40-character")
	res = s.slot.call(t, "review_request", map[string]any{"head_sha": reviewHead, "intent": " "})
	assert.True(t, res.IsError)
	res = s.slot.call(t, "review_request", map[string]any{"head_sha": reviewHead, "intent": strings.Repeat("x", report.MaxTextBytes+1)})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "over")

	res = s.slot.call(t, "review_request", map[string]any{"head_sha": strings.ToUpper(reviewHead), "intent": "Spin the widget."})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, true, structured(t, res)["ok"])
	assert.Contains(t, resultText(t, res), "End your turn now")

	open, err := reviewreport.NewPostgresStore(s.database).OpenRequest(s.ctx, s.slot.agentID, s.held.ID, s.attempt)
	require.NoError(t, err)
	require.NotNil(t, open)
	assert.Equal(t, reviewHead, open.HeadSHA, "stored as the sha's canonical lower case")
	assert.Equal(t, "Spin the widget.", open.Intent)
}

func TestReviewFindingsAndLabel(t *testing.T) {
	s := newSlotRig(t)
	res := s.slot.call(t, "review_findings", nil)
	require.False(t, res.IsError)
	assert.Contains(t, resultText(t, res), "review_request", "nothing requested yet")

	require.False(t, s.slot.call(t, "review_request", map[string]any{"head_sha": reviewHead, "intent": "Spin."}).IsError)
	finding := report.Finding{ID: uuid.New(), File: "spin.go", Line: 12, Severity: "major", Summary: "spins backwards",
		Detail: "the sign is flipped", Personas: []string{"standard"}}
	s.postRun(t, "standard", finding)

	res = s.slot.call(t, "review_findings", nil)
	require.False(t, res.IsError, resultText(t, res))
	body := resultText(t, res)
	for _, want := range []string{finding.ID.String(), "[major]", "spin.go:12", "spins backwards", "the sign is flipped", "label: none", "request_changes"} {
		assert.Contains(t, body, want)
	}
	runs := structured(t, res)["runs"].([]any)
	require.Len(t, runs, 1)
	assert.Equal(t, "supervisor", runs[0].(map[string]any)["posted_by"])

	res = s.slot.call(t, "review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "maybe"})
	assert.True(t, res.IsError, "the verdicts are the report route's")
	res = s.slot.call(t, "review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "false_positive", "fixed_in": "abc1234"})
	assert.True(t, res.IsError, "fixed_in is for a confirmed finding")
	res = s.slot.call(t, "review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "false_positive",
		"reason": strings.Repeat("y", reviewreport.MaxLabelReasonChars+1)})
	assert.True(t, res.IsError, "the route's reason limit holds here too")
	res = s.slot.call(t, "review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "confirmed",
		"fixed_in": strings.Repeat("a", reviewreport.MaxFixedInChars+1)})
	assert.True(t, res.IsError, "the route's fixed_in limit holds here too")
	res = s.slot.call(t, "review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "false_positive", "reason": "a\x00b"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "cannot be stored")
	res = s.slot.call(t, "review_label", map[string]any{"finding_id": uuid.NewString(), "verdict": "false_positive"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "no such finding")

	res = s.slot.call(t, "review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "confirmed", "fixed_in": "abc1234", "reason": "flipped it"})
	require.False(t, res.IsError, resultText(t, res))

	body = resultText(t, s.slot.call(t, "review_findings", map[string]any{"head_sha": reviewHead[:7]}))
	assert.Contains(t, body, "label: confirmed in abc1234 -- flipped it")

	// The rig's own agent holds nothing of this attempt, so it labels nothing.
	res = s.call("review_label", map[string]any{"finding_id": finding.ID.String(), "verdict": "wont_fix"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "no such finding")
	body = resultText(t, s.slot.call(t, "review_findings", nil))
	assert.Contains(t, body, "label: confirmed in abc1234", "the refused label changed nothing")
}

func TestReviewTools_WithoutALeaseSayTheAgentHoldsNothing(t *testing.T) {
	s := newSlotRig(t)
	_, err := s.database.Exec(s.ctx, `UPDATE leases SET released_at = now(), release_reason = 'done' WHERE agent_id = $1`, s.slot.agentID)
	require.NoError(t, err)
	for tool, args := range map[string]map[string]any{
		"review_request":  {"head_sha": reviewHead, "intent": "x"},
		"review_findings": nil,
	} {
		res := s.slot.call(t, tool, args)
		assert.True(t, res.IsError, tool)
		assert.Contains(t, strings.ToLower(resultText(t, res)), "lease", tool)
	}
}
