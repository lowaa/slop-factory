package githubhook_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// installationLookupStore fails any use of the database but the installation
// lookup a delivery naming one costs, which it counts.
type installationLookupStore struct {
	githubhook.Store
	lookups atomic.Int32
}

func (s *installationLookupStore) AppInstallations(context.Context, int64) ([]githubhook.AppInstallation, error) {
	s.lookups.Add(1)
	return nil, nil
}

// countedBody serves a delivery naming an installation, and counts its reads.
type countedBody struct {
	reads atomic.Int32
	rest  *strings.Reader
}

func (b *countedBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	if b.rest == nil {
		b.rest = strings.NewReader(`{"installation":{"id":5}}`)
	}
	return b.rest.Read(p)
}

func TestClientGate_RefusesBeforeTheBodyOrTheDatabase(t *testing.T) {
	store := &installationLookupStore{}
	svc := newService(t, store, nil, &githubhook.Config{AppSecrets: openerFromKeyring{}})
	resolver, err := clientip.NewResolver(nil)
	require.NoError(t, err)
	lim := ratelimit.New(&ratelimit.Config{Name: "t", Limit: 1, Window: time.Minute, MaxKeys: 10})
	gate := hookkit.ClientGate(lim, func(r *http.Request) netip.Addr {
		return resolver.Resolve(r.RemoteAddr, nil)
	}, time.Minute, slog.DiscardHandler)
	handler := githubhook.NewReceiver(svc, slog.DiscardHandler).HandlerBehind(gate)

	post := func() (*httptest.ResponseRecorder, *countedBody) {
		body := &countedBody{}
		req := httptest.NewRequest(http.MethodPost, "/hooks/github-app", io.NopCloser(body))
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-GitHub-Delivery", "d")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec, body
	}

	rec, _ := post()
	require.Equal(t, http.StatusOK, rec.Code, "within the limit the delivery is handled as before")
	require.EqualValues(t, 1, store.lookups.Load(), "the fake store is reached by an admitted delivery")

	rec, body := post()
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Zero(t, body.reads.Load(), "the body was not read")
	assert.EqualValues(t, 1, store.lookups.Load(), "the database was not touched")
}

func TestClientGate_CountsRequestsToUnknownPathsToo(t *testing.T) {
	lim := ratelimit.New(&ratelimit.Config{Name: "t", Limit: 1, Window: time.Minute, MaxKeys: 10})
	gate := hookkit.ClientGate(lim, func(r *http.Request) netip.Addr { return netip.MustParseAddr("192.0.2.1") }, time.Minute, slog.DiscardHandler)
	handler := githubhook.NewReceiver(nil, slog.DiscardHandler).HandlerBehind(gate)

	codes := make([]int, 2)
	for i := range codes {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nothing/here", nil))
		codes[i] = rec.Code
	}
	assert.Equal(t, []int{http.StatusNotFound, http.StatusTooManyRequests}, codes)
}
