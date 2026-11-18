package mcpserver_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func newStewardRig(t *testing.T, mode steward.Mode, dailyCap int) *realRig {
	t.Helper()
	return newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{steward: &steward.Config{Level: steward.LevelOfMode(mode), Mode: mode, DailyCap: dailyCap}})
}

func (r *realRig) stewardSession(name string) *consoleSession {
	r.t.Helper()
	// The database only takes a steward that a supervisor minted.
	supervisor := uuid.New()
	_, err := r.database.Exec(r.ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, $2, 'supervisor', 1)`, supervisor, "sup-of-"+name)
	require.NoError(r.t, err)
	id := uuid.New()
	_, err = r.database.Exec(r.ctx, `INSERT INTO agents (id, name, kind, steward_minted_by) VALUES ($1, $2, $3, $4)`, id, name, string(agent.KindSteward), supervisor)
	require.NoError(r.t, err)
	return r.connectAs(name, id)
}

func (r *realRig) assessmentCount() int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM steward_assessments`).Scan(&n))
	return n
}

// proposeReady has s propose opening a preparing ticket and returns the
// proposal's id.
func proposeReady(t *testing.T, r *realRig, s *consoleSession) (string, *ticket.Ticket) {
	t.Helper()
	tk := r.forceStatus(ticket.StatusPreparing)
	res := s.call(t, "propose_ready", map[string]any{"ticket_id": tk.ID})
	require.False(t, res.IsError, resultText(t, res))
	var out struct {
		ID string `json:"id"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &out))
	return out.ID, tk
}

func assessOutput(t *testing.T, res any) (verdict, tier string) {
	t.Helper()
	out := assessEvidence(t, res)
	return out.Verdict, out.Tier
}

type assessed struct {
	Verdict, Tier string
	Evidence      []steward.Evidence
}

func assessEvidence(t *testing.T, res any) assessed {
	t.Helper()
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var out assessed
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func (a assessed) check(name string) *steward.Evidence {
	for i := range a.Evidence {
		if a.Evidence[i].Check == name {
			return &a.Evidence[i]
		}
	}
	return nil
}

func TestStewardAssess_RefusesAnyoneButTheSteward(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	console := r.agentSession("console", true)
	id, _ := proposeReady(t, r, console)

	for name, s := range map[string]*consoleSession{"console": console, "worker": r.agentSession("worker", false)} {
		res := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "routine"})
		assert.True(t, res.IsError, name)
		assert.Contains(t, resultText(t, res), "only the steward", name)
	}
	assert.Zero(t, r.assessmentCount())
}

func TestStewardAssess_RecordsTheServersVerdict(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	s := r.stewardSession("steward")
	id, _ := proposeReady(t, r, s)

	res := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "unchanged since filed"})
	require.False(t, res.IsError, resultText(t, res))
	out := assessEvidence(t, res.StructuredContent)
	// A judgment category is the owner's at level 3, whatever its checks say.
	assert.Equal(t, "needs_human", out.Verdict)
	assert.Equal(t, "human", out.Tier)
	require.NotNil(t, out.check(steward.CheckTicketRevUnchanged), "the evidence comes back with the verdict")
	assert.True(t, out.check(steward.CheckTicketRevUnchanged).OK)
	// Every check the category names is wired: none reports itself unavailable.
	cat, ok := steward.Lookup("ticket_ready")
	require.True(t, ok)
	for _, name := range cat.Checks {
		ev := out.check(name)
		require.NotNil(t, ev, name)
		assert.NotEqual(t, "check is not available", ev.Detail, name)
	}
	assert.False(t, out.check(steward.CheckTicketHasBrief).OK, "the bare ticket has no brief")
	assert.Equal(t, 1, r.assessmentCount())

	again := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "again"})
	assert.True(t, again.IsError)
	assert.Equal(t, 1, r.assessmentCount())
}

// The category is the steward's claim, never the verdict: a category that
// does not cover the proposal's kind, or one that is not on the list, stays
// with the owner however it is argued.
func TestStewardAssess_AClaimTheChecksDoNotBackIsTheOwners(t *testing.T) {
	for name, category := range map[string]string{
		"a category that does not cover the kind": "merge_continue",
		"a category that does not exist":          "approve_everything",
	} {
		t.Run(name, func(t *testing.T) {
			r := newStewardRig(t, steward.ModeShadow, 10)
			s := r.stewardSession("steward")
			id, _ := proposeReady(t, r, s)

			res := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": category, "reason": "this is auto, trust me"})
			require.False(t, res.IsError, resultText(t, res))
			verdict, _ := assessOutput(t, res.StructuredContent)
			assert.Equal(t, "needs_human", verdict)
		})
	}
}

func TestStewardAssess_ATicketEditedSinceProposingIsTheOwners(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	s := r.stewardSession("steward")
	id, tk := proposeReady(t, r, s)
	_, err := r.database.Exec(r.ctx, `UPDATE tickets SET revision = revision + 1 WHERE id = $1`, tk.ID)
	require.NoError(t, err)

	res := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "routine"})
	require.False(t, res.IsError, resultText(t, res))
	out := assessEvidence(t, res.StructuredContent)
	assert.Equal(t, "needs_human", out.Verdict)
	require.NotNil(t, out.check(steward.CheckTicketRevUnchanged))
	assert.False(t, out.check(steward.CheckTicketRevUnchanged).OK, "the failed check is named in the evidence")
}

// An edit that claims a path the ticket does not have is not narrowing, so
// narrow_scope is the owner's however it is labelled.
func TestStewardAssess_AnEditThatAddsAPathIsTheOwners(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	s := r.stewardSession("steward")
	tk := r.forceStatus(ticket.StatusPreparing)
	res := s.call(t, "propose_ticket_edit", map[string]any{"ticket_id": tk.ID, "base_revision": tk.Revision, "paths": []string{"new/dir/"}})
	require.False(t, res.IsError, resultText(t, res))
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var proposed struct{ ID string }
	require.NoError(t, json.Unmarshal(raw, &proposed))

	res = s.call(t, "steward_assess", map[string]any{"proposal_id": proposed.ID, "category": "narrow_scope", "reason": "shrinks the scope"})
	require.False(t, res.IsError, resultText(t, res))
	out := assessEvidence(t, res.StructuredContent)
	assert.Equal(t, "needs_human", out.Verdict)
	require.NotNil(t, out.check(steward.CheckNoNewPaths))
	assert.False(t, out.check(steward.CheckNoNewPaths).OK)
}

func TestStewardAssess_ADisabledStewardIsRefused(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	s := r.stewardSession("steward")
	id, _ := proposeReady(t, r, s)
	_, err := r.database.Exec(r.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, s.agentID)
	require.NoError(t, err)

	res := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "routine"})
	assert.True(t, res.IsError)
	assert.Zero(t, r.assessmentCount())
}

func TestStewardAssess_RefusedWhenOffOrOverTheDailyCap(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		r := newStewardRig(t, steward.ModeOff, 10)
		s := r.stewardSession("steward")
		id, _ := proposeReady(t, r, s)
		res := s.call(t, "steward_assess", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "routine"})
		assert.True(t, res.IsError)
		assert.Contains(t, resultText(t, res), "switched off")
		assert.Zero(t, r.assessmentCount())
	})
	t.Run("daily cap", func(t *testing.T) {
		r := newStewardRig(t, steward.ModeShadow, 1)
		s := r.stewardSession("steward")
		first, _ := proposeReady(t, r, s)
		second, _ := proposeReady(t, r, s)
		require.False(t, s.call(t, "steward_assess", map[string]any{"proposal_id": first, "category": "ticket_ready", "reason": "routine"}).IsError)
		res := s.call(t, "steward_assess", map[string]any{"proposal_id": second, "category": "ticket_ready", "reason": "routine"})
		assert.True(t, res.IsError)
		assert.Contains(t, resultText(t, res), "daily cap")
		assert.Equal(t, 1, r.assessmentCount())
	})
}

func TestStewardAssess_OnlyTheProposersOwnProposal(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	console := r.agentSession("console", true)
	id, _ := proposeReady(t, r, console)
	s := r.stewardSession("steward")

	for _, proposal := range []string{id, uuid.NewString()} {
		res := s.call(t, "steward_assess", map[string]any{"proposal_id": proposal, "category": "ticket_ready", "reason": "routine"})
		assert.True(t, res.IsError)
		assert.Contains(t, resultText(t, res), "no such proposal")
	}
	assert.Zero(t, r.assessmentCount())
}

func TestStewardAssess_NotOfferedWithoutAnAssessor(t *testing.T) {
	r := newRealRig(t)
	res, err := r.agentSession("console", true).session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	for _, tool := range res.Tools {
		assert.NotEqual(t, "steward_assess", tool.Name)
	}
}

// The steward may propose, but closing a ticket as merged is a console's or a
// conductor's to ask for.
func TestStewardCannotProposeCloseMerged(t *testing.T) {
	r := newStewardRig(t, steward.ModeShadow, 10)
	tk := r.forceStatus(ticket.StatusOpen)
	s := r.stewardSession("steward")

	res := s.call(t, "propose_close_merged", map[string]any{"ticket_id": tk.ID, "reason": "merged"})

	assert.True(t, res.IsError)
	assert.Zero(t, r.proposalCount())
}
