package mcpserver_test

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

// fakeBatches returns what the test gives it and records the arguments.
type fakeBatches struct {
	out   []landing.BatchSummary
	limit int
	since *time.Time
}

func (f *fakeBatches) Recent(_ context.Context, limit int, since *time.Time) ([]landing.BatchSummary, error) {
	f.limit, f.since = limit, since
	return f.out, nil
}

func callBatches(t *testing.T, session *mcp.ClientSession, args map[string]any) map[string]any {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "landing_batches", Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	return structured(t, res)
}

func TestLandingBatches_AReadOnlyCallerGetsTheSchema(t *testing.T) {
	created := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	fake := &fakeBatches{out: []landing.BatchSummary{
		{Batch: landing.Batch{ID: 9, State: landing.BatchDone, Branch: "slop-haus-batch-9", BaseSHA: "base", Members: []int64{4, 3},
			Heads: []string{"h4", "h3"}, CIPipeline: "55", Change: &codehost.ChangeRef{Number: 12}, CreatedAt: created, UpdatedAt: created.Add(time.Hour)},
			Reason: "batch_voided: main moved"},
		{Batch: landing.Batch{ID: 8, State: landing.BatchBuilding, Members: []int64{2}, CreatedAt: created, UpdatedAt: created}},
	}}
	r := newRigWith(t, &rigOptions{readOnly: true, batches: fake})

	out := callBatches(t, r.session, map[string]any{"limit": 500, "since": "2026-05-01T00:00:00Z"})

	assert.Equal(t, 100, fake.limit, "limit is capped")
	require.NotNil(t, fake.since)
	assert.True(t, fake.since.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)), "the cutoff reaches the store as sent")
	batches := out["batches"].([]any)
	require.Len(t, batches, 2)
	first := batches[0].(map[string]any)
	assert.EqualValues(t, 9, first["id"])
	assert.Equal(t, "done", first["state"])
	assert.EqualValues(t, 12, first["pr_number"])
	assert.Equal(t, "55", first["ci_pipeline"])
	assert.Equal(t, "2026-05-01T11:00:00Z", first["finished_at"])
	assert.Equal(t, "batch_voided: main moved", first["reason"])
	members := first["members"].([]any)
	assert.Equal(t, map[string]any{"ticket_id": float64(4), "tested_head": "h4"}, members[0])
	assert.Equal(t, map[string]any{"ticket_id": float64(3), "tested_head": "h3"}, members[1])
	second := batches[1].(map[string]any)
	assert.Nil(t, second["ci_pipeline"], "a pipeline never recorded is null, not 0")
	assert.Nil(t, second["pr_number"])
	assert.Nil(t, second["finished_at"])
	assert.Nil(t, second["members"].([]any)[0].(map[string]any)["tested_head"])
}

func TestLandingBatches_DefaultLimitAndBadArguments(t *testing.T) {
	fake := &fakeBatches{}
	r := newRigWith(t, &rigOptions{batches: fake})

	assert.Empty(t, callBatches(t, r.session, map[string]any{})["batches"])
	assert.Equal(t, 20, fake.limit)
	assert.Nil(t, fake.since)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "landing_batches", Arguments: map[string]any{"since": "yesterday"}})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "RFC 3339")
}

func TestLandingBatches_NotOfferedWithoutAStore(t *testing.T) {
	r := newRigWith(t, &rigOptions{})
	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		assert.NotEqual(t, "landing_batches", tool.Name)
	}
}

// The caller sees its own tenant's batches from the real store, and not
// another tenant's.
func TestLandingBatches_OnlyTheCallersTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	r := newRealRigWith(t, database, &realRigOptions{batches: landing.NewBatchStore(database)})
	insert := func(ctx context.Context, branch string) {
		_, err := database.Exec(ctx, `INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ('b', $1, '{1}', 'testing')`, branch)
		require.NoError(t, err)
	}
	insert(r.ctx, "mine")
	insert(db.WithTenant(context.Background(), dbtest.NewTenant(t, database)), "theirs")

	out := callBatches(t, r.session, map[string]any{})

	batches := out["batches"].([]any)
	require.Len(t, batches, 1)
	assert.Equal(t, "mine", batches[0].(map[string]any)["branch"])
}
