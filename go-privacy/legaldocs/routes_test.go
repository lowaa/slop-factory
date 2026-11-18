package legaldocs_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn"
	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

func newMux(t *testing.T, svc *legaldocs.Service) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0"))
	require.NoError(t, legaldocs.NewHandler(svc).RegisterRoutes(api, "", &legaldocs.Operations{Read: authn.Public()}))
	return mux
}

func get(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	k := "routes_notice"
	on := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	reviewed := versionOf(k, "v2", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), false, "# Reviewed text")
	reviewed.Reviewed, reviewed.ReviewedBy, reviewed.ReviewedOn = true, "Synthetic Reviewer", &on
	require.NoError(t, svc.Publish(ctx, []*legaldocs.Version{
		versionOf(k, "v1", jan1, true, "# Draft text"), reviewed, versionOf(k, "v3", jun1, true, "# Scheduled"),
	}))
	mux := newMux(t, svc)
	base := "/api/privacy/documents/" + k

	t.Run("the current version is readable signed out", func(t *testing.T) {
		rec := get(t, mux, base)
		require.Equal(t, http.StatusOK, rec.Code)
		var b legaldocs.DocumentBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
		assert.Equal(t, "v2", b.Version)
		assert.True(t, b.Current)
		assert.True(t, b.Reviewed)
		assert.Equal(t, "Synthetic Reviewer", b.ReviewedBy)
		assert.Equal(t, "2026-02-01", b.ReviewedOn)
		assert.Equal(t, "2026-02-01", b.Effective)
		assert.Equal(t, "# Reviewed text", b.Body)
		assert.Contains(t, rec.Header().Get("Cache-Control"), "public")
	})

	t.Run("an exact version carries its draft flag and whether it is current", func(t *testing.T) {
		rec := get(t, mux, base+"/versions/v1")
		require.Equal(t, http.StatusOK, rec.Code)
		var b legaldocs.DocumentBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
		assert.Equal(t, "v1", b.Version)
		assert.False(t, b.Reviewed, "placeholder text is flagged as a draft")
		assert.False(t, b.Current)
		assert.Equal(t, "# Draft text", b.Body)
	})

	t.Run("the history is newest first and leaves out what is not in force yet", func(t *testing.T) {
		rec := get(t, mux, base+"/versions")
		require.Equal(t, http.StatusOK, rec.Code)
		var h legaldocs.HistoryBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &h))
		require.Len(t, h.Versions, 2)
		assert.Equal(t, "v2", h.Versions[0].Version)
		assert.True(t, h.Versions[0].Current)
		assert.True(t, h.Versions[0].Reviewed)
		assert.Equal(t, "v1", h.Versions[1].Version)
		assert.False(t, h.Versions[1].Reviewed)
		assert.NotContains(t, rec.Body.String(), "Scheduled")
	})

	t.Run("what is missing is a 404", func(t *testing.T) {
		for _, target := range []string{
			"/api/privacy/documents/nope", "/api/privacy/documents/nope/versions",
			base + "/versions/v9", base + "/versions/v3", base + "?locale=fr", base + "/versions?locale=fr",
			"/api/privacy/documents/empty_notice", "/api/privacy/documents/empty_notice/versions",
		} {
			assert.Equal(t, http.StatusNotFound, get(t, mux, target).Code, target)
		}
	})
}

func TestRegisterRoutesNeedsAnAuthnMarker(t *testing.T) {
	svc, _ := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), jan1)
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "0"))
	require.Error(t, legaldocs.NewHandler(svc).RegisterRoutes(api, "", &legaldocs.Operations{}))
	require.Error(t, legaldocs.NewHandler(svc).RegisterRoutes(api, "", nil))
}

type failingStore struct{ legaldocs.Store }

func (failingStore) List(context.Context, string, string) ([]*legaldocs.Version, error) {
	return nil, errors.New("synthetic secret row value")
}

func TestAStoreFailureIsA500ThatSaysNothingOfIt(t *testing.T) {
	svc, _ := newService(t, failingStore{}, jan1)
	rec := get(t, newMux(t, svc), "/api/privacy/documents/terms")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret")
}
