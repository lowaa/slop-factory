package mcpserver_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

func newAssistRig(t *testing.T, level steward.Level) *realRig {
	t.Helper()
	return newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{steward: &steward.Config{Level: level, Mode: level.Mode(), DailyCap: 10}})
}

func (r *realRig) draftCount() int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM steward_drafts`).Scan(&n))
	return n
}

func TestStewardDraft_RefusesAnyoneButTheSteward(t *testing.T) {
	r := newAssistRig(t, steward.LevelAssist)
	console := r.agentSession("console", true)
	id, _ := proposeReady(t, r, console)

	for name, s := range map[string]*consoleSession{"console": console, "worker": r.agentSession("worker", false)} {
		res := s.call(t, "steward_draft", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "routine"})
		assert.True(t, res.IsError, name)
		assert.Contains(t, resultText(t, res), "only the steward", name)
	}
	assert.Zero(t, r.draftCount())
}

func TestStewardDraft_OnlyAtTheAssistLevel(t *testing.T) {
	for _, level := range []steward.Level{steward.LevelShadow, steward.LevelAssist + 1} {
		r := newAssistRig(t, level)
		s := r.stewardSession("steward")
		id, _ := proposeReady(t, r, s)

		res := s.call(t, "steward_draft", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "routine"})
		assert.True(t, res.IsError, "level %d", level)
		assert.Contains(t, resultText(t, res), "assist level", "level %d", level)
		assert.Zero(t, r.draftCount())
	}
}

func TestStewardDraft_IsKeptAndShownOnTheStewardsProposal(t *testing.T) {
	r := newAssistRig(t, steward.LevelAssist)
	s := r.stewardSession("steward")
	id, tk := proposeReady(t, r, s)

	res := s.call(t, "steward_draft", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "unchanged since filed", "body": "open it"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, 1, r.draftCount())
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var drafted struct {
		Revision int
		Evidence []steward.Evidence
	}
	require.NoError(t, json.Unmarshal(raw, &drafted))
	assert.Equal(t, tk.Revision, drafted.Revision, "the revision is the server's reading")
	assert.NotEmpty(t, drafted.Evidence)

	again := s.call(t, "steward_draft", map[string]any{"proposal_id": id, "category": "ticket_ready", "reason": "again"})
	assert.True(t, again.IsError, "one draft per proposal")

	listed, err := json.Marshal(s.call(t, "list_proposals", map[string]any{}).StructuredContent)
	require.NoError(t, err)
	var list struct {
		Proposals []struct {
			ID    string
			Draft *struct {
				Category string
				Body     string
				Reason   string
				Revision int
				Evidence []steward.Evidence
			}
		}
	}
	require.NoError(t, json.Unmarshal(listed, &list))
	require.Len(t, list.Proposals, 1)
	require.NotNil(t, list.Proposals[0].Draft)
	assert.Equal(t, "ticket_ready", list.Proposals[0].Draft.Category)
	assert.Equal(t, "open it", list.Proposals[0].Draft.Body)
	assert.Equal(t, tk.Revision, list.Proposals[0].Draft.Revision)
	assert.NotEmpty(t, list.Proposals[0].Draft.Evidence)
}
