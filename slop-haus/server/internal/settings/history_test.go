package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// historyRig serves the real service over the real store, so the wire shape,
// the paging and the people behind each change are the ones production gives.
type historyRig struct {
	database *db.DB
	store    *settings.PostgresStore
	svc      *settings.Service
	mux      *http.ServeMux
	tokens   map[string]*authn.User
}

func newHistoryRig(t *testing.T) *historyRig {
	t.Helper()
	database := dbtest.WithDB(t)
	r := &historyRig{database: database, mux: http.NewServeMux(), tokens: map[string]*authn.User{}}
	r.store = settings.NewPostgresStore(database)
	r.svc = newService(r.store)

	api := humago.New(r.mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		if u, ok := r.tokens[token]; ok {
			return u, nil
		}
		return nil, errors.New("unknown token")
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	settings.RegisterRoutes(api, settings.NewHandler(r.svc, slog.DiscardHandler))
	return r
}

// member creates a person who belongs to the tenant, with a display name.
func (r *historyRig) member(t *testing.T, tenant uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := dbtest.NewUser(t, r.database)
	ctx := context.Background()
	_, err := r.database.Global().Exec(ctx, `UPDATE global_users SET display_name = $2 WHERE id = $1`, id, name)
	require.NoError(t, err)
	_, err = r.database.Global().Exec(ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)`, uuid.New(), tenant, id)
	require.NoError(t, err)
	return id
}

func (r *historyRig) signIn(user, tenant uuid.UUID) string {
	token := uuid.NewString()
	r.tokens[token] = &authn.User{ID: user, TenantID: tenant}
	return token
}

func (r *historyRig) get(path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

type valueFields struct {
	Type      string   `json:"type"`
	StringVal *string  `json:"string_val"`
	IntVal    *int64   `json:"int_val"`
	FloatVal  *float64 `json:"float_val"`
	BoolVal   *bool    `json:"bool_val"`
}

type historyPage struct {
	Changes []struct {
		ID                    int64       `json:"id"`
		From                  valueFields `json:"from"`
		To                    valueFields `json:"to"`
		ChangedAt             string      `json:"changed_at"`
		ChangedByID           *uuid.UUID  `json:"changed_by_id"`
		ChangedByName         *string     `json:"changed_by_name"`
		ChangedByFormerMember bool        `json:"changed_by_former_member"`
		ChangedByDeleted      bool        `json:"changed_by_deleted"`
		BeforeHistoryKept     bool        `json:"before_history_kept"`
	} `json:"changes"`
	NextBefore *int64 `json:"next_before"`
}

func (r *historyRig) history(t *testing.T, query, token string) historyPage {
	t.Helper()
	rec := r.get("/api/settings/"+settings.TicketProcessingEnabled+"/history"+query, token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page historyPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	return page
}

func (r *historyRig) set(t *testing.T, ctx context.Context, enabled bool, by uuid.UUID) {
	t.Helper()
	_, err := r.svc.Set(ctx, settings.TicketProcessingEnabled, settings.BoolValue(enabled), by)
	require.NoError(t, err)
}

func TestHistory_RequiresSignIn(t *testing.T) {
	r := newHistoryRig(t)
	rec := r.get("/api/settings/"+settings.TicketProcessingEnabled+"/history", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHistory_UnknownSettingIsNotFound(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	token := r.signIn(r.member(t, tenant, "Ada"), tenant)

	rec := r.get("/api/settings/no_such_setting/history", token)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHistory_NothingWrittenIsAnEmptyList(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	token := r.signIn(r.member(t, tenant, "Ada"), tenant)

	page := r.history(t, "", token)
	assert.NotNil(t, page.Changes, "an empty list, not null")
	assert.Empty(t, page.Changes)
	assert.Nil(t, page.NextBefore)
}

func TestHistory_NewestFirstWithWhoWhenAndFromTo(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")
	grace := r.member(t, tenant, "Grace")

	r.set(t, ctx, false, ada)   // default true -> false
	r.set(t, ctx, false, grace) // no change: not recorded
	r.set(t, ctx, true, grace)  // false -> true

	page := r.history(t, "", r.signIn(ada, tenant))
	require.Len(t, page.Changes, 2, "the write that changed nothing adds nothing")
	newest, oldest := page.Changes[0], page.Changes[1]
	assert.Greater(t, newest.ID, oldest.ID)

	for _, v := range []valueFields{newest.From, newest.To, oldest.From, oldest.To} {
		assert.Equal(t, "bool", v.Type)
		assert.Nil(t, v.StringVal)
		assert.Nil(t, v.IntVal)
		assert.Nil(t, v.FloatVal)
		require.NotNil(t, v.BoolVal)
	}
	assert.Equal(t, false, *newest.From.BoolVal)
	assert.Equal(t, true, *newest.To.BoolVal)
	require.NotNil(t, newest.ChangedByName)
	assert.Equal(t, "Grace", *newest.ChangedByName)
	assert.Equal(t, grace, *newest.ChangedByID)
	assert.NotEmpty(t, newest.ChangedAt)

	assert.Equal(t, true, *oldest.From.BoolVal, "the first change reads from the declared default")
	assert.Equal(t, false, *oldest.To.BoolVal)
	assert.Equal(t, "Ada", *oldest.ChangedByName)
	for _, c := range page.Changes {
		assert.False(t, c.ChangedByFormerMember)
		assert.False(t, c.ChangedByDeleted)
		assert.False(t, c.BeforeHistoryKept)
	}
}

func TestHistory_Pages(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")
	token := r.signIn(ada, tenant)

	// Five changes: false, true, false, true, false.
	for i := range 5 {
		r.set(t, ctx, i%2 == 1, ada)
	}

	first := r.history(t, "?limit=2", token)
	require.Len(t, first.Changes, 2)
	require.NotNil(t, first.NextBefore)
	second := r.history(t, fmt.Sprintf("?limit=2&before=%d", *first.NextBefore), token)
	require.Len(t, second.Changes, 2)
	require.NotNil(t, second.NextBefore)
	last := r.history(t, fmt.Sprintf("?limit=2&before=%d", *second.NextBefore), token)
	require.Len(t, last.Changes, 1)
	assert.Nil(t, last.NextBefore, "the oldest page has no cursor")

	all := r.history(t, "", token)
	require.Len(t, all.Changes, 5)
	var paged []int64
	for _, p := range []historyPage{first, second, last} {
		for _, c := range p.Changes {
			paged = append(paged, c.ID)
		}
	}
	var whole []int64
	for _, c := range all.Changes {
		whole = append(whole, c.ID)
	}
	assert.Equal(t, whole, paged, "pages tile the history with no gap or overlap")

	// A page's last change still reads from the change just before it, which is
	// on the next page.
	lastOfFirst := first.Changes[1]
	assert.Equal(t, *all.Changes[2].To.BoolVal, *lastOfFirst.From.BoolVal)

	// Every change reads from the one before it, across page boundaries too;
	// only the oldest reads from the default.
	for i := 0; i < len(all.Changes)-1; i++ {
		assert.Equal(t, *all.Changes[i+1].To.BoolVal, *all.Changes[i].From.BoolVal, "change %d", i)
	}
	assert.Equal(t, true, *all.Changes[4].From.BoolVal, "the declared default")
	assert.Equal(t, *second.Changes[1].To.BoolVal, *last.Changes[0].From.BoolVal)
}

// A history that is an exact multiple of the page size ends on a full page
// whose last change is the oldest: it reads from the default, not from a row
// that is not there.
func TestHistory_ExactMultipleOfThePageSize(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")
	token := r.signIn(ada, tenant)
	for _, v := range []bool{false, true, false, true} {
		r.set(t, ctx, v, ada)
	}

	first := r.history(t, "?limit=2", token)
	require.NotNil(t, first.NextBefore)
	second := r.history(t, fmt.Sprintf("?limit=2&before=%d", *first.NextBefore), token)
	require.Len(t, second.Changes, 2)
	assert.Nil(t, second.NextBefore)
	assert.Equal(t, true, *second.Changes[0].To.BoolVal)
	assert.Equal(t, *second.Changes[0].To.BoolVal, *first.Changes[1].From.BoolVal)
	assert.Equal(t, true, *second.Changes[1].From.BoolVal, "the oldest reads from the default")
	assert.Equal(t, false, *second.Changes[1].To.BoolVal)
}

func TestHistory_AnotherTenantsChangesAreNotVisible(t *testing.T) {
	r := newHistoryRig(t)
	tenantA, tenantB := dbtest.NewTenant(t, r.database), dbtest.NewTenant(t, r.database)
	ada := r.member(t, tenantA, "Ada")
	bob := r.member(t, tenantB, "Bob")
	r.set(t, db.WithTenant(context.Background(), tenantA), false, ada)

	page := r.history(t, "", r.signIn(bob, tenantB))
	assert.Empty(t, page.Changes)
}

func TestHistory_ARowFromBeforeHistoryWasKeptIsNotADeletedUser(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")

	// What the backfill of the old settings table left: a row naming no one.
	_, err := r.database.Exec(ctx, `
		INSERT INTO tenant_settings (setting_name, setting_type, bool_value) VALUES ($1, 'bool', false)`, settings.TicketProcessingEnabled)
	require.NoError(t, err)
	r.set(t, ctx, true, ada)

	page := r.history(t, "", r.signIn(ada, tenant))
	require.Len(t, page.Changes, 2)
	assert.False(t, page.Changes[0].BeforeHistoryKept)

	old := page.Changes[1]
	assert.True(t, old.BeforeHistoryKept)
	assert.False(t, old.ChangedByDeleted, "not a deleted user")
	assert.Nil(t, old.ChangedByID)
	assert.Nil(t, old.ChangedByName)
}

func TestHistory_ADeletedUserIsFlaggedAndNamesNoOne(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")
	gone := r.member(t, tenant, "Gone")
	r.set(t, ctx, false, gone)

	// What the account-removal routine leaves behind.
	_, err := r.database.Exec(ctx, `UPDATE tenant_settings SET changed_by = NULL, changed_by_deleted = true WHERE changed_by = $1`, gone)
	require.NoError(t, err)

	page := r.history(t, "", r.signIn(ada, tenant))
	require.Len(t, page.Changes, 1)
	c := page.Changes[0]
	assert.True(t, c.ChangedByDeleted)
	assert.False(t, c.BeforeHistoryKept)
	assert.Nil(t, c.ChangedByID)
	assert.Nil(t, c.ChangedByName)
}

func TestHistory_AFormerMemberIsStillNamedAndFlagged(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")
	left := r.member(t, tenant, "Lee")
	r.set(t, ctx, false, left)
	_, err := r.database.Global().Exec(context.Background(),
		`DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, tenant, left)
	require.NoError(t, err)

	page := r.history(t, "", r.signIn(ada, tenant))
	require.Len(t, page.Changes, 1)
	c := page.Changes[0]
	assert.True(t, c.ChangedByFormerMember)
	require.NotNil(t, c.ChangedByName)
	assert.Equal(t, "Lee", *c.ChangedByName)
	assert.False(t, c.ChangedByDeleted)
}

func TestHistory_APersonWithNoNameHasNoNameOnTheWire(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	nameless := r.member(t, tenant, "")
	r.set(t, ctx, false, nameless)

	page := r.history(t, "", r.signIn(nameless, tenant))
	require.Len(t, page.Changes, 1)
	assert.Equal(t, nameless, *page.Changes[0].ChangedByID)
	assert.Nil(t, page.Changes[0].ChangedByName, "never the address as a stand-in")
}

func TestHistory_LimitOutOfRangeIsRefused(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	token := r.signIn(r.member(t, tenant, "Ada"), tenant)
	for _, q := range []string{"?limit=0", "?limit=201", "?before=-1"} {
		rec := r.get("/api/settings/"+settings.TicketProcessingEnabled+"/history"+q, token)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, q)
	}
}

// Who may write is the service's to decide, whatever transport asks: a write
// naming no person is refused before anything is stored.
func TestSetWithoutAPersonIsRefusedAndStoresNothing(t *testing.T) {
	r := newHistoryRig(t)
	ctx := db.WithTenant(context.Background(), dbtest.NewTenant(t, r.database))

	_, err := r.svc.Set(ctx, settings.TicketProcessingEnabled, settings.BoolValue(false), uuid.Nil)
	assert.ErrorIs(t, err, settings.ErrNoActor)

	got, err := r.svc.Get(ctx, settings.TicketProcessingEnabled)
	require.NoError(t, err)
	assert.Equal(t, settings.BoolValue(true), got.Value, "the default stands")
}
