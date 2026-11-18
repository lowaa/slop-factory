package settings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// fakeStore pins the *rules* -- defaults, kind checking, which names
// exist -- independently of the SQL, which store_test.go covers against a
// real database. Against a real empty table, a Get that ignored the
// registry entirely would still return the right answer for the one
// setting that exists.
type fakeStore struct {
	rows       map[string]settings.Setting
	getErr     error
	listErr    error
	upsertErr  error
	upsertName string
	upsertVal  settings.Value
	upsertBy   uuid.UUID
	upserts    int
	// unchanged makes the next Upsert report a write that changed nothing.
	unchanged bool
	previous  settings.Value
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]settings.Setting{}} }

func (f *fakeStore) Get(_ context.Context, name string) (*settings.Setting, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	found, ok := f.rows[name]
	if !ok {
		return nil, settings.ErrNotStored
	}
	return &found, nil
}

func (f *fakeStore) List(context.Context) (map[string]settings.Setting, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func (f *fakeStore) Upsert(_ context.Context, name string, value settings.Value, by uuid.UUID) (*settings.Written, error) {
	f.upserts++
	f.upsertName, f.upsertVal, f.upsertBy = name, value, by
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	stored := settings.Setting{Name: name, Value: value, UpdatedAt: fixedTime}
	f.rows[name] = stored
	return &settings.Written{Setting: stored, Previous: f.previous, Changed: !f.unchanged}, nil
}

// History is not under test here (history_test.go runs it against the real
// store), so this fails loudly if a route reaches it by mistake.
func (f *fakeStore) History(context.Context, string, int64, int) ([]settings.Revision, error) {
	return nil, errors.New("fakeStore: History called")
}

var (
	actor     = uuid.New()
	fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

func newService(store settings.Store) *settings.Service {
	return settings.NewService(store, audit.New(slog.DiscardHandler, nil), slog.DiscardHandler)
}

// The ticket's stated default, and the single most load-bearing fact in
// this package: a fresh install drains tickets.
func TestTicketProcessingEnabled_DefaultsToEnabled(t *testing.T) {
	svc := newService(newFakeStore())

	enabled, err := svc.TicketProcessingEnabled(context.Background())
	require.NoError(t, err)
	assert.True(t, enabled, "ticket processing is enabled by default")
}

func TestTicketProcessingEnabled_ReflectsWhatWasStored(t *testing.T) {
	store := newFakeStore()
	svc := newService(store)
	ctx := context.Background()

	_, err := svc.Set(ctx, settings.TicketProcessingEnabled, settings.BoolValue(false), actor)
	require.NoError(t, err)
	enabled, err := svc.TicketProcessingEnabled(ctx)
	require.NoError(t, err)
	assert.False(t, enabled)

	_, err = svc.Set(ctx, settings.TicketProcessingEnabled, settings.BoolValue(true), actor)
	require.NoError(t, err)
	enabled, err = svc.TicketProcessingEnabled(ctx)
	require.NoError(t, err)
	assert.True(t, enabled, "the switch must go back on again")
}

// A bool is stored as 0/1 in int_val. The accessor must not read a stored
// false as "nothing stored" and hand back the default, which is the
// mistake that makes the toggle look like it does nothing.
func TestTicketProcessingEnabled_StoredFalseIsNotMistakenForUnset(t *testing.T) {
	store := newFakeStore()
	svc := newService(store)

	_, err := svc.Set(context.Background(), settings.TicketProcessingEnabled, settings.BoolValue(false), actor)
	require.NoError(t, err)
	require.Equal(t, settings.BoolValue(false), store.upsertVal)

	enabled, err := svc.TicketProcessingEnabled(context.Background())
	require.NoError(t, err)
	assert.False(t, enabled)
}

// Guessing "enabled" because the database was unreachable is exactly the
// wrong way to be wrong: the callers are deciding whether to hand out
// work.
func TestTicketProcessingEnabled_PropagatesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	sentinel := errors.New("database is down")
	store.getErr = sentinel
	svc := newService(store)

	_, err := svc.TicketProcessingEnabled(context.Background())
	assert.ErrorIs(t, err, sentinel)
}

func TestGet_RejectsAnUnknownSetting(t *testing.T) {
	svc := newService(newFakeStore())

	_, err := svc.Get(context.Background(), "no_such_setting")
	assert.ErrorIs(t, err, settings.ErrUnknownSetting)
}

// A typo in a PATCH must not quietly create a row nothing ever reads.
func TestSet_RefusesAnUnknownSettingWithoutWriting(t *testing.T) {
	store := newFakeStore()
	svc := newService(store)

	_, err := svc.Set(context.Background(), "no_such_setting", settings.BoolValue(true), actor)
	assert.ErrorIs(t, err, settings.ErrUnknownSetting)
	assert.Zero(t, store.upserts, "a rejected write must not reach the store")
}

func TestSet_RefusesAValueOfTheWrongKind(t *testing.T) {
	store := newFakeStore()
	svc := newService(store)

	_, err := svc.Set(context.Background(), settings.TicketProcessingEnabled, settings.StrValue("yes"), actor)
	assert.ErrorIs(t, err, settings.ErrTypeMismatch)
	assert.Zero(t, store.upserts, "a rejected write must not reach the store")
}

// A stored row whose kind no longer matches the declaration is a setting
// that changed shape between releases. The default is the safe answer;
// coercing it would hand callers a value the declaration says is
// impossible.
func TestGet_FallsBackToTheDefaultWhenAStoredRowHasTheWrongKind(t *testing.T) {
	store := newFakeStore()
	store.rows[settings.TicketProcessingEnabled] = settings.Setting{
		Name:      settings.TicketProcessingEnabled,
		Value:     settings.StrValue("false"),
		UpdatedAt: fixedTime,
	}
	svc := newService(store)

	found, err := svc.Get(context.Background(), settings.TicketProcessingEnabled)
	require.NoError(t, err)
	assert.True(t, found.Value.Bool, "the declared default, not a coerced string")
	assert.Equal(t, settings.TypeBool, found.Value.Type)
	// And it says so. Keeping the discarded row's UpdatedAt would have the
	// API report is_default:false and a change time for a value it just
	// threw away -- a setting that claims somebody chose it.
	assert.True(t, found.IsDefault(), "a discarded row is not a stored value")
	assert.Zero(t, found.UpdatedAt)
}

// The UI renders this list, so a setting nobody has touched still has to
// appear -- with its default -- rather than being missing until somebody
// happens to change it.
func TestList_IncludesSettingsThatHaveNeverBeenWritten(t *testing.T) {
	svc := newService(newFakeStore())

	listed, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, len(settings.Names()))

	found := listed[slices.IndexFunc(listed, func(s settings.Setting) bool { return s.Name == settings.TicketProcessingEnabled })]
	assert.True(t, found.Value.Bool)
	assert.True(t, found.IsDefault(), "an untouched setting reports itself as a default")
}

func TestList_ReportsStoredValuesAsNotDefault(t *testing.T) {
	store := newFakeStore()
	svc := newService(store)
	ctx := context.Background()

	_, err := svc.Set(ctx, settings.TicketProcessingEnabled, settings.BoolValue(false), actor)
	require.NoError(t, err)

	listed, err := svc.List(ctx)
	require.NoError(t, err)
	found := listed[slices.IndexFunc(listed, func(s settings.Setting) bool { return s.Name == settings.TicketProcessingEnabled })]
	assert.False(t, found.Value.Bool)
	assert.False(t, found.IsDefault())
}

// A store failure must surface, not be smoothed into a zero value. These
// paths are the ones that decide whether a runtime control is trustworthy:
// a List or Set that quietly returned success on a failed query would show
// the human a switch that does not reflect the database.
func TestList_PropagatesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	sentinel := errors.New("database is down")
	store.listErr = sentinel
	svc := newService(store)

	listed, err := svc.List(context.Background())
	assert.ErrorIs(t, err, sentinel)
	assert.Nil(t, listed, "a failed list must not look like an empty one")
}

func TestSet_PropagatesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	sentinel := errors.New("database is down")
	store.upsertErr = sentinel
	svc := newService(store)

	written, err := svc.Set(context.Background(), settings.TicketProcessingEnabled, settings.BoolValue(false), actor)
	assert.ErrorIs(t, err, sentinel)
	assert.Zero(t, written, "a failed write must not report a value")
}

func TestGet_PropagatesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	sentinel := errors.New("database is down")
	store.getErr = sentinel
	svc := newService(store)

	_, err := svc.Get(context.Background(), settings.TicketProcessingEnabled)
	assert.ErrorIs(t, err, sentinel)
}

func auditedService(store settings.Store) (*settings.Service, *bytes.Buffer) {
	var logs bytes.Buffer
	events := audit.New(slog.NewJSONHandler(&logs, nil), nil)
	return settings.NewService(store, events, slog.DiscardHandler), &logs
}

func TestSet_RecordsAnAuditEventNamingWhoAndTheChange(t *testing.T) {
	store := newFakeStore()
	store.previous = settings.BoolValue(true)
	svc, logs := auditedService(store)

	_, err := svc.Set(context.Background(), settings.TicketProcessingEnabled, settings.BoolValue(false), actor)
	require.NoError(t, err)

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line), "logs: %s", logs.String())
	assert.Equal(t, "audit", line["msg"])
	assert.Equal(t, string(audit.SettingChanged), line["action"])
	assert.Equal(t, actor.String(), line["user_id"])
	assert.Equal(t, settings.TicketProcessingEnabled, line["setting"])
	assert.Equal(t, "true", line["old"])
	assert.Equal(t, "false", line["new"])
	assert.Equal(t, actor, store.upsertBy)
}

func TestSet_AWriteThatChangedNothingIsNotAudited(t *testing.T) {
	store := newFakeStore()
	store.unchanged = true
	svc, logs := auditedService(store)

	_, err := svc.Set(context.Background(), settings.TicketProcessingEnabled, settings.BoolValue(true), actor)
	require.NoError(t, err)
	assert.Empty(t, logs.String())
}

func TestSet_LogsTheOldAndNewValueOfAChange(t *testing.T) {
	var logs bytes.Buffer
	store := newFakeStore()
	store.previous = settings.BoolValue(false)
	svc := settings.NewService(store, audit.New(slog.DiscardHandler, nil),
		slog.NewTextHandler(&logs, nil))
	ctx := context.Background()

	_, err := svc.Set(ctx, settings.TicketProcessingEnabled, settings.BoolValue(true), uuid.New())
	require.NoError(t, err)

	assert.Contains(t, logs.String(), "setting changed")
	assert.Contains(t, logs.String(), "old=false")
	assert.Contains(t, logs.String(), "new=true")
}
