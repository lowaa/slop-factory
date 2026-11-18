package fleetinstructions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

type fixture struct {
	t      *testing.T
	db     *db.DB
	id     uuid.UUID
	ctx    context.Context
	owner  *authn.User
	member *authn.User
	svc    *fleetinstructions.Service
	events *audittest.Counter
	log    *bytes.Buffer
	// codeHost is what the handler is told about the tenant's credential.
	codeHost *codeHostFake
}

func newTenant(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	id := dbtest.NewTenant(t, database)
	counter := audittest.NewCounter()
	log := &bytes.Buffer{}
	recorder := audit.New(slog.NewJSONHandler(log, nil), counter)
	f := &fixture{
		t: t, db: database, id: id, ctx: db.WithTenant(context.Background(), id), events: counter, log: log, codeHost: &codeHostFake{},
		svc: fleetinstructions.NewService(fleetinstructions.NewPostgresStore(database),
			user.NewPostgresStore(database), recorder, slog.DiscardHandler),
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

func (f *fixture) save(name, body string, makeDefault bool) *fleetinstructions.Version {
	f.t.Helper()
	v, err := f.svc.Save(f.ctx, f.owner, fleetinstructions.KindFleet, name, body, makeDefault)
	require.NoError(f.t, err)
	return v
}

func TestSave_NumbersVersionsPerNameAndNeverChangesAnEarlierOne(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	a1 := f.save("review-first", "one", false)
	a2 := f.save("review-first", "two", false)
	b1 := f.save("terse", "other", false)

	assert.Equal(t, []int{1, 2, 1}, []int{a1.Version, a2.Version, b1.Version})
	all, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	byID := map[uuid.UUID]string{}
	for _, v := range all.Versions {
		byID[v.ID] = v.Body
	}
	assert.Equal(t, map[uuid.UUID]string{a1.ID: "one", a2.ID: "two", b1.ID: "other"}, byID)
	assert.Equal(t, "review-first", all.Versions[0].Name, "by name, then newest version first")
	assert.Equal(t, 2, all.Versions[0].Version)
}

func TestSave_ConcurrentSavesOfOneNameGetDistinctVersions(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	const n = 8
	versions := make(chan int, n)
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			v, err := f.svc.Save(f.ctx, f.owner, fleetinstructions.KindFleet, "same", "text", false)
			if err != nil {
				errs <- err
				return
			}
			versions <- v.Version
		}()
	}
	close(start)
	seen := map[int]bool{}
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			require.NoError(t, err)
		case v := <-versions:
			seen[v] = true
		}
	}
	for v := 1; v <= n; v++ {
		assert.True(t, seen[v], "version %d was handed out", v)
	}
}

func TestVersionsAreImmutableInTheDatabase(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n", "original", false)

	_, err := f.db.Exec(f.ctx, `UPDATE fleet_instruction_versions SET body = 'changed' WHERE id = $1`, v.ID)
	assert.Error(t, err, "an update is refused")
	_, err = f.db.Exec(f.ctx, `DELETE FROM fleet_instruction_versions WHERE id = $1`, v.ID)
	assert.Error(t, err, "a delete is refused")

	all, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	require.Len(t, all.Versions, 1)
	assert.Equal(t, "original", all.Versions[0].Body)
}

func TestDefault_IsPinnedToAVersionAndSavingDoesNotMoveIt(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	none, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, none, "a tenant that chose nothing has no default")

	v1 := f.save("n", "first", true)
	got, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, v1.ID, got.ID)

	f.save("n", "second", false)
	got, err = f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, v1.ID, got.ID, "a newer version of the same name is not the default until chosen")
	assert.Equal(t, "first", got.Body)

	v3 := f.save("n", "third", true)
	got, err = f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, v3.ID, got.ID)
}

func TestSetDefault_PicksAnyVersionClearsAndRefusesUnknownOnes(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v1 := f.save("n", "first", true)
	f.save("n", "second", true)

	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &v1.ID))
	got, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, v1.ID, got.ID, "an older version can be chosen again")

	unknown := uuid.New()
	assert.ErrorIs(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &unknown), fleetinstructions.ErrNotFound)
	got, _ = f.svc.Default(f.ctx)
	assert.Equal(t, v1.ID, got.ID, "a refused change leaves the default alone")

	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, nil))
	got, err = f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTenancy_AnotherTenantsVersionsAreInvisibleAndUnchoosable(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newTenant(t, database), newTenant(t, database)
	av := a.save("n", "a's text", true)

	all, err := b.svc.Load(b.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	assert.Empty(t, all.Versions)
	assert.Nil(t, all.DefaultID)
	def, err := b.svc.Default(b.ctx)
	require.NoError(t, err)
	assert.Nil(t, def, "a's default is not b's")

	assert.ErrorIs(t, b.svc.SetDefault(b.ctx, b.owner, fleetinstructions.KindFleet, &av.ID), fleetinstructions.ErrNotFound)

	bv := b.save("n", "b's text", false)
	assert.Equal(t, 1, bv.Version, "version numbers are per tenant")
}

func TestOnlyOwnersChange(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n", "text", false)

	_, err := f.svc.Save(f.ctx, f.member, fleetinstructions.KindFleet, "n", "member's text", true)
	assert.ErrorIs(t, err, fleetinstructions.ErrNotOwner)
	assert.ErrorIs(t, f.svc.SetDefault(f.ctx, f.member, fleetinstructions.KindFleet, &v.ID), fleetinstructions.ErrNotOwner)

	all, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	assert.Len(t, all.Versions, 1, "a member saved nothing")
	assert.Nil(t, all.DefaultID)
	assert.Equal(t, 2, f.events.Count(audit.FleetInstructionsRefused))
}

func TestSave_ChecksTheText(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	for name, c := range map[string]struct{ name, body string }{
		"empty text":      {"n", " \n "},
		"empty name":      {" ", "text"},
		"too large":       {"n", strings.Repeat("x", fleetinstructions.MaxBodyBytes+1)},
		"nul":             {"n", "a\x00b"},
		"escape sequence": {"n", "a\x1b[2Jb"},
		"control in name": {"a\nb", "text"},
		"overlong name":   {strings.Repeat("n", fleetinstructions.MaxNameLen+1), "text"},
		"bidi override":   {"n", "safe \u202e txet"},
		"zero width":      {"n", "a\u200bb"},
		"invalid utf-8":   {"n", "a\xffb"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Save(f.ctx, f.owner, fleetinstructions.KindFleet, c.name, c.body, false)
			assert.ErrorIs(t, err, fleetinstructions.ErrInvalid)
		})
	}

	v := f.save(" padded ", "line one\r\nline two\t\n\n", false)
	assert.Equal(t, "padded", v.Name)
	assert.Equal(t, "line one\nline two", v.Body, "line endings settle on newline and the ends are trimmed")
	f.save("n", strings.Repeat("x", fleetinstructions.MaxBodyBytes), false)
}

func TestChangesAreAudited(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	v := f.save("n", "text", false)
	assert.Equal(t, 1, f.events.Count(audit.FleetInstructionsVersionSaved))
	assert.Equal(t, 0, f.events.Count(audit.FleetInstructionsDefaultChanged), "saving without choosing changes no default")

	f.save("n", "text 2", true)
	assert.Equal(t, 2, f.events.Count(audit.FleetInstructionsVersionSaved))
	assert.Equal(t, 1, f.events.Count(audit.FleetInstructionsDefaultChanged), "saving as default records the change")

	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &v.ID))
	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, nil))
	assert.Equal(t, 3, f.events.Count(audit.FleetInstructionsDefaultChanged))
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

func TestAuditRecordsSayWhoWhichVersionAndNeverTheText(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	v1 := f.save("house", "secret-ish words", true)
	v2 := f.save("house", "other", true)
	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, nil))

	saved := f.auditRecords(audit.FleetInstructionsVersionSaved)
	require.Len(t, saved, 2)
	assert.Equal(t, f.owner.ID.String(), saved[0]["user_id"])
	assert.Equal(t, f.id.String(), saved[0]["tenant"])
	assert.Equal(t, "house", saved[0]["name"])
	assert.EqualValues(t, v1.Version, saved[0]["version"])
	assert.EqualValues(t, len(v1.Body), saved[0]["bytes"])
	assert.EqualValues(t, v2.Version, saved[1]["version"])

	changed := f.auditRecords(audit.FleetInstructionsDefaultChanged)
	require.Len(t, changed, 3)
	assert.Equal(t, "", changed[0]["old_name"], "the first default replaced nothing")
	assert.EqualValues(t, 1, changed[0]["new_version"])
	assert.EqualValues(t, 1, changed[1]["old_version"])
	assert.EqualValues(t, 2, changed[1]["new_version"])
	assert.EqualValues(t, 2, changed[2]["old_version"])
	assert.EqualValues(t, 0, changed[2]["new_version"], "clearing it leaves none")
	assert.NotContains(t, f.log.String(), "secret-ish", "the text is never logged")
}
