package fleetinstructions_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
)

var defaultWords = []string{
	"secret", "secrets", "token", "password", "credential", "api key", "private key",
	"prod", "production", "real user", "delete", "drop", "truncate", "purge", "erase",
	"restore", "billing", "cost", "spend", "invoice", "payment",
}

func (f *fixture) saveKind(kind fleetinstructions.Kind, name, body string, makeDefault bool) *fleetinstructions.Version {
	f.t.Helper()
	v, err := f.svc.Save(f.ctx, f.owner, kind, name, body, makeDefault)
	require.NoError(f.t, err)
	return v
}

func TestKinds_VersionsPinAndArchivePerKind(t *testing.T) {
	for _, kind := range []fleetinstructions.Kind{fleetinstructions.KindFleet, fleetinstructions.KindSteward, fleetinstructions.KindSensitiveWords} {
		t.Run(string(kind), func(t *testing.T) {
			f := newTenant(t, dbtest.WithDB(t))
			before, err := f.svc.DefaultOf(f.ctx, kind)
			require.NoError(t, err)

			v1 := f.saveKind(kind, "house", "one", false)
			v2 := f.saveKind(kind, "house", "two", true)
			assert.Equal(t, []int{1, 2}, []int{v1.Version, v2.Version})
			assert.Equal(t, kind, v2.Kind)

			// Saving a newer version does not move the pin.
			f.saveKind(kind, "house", "three", false)
			got, err := f.svc.DefaultOf(f.ctx, kind)
			require.NoError(t, err)
			assert.Equal(t, v2.ID, got.ID, "the pinned version, not the newest")

			require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, kind, &v1.ID))
			got, err = f.svc.DefaultOf(f.ctx, kind)
			require.NoError(t, err)
			assert.Equal(t, v1.ID, got.ID)

			_, err = f.svc.ArchiveVersion(f.ctx, f.owner, v1.ID)
			assert.ErrorIs(t, err, fleetinstructions.ErrConflict, "the pinned version cannot be archived")
			n, err := f.svc.ArchiveGroup(f.ctx, f.owner, kind, "house")
			assert.ErrorIs(t, err, fleetinstructions.ErrConflict)
			assert.Zero(t, n)
			_, err = f.svc.ArchiveVersion(f.ctx, f.owner, v2.ID)
			require.NoError(t, err)
			listed, err := f.svc.Load(f.ctx, kind, false)
			require.NoError(t, err)
			for _, v := range listed.Versions {
				assert.NotEqual(t, v2.ID, v.ID)
				assert.Equal(t, kind, v.Kind)
			}

			require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, kind, nil))
			n, err = f.svc.ArchiveGroup(f.ctx, f.owner, kind, "house")
			require.NoError(t, err)
			assert.Equal(t, 2, n)
			n, err = f.svc.UnarchiveGroup(f.ctx, f.owner, kind, "house")
			require.NoError(t, err)
			assert.Equal(t, 3, n)
			_ = before
		})
	}
}

func TestKinds_AreSeparate(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	fleet := f.saveKind(fleetinstructions.KindFleet, "same", "fleet text", true)
	steward := f.saveKind(fleetinstructions.KindSteward, "same", "steward text", true)

	assert.Equal(t, 1, steward.Version, "version numbers are per kind")
	gotFleet, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, fleet.ID, gotFleet.ID)
	gotSteward, err := f.svc.DefaultOf(f.ctx, fleetinstructions.KindSteward)
	require.NoError(t, err)
	assert.Equal(t, steward.ID, gotSteward.ID)

	listed, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	require.Len(t, listed.Versions, 1)
	assert.Equal(t, fleet.ID, *listed.DefaultID)

	t.Run("a default is a version of its own kind", func(t *testing.T) {
		err := f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &steward.ID)
		assert.ErrorIs(t, err, fleetinstructions.ErrInvalid)
	})
	t.Run("archiving a group touches its kind only", func(t *testing.T) {
		require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindSteward, nil))
		n, err := f.svc.ArchiveGroup(f.ctx, f.owner, fleetinstructions.KindSteward, "same")
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		fleetNow, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, false)
		require.NoError(t, err)
		assert.Len(t, fleetNow.Versions, 1)
	})
}

func TestKinds_UnknownKindIsInvalid(t *testing.T) {
	_, err := fleetinstructions.ParseKind("nope")
	assert.ErrorIs(t, err, fleetinstructions.ErrInvalid)
	k, err := fleetinstructions.ParseKind("")
	require.NoError(t, err)
	assert.Equal(t, fleetinstructions.KindFleet, k)
}

func TestSensitiveWords_TenantStartsWithTheSeededPinnedList(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))

	def, err := f.svc.DefaultOf(f.ctx, fleetinstructions.KindSensitiveWords)
	require.NoError(t, err)
	require.NotNil(t, def)
	assert.Equal(t, 1, def.Version)
	assert.Equal(t, strings.Join(defaultWords, "\n"), def.Body)
	assert.Nil(t, def.CreatedBy)

	t.Run("other kinds start with none, and fleet's list is untouched", func(t *testing.T) {
		d, err := f.svc.Default(f.ctx)
		require.NoError(t, err)
		assert.Nil(t, d)
		d, err = f.svc.DefaultOf(f.ctx, fleetinstructions.KindSteward)
		require.NoError(t, err)
		assert.Nil(t, d)
		listed, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
		require.NoError(t, err)
		assert.Empty(t, listed.Versions)
	})
	t.Run("the seeded list matches", func(t *testing.T) {
		word, ok := fleetinstructions.Match(def.Body, "please drop the table")
		assert.True(t, ok)
		assert.Equal(t, "drop", word)
	})
	t.Run("the database was left scoped as it was", func(t *testing.T) {
		other := newTenant(t, dbtest.WithDB(t))
		d, err := other.svc.DefaultOf(other.ctx, fleetinstructions.KindSensitiveWords)
		require.NoError(t, err)
		assert.NotEqual(t, def.ID, d.ID, "each tenant has its own")
	})
}

func TestSensitiveWords_OwnerEditsFreelyAndTheListMayBeEmpty(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	seeded, err := f.svc.DefaultOf(f.ctx, fleetinstructions.KindSensitiveWords)
	require.NoError(t, err)

	edited := f.saveKind(fleetinstructions.KindSensitiveWords, "sensitive words", "secret\n\n  prod  ", true)
	assert.Equal(t, 2, edited.Version, "an edit is the next version of the seeded name")
	got, err := f.svc.DefaultOf(f.ctx, fleetinstructions.KindSensitiveWords)
	require.NoError(t, err)
	assert.Equal(t, edited.ID, got.ID)

	empty := f.saveKind(fleetinstructions.KindSensitiveWords, "sensitive words", " \n\n ", true)
	assert.Empty(t, empty.Body)
	_, ok := fleetinstructions.Match(empty.Body, "secret")
	assert.False(t, ok)

	listed, err := f.svc.Load(f.ctx, fleetinstructions.KindSensitiveWords, true)
	require.NoError(t, err)
	assert.Len(t, listed.Versions, 3, "every version is kept")
	assert.Contains(t, bodies(listed.Versions), seeded.Body)

	t.Run("other kinds still refuse empty text", func(t *testing.T) {
		_, err := f.svc.Save(f.ctx, f.owner, fleetinstructions.KindSteward, "n", "  ", false)
		assert.ErrorIs(t, err, fleetinstructions.ErrInvalid)
		_, err = f.svc.Save(f.ctx, f.owner, fleetinstructions.KindFleet, "n", "", false)
		assert.ErrorIs(t, err, fleetinstructions.ErrInvalid)
	})
	t.Run("only owners", func(t *testing.T) {
		_, err := f.svc.Save(f.ctx, f.member, fleetinstructions.KindSensitiveWords, "n", "x", false)
		assert.ErrorIs(t, err, fleetinstructions.ErrNotOwner)
	})
}

func bodies(vs []fleetinstructions.Version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Body)
	}
	return out
}

func TestAudit_CarriesTheKind(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.saveKind(fleetinstructions.KindSteward, "n", "text", true)
	assert.Contains(t, f.log.String(), `"kind":"steward"`)
}

func TestHandler_KindSelectsTheTexts(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	srv := f.server(f.owner)

	rec := call(srv, http.MethodPost, "/api/fleet_instructions/versions?kind=steward", `{"name":"s","body":"steward text","make_default":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = call(srv, http.MethodGet, "/api/fleet_instructions?kind=steward", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "steward text")
	rec = call(srv, http.MethodGet, "/api/fleet_instructions", "")
	assert.NotContains(t, rec.Body.String(), "steward text", "no kind is fleet")
	rec = call(srv, http.MethodGet, "/api/fleet_instructions?kind=sensitive_words", "")
	assert.Contains(t, rec.Body.String(), "api key")

	rec = call(srv, http.MethodGet, "/api/fleet_instructions?kind=bogus", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = call(srv, http.MethodPost, "/api/fleet_instructions/versions?kind=sensitive_words", `{"name":"sensitive words","body":"","make_default":false}`)
	assert.Equal(t, http.StatusCreated, rec.Code, "an empty word list is allowed: %s", rec.Body.String())
}

func TestHandler_KindReachesDefaultAndGroupArchive(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	srv := f.server(f.owner)
	steward := f.saveKind(fleetinstructions.KindSteward, "s", "text", false)
	fleet := f.saveKind(fleetinstructions.KindFleet, "s", "text", false)

	rec := call(srv, http.MethodPost, "/api/fleet_instructions/versions?kind=steward", `{"name":"s","body":"two","make_default":false}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"kind":"steward"`, "a version says its kind")

	rec = call(srv, http.MethodPut, "/api/fleet_instructions/default?kind=steward", `{"version_id":"`+steward.ID.String()+`"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got, err := f.svc.DefaultOf(f.ctx, fleetinstructions.KindSteward)
	require.NoError(t, err)
	assert.Equal(t, steward.ID, got.ID)
	d, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, d, "the fleet default is not touched")

	rec = call(srv, http.MethodPut, "/api/fleet_instructions/default", `{"version_id":"`+steward.ID.String()+`"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a steward version is not a fleet default")

	rec = call(srv, http.MethodPost, "/api/fleet_instructions/groups/archive?kind=steward", `{"name":"s"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "the steward default is in the group")
	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindSteward, nil))
	rec = call(srv, http.MethodPost, "/api/fleet_instructions/groups/archive?kind=steward", `{"name":"s"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"versions":2`)
	listed, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, false)
	require.NoError(t, err)
	require.Len(t, listed.Versions, 1, "the fleet group of the same name stays listed")
	assert.Equal(t, fleet.ID, listed.Versions[0].ID)

	rec = call(srv, http.MethodPost, "/api/fleet_instructions/groups/unarchive?kind=steward", `{"name":"s"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"versions":2`)
}
