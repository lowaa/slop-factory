package fleetinstructions_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
)

func (f *fixture) listed(includeArchived bool) []string {
	f.t.Helper()
	t, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, includeArchived)
	require.NoError(f.t, err)
	var out []string
	for _, v := range t.Versions {
		out = append(out, v.Name+"@"+string(rune('0'+v.Version)))
	}
	return out
}

func TestArchiveVersion_HidesItKeepsTheTextAndUnarchiveRestoresIt(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v1 := f.save("n", "first", false)
	f.save("n", "second", false)

	got, err := f.svc.ArchiveVersion(f.ctx, f.owner, v1.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ArchivedAt)

	assert.Equal(t, []string{"n@2"}, f.listed(false), "archived versions are hidden by default")
	assert.Equal(t, []string{"n@2", "n@1"}, f.listed(true))
	all, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	assert.Equal(t, "first", all.Versions[1].Body, "archiving never rewrites the text")
	assert.NotNil(t, all.Versions[1].ArchivedAt)
	assert.Nil(t, all.Versions[0].ArchivedAt)

	// Saving the next version of an archived name still numbers past it.
	assert.Equal(t, 3, f.save("n", "third", false).Version)

	back, err := f.svc.UnarchiveVersion(f.ctx, f.owner, v1.ID)
	require.NoError(t, err)
	assert.Nil(t, back.ArchivedAt)
	assert.Equal(t, []string{"n@3", "n@2", "n@1"}, f.listed(false))
}

func TestArchiveVersion_IsIdempotentAndUnknownIsNotFound(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n", "x", false)

	first, err := f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	again, err := f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ArchivedAt, again.ArchivedAt, "archiving twice keeps the first time")

	unknown := uuid.New()
	_, err = f.svc.ArchiveVersion(f.ctx, f.owner, unknown)
	assert.ErrorIs(t, err, fleetinstructions.ErrNotFound)
	_, err = f.svc.UnarchiveVersion(f.ctx, f.owner, unknown)
	assert.ErrorIs(t, err, fleetinstructions.ErrNotFound)
}

func TestArchiveGroup_HidesEveryVersionOfTheNameOnly(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.save("old", "1", false)
	f.save("old", "2", false)
	f.save("keep", "1", false)

	n, err := f.svc.ArchiveGroup(f.ctx, f.owner, fleetinstructions.KindFleet, "old")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"keep@1"}, f.listed(false))

	n, err = f.svc.ArchiveGroup(f.ctx, f.owner, fleetinstructions.KindFleet, "old")
	require.NoError(t, err)
	assert.Equal(t, 0, n, "nothing newly hidden the second time")

	_, err = f.svc.ArchiveGroup(f.ctx, f.owner, fleetinstructions.KindFleet, "nope")
	assert.ErrorIs(t, err, fleetinstructions.ErrNotFound)

	n, err = f.svc.UnarchiveGroup(f.ctx, f.owner, fleetinstructions.KindFleet, "  old ")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the name is trimmed as when saving")
	assert.Equal(t, []string{"keep@1", "old@2", "old@1"}, f.listed(false))
}

func TestArchive_RefusesTheDefaultAndAGroupHoldingIt(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.save("g", "1", false)
	def := f.save("g", "2", true)
	f.save("g", "3", false)

	_, err := f.svc.ArchiveVersion(f.ctx, f.owner, def.ID)
	assert.ErrorIs(t, err, fleetinstructions.ErrConflict)
	_, err = f.svc.ArchiveGroup(f.ctx, f.owner, fleetinstructions.KindFleet, "g")
	assert.ErrorIs(t, err, fleetinstructions.ErrConflict)
	assert.Len(t, f.listed(false), 3, "a refused group archive hides none of its versions")
	got, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, def.ID, got.ID)

	require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, nil))
	_, err = f.svc.ArchiveGroup(f.ctx, f.owner, fleetinstructions.KindFleet, "g")
	assert.NoError(t, err, "once the default is unset the group can go")
}

func TestSetDefault_RefusesAnArchivedVersion(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n", "x", false)
	_, err := f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &v.ID), fleetinstructions.ErrConflict)
	got, err := f.svc.Default(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)

	_, err = f.svc.UnarchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	assert.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &v.ID))
}

// Whichever of archiving and choosing runs first, the default must never end
// up archived: one of the two is refused.
func TestArchiveRacingSetDefault_NeverLeavesAnArchivedDefault(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	for i := 0; i < 25; i++ {
		v := f.save("race", "x", false)
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, errs[0] = f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			errs[1] = f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, &v.ID)
		}()
		close(start)
		wg.Wait()

		all, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
		require.NoError(t, err)
		// Exactly one side lost: the default is set and the archive refused,
		// or archived first and the choice refused.
		assert.Equal(t, all.DefaultID == nil, errs[1] != nil, "round %d", i)
		assert.Equal(t, all.DefaultID != nil, errs[0] != nil, "round %d", i)
		for _, x := range all.Versions {
			if all.DefaultID != nil && x.ID == *all.DefaultID {
				assert.Nil(t, x.ArchivedAt, "round %d: the default is archived (archive err %v, set err %v)", i, errs[0], errs[1])
			}
		}
		require.NoError(t, f.svc.SetDefault(f.ctx, f.owner, fleetinstructions.KindFleet, nil))
		_, err = f.svc.UnarchiveVersion(f.ctx, f.owner, v.ID)
		require.NoError(t, err)
	}
}

func TestArchive_OnlyOwnersAndAudited(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n", "x", false)

	_, err := f.svc.ArchiveVersion(f.ctx, f.member, v.ID)
	assert.ErrorIs(t, err, fleetinstructions.ErrNotOwner)
	_, err = f.svc.ArchiveGroup(f.ctx, f.member, fleetinstructions.KindFleet, "n")
	assert.ErrorIs(t, err, fleetinstructions.ErrNotOwner)
	assert.Equal(t, []string{"n@1"}, f.listed(false))

	_, err = f.svc.ArchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	_, err = f.svc.UnarchiveVersion(f.ctx, f.member, v.ID)
	assert.ErrorIs(t, err, fleetinstructions.ErrNotOwner)
	assert.Empty(t, f.listed(false), "a member's unarchive did nothing")
	_, err = f.svc.UnarchiveGroup(f.ctx, f.member, fleetinstructions.KindFleet, "n")
	assert.ErrorIs(t, err, fleetinstructions.ErrNotOwner)

	assert.Equal(t, 4, f.events.Count(audit.FleetInstructionsRefused))
	assert.Equal(t, 1, f.events.Count(audit.FleetInstructionsArchived))
	_, err = f.svc.UnarchiveVersion(f.ctx, f.owner, v.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, f.events.Count(audit.FleetInstructionsUnarchived))
	rec := f.auditRecords(audit.FleetInstructionsArchived)
	require.Len(t, rec, 1)
	assert.Equal(t, f.owner.ID.String(), rec[0]["user_id"])
	assert.Equal(t, "n", rec[0]["name"])
}

func TestArchive_AnotherTenantsVersionsAreOutOfReach(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newTenant(t, database), newTenant(t, database)
	av := a.save("n", "x", false)

	_, err := b.svc.ArchiveVersion(b.ctx, b.owner, av.ID)
	assert.ErrorIs(t, err, fleetinstructions.ErrNotFound)
	_, err = b.svc.ArchiveGroup(b.ctx, b.owner, fleetinstructions.KindFleet, "n")
	assert.ErrorIs(t, err, fleetinstructions.ErrNotFound)
	assert.Equal(t, []string{"n@1"}, a.listed(false))
}

func TestHandler_ArchiveRoutes(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	owner, member := f.server(f.owner), f.server(f.member)
	v1 := f.save("g", "1", false)
	def := f.save("g", "2", true)
	other := f.save("o", "1", false)

	versions := func(query string) []fleetinstructions.VersionBody {
		rec := call(owner, http.MethodGet, "/api/fleet_instructions"+query, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body fleetinstructions.FleetInstructionsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Versions
	}
	idPath := func(id uuid.UUID, verb string) string {
		return "/api/fleet_instructions/versions/" + id.String() + "/" + verb
	}

	for name, c := range map[string]struct {
		mux          *http.ServeMux
		method, path string
		body         string
		want         int
	}{
		"member archives a version":   {member, http.MethodPost, idPath(v1.ID, "archive"), ``, http.StatusForbidden},
		"member unarchives a version": {member, http.MethodPost, idPath(v1.ID, "unarchive"), ``, http.StatusForbidden},
		"member archives a group":     {member, http.MethodPost, "/api/fleet_instructions/groups/archive", `{"name":"o"}`, http.StatusForbidden},
		"member unarchives a group":   {member, http.MethodPost, "/api/fleet_instructions/groups/unarchive", `{"name":"o"}`, http.StatusForbidden},
		"nobody signed in":            {f.server(nil), http.MethodPost, idPath(v1.ID, "archive"), ``, http.StatusUnauthorized},
		"the default":                 {owner, http.MethodPost, idPath(def.ID, "archive"), ``, http.StatusConflict},
		"a group holding the default": {owner, http.MethodPost, "/api/fleet_instructions/groups/archive", `{"name":"g"}`, http.StatusConflict},
		"unknown version":             {owner, http.MethodPost, idPath(uuid.New(), "archive"), ``, http.StatusNotFound},
		"unknown group":               {owner, http.MethodPost, "/api/fleet_instructions/groups/archive", `{"name":"zzz"}`, http.StatusNotFound},
		"not a uuid":                  {owner, http.MethodPost, "/api/fleet_instructions/versions/nope/archive", ``, http.StatusUnprocessableEntity},
		"no group name":               {owner, http.MethodPost, "/api/fleet_instructions/groups/archive", `{}`, http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(c.mux, c.method, c.path, c.body)
			assert.Equal(t, c.want, rec.Code, rec.Body.String())
		})
	}
	assert.Len(t, versions("?include_archived=true"), 3, "no refused call archived anything")

	rec := call(owner, http.MethodPost, idPath(v1.ID, "archive"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body fleetinstructions.VersionBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Archived)
	assert.NotNil(t, body.ArchivedAt)

	assert.Len(t, versions(""), 2, "hidden by default")
	all := versions("?include_archived=true")
	require.Len(t, all, 3)
	archived := 0
	for _, v := range all {
		if v.Archived {
			archived++
			assert.NotNil(t, v.ArchivedAt)
		} else {
			assert.Nil(t, v.ArchivedAt)
		}
	}
	assert.Equal(t, 1, archived)

	rec = call(owner, http.MethodPost, "/api/fleet_instructions/groups/archive", `{"name":"o"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var counted fleetinstructions.ArchiveGroupResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &counted.Body))
	assert.Equal(t, 1, counted.Body.Versions)
	assert.Len(t, versions(""), 1)

	rec = call(owner, http.MethodPost, "/api/fleet_instructions/groups/unarchive", `{"name":"o"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(owner, http.MethodPost, idPath(v1.ID, "unarchive"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, versions(""), 3)
	_ = other
}
