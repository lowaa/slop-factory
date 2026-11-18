package wakeup_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

type storeRig struct {
	db    *db.DB
	store *wakeup.PostgresStore
	ctx   context.Context
	agent uuid.UUID
}

func newStoreRig(t *testing.T) *storeRig {
	t.Helper()
	database := dbtest.WithDB(t)
	r := &storeRig{db: database, store: wakeup.NewPostgresStore(database)}
	r.ctx = db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	r.agent = r.newAgent(t)
	return r
}

func (r *storeRig) newAgent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := r.db.Exec(r.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, id, "agent-"+id.String())
	require.NoError(t, err)
	return id
}

func TestStore_RecordPollNeverMovesLastPolledAtBackwards(t *testing.T) {
	r := newStoreRig(t)
	require.NoError(t, r.store.RecordPoll(r.ctx, r.agent, "v1", 0))

	// A poll whose transaction started earlier but committed later would write
	// an older time over this one; model it as a stored time ahead of the clock.
	var later time.Time
	require.NoError(t, r.db.QueryRow(r.ctx, `
		UPDATE worker_polls SET last_polled_at = clock_timestamp() + interval '1 hour'
		WHERE agent_id = $1 RETURNING last_polled_at`, r.agent).Scan(&later))

	require.NoError(t, r.store.RecordPoll(r.ctx, r.agent, "v2", 0))

	var got time.Time
	var version string
	require.NoError(t, r.db.QueryRow(r.ctx,
		`SELECT last_polled_at, version FROM worker_polls WHERE agent_id = $1`, r.agent).Scan(&got, &version))
	assert.True(t, got.Equal(later), "last_polled_at moved from %v to %v", later, got)
	assert.Equal(t, "v2", version, "the version still follows the latest poll")
}
