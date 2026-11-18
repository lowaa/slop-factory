package intake_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
)

// credentialStore fails any use of the database but the credential lookup
// every unauthenticated delivery costs, which it counts.
type credentialStore struct {
	intake.Store
	lookups atomic.Int32
}

func (s *credentialStore) CredentialOf(context.Context, uuid.UUID) (*intake.Credential, error) {
	s.lookups.Add(1)
	return nil, intake.ErrNotFound
}

// unreadBody counts its reads.
type unreadBody struct{ reads atomic.Int32 }

func (b *unreadBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, io.EOF
}

func TestClientGate_RefusesBeforeTheBodyOrTheDatabase(t *testing.T) {
	store := &credentialStore{}
	svc := intake.NewService(store, nil, &intake.Config{Enabled: true}, nil, slog.DiscardHandler)
	resolver, err := clientip.NewResolver(nil)
	require.NoError(t, err)
	lim := ratelimit.New(&ratelimit.Config{Name: "t", Limit: 1, Window: time.Minute, MaxKeys: 10})
	gate := hookkit.ClientGate(lim, func(r *http.Request) netip.Addr {
		return resolver.Resolve(r.RemoteAddr, nil)
	}, time.Minute, slog.DiscardHandler)
	handler := intake.NewReceiver(svc, slog.DiscardHandler).HandlerBehind(gate)

	post := func() (*httptest.ResponseRecorder, *unreadBody) {
		body := &unreadBody{}
		req := httptest.NewRequest(http.MethodPost, "/hooks/intake/"+uuid.NewString(), io.NopCloser(body))
		req.RemoteAddr = "192.0.2.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec, body
	}

	rec, _ := post()
	require.Equal(t, http.StatusUnauthorized, rec.Code, "within the limit the delivery is handled as before")
	require.EqualValues(t, 1, store.lookups.Load())

	rec, body := post()
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Zero(t, body.reads.Load(), "the body was not read")
	assert.EqualValues(t, 1, store.lookups.Load(), "the database was not touched")
}

func TestClientGate_CountsRequestsToUnknownPathsToo(t *testing.T) {
	lim := ratelimit.New(&ratelimit.Config{Name: "t", Limit: 1, Window: time.Minute, MaxKeys: 10})
	gate := hookkit.ClientGate(lim, func(r *http.Request) netip.Addr { return netip.MustParseAddr("192.0.2.1") }, time.Minute, slog.DiscardHandler)
	handler := intake.NewReceiver(nil, slog.DiscardHandler).HandlerBehind(gate)

	codes := make([]int, 2)
	for i := range codes {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nothing/here", nil))
		codes[i] = rec.Code
	}
	assert.Equal(t, []int{http.StatusNotFound, http.StatusTooManyRequests}, codes)
}
