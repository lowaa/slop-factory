package otelproxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-privacy/otelproxy"
	"github.com/lowaa/slop-factory/go-privacy/telemetry"
)

const (
	serviceName = "svc-frontend"
	logsPath    = "/api/otel/v1/logs"
	tracesPath  = "/api/otel/v1/traces"
	knownRoute  = "/api/things/{id}"
)

type ctxKey string

// The test's stand-in for an app's authentication: headers say who the
// request is from, and Identify reads that back off the context.
const (
	userHeader     = "X-Test-User"
	rejectedHeader = "X-Test-Rejected"
)

type received struct {
	path        string
	contentType string
	body        []byte
}

// store is a fake log and trace store that keeps what it was sent.
type store struct {
	*httptest.Server
	mu     sync.Mutex
	got    []received
	status int
	hold   func()
}

func newStore(t *testing.T) *store {
	t.Helper()
	s := &store{status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.got = append(s.got, received{r.URL.Path, r.Header.Get("Content-Type"), b})
		status, hold := s.status, s.hold
		s.mu.Unlock()
		if hold != nil {
			hold()
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *store) received() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.got...)
}

type rigOptions struct {
	limit          int
	trustedProxies []string
	encoding       otelproxy.Encoding
	noEndpoint     bool
	noSpans        bool
	refuseUnknown  bool
	maxBody        int64
	maxForwards    int
	routes         *otelproxy.Routes
}

type rig struct {
	mux   *http.ServeMux
	store *store
	api   huma.API
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	st := newStore(t)
	if o.limit == 0 {
		o.limit = 1000
	}
	policy, err := telemetry.New(&telemetry.Config{
		ServiceName:           serviceName,
		OperationalAttributes: []string{"error.type"},
		Consented: map[string]*telemetry.ConsentedTier{
			"analytics": {Attributes: []string{"screen"}, Granted: func(context.Context) bool { return true }},
		},
	})
	require.NoError(t, err)
	spans, err := telemetry.NewSpanPolicy(serviceName)
	require.NoError(t, err)
	opts := &otelproxy.Options{
		LogsEndpoint:   st.URL + "/v1/logs",
		TracesEndpoint: st.URL + "/v1/traces",
		Encoding:       o.encoding,
		Logs:           policy,
		Spans:          spans,
		Limit:          &ratelimit.Config{Name: "otel_proxy", Limit: o.limit, Window: time.Hour, MaxKeys: 100},
		MaxBodyBytes:   o.maxBody,
		MaxForwards:    o.maxForwards,
		Identify: func(ctx context.Context) *telemetry.Caller {
			user, _ := ctx.Value(ctxKey(userHeader)).(string)
			rejected, _ := ctx.Value(ctxKey(rejectedHeader)).(bool)
			return &telemetry.Caller{UserID: user, ConsentUnknown: rejected}
		},
	}
	if o.noEndpoint {
		opts.LogsEndpoint, opts.TracesEndpoint = "", ""
	}
	if o.noSpans {
		opts.Spans = nil
	}
	if o.refuseUnknown {
		opts.ConsentUnknownError = func() error { return huma.Error403Forbidden("credential refused") }
	}
	h, err := otelproxy.NewHandler(opts)
	require.NoError(t, err)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	resolver, err := clientip.NewResolver(o.trustedProxies)
	require.NoError(t, err)
	api.UseMiddleware(resolver.Middleware())
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		c := ctx.Context()
		if u := ctx.Header(userHeader); u != "" {
			c = context.WithValue(c, ctxKey(userHeader), u)
		}
		if ctx.Header(rejectedHeader) != "" {
			c = context.WithValue(c, ctxKey(rejectedHeader), true)
		}
		next(huma.WithContext(ctx, c))
	})
	// A route of the API, for the span check to find.
	huma.Register(api, huma.Operation{OperationID: "getThing", Method: http.MethodGet, Path: knownRoute}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	routes := o.routes
	if routes == nil {
		routes = &otelproxy.Routes{}
	}
	routes.LogsPath, routes.TracesPath = logsPath, tracesPath
	h.Register(api, routes)
	return &rig{mux: mux, store: st, api: api}
}

type request struct {
	path    string
	body    string
	remote  string
	headers map[string]string
}

func (r *rig) post(q request) *httptest.ResponseRecorder {
	if q.path == "" {
		q.path = logsPath
	}
	req := httptest.NewRequest(http.MethodPost, q.path, strings.NewReader(q.body))
	req.Header.Set("Content-Type", "application/json")
	if q.remote != "" {
		req.RemoteAddr = q.remote
	}
	for k, v := range q.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
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

func TestLogs_ForwardsRebuiltJSON_ForcingServiceName(t *testing.T) {
	r := newRig(t, rigOptions{})
	rec := r.post(request{body: payload("someone-elses-api", errorRecord(str("error.type", "TypeError"), str("form.title", "Jane Doe")))})

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got := r.store.received()
	require.Len(t, got, 1)
	assert.Equal(t, "/v1/logs", got[0].path)
	assert.Equal(t, "application/json", got[0].contentType)
	assert.Contains(t, string(got[0].body), serviceName)
	assert.NotContains(t, string(got[0].body), "someone-elses-api")
	assert.NotContains(t, string(got[0].body), "Jane")
}

func TestLogs_ProtobufEncoding(t *testing.T) {
	r := newRig(t, rigOptions{encoding: otelproxy.Protobuf})
	rec := r.post(request{body: payload("x", errorRecord(str("error.type", "TypeError")))})

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got := r.store.received()
	require.Len(t, got, 1)
	assert.Equal(t, "application/x-protobuf", got[0].contentType)
	var msg collogs.ExportLogsServiceRequest
	require.NoError(t, proto.Unmarshal(got[0].body, &msg))
	j, err := protojson.Marshal(&msg)
	require.NoError(t, err)
	assert.Contains(t, string(j), "TypeError")
	assert.Contains(t, string(j), serviceName)
}

func TestLogs_NothingPassingMeansNothingForwarded(t *testing.T) {
	r := newRig(t, rigOptions{})
	info := errorRecord()
	info["severityNumber"] = 9
	rec := r.post(request{body: payload("x", info, attr{"severityNumber": 17})})

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, r.store.received())
}

func TestLogs_RateLimitedPerClientAddress(t *testing.T) {
	r := newRig(t, rigOptions{limit: 3})
	send := func(remote string, headers map[string]string) *httptest.ResponseRecorder {
		return r.post(request{body: payload("x", errorRecord()), remote: remote, headers: headers})
	}

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusNoContent, send("203.0.113.5:1000", nil).Code)
	}
	limited := send("203.0.113.5:1001", nil)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Equal(t, "60", limited.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusNoContent, send("203.0.113.6:1000", nil).Code, "another address has its own allowance")

	// An untrusted peer's X-Forwarded-For is a client's own claim: rotating
	// it must not buy a fresh allowance.
	assert.Equal(t, http.StatusTooManyRequests, send("203.0.113.5:1002", map[string]string{"X-Forwarded-For": "198.51.100.77"}).Code)
}

func TestLogs_RateLimitKeyComesFromTrustedProxiesOnly(t *testing.T) {
	r := newRig(t, rigOptions{limit: 1, trustedProxies: []string{"10.0.0.0/8"}})
	send := func(xff string) int {
		return r.post(request{body: payload("x", errorRecord()), remote: "10.1.1.1:5000", headers: map[string]string{"X-Forwarded-For": xff}}).Code
	}
	assert.Equal(t, http.StatusNoContent, send("198.51.100.1"))
	assert.Equal(t, http.StatusTooManyRequests, send("198.51.100.1"))
	assert.Equal(t, http.StatusNoContent, send("198.51.100.2"), "behind a trusted proxy the forwarded client is the key")
	// What a client wrote itself sits left of the address the proxy saw.
	assert.Equal(t, http.StatusTooManyRequests, send("1.2.3.4, 198.51.100.1"))
}

func TestLogs_RateLimitIsSharedByBothRoutes(t *testing.T) {
	r := newRig(t, rigOptions{limit: 1})
	require.Equal(t, http.StatusNoContent, r.post(request{body: payload("x", errorRecord())}).Code)
	assert.Equal(t, http.StatusTooManyRequests, r.post(request{path: tracesPath, body: `{"resourceSpans":[]}`}).Code)
}

func TestLogs_BodySizeLimited(t *testing.T) {
	for name, max := range map[string]int64{"the default": 0, "the app's own": 4 * 1024} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, rigOptions{maxBody: max})
			stack := strings.Repeat("a", 200*1024)
			if max != 0 {
				stack = strings.Repeat("a", 8*1024)
			}
			rec := r.post(request{body: payload("x", errorRecord(str("error.stack", stack)))})
			assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
			assert.Empty(t, r.store.received())
		})
	}
}

func TestLogs_MalformedPayloadRejected(t *testing.T) {
	r := newRig(t, rigOptions{})
	for _, body := range []string{`{"not":"otlp"}`, `nope`, ``} {
		assert.Equal(t, http.StatusBadRequest, r.post(request{body: body}).Code, body)
	}
	assert.Empty(t, r.store.received())
}

func TestLogs_NoStoreConfigured_AcceptsAndDiscards(t *testing.T) {
	r := newRig(t, rigOptions{noEndpoint: true})
	assert.Equal(t, http.StatusNoContent, r.post(request{body: payload("x", errorRecord())}).Code)
	assert.Empty(t, r.store.received())
}

func TestLogs_StoreFailureIs502(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.store.mu.Lock()
	r.store.status = http.StatusInternalServerError
	r.store.mu.Unlock()
	rec := r.post(request{body: payload("x", errorRecord())})
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), r.store.URL, "the store's address is not for callers")
}

func TestLogs_ForwardsAreCapped(t *testing.T) {
	for name, configured := range map[string]int{"the default": 0, "the app's own": 2} {
		t.Run(name, func(t *testing.T) {
			cap := configured
			if cap == 0 {
				cap = otelproxy.DefaultMaxForwards
			}
			r := newRig(t, rigOptions{maxForwards: configured})
			entered := make(chan struct{}, cap)
			release := make(chan struct{})
			r.store.mu.Lock()
			r.store.hold = func() {
				entered <- struct{}{}
				<-release
			}
			r.store.mu.Unlock()

			var wg sync.WaitGroup
			for i := 0; i < cap; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r.post(request{body: payload("x", errorRecord())})
				}()
			}
			for i := 0; i < cap; i++ {
				<-entered
			}
			rec := r.post(request{body: payload("x", errorRecord())})
			close(release)
			wg.Wait()

			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		})
	}
}

func TestLogs_UserIDIsTheIdentifiedCallers_NotTheReports(t *testing.T) {
	r := newRig(t, rigOptions{})
	body := payload("x", errorRecord(str("user.id", "someone-else")))

	require.Equal(t, http.StatusNoContent, r.post(request{body: body, headers: map[string]string{userHeader: "verified"}}).Code)
	require.Equal(t, http.StatusNoContent, r.post(request{body: body}).Code)

	got := r.store.received()
	require.Len(t, got, 2)
	assert.Contains(t, string(got[0].body), "verified")
	assert.NotContains(t, string(got[0].body), "someone-else")
	assert.NotContains(t, string(got[1].body), "user.id", "an anonymous report carries no user id")
	assert.NotContains(t, string(got[1].body), "someone-else")
}

func TestLogs_ARefusedCredential(t *testing.T) {
	analytics := attr{"severityNumber": 9, "attributes": []attr{str("privacy.tier", "analytics"), str("screen", "board")}}
	body := payload("x", errorRecord(), analytics)
	rejected := map[string]string{rejectedHeader: "1"}

	t.Run("without ConsentUnknownError the consented record is dropped and the rest goes on", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		require.Equal(t, http.StatusNoContent, r.post(request{body: body, headers: rejected}).Code)
		got := r.store.received()
		require.Len(t, got, 1)
		assert.NotContains(t, string(got[0].body), "analytics")
		assert.Contains(t, string(got[0].body), "operational")
	})
	t.Run("with it, a batch holding a consented record is refused whole", func(t *testing.T) {
		r := newRig(t, rigOptions{refuseUnknown: true})
		assert.Equal(t, http.StatusForbidden, r.post(request{body: body, headers: rejected}).Code)
		assert.Empty(t, r.store.received())
	})
	t.Run("with it, a batch of operational records only still goes through", func(t *testing.T) {
		r := newRig(t, rigOptions{refuseUnknown: true})
		require.Equal(t, http.StatusNoContent, r.post(request{body: payload("x", errorRecord()), headers: rejected}).Code)
		assert.Len(t, r.store.received(), 1)
	})
	t.Run("with a good credential the consented record passes", func(t *testing.T) {
		r := newRig(t, rigOptions{refuseUnknown: true})
		require.Equal(t, http.StatusNoContent, r.post(request{body: body}).Code)
		got := r.store.received()
		require.Len(t, got, 1)
		assert.Contains(t, string(got[0].body), "analytics")
	})
}

func goodSpan() attr {
	return attr{
		"traceId":           "4bf92f3577b34da6a3ce929d0e0e4736",
		"spanId":            "00f067aa0ba902b7",
		"kind":              3,
		"startTimeUnixNano": "1700000000000000000",
		"endTimeUnixNano":   "1700000000250000000",
		"attributes":        []attr{str("http.request.method", "POST"), str("http.route", knownRoute)},
		"status":            attr{"code": 2},
	}
}

func spansPayload(spans ...attr) string {
	b, _ := json.Marshal(attr{"resourceSpans": []any{attr{
		"resource":   attr{"attributes": []attr{str("service.name", "other")}},
		"scopeSpans": []any{attr{"spans": spans}},
	}}})
	return string(b)
}

func TestTraces_ForwardsAFailedRequestSpanOfTheAPI(t *testing.T) {
	for name, enc := range map[string]otelproxy.Encoding{"json": otelproxy.JSON, "protobuf logs leave traces as json": otelproxy.Protobuf} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, rigOptions{encoding: enc})
			rec := r.post(request{path: tracesPath, body: spansPayload(goodSpan())})

			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			got := r.store.received()
			require.Len(t, got, 1)
			assert.Equal(t, "/v1/traces", got[0].path)
			assert.Equal(t, "application/json", got[0].contentType)
			assert.Contains(t, string(got[0].body), serviceName)
			assert.NotContains(t, string(got[0].body), "other")
			assert.Contains(t, string(got[0].body), "4bf92f3577b34da6a3ce929d0e0e4736", "ids stay hex")
		})
	}
}

func TestTraces_ASpanOfARouteTheAPIDoesNotHaveIsDropped(t *testing.T) {
	r := newRig(t, rigOptions{})
	span := goodSpan()
	span["attributes"] = []attr{str("http.request.method", "POST"), str("http.route", "/api/not-ours")}
	require.Equal(t, http.StatusNoContent, r.post(request{path: tracesPath, body: spansPayload(span)}).Code)
	assert.Empty(t, r.store.received())
}

func TestTraces_MalformedRejected(t *testing.T) {
	r := newRig(t, rigOptions{})
	assert.Equal(t, http.StatusBadRequest, r.post(request{path: tracesPath, body: `{"resourceLogs":[]}`}).Code)
	assert.Empty(t, r.store.received())
}

func TestTraces_NoRouteWithoutASpanPolicy(t *testing.T) {
	r := newRig(t, rigOptions{noSpans: true})
	assert.Equal(t, http.StatusNotFound, r.post(request{path: tracesPath, body: spansPayload(goodSpan())}).Code)
}

func TestTraces_NoStoreConfigured_AcceptsAndDiscards(t *testing.T) {
	r := newRig(t, rigOptions{noEndpoint: true})
	assert.Equal(t, http.StatusNoContent, r.post(request{path: tracesPath, body: spansPayload(goodSpan())}).Code)
}

func TestNewHandler_RefusesAnIncompleteConfig(t *testing.T) {
	policy, err := telemetry.New(&telemetry.Config{ServiceName: serviceName})
	require.NoError(t, err)
	valid := func() *otelproxy.Options {
		return &otelproxy.Options{
			Logs:     policy,
			Limit:    &ratelimit.Config{Name: "otel_proxy", Limit: 1, Window: time.Hour, MaxKeys: 1},
			Identify: func(context.Context) *telemetry.Caller { return nil },
		}
	}
	_, err = otelproxy.NewHandler(valid())
	require.NoError(t, err)
	for name, break_ := range map[string]func(*otelproxy.Options){
		"no logs policy": func(o *otelproxy.Options) { o.Logs = nil },
		"no Identify":    func(o *otelproxy.Options) { o.Identify = nil },
		"no rate limit":  func(o *otelproxy.Options) { o.Limit = nil },
	} {
		o := valid()
		break_(o)
		_, err := otelproxy.NewHandler(o)
		assert.Error(t, err, name)
	}
}

func TestRegister_AppliesTheRoutesMetadataAndHiding(t *testing.T) {
	visible := newRig(t, rigOptions{routes: &otelproxy.Routes{Metadata: map[string]any{"auth": "optional"}}})
	for _, path := range []string{logsPath, tracesPath} {
		op := visible.api.OpenAPI().Paths[path].Post
		require.NotNil(t, op, path)
		assert.Equal(t, map[string]any{"auth": "optional"}, op.Metadata, path)
	}

	hidden := newRig(t, rigOptions{routes: &otelproxy.Routes{Hidden: true}})
	assert.NotContains(t, hidden.api.OpenAPI().Paths, logsPath)
	assert.NotContains(t, hidden.api.OpenAPI().Paths, tracesPath)
}
