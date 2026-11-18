package settings_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

type storeFixture struct {
	store    *settings.PostgresStore
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	user     uuid.UUID
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	return &storeFixture{
		store:    settings.NewPostgresStore(database),
		database: database,
		ctx:      db.WithTenant(context.Background(), tenant),
		tenant:   tenant,
		user:     dbtest.NewUser(t, database),
	}
}

// historyRow is a tenant_settings row as the audit trail reads it.
type historyRow struct {
	Value     settings.Value
	ChangedBy *uuid.UUID
}

func (f *storeFixture) history(t *testing.T, name string) []historyRow {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT setting_type, str_value, int_value, bool_value, float_value, changed_by
		  FROM tenant_settings WHERE setting_name = $1 ORDER BY id`, name)
	require.NoError(t, err)
	defer rows.Close()
	var out []historyRow
	for rows.Next() {
		var (
			typ string
			str *string
			i   *int64
			b   *bool
			fl  *float64
			by  *uuid.UUID
		)
		require.NoError(t, rows.Scan(&typ, &str, &i, &b, &fl, &by))
		v := settings.Value{Type: settings.Type(typ)}
		switch v.Type {
		case settings.TypeStr:
			v.Str = *str
		case settings.TypeInt:
			v.Int = *i
		case settings.TypeBool:
			v.Bool = *b
		case settings.TypeFloat:
			v.Float = *fl
		}
		out = append(out, historyRow{Value: v, ChangedBy: by})
	}
	require.NoError(t, rows.Err())
	return out
}

func TestStore_GetReportsNotStoredBeforeAnythingIsWritten(t *testing.T) {
	f := newStoreFixture(t)

	_, err := f.store.Get(f.ctx, settings.TicketProcessingEnabled)
	assert.ErrorIs(t, err, settings.ErrNotStored)
}

func TestStore_UpsertRoundTripsThroughTenantSettings(t *testing.T) {
	f := newStoreFixture(t)

	written, err := f.store.Upsert(f.ctx, settings.TicketProcessingEnabled, settings.BoolValue(false), f.user)
	require.NoError(t, err)
	assert.True(t, written.Changed)
	assert.Equal(t, settings.BoolValue(true), written.Previous, "the default, as nothing was stored")

	read, err := f.store.Get(f.ctx, settings.TicketProcessingEnabled)
	require.NoError(t, err)
	assert.Equal(t, settings.BoolValue(false), read.Value)
	assert.False(t, read.IsDefault())

	listed, err := f.store.List(f.ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, settings.BoolValue(false), listed[settings.TicketProcessingEnabled].Value)
}

// Every change is a row of its own, so the table is the history: who, when and
// from what to what.
func TestStore_EachChangeIsARowRecordingWhoMadeIt(t *testing.T) {
	f := newStoreFixture(t)
	other := dbtest.NewUser(t, f.database)
	name := settings.TicketProcessingEnabled
	before := time.Now().Add(-time.Second)

	first, err := f.store.Upsert(f.ctx, name, settings.BoolValue(false), f.user)
	require.NoError(t, err)
	second, err := f.store.Upsert(f.ctx, name, settings.BoolValue(true), other)
	require.NoError(t, err)
	assert.Equal(t, settings.BoolValue(false), second.Previous)
	assert.Equal(t, settings.BoolValue(false), first.Setting.Value)

	assert.Equal(t, []historyRow{
		{Value: settings.BoolValue(false), ChangedBy: &f.user},
		{Value: settings.BoolValue(true), ChangedBy: &other},
	}, f.history(t, name))
	assert.True(t, second.Setting.UpdatedAt.After(before))
	assert.True(t, second.Setting.UpdatedAt.After(first.Setting.UpdatedAt), "updated_at is the newest row's time")
}

func TestStore_AWriteThatChangesNothingRecordsNothing(t *testing.T) {
	f := newStoreFixture(t)
	name := settings.TicketProcessingEnabled

	// Equal to the default with no row stored.
	written, err := f.store.Upsert(f.ctx, name, settings.BoolValue(true), f.user)
	require.NoError(t, err)
	assert.False(t, written.Changed)
	assert.True(t, written.Setting.IsDefault())
	assert.Empty(t, f.history(t, name))

	_, err = f.store.Upsert(f.ctx, name, settings.BoolValue(false), f.user)
	require.NoError(t, err)
	// Equal to the latest row.
	written, err = f.store.Upsert(f.ctx, name, settings.BoolValue(false), f.user)
	require.NoError(t, err)
	assert.False(t, written.Changed)
	assert.False(t, written.Setting.IsDefault(), "it reports the stored row")
	assert.Len(t, f.history(t, name), 1)
}

func TestStore_ARecordedWriteNeedsAUser(t *testing.T) {
	f := newStoreFixture(t)

	_, err := f.store.Upsert(f.ctx, settings.TicketProcessingEnabled, settings.BoolValue(false), uuid.Nil)
	assert.ErrorIs(t, err, settings.ErrNoActor)
	assert.Empty(t, f.history(t, settings.TicketProcessingEnabled))
}

func TestStore_ARowOfTheWrongTypeReadsAsTheDefault(t *testing.T) {
	f := newStoreFixture(t)
	name := settings.TicketProcessingEnabled
	_, err := f.database.Exec(f.ctx,
		`INSERT INTO tenant_settings (setting_name, setting_type, str_value) VALUES ($1, 'str', 'false')`, name)
	require.NoError(t, err)

	enabled, err := settings.EnabledIn(f.ctx, f.database, name)
	require.NoError(t, err)
	assert.True(t, enabled)

	// So writing the default is no change either, as Service.Get reads it, and
	// it reports the default rather than the discarded row.
	written, err := f.store.Upsert(f.ctx, name, settings.BoolValue(true), f.user)
	require.NoError(t, err)
	assert.False(t, written.Changed)
	assert.Equal(t, settings.BoolValue(true), written.Setting.Value)
	assert.True(t, written.Setting.IsDefault())
}

func TestStore_AnotherTenantNeverSeesTheRows(t *testing.T) {
	f := newStoreFixture(t)
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	name := settings.TicketProcessingEnabled

	_, err := f.store.Upsert(f.ctx, name, settings.BoolValue(false), f.user)
	require.NoError(t, err)

	_, err = f.store.Get(otherCtx, name)
	assert.ErrorIs(t, err, settings.ErrNotStored)
	listed, err := f.store.List(otherCtx)
	require.NoError(t, err)
	assert.Empty(t, listed)
	enabled, err := settings.EnabledIn(otherCtx, f.database, name)
	require.NoError(t, err)
	assert.True(t, enabled, "the other tenant's pause is not this one's")

	// Writing there neither reads nor disturbs the first tenant's value.
	_, err = f.store.Upsert(otherCtx, name, settings.BoolValue(false), f.user)
	require.NoError(t, err)
	_, err = f.store.Upsert(otherCtx, name, settings.BoolValue(true), f.user)
	require.NoError(t, err)
	read, err := f.store.Get(f.ctx, name)
	require.NoError(t, err)
	assert.Equal(t, settings.BoolValue(false), read.Value)
}

// A setting of the same name in a user's own scope is a different setting. The
// tenant-wide layer reads tenant_settings and nothing else, so a user's value
// -- whatever they set it to -- never reads as the tenant's, nor the tenant's as
// theirs.
func TestStore_ASameNamedUserSettingIsNotTheTenantSetting(t *testing.T) {
	f := newStoreFixture(t)
	name := settings.TicketProcessingEnabled
	_, err := f.database.Global().Exec(f.ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role, created_at) VALUES ($1, $2, $3, 'member', $4)`,
		uuid.New(), f.tenant, f.user, time.Now().Add(-time.Hour))
	require.NoError(t, err)

	// The user's rows, as the user scopes write them: off, in both.
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		if _, err := q.Exec(f.ctx, `SELECT set_config('app.current_user_id', $1, true)`, f.user.String()); err != nil {
			return err
		}
		if _, err := q.Exec(f.ctx, `
			INSERT INTO user_tenant_settings (user_id, setting_name, setting_type, bool_value) VALUES ($1, $2, 'bool', false)`,
			f.user, name); err != nil {
			return err
		}
		_, err := q.Exec(f.ctx, `
			INSERT INTO global_user_settings (user_id, setting_name, setting_type, bool_value) VALUES ($1, $2, 'bool', false)`,
			f.user, name)
		return err
	}))

	_, err = f.store.Get(f.ctx, name)
	assert.ErrorIs(t, err, settings.ErrNotStored, "the user's rows are not the tenant's")
	enabled, err := settings.EnabledIn(f.ctx, f.database, name)
	require.NoError(t, err)
	assert.True(t, enabled)

	// And the other way: the tenant's write does not appear as the user's.
	_, err = f.store.Upsert(f.ctx, name, settings.BoolValue(false), f.user)
	require.NoError(t, err)
	var users int
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		if _, err := q.Exec(f.ctx, `SELECT set_config('app.current_user_id', $1, true)`, f.user.String()); err != nil {
			return err
		}
		return q.QueryRow(f.ctx, `SELECT count(*) FROM user_tenant_settings WHERE setting_name = $1`, name).Scan(&users)
	}))
	assert.Equal(t, 1, users, "writing the tenant's setting adds nothing to the user's")
}

// EnabledIn is how the claim path reads the switch inside its own
// transaction, so it has to give the same answers Service.Get does.
func TestEnabledIn_ReadsTheLatestRowOrTheDefault(t *testing.T) {
	f := newStoreFixture(t)
	name := settings.TicketProcessingEnabled

	enabled, err := settings.EnabledIn(f.ctx, f.database, name)
	require.NoError(t, err)
	assert.True(t, enabled, "nothing stored reads as the declared default")

	for _, want := range []bool{false, true, false} {
		_, err = f.store.Upsert(f.ctx, name, settings.BoolValue(want), f.user)
		require.NoError(t, err)
		enabled, err = settings.EnabledIn(f.ctx, f.database, name)
		require.NoError(t, err)
		assert.Equal(t, want, enabled)
	}
}

func TestEnabledIn_RefusesAnUnknownSetting(t *testing.T) {
	f := newStoreFixture(t)

	_, err := settings.EnabledIn(f.ctx, f.database, "no_such_setting")
	assert.ErrorIs(t, err, settings.ErrUnknownSetting)
}

// A failed read must surface as an error, never as "disabled" or as the
// default: the claim path refuses on an error, whereas a guessed false would
// pause the fleet and a guessed default could serve work through a pause.
func TestEnabledIn_ReportsAFailedRead(t *testing.T) {
	f := newStoreFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()

	_, err := settings.EnabledIn(ctx, f.database, settings.TicketProcessingEnabled)
	assert.ErrorIs(t, err, context.Canceled)
}

// Writers queue on the claim lock and compare with the latest row only once
// they hold it, so two writes of one value make one change. Without the lock
// both read the old value and both append.
func TestStore_ConcurrentWritesOfOneValueMakeOneChange(t *testing.T) {
	f := newStoreFixture(t)
	name := settings.TicketProcessingEnabled

	held, release := make(chan struct{}), make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- f.database.InTx(f.ctx, func(q db.Querier) error {
			if err := db.TakeAdvisoryLock(f.ctx, q, db.ClaimLockKey); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLock)

	const writers = 2
	results := make(chan *settings.Written, writers)
	for range writers {
		go func() {
			written, err := f.store.Upsert(f.ctx, name, settings.BoolValue(false), f.user)
			assert.NoError(t, err)
			results <- written
		}()
	}
	require.Eventually(t, func() bool {
		var waiting int
		err := f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting)
		return err == nil && waiting >= writers
	}, 10*time.Second, 10*time.Millisecond, "the writers never queued on the claim lock")

	releaseLock()
	require.NoError(t, <-holderDone)
	changed := 0
	for range writers {
		if w := <-results; w != nil && w.Changed {
			changed++
		}
	}
	assert.Equal(t, 1, changed)
	assert.Len(t, f.history(t, name), 1)
}
