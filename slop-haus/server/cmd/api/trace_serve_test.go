package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// ctxRecorder keeps the trace id each record's context carried.
type ctxRecorder struct {
	mu       sync.Mutex
	traceIDs map[string]string
}

func (h *ctxRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h *ctxRecorder) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.traceIDs[r.Message] = trace.SpanContextFromContext(ctx).TraceID().String()
	return nil
}
func (h *ctxRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *ctxRecorder) WithGroup(string) slog.Handler      { return h }

func (h *ctxRecorder) traceIDOf(msg string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.traceIDs[msg]
}

// The board is served through reqtrace: a request the browser tagged with a
// traceparent, and that the api answers with a 5xx, is logged under that
// trace id.
func TestServe_AFailedRequestIsLoggedUnderTheBrowsersTraceID(t *testing.T) {
	t.Parallel()
	logs := &ctxRecorder{traceIDs: map[string]string{}}
	b := boot(t, &bootOptions{logs: logs})
	_, userToken := b.signedInUser(t)

	conn, err := pgx.Connect(context.Background(), b.ownerDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	_, err = conn.Exec(context.Background(), `DROP TABLE global_consent_events, global_consent_merge_tokens`)
	require.NoError(t, err)

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/privacy/consent", token: userToken,
		header: map[string]string{"Traceparent": "00-" + traceID + "-00f067aa0ba902b7-01"}})

	require.Equal(t, http.StatusInternalServerError, resp.code, resp.body)
	assert.Equal(t, traceID, logs.traceIDOf("request failed"))
}
