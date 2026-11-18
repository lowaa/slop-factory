package variant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variant"
)

type fixture struct {
	t      *testing.T
	db     *db.DB
	id     uuid.UUID
	ctx    context.Context
	owner  *authn.User
	member *authn.User
	svc    *variant.Service
	fleet  *fleetinstructions.Service
	events *audittest.Counter
	log    *bytes.Buffer
}

func newTenant(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	id := dbtest.NewTenant(t, database)
	counter := audittest.NewCounter()
	log := &bytes.Buffer{}
	recorder := audit.New(slog.NewJSONHandler(log, nil), counter)
	owners := user.NewPostgresStore(database)
	f := &fixture{
		t: t, db: database, id: id, ctx: db.WithTenant(context.Background(), id), events: counter, log: log,
		svc:   variant.NewService(variant.NewPostgresStore(database, agent.VariantWriter{}), owners, recorder, slog.DiscardHandler),
		fleet: fleetinstructions.NewService(fleetinstructions.NewPostgresStore(database), owners, recorder, slog.DiscardHandler),
	}
	f.owner, f.member = f.join("owner"), f.join("member")
	return f
}

func (f *fixture) join(role string) *authn.User {
	f.t.Helper()
	uid := dbtest.NewUser(f.t, f.db)
	_, err := f.db.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), f.id, uid, role)
	require.NoError(f.t, err)
	return &authn.User{ID: uid, TenantID: f.id}
}

func (f *fixture) save(name string) *variant.Version {
	f.t.Helper()
	v, err := f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{Name: name, Harness: "claude", Settings: map[string]string{"model": "sonnet"}})
	require.NoError(f.t, err)
	return v
}

func (f *fixture) listed(includeArchived bool) []string {
	f.t.Helper()
	all, err := f.svc.List(f.ctx, f.owner, includeArchived)
	require.NoError(f.t, err)
	var out []string
	for _, v := range all {
		out = append(out, v.Name+"@"+string(rune('0'+v.Version)))
	}
	return out
}

func TestSave_NumbersVersionsPerNameAndKeepsWhatWasSaved(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	fleetV, err := f.fleet.Save(f.ctx, f.owner, fleetinstructions.KindFleet, "house", "be brief", false)
	require.NoError(t, err)

	a1 := f.save("fast")
	a2, err := f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{
		Name: " fast ", Harness: "pi", Settings: map[string]string{"provider": "openrouter", "model": "qwen/qwen3:free", "thinking": "low"},
		FleetInstructionsVersionID: &fleetV.ID,
	})
	require.NoError(t, err)
	b1 := f.save("slow")

	assert.Equal(t, []int{1, 2, 1}, []int{a1.Version, a2.Version, b1.Version})
	assert.Equal(t, "fast", a2.Name, "names are trimmed")
	assert.Nil(t, a1.FleetInstructionsVersionID, "none is stored as none")
	assert.Equal(t, fleetV.ID, *a2.FleetInstructionsVersionID)

	all, err := f.svc.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, []int{2, 1, 1}, []int{all[0].Version, all[1].Version, all[2].Version}, "by name, newest first")
	assert.Equal(t, "pi", all[0].Harness)
	assert.Equal(t, map[string]string{"provider": "openrouter", "model": "qwen/qwen3:free", "thinking": "low"}, all[0].Settings)
	assert.Equal(t, map[string]string{"model": "sonnet"}, all[1].Settings, "the earlier version is untouched")
}

func TestSave_ConcurrentSavesOfOneNameGetDistinctVersions(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	versions := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, err := f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{Name: "same", Harness: "claude"})
			if assert.NoError(t, err) {
				versions[i] = v.Version
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.ElementsMatch(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, versions)
}

func TestVersionsAreImmutableInTheDatabase(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n")

	for name, stmt := range map[string]string{
		"update harness":  `UPDATE variant_versions SET harness = 'pi' WHERE id = $1`,
		"update settings": `UPDATE variant_versions SET settings = '{}' WHERE id = $1`,
		"delete":          `DELETE FROM variant_versions WHERE id = $1`,
	} {
		_, err := f.db.Exec(f.ctx, stmt, v.ID)
		assert.Error(t, err, name)
	}

	all, err := f.svc.List(f.ctx, f.owner, true)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "claude", all[0].Harness)
	assert.Equal(t, map[string]string{"model": "sonnet"}, all[0].Settings)
}

func TestSave_ChecksHarnessSettingsAndName(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	for name, c := range map[string]variant.SaveRequest{
		"unknown harness":            {Name: "n", Harness: "emacs"},
		"empty harness":              {Name: "n"},
		"pi setting on claude":       {Name: "n", Harness: "claude", Settings: map[string]string{"provider": "openrouter"}},
		"claude setting on pi":       {Name: "n", Harness: "pi", Settings: map[string]string{"effort": "high"}},
		"unknown setting":            {Name: "n", Harness: "claude", Settings: map[string]string{"nope": "x"}},
		"bad enum":                   {Name: "n", Harness: "claude", Settings: map[string]string{"effort": "extreme"}},
		"model that looks like flag": {Name: "n", Harness: "pi", Settings: map[string]string{"model": "--exec"}},
		"empty name":                 {Name: " ", Harness: "claude"},
		"control in name":            {Name: "a\nb", Harness: "claude"},
		"overlong name":              {Name: strings.Repeat("n", variant.MaxNameLen+1), Harness: "claude"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Save(f.ctx, f.owner, &c)
			assert.ErrorIs(t, err, variant.ErrInvalid)
		})
	}
	all, err := f.svc.List(f.ctx, f.owner, true)
	require.NoError(t, err)
	assert.Empty(t, all, "none of the refused saves stored anything")
}

func TestDatabaseRefusesWhatTheServiceWould(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	for name, stmt := range map[string]string{
		"harness":       `INSERT INTO variant_versions (name, version, harness) VALUES ('n', 1, 'emacs')`,
		"settings type": `INSERT INTO variant_versions (name, version, harness, settings) VALUES ('n', 1, 'pi', '[]')`,
		"version zero":  `INSERT INTO variant_versions (name, version, harness) VALUES ('n', 0, 'pi')`,
	} {
		_, err := f.db.Exec(f.ctx, stmt)
		assert.Error(t, err, name)
	}
}

func TestSave_PinnedFleetInstructionsMustBeTheTenants(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newTenant(t, database), newTenant(t, database)
	av, err := a.fleet.Save(a.ctx, a.owner, fleetinstructions.KindFleet, "house", "a's text", false)
	require.NoError(t, err)

	for name, id := range map[string]uuid.UUID{"another tenant's": av.ID, "unknown": uuid.New()} {
		_, err = b.svc.Save(b.ctx, b.owner, &variant.SaveRequest{Name: "n", Harness: "claude", FleetInstructionsVersionID: &id})
		assert.ErrorIs(t, err, variant.ErrInvalid, name)
	}
	all, err := b.svc.List(b.ctx, b.owner, true)
	require.NoError(t, err)
	assert.Empty(t, all)

	_, err = a.svc.Save(a.ctx, a.owner, &variant.SaveRequest{Name: "n", Harness: "claude", FleetInstructionsVersionID: &av.ID})
	assert.NoError(t, err, "its own tenant may pin it")
}

func TestSave_OnlyFleetInstructionsCanBePinned(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	for _, kind := range []fleetinstructions.Kind{fleetinstructions.KindSteward, fleetinstructions.KindSensitiveWords} {
		v, err := f.fleet.Save(f.ctx, f.owner, kind, "other", "text", false)
		require.NoError(t, err)
		_, err = f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{Name: "n", Harness: "claude", FleetInstructionsVersionID: &v.ID})
		assert.ErrorIs(t, err, variant.ErrInvalid, string(kind))
	}
}

func TestTenancy_AnotherTenantsVariantsAreInvisibleAndUnreachable(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newTenant(t, database), newTenant(t, database)
	av := a.save("n")

	all, err := b.svc.List(b.ctx, b.owner, true)
	require.NoError(t, err)
	assert.Empty(t, all)
	_, err = b.svc.ArchiveVersion(b.ctx, b.owner, av.ID)
	assert.ErrorIs(t, err, variant.ErrNotFound)
	_, err = b.svc.ArchiveGroup(b.ctx, b.owner, "n")
	assert.ErrorIs(t, err, variant.ErrNotFound)
	assert.Equal(t, 1, b.save("n").Version, "version numbers are per tenant")
}

func TestOnlyOwnersUseVariantsOnEveryCall(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n")

	_, err := f.svc.List(f.ctx, f.member, true)
	assert.ErrorIs(t, err, variant.ErrNotOwner, "list")
	_, err = f.svc.Save(f.ctx, f.member, &variant.SaveRequest{Name: "n", Harness: "claude"})
	assert.ErrorIs(t, err, variant.ErrNotOwner, "save")
	_, err = f.svc.ArchiveVersion(f.ctx, f.member, v.ID)
	assert.ErrorIs(t, err, variant.ErrNotOwner, "archive")
	_, err = f.svc.UnarchiveVersion(f.ctx, f.member, v.ID)
	assert.ErrorIs(t, err, variant.ErrNotOwner, "unarchive")
	_, err = f.svc.ArchiveGroup(f.ctx, f.member, "n")
	assert.ErrorIs(t, err, variant.ErrNotOwner, "archive group")
	_, err = f.svc.UnarchiveGroup(f.ctx, f.member, "n")
	assert.ErrorIs(t, err, variant.ErrNotOwner, "unarchive group")

	assert.Equal(t, []string{"n@1"}, f.listed(true), "a member changed nothing")
	assert.Equal(t, 6, f.events.Count(audit.VariantRefused))
}

func TestArchive_HidesVersionsAndGroupsKeepsThemAndRestores(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	a1, _ := f.save("a"), f.save("a")
	f.save("b")

	got, err := f.svc.ArchiveVersion(f.ctx, f.owner, a1.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ArchivedAt)
	assert.Equal(t, []string{"a@2", "b@1"}, f.listed(false))
	assert.Equal(t, []string{"a@2", "a@1", "b@1"}, f.listed(true))

	_, err = f.svc.ArchiveVersion(f.ctx, f.owner, a1.ID)
	assert.NoError(t, err, "archiving again is a no-op")
	_, err = f.svc.ArchiveVersion(f.ctx, f.owner, uuid.New())
	assert.ErrorIs(t, err, variant.ErrNotFound)

	n, err := f.svc.ArchiveGroup(f.ctx, f.owner, "a")
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the version not yet archived is newly hidden")
	assert.Equal(t, []string{"b@1"}, f.listed(false))
	_, err = f.svc.ArchiveGroup(f.ctx, f.owner, "nope")
	assert.ErrorIs(t, err, variant.ErrNotFound)

	back, err := f.svc.UnarchiveVersion(f.ctx, f.owner, a1.ID)
	require.NoError(t, err)
	assert.Nil(t, back.ArchivedAt)
	assert.Equal(t, map[string]string{"model": "sonnet"}, back.Settings, "archiving kept what was saved")
	n, err = f.svc.UnarchiveGroup(f.ctx, f.owner, "a")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"a@2", "a@1", "b@1"}, f.listed(false))
}

// auditRecords is what the audit recorder logged, one map per line.
func (f *fixture) auditRecords(action audit.Action) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.log.String()), "\n") {
		var rec map[string]any
		require.NoError(f.t, json.Unmarshal([]byte(line), &rec))
		if rec["action"] == string(action) {
			out = append(out, rec)
		}
	}
	return out
}

func TestSaveAndArchiveAreAudited(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("house")
	f.save("house")
	_, err := f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	_, err = f.svc.UnarchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	_, err = f.svc.ArchiveGroup(f.ctx, f.owner, "house")
	require.NoError(t, err)

	saved := f.auditRecords(audit.VariantVersionSaved)
	require.Len(t, saved, 2)
	assert.Equal(t, f.owner.ID.String(), saved[0]["user_id"])
	assert.Equal(t, f.id.String(), saved[0]["tenant"])
	assert.Equal(t, "house", saved[0]["name"])
	assert.EqualValues(t, 1, saved[0]["version"])
	assert.EqualValues(t, 2, saved[1]["version"])
	assert.Equal(t, "claude", saved[0]["harness"])

	archived := f.auditRecords(audit.VariantArchived)
	require.Len(t, archived, 2)
	assert.EqualValues(t, 1, archived[0]["version"])
	assert.EqualValues(t, 0, archived[1]["version"], "a group is version 0")
	assert.EqualValues(t, 2, archived[1]["versions"])
	assert.Len(t, f.auditRecords(audit.VariantUnarchived), 1)
}

func (f *fixture) server(who *authn.User) *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if who != nil {
			ctx = huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), who))
		}
		next(ctx)
	})
	variant.RegisterRoutes(api, variant.NewHandler(f.svc, slog.DiscardHandler))
	return mux
}

func call(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandler_OwnerSavesListsAndArchives(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	owner := f.server(f.owner)

	rec := call(owner, http.MethodPost, "/api/variants/versions",
		`{"name":"fast","harness":"claude","settings":{"model":"haiku","effort":"low"},"fleet_instructions_version_id":null}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var saved variant.VariantVersionBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &saved))
	assert.Equal(t, 1, saved.Version)
	assert.Nil(t, saved.FleetInstructionsVersionID)
	assert.Equal(t, map[string]string{"model": "haiku", "effort": "low"}, saved.Settings)

	var list struct{ Versions []variant.VariantVersionBody }
	rec = call(owner, http.MethodGet, "/api/variants", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Versions, 1)

	rec = call(owner, http.MethodPost, "/api/variants/versions/"+saved.ID+"/archive", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(owner, http.MethodGet, "/api/variants", "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Empty(t, list.Versions)
	rec = call(owner, http.MethodGet, "/api/variants?include_archived=true", "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Versions, 1)
	assert.True(t, list.Versions[0].Archived)

	rec = call(owner, http.MethodPost, "/api/variants/groups/unarchive", `{"name":"fast"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestHandler_StatusesAndNoRouteChangesAVersion(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n")
	id := v.ID.String()
	const missing = "7f2c1c1e-0000-4000-8000-000000000000"
	good := `{"name":"n","harness":"claude","settings":{},"fleet_instructions_version_id":null}`

	for name, c := range map[string]struct {
		who          *authn.User
		method, path string
		body         string
		want         int
	}{
		"member lists":                 {f.member, http.MethodGet, "/api/variants", ``, http.StatusForbidden},
		"member saves":                 {f.member, http.MethodPost, "/api/variants/versions", good, http.StatusForbidden},
		"member archives":              {f.member, http.MethodPost, "/api/variants/versions/" + id + "/archive", ``, http.StatusForbidden},
		"member unarchives":            {f.member, http.MethodPost, "/api/variants/versions/" + id + "/unarchive", ``, http.StatusForbidden},
		"member archives group":        {f.member, http.MethodPost, "/api/variants/groups/archive", `{"name":"n"}`, http.StatusForbidden},
		"member unarchives group":      {f.member, http.MethodPost, "/api/variants/groups/unarchive", `{"name":"n"}`, http.StatusForbidden},
		"nobody signed in":             {nil, http.MethodGet, "/api/variants", ``, http.StatusUnauthorized},
		"harness mismatch":             {f.owner, http.MethodPost, "/api/variants/versions", `{"name":"n","harness":"pi","settings":{"effort":"low"},"fleet_instructions_version_id":null}`, http.StatusUnprocessableEntity},
		"unknown harness":              {f.owner, http.MethodPost, "/api/variants/versions", `{"name":"n","harness":"emacs","settings":{},"fleet_instructions_version_id":null}`, http.StatusUnprocessableEntity},
		"foreign fleet version":        {f.owner, http.MethodPost, "/api/variants/versions", `{"name":"n","harness":"claude","settings":{},"fleet_instructions_version_id":"` + missing + `"}`, http.StatusUnprocessableEntity},
		"missing fleet field":          {f.owner, http.MethodPost, "/api/variants/versions", `{"name":"n","harness":"claude","settings":{}}`, http.StatusUnprocessableEntity},
		"unknown version archived":     {f.owner, http.MethodPost, "/api/variants/versions/" + missing + "/archive", ``, http.StatusNotFound},
		"update route (no such route)": {f.owner, http.MethodPut, "/api/variants/versions/" + id, good, http.StatusNotFound},
		"patch route":                  {f.owner, http.MethodPatch, "/api/variants/versions/" + id, good, http.StatusNotFound},
		"delete route":                 {f.owner, http.MethodDelete, "/api/variants/versions/" + id, ``, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(f.server(c.who), c.method, c.path, c.body)
			assert.Equal(t, c.want, rec.Code, rec.Body.String())
		})
	}
	assert.Equal(t, []string{"n@1"}, f.listed(true), "none of the refused calls changed anything")
}

// A group archive reads the name's versions and then hides them, so a save
// of that name committing between the two would stay listed. Holding the
// name's lock as a save does makes the archive wait for it.
func TestArchiveGroup_WaitsForASaveOfTheSameName(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.save("a")

	holding, release, saved := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		saved <- f.db.InTx(f.ctx, func(q db.Querier) error {
			if _, err := q.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/variant/' || 'a', 0))`); err != nil {
				return err
			}
			if _, err := q.Exec(f.ctx, `INSERT INTO variant_versions (name, version, harness) VALUES ('a', 2, 'claude')`); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	// A failed assertion must not leave the save holding its transaction.
	var once sync.Once
	releaseSave := func() { once.Do(func() { close(release) }) }
	defer releaseSave()

	archived := make(chan int, 1)
	go func() {
		n, err := f.svc.ArchiveGroup(f.ctx, f.owner, "a")
		assert.NoError(t, err)
		archived <- n
	}()
	// Wait on database state, not time: either the archive is queued behind
	// the lock, or (without the lock) it has already finished.
	for waiting := false; !waiting; {
		select {
		case <-archived:
			t.Fatal("the archive finished while a save of the name was in flight")
		default:
		}
		require.NoError(t, f.db.Global().QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted)`).Scan(&waiting))
	}
	releaseSave()
	require.NoError(t, <-saved)
	assert.Equal(t, 2, <-archived, "the version saved meanwhile is archived too")
	assert.Empty(t, f.listed(false))
}

func recorder() *audit.Recorder {
	r, _ := audittest.NewRecorder()
	return r
}

func (f *fixture) newAgent(name string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(f.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, id, name)
	require.NoError(f.t, err)
	return id
}

func TestSetAgentVariant_OwnersAloneAndAudited(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	agentID, v := f.newAgent("a"), f.save("fast")

	err := f.svc.SetAgentVariant(f.ctx, f.member, agentID, &v.ID)
	assert.ErrorIs(t, err, variant.ErrNotOwner)
	m, err := variant.NewPostgresStore(f.db, agent.VariantWriter{}).ForAgent(f.ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, m, "a refused call changes nothing")
	assert.Equal(t, 1, f.events.Count(audit.VariantRefused))

	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v.ID))
	assert.Equal(t, 1, f.events.Count(audit.AgentVariantChanged))
	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v.ID))
	assert.Equal(t, 1, f.events.Count(audit.AgentVariantChanged), "repeating it is not a change")
	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, nil))
	assert.Equal(t, 2, f.events.Count(audit.AgentVariantChanged))
}

func TestSetAgentVariant_RefusesWhatCannotBeAssigned(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	agentID, v := f.newAgent("a"), f.save("fast")
	other := dbtest.NewTenant(t, f.db)
	otherCtx := db.WithTenant(context.Background(), other)
	foreign, err := variant.NewPostgresStore(f.db, agent.VariantWriter{}).Create(otherCtx, &variant.Version{Name: "x", Harness: "claude", Settings: map[string]string{}})
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &foreign.ID), variant.ErrNotFound, "another tenant's version looks absent")
	assert.ErrorIs(t, f.svc.SetAgentVariant(f.ctx, f.owner, uuid.New(), &v.ID), variant.ErrAgentNotFound)

	_, err = f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	assert.ErrorIs(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v.ID), variant.ErrInvalid, "archived: no new assignment")
}

func TestSetAgentVariant_ArchivingDoesNotMoveAnAgentAlreadyOnIt(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	agentID, v := f.newAgent("a"), f.save("fast")
	store := variant.NewPostgresStore(f.db, agent.VariantWriter{})
	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v.ID))
	before, err := store.ForAgent(f.ctx, agentID)
	require.NoError(t, err)

	_, err = f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v.ID), "it is not a new assignment")
	after, err := store.ForAgent(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// Versions never change, so the membership's revision is what marks a
// running session stale: every change takes a new one, later than the last
// and than any saved config's, and a repeat takes none.
func TestSetAgentVariant_EachChangeTakesANewerRevision(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	agentID, v1 := f.newAgent("a"), f.save("fast")
	v2 := f.save("fast")
	store := variant.NewPostgresStore(f.db, agent.VariantWriter{})
	revision := func() int64 {
		m, err := store.ForAgent(f.ctx, agentID)
		require.NoError(t, err)
		require.NotNil(t, m)
		return m.Revision
	}

	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v1.ID))
	first := revision()
	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v1.ID))
	assert.Equal(t, first, revision(), "a repeat is not a change")

	harness := harnessconfig.NewService(harnessconfig.NewPostgresStore(f.db), recorder(), slog.DiscardHandler)
	saved, err := harness.Save(f.ctx, nil, harnessconfig.Values{"claude": {"model": "opus"}}, f.owner)
	require.NoError(t, err)
	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v2.ID))
	second := revision()
	assert.Greater(t, second, saved.Revision, "later than any saved config's")
	assert.Greater(t, saved.Revision, first)
	assert.Equal(t, 2, mustMembership(t, f, store, agentID).Version)
}

func mustMembership(t *testing.T, f *fixture, store *variant.PostgresStore, agentID uuid.UUID) *harnessconfig.VariantMembership {
	t.Helper()
	m, err := store.ForAgent(f.ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, m)
	return m
}

func TestHarnessConfig_ResolvesAVariantAloneAndRestoresTheOverrideOnLeaving(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	agentID := f.newAgent("a")
	pi, err := f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{Name: "pi", Harness: "pi", Settings: map[string]string{"model": "qwen/qwen3:free"}})
	require.NoError(t, err)
	store := variant.NewPostgresStore(f.db, agent.VariantWriter{})
	harness := harnessconfig.NewService(harnessconfig.NewPostgresStore(f.db), recorder(), slog.DiscardHandler,
		harnessconfig.WithVariants(store), harnessconfig.WithOverrideGate(allowAllConfig{}))
	_, err = harness.Save(f.ctx, nil, harnessconfig.Values{"claude": {"model": "opus"}}, f.owner)
	require.NoError(t, err)
	override, err := harness.Save(f.ctx, &agentID, harnessconfig.Values{"claude": {"effort": "high"}}, f.owner)
	require.NoError(t, err)

	outside, err := harness.Effective(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"model": "opus", "effort": "high"}, outside.Values["claude"])
	assert.Equal(t, override.Revision, outside.Revision)

	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &pi.ID))
	in, err := harness.Effective(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "pi", in.Harness)
	assert.Equal(t, harnessconfig.Values{"claude": {}, "codex": {}, "pi": {"model": "qwen/qwen3:free"}}, in.Values)
	assert.True(t, in.OverrideInactive)
	assert.NotEqual(t, outside.Revision, in.Revision)
	saved, err := harness.Load(f.ctx)
	require.NoError(t, err)
	assert.Contains(t, saved.Overrides, agentID, "the override stays saved")

	require.NoError(t, f.svc.SetAgentVariant(f.ctx, f.owner, agentID, nil))
	back, err := harness.Effective(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, outside.Values, back.Values)
	assert.False(t, back.OverrideInactive)
	assert.NotEqual(t, in.Revision, back.Revision, "a session started in the variant is stale once the agent leaves")
	assert.Equal(t, outside.Revision, back.Revision, "one started before joining runs exactly this config again, so it is current")
}

// Assigning checks the version is not archived, so an archive of it
// committing between that check and the assignment would leave an agent newly
// on an archived version. Holding the name's lock as an archive does makes
// the assignment wait for it, and then see the archive.
func TestSetAgentVariant_WaitsForAnArchiveOfTheSameName(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	agentID, v := f.newAgent("a"), f.save("a")

	holding, release, archived := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		archived <- f.db.InTx(f.ctx, func(q db.Querier) error {
			if _, err := q.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/variant/' || 'a', 0))`); err != nil {
				return err
			}
			if _, err := q.Exec(f.ctx, `INSERT INTO variant_archives (version_id) VALUES ($1)`, v.ID); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	var once sync.Once
	releaseArchive := func() { once.Do(func() { close(release) }) }
	defer releaseArchive()

	assigned := make(chan error, 1)
	go func() { assigned <- f.svc.SetAgentVariant(f.ctx, f.owner, agentID, &v.ID) }()
	for waiting := false; !waiting; {
		select {
		case err := <-assigned:
			t.Fatalf("the assignment finished while an archive of the name was in flight: %v", err)
		default:
		}
		require.NoError(t, f.db.Global().QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted)`).Scan(&waiting))
	}
	releaseArchive()
	require.NoError(t, <-archived)
	assert.ErrorIs(t, <-assigned, variant.ErrInvalid)
}

func TestOptions_AnyMemberSeesUnarchivedVersionsWithoutSettings(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	hidden, shown := f.save("a"), f.save("a")
	f.save("b")
	_, err := f.svc.ArchiveVersion(f.ctx, f.owner, hidden.ID)
	require.NoError(t, err)

	rec := call(f.server(f.member), http.MethodGet, "/api/variants/options", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body variant.ListVariantOptionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body.Body))
	var got []string
	for _, o := range body.Body.Options {
		got = append(got, fmt.Sprintf("%s@%d", o.Name, o.Version))
	}
	assert.Equal(t, []string{"a@2", "b@1"}, got, "the archived version is not offered")
	assert.Equal(t, shown.ID.String(), body.Body.Options[0].ID)
	assert.NotContains(t, rec.Body.String(), "settings")

	assert.Equal(t, http.StatusUnauthorized, call(f.server(nil), http.MethodGet, "/api/variants/options", "").Code)
}

// A version outlives its author's account, without their id. A second owner
// reads it: the deleted one can no longer be asked.
func TestHandler_AVersionOfADeletedAuthorIsFlaggedAndNamesNoId(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.save("fast")
	reader := f.join("owner")
	before := call(f.server(reader), http.MethodGet, "/api/variants", "")
	var live struct{ Versions []variant.VariantVersionBody }
	require.NoError(t, json.Unmarshal(before.Body.Bytes(), &live))
	require.Len(t, live.Versions, 1)
	require.NotNil(t, live.Versions[0].CreatedBy)
	assert.Equal(t, f.owner.ID.String(), *live.Versions[0].CreatedBy)
	assert.False(t, live.Versions[0].CreatedByDeleted)
	_, err := f.db.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.owner.ID)
	require.NoError(t, err)

	rec := call(f.server(reader), http.MethodGet, "/api/variants", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), f.owner.ID.String())
	var list struct{ Versions []variant.VariantVersionBody }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Versions, 1)
	assert.Nil(t, list.Versions[0].CreatedBy)
	assert.True(t, list.Versions[0].CreatedByDeleted)
}

// allowAllConfig leaves the override gate's own rules to harnessconfig's
// tests.
type allowAllConfig struct{}

func (allowAllConfig) MayChangeConfig(context.Context, *authn.User, uuid.UUID) (bool, error) {
	return true, nil
}

// The registry is the only list of harnesses: validation accepts each of its
// names and the database takes it, and refuses anything else.
func TestSave_AcceptsExactlyTheRegisteredHarnesses(t *testing.T) {
	app := dbtest.WithDB(t)
	f := newTenant(t, app)
	for _, name := range harness.Names() {
		_, err := f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{Name: "v-" + name, Harness: name})
		assert.NoError(t, err, name)
	}
	_, err := f.svc.Save(f.ctx, f.owner, &variant.SaveRequest{Name: "v-other", Harness: "not-registered"})
	assert.ErrorIs(t, err, variant.ErrInvalid)

	_, err = f.db.Exec(f.ctx, `INSERT INTO variant_versions (name, version, harness) VALUES ('raw', 1, $1)`, harness.Names()[0])
	assert.NoError(t, err, "the database accepts a registered harness")
	_, err = f.db.Exec(f.ctx, `INSERT INTO variant_versions (name, version, harness) VALUES ('raw', 2, 'not-registered')`)
	assert.Error(t, err, "and refuses an unknown one")
}
