package otelproxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-privacy/telemetry"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/otelproxy"
)

const (
	board     = "https://slop.example"
	goodToken = "good-token"
)

var signedIn = &authn.User{ID: uuid.New(), TenantID: uuid.New(), SessionID: "session-a"}

func verify(_ context.Context, token string) (*authn.User, error) {
	if token == goodToken {
		return signedIn, nil
	}
	return nil, errors.New("bad token")
}

func validCSRF(sessionID, token string) bool { return sessionID != "" && token == "csrf-"+sessionID }

// collector is a fake log store that keeps what it was sent.
type collector struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	status int
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{status: http.StatusOK}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		// VictoriaLogs takes OTLP protobuf only; what it was sent is kept as
		// compact JSON, so tests read the same fields whatever the wire.
		decoded := "not protobuf"
		var msg collogs.ExportLogsServiceRequest
		if r.Header.Get("Content-Type") == "application/x-protobuf" && proto.Unmarshal(b, &msg) == nil {
			j, _ := protojson.Marshal(&msg)
			var compact bytes.Buffer
			_ = json.Compact(&compact, j)
			decoded = compact.String()
		}
		c.mu.Lock()
		c.bodies = append(c.bodies, decoded)
		status := c.status
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) received() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

type rig struct {
	mux       *http.ServeMux
	collector *collector
}

type rigOptions struct {
	limit          int
	trustedProxies []string
	consentGranted bool
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	col := newCollector(t)
	if o.limit == 0 {
		o.limit = 1000
	}
	// The production policy, with a consented tier added to exercise the
	// consent path slop-haus doesn't use yet.
	policy, err := telemetry.New(&telemetry.Config{
		ServiceName:           otelproxy.ServiceName,
		OperationalAttributes: otelproxy.OperationalAttributes,
		Consented: map[string]*telemetry.ConsentedTier{
			"analytics": {Attributes: []string{"screen"}, Granted: func(context.Context) bool { return o.consentGranted }},
		},
	})
	require.NoError(t, err)
	if !o.consentGranted {
		policy, err = otelproxy.NewPolicy()
		require.NoError(t, err)
	}
	endpoint := col.URL
	h, err := otelproxy.NewHandler(&otelproxy.Options{
		Endpoint: endpoint,
		Policy:   policy,
		Limit:    &ratelimit.Config{Name: "otel_proxy", Limit: o.limit, Window: time.Hour, MaxKeys: 100},
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	resolver, err := clientip.NewResolver(o.trustedProxies)
	require.NoError(t, err)
	logger := slog.New(slog.DiscardHandler)
	guard, err := authn.NewBrowserGuard(board, validCSRF, audit.New(slog.DiscardHandler, nil))
	require.NoError(t, err)
	// The chain cmd/api installs.
	api.UseMiddleware(resolver.Middleware())
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(guard.PublicWrites(api))
	api.UseMiddleware(authn.UserMiddleware(api, verify, guard, logger))
	otelproxy.RegisterRoutes(api, h)
	return &rig{mux: mux, collector: col}
}

type attr = map[string]any

func str(k, v string) attr { return attr{"key": k, "value": attr{"stringValue": v}} }

func errorRecord(attrs ...attr) attr {
	return attr{
		"severityNumber": 17,
		"body":           attr{"stringValue": "render error"},
		"attributes":     append([]attr{str("privacy.tier", "operational")}, attrs...),
	}
}

func payload(serviceName string, records ...attr) string {
	b, _ := json.Marshal(attr{"resourceLogs": []any{attr{
		"resource":  attr{"attributes": []attr{str("service.name", serviceName)}},
		"scopeLogs": []any{attr{"logRecords": records}},
	}}})
	return string(b)
}

type request struct {
	body    string
	origin  string
	remote  string
	headers map[string]string
	cookie  string
}

func (r *rig) post(t *testing.T, q request) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, otelproxy.Path, strings.NewReader(q.body))
	req.Header.Set("Content-Type", "application/json")
	if q.origin == "" {
		q.origin = board
	}
	if q.origin != "none" {
		req.Header.Set("Origin", q.origin)
	}
	if q.remote != "" {
		req.RemoteAddr = q.remote
	}
	for k, v := range q.headers {
		req.Header.Set(k, v)
	}
	if q.cookie != "" {
		req.AddCookie(&http.Cookie{Name: authn.SessionCookie, Value: q.cookie})
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func TestExport_ForwardsOperationalErrors_ForcingServiceName(t *testing.T) {
	r := newRig(t, rigOptions{})
	// A caller posing as the backend's own log stream.
	rec := r.post(t, request{body: payload("slop-haus-api", errorRecord(str("error.type", "TypeError")))})

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got := r.collector.received()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], `"stringValue":"slop-haus-frontend"`)
	assert.NotContains(t, got[0], "slop-haus-api", "service.name must be forced server-side")
	assert.Contains(t, got[0], "TypeError")
}

func TestExport_StripsAttributesOutsideTheAllowlist(t *testing.T) {
	r := newRig(t, rigOptions{})
	rec := r.post(t, request{body: payload("x", errorRecord(
		str("error.type", "Error"),
		str("form.title", "Hemoglobin 13.5 Jane Doe"),
		str("file.name", "results.pdf"),
	))})

	require.Equal(t, http.StatusNoContent, rec.Code)
	got := r.collector.received()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "error.type")
	assert.NotContains(t, got[0], "Hemoglobin")
	assert.NotContains(t, got[0], "results.pdf")
}

func TestExport_KeepsTheTraceIDOfAFailedRequest(t *testing.T) {
	r := newRig(t, rigOptions{})
	rec := r.post(t, request{body: payload("x", errorRecord(str("trace_id", "4bf92f3577b34da6a3ce929d0e0e4736")))})

	require.Equal(t, http.StatusNoContent, rec.Code)
	got := r.collector.received()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "4bf92f3577b34da6a3ce929d0e0e4736")
}

func TestExport_DropsWhatTheOperationalTierDoesNotAllow(t *testing.T) {
	r := newRig(t, rigOptions{})
	info := errorRecord()
	info["severityNumber"] = 9
	noTier := attr{"severityNumber": 17, "body": attr{"stringValue": "x"}, "attributes": []attr{}}
	analytics := attr{"severityNumber": 17, "attributes": []attr{str("privacy.tier", "analytics")}}

	rec := r.post(t, request{body: payload("x", info, noTier, analytics)})

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, r.collector.received(), "nothing passed, so nothing is forwarded")
}

func TestExport_RateLimitedPerClientAddress(t *testing.T) {
	r := newRig(t, rigOptions{limit: 3})
	send := func(remote string, headers map[string]string) int {
		return r.post(t, request{body: payload("x", errorRecord()), remote: remote, headers: headers}).Code
	}

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusNoContent, send("203.0.113.5:1000", nil))
	}
	assert.Equal(t, http.StatusTooManyRequests, send("203.0.113.5:1001", nil))
	assert.Equal(t, http.StatusNoContent, send("203.0.113.6:1000", nil), "another address has its own allowance")

	// An untrusted peer's X-Forwarded-For is a client's own claim: rotating
	// it must not buy a fresh allowance.
	assert.Equal(t, http.StatusTooManyRequests, send("203.0.113.5:1002", map[string]string{"X-Forwarded-For": "198.51.100.77"}))
}

func TestExport_BodySizeLimited(t *testing.T) {
	r := newRig(t, rigOptions{})
	big := payload("x", errorRecord(str("error.stack", strings.Repeat("a", 200*1024))))

	rec := r.post(t, request{body: big})

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Empty(t, r.collector.received())
}

func TestExport_Auth(t *testing.T) {
	body := payload("x", errorRecord())

	t.Run("anonymous browser write passes the origin check", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		assert.Equal(t, http.StatusNoContent, r.post(t, request{body: body}).Code)
	})
	t.Run("another origin is refused", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		assert.Equal(t, http.StatusForbidden, r.post(t, request{body: body, origin: "https://evil.example"}).Code)
		assert.Equal(t, http.StatusForbidden, r.post(t, request{body: body, origin: "none"}).Code)
		assert.Empty(t, r.collector.received())
	})
	t.Run("a signed-in browser sends its CSRF token", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		assert.Equal(t, http.StatusForbidden, r.post(t, request{body: body, cookie: goodToken}).Code)
		rec := r.post(t, request{body: body, cookie: goodToken, headers: map[string]string{authnCSRF: "csrf-session-a"}})
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
	t.Run("a bearer token needs no browser checks", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		rec := r.post(t, request{body: body, origin: "none", headers: map[string]string{"Authorization": "Bearer " + goodToken}})
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
}

const authnCSRF = "X-CSRF-Token"

// An expired or forged credential makes the request anonymous, never a
// refusal: a crash report from a visitor whose session just lapsed is
// still wanted. It is flagged, and a record needing consent is dropped
// rather than judged as a visitor's.
func TestExport_RejectedCredential(t *testing.T) {
	analytics := attr{"severityNumber": 9, "attributes": []attr{str("privacy.tier", "analytics"), str("screen", "board")}}
	body := payload("x", errorRecord(), analytics)

	t.Run("with no credential and consent, both pass", func(t *testing.T) {
		r := newRig(t, rigOptions{consentGranted: true})
		require.Equal(t, http.StatusNoContent, r.post(t, request{body: body}).Code)
		got := r.collector.received()
		require.Len(t, got, 1)
		assert.Contains(t, got[0], `"stringValue":"analytics"`)
	})
	t.Run("with a refused credential, the operational record passes and the consented one is dropped", func(t *testing.T) {
		r := newRig(t, rigOptions{consentGranted: true})
		rec := r.post(t, request{body: body, cookie: "expired"})
		require.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, "rejected", rec.Header().Get("X-Auth-Credential"))
		got := r.collector.received()
		require.Len(t, got, 1)
		assert.NotContains(t, got[0], "analytics")
		assert.Contains(t, got[0], "operational")
	})
}

func TestNewPolicy_IsOperationalOnlyAndForcesTheFrontendService(t *testing.T) {
	policy, err := otelproxy.NewPolicy()
	require.NoError(t, err)
	res, err := policy.Apply(context.Background(), []byte(payload("other",
		errorRecord(str("error.type", "TypeError")),
		attr{"severityNumber": 17, "attributes": []attr{str("privacy.tier", "analytics")}},
	)), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Forwarded)
	assert.Equal(t, 1, res.DroppedUnknownTier, "slop-haus has no consented tier")
	assert.Contains(t, string(res.Body), otelproxy.ServiceName)
}

func TestExport_UserIDIsTheVerifiedUsers_NotTheReports(t *testing.T) {
	r := newRig(t, rigOptions{})
	body := payload("x", errorRecord(str("user.id", "someone-else")))

	rec := r.post(t, request{body: body, origin: "none", headers: map[string]string{"Authorization": "Bearer " + goodToken}})
	require.Equal(t, http.StatusNoContent, rec.Code)
	got := r.collector.received()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], signedIn.ID.String())
	assert.NotContains(t, got[0], "someone-else")

	rec = r.post(t, request{body: body})
	require.Equal(t, http.StatusNoContent, rec.Code)
	got = r.collector.received()
	require.Len(t, got, 2)
	assert.NotContains(t, got[1], "user.id", "an anonymous report carries no user id")
	assert.NotContains(t, got[1], "someone-else")
}
