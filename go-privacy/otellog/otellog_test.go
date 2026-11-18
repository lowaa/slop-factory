package otellog_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/lowaa/slop-factory/go-privacy/otellog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// fakeCollector stands in for the OTLP endpoint, keeping every body it is
// sent. OTLP/HTTP bodies are protobuf, where strings are stored as they are,
// so a message can be looked for in the raw bytes.
type fakeCollector struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newFakeCollector(t *testing.T, status int) *fakeCollector {
	c := &fakeCollector{status: status}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		w.WriteHeader(c.status)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *fakeCollector) requests() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.bodies...)
}

func (c *fakeCollector) received() string {
	return string(bytes.Join(c.requests(), nil))
}

func (c *fakeCollector) endpoint() string { return c.URL + "/v1/logs" }

func newLogs(t *testing.T, endpoint string, format otellog.Format) (*otellog.Logs, *bytes.Buffer) {
	var out bytes.Buffer
	logs, err := otellog.New(&otellog.Options{ServiceName: "test-service", OTLPEndpoint: endpoint, Output: &out, Format: format})
	require.NoError(t, err)
	return logs, &out
}

func TestRecordsGoToTheLocalCopyAndTheCollector(t *testing.T) {
	collector := newFakeCollector(t, http.StatusOK)
	logs, out := newLogs(t, collector.endpoint(), otellog.FormatText)

	logger := slog.New(logs.Handler()).With("component", "test")
	logger.Info("audit", "action", "sign_in")
	logger.Debug("chatter nobody asked for")
	require.NoError(t, logs.Close(t.Context()), "closing flushes the batch")

	exported := collector.received()
	assert.Contains(t, exported, "audit")
	assert.Contains(t, exported, "sign_in", "attributes travel as fields")
	assert.Contains(t, exported, "test-service", "with the service name streams are grouped by")
	assert.NotContains(t, exported, "chatter", "debug is not exported")
	assert.Contains(t, out.String(), "action=sign_in")
	assert.NotContains(t, out.String(), "chatter")
}

func TestWithoutAnEndpointNothingIsExported(t *testing.T) {
	logs, out := newLogs(t, "", otellog.FormatText)

	slog.New(logs.Handler()).Info("local only")

	assert.NoError(t, logs.Close(t.Context()))
	assert.Contains(t, out.String(), "local only")
}

func TestFormatSelectsTheLocalCopyEncoding(t *testing.T) {
	logs, out := newLogs(t, "", otellog.FormatJSON)

	slog.New(logs.Handler()).Info("hello", "k", "v")

	assert.Contains(t, out.String(), `"msg":"hello"`)
	assert.Contains(t, out.String(), `"k":"v"`)
}

// The collector routes by the tier attribute and stores anything without it as
// a fault: every exported record carries it, whichever logger it came through,
// and the local copy (where it means nothing) does not.
func TestExportedRecordsCarryTheOperationalTier(t *testing.T) {
	collector := newFakeCollector(t, http.StatusOK)
	logs, out := newLogs(t, collector.endpoint(), otellog.FormatText)

	logger := slog.New(logs.Handler())
	logger.Info("plain")
	logger.With("component", "x").WithGroup("g").Warn("grouped", "k", "v")
	require.NoError(t, logs.Close(t.Context()))

	var records int
	for _, body := range collector.requests() {
		var req collogs.ExportLogsServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					records++
					tiers := map[string]string{}
					for _, kv := range lr.Attributes {
						if kv.Key == otellog.TierAttribute {
							tiers[kv.Key] = kv.Value.GetStringValue()
						}
					}
					assert.Equal(t, map[string]string{otellog.TierAttribute: otellog.OperationalTier}, tiers, "record %q", lr.Body.GetStringValue())
				}
			}
		}
	}
	assert.Equal(t, 2, records)
	assert.NotContains(t, out.String(), otellog.TierAttribute)
}

// The OTLP record's own trace id is what VictoriaLogs stores as trace_id, so a
// frontend error's id finds the exported record, not only the local copy.
func TestTraceIDsReachBothCopies(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
	collector := newFakeCollector(t, http.StatusOK)
	logs, out := newLogs(t, collector.endpoint(), otellog.FormatJSON)

	logger := slog.New(logs.Handler()).With("k", "v")
	logger.InfoContext(ctx, "traced")
	logger.Info("untraced")
	require.NoError(t, logs.Close(t.Context()))

	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)
	assert.Contains(t, string(lines[0]), `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`)
	assert.Contains(t, string(lines[0]), `"span_id":"00f067aa0ba902b7"`)
	assert.NotContains(t, string(lines[1]), "trace_id")

	var traced int
	for _, body := range collector.requests() {
		var req collogs.ExportLogsServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					if bytes.Equal(lr.TraceId, traceID[:]) {
						traced++
					}
				}
			}
		}
	}
	assert.Equal(t, 1, traced)
}

// The process's own context is cancelled by the signal that stops it, so the
// flush that follows must not depend on it.
func TestCloseFlushesWithAContextOfItsOwn(t *testing.T) {
	collector := newFakeCollector(t, http.StatusOK)
	logs, _ := newLogs(t, collector.endpoint(), otellog.FormatText)

	slog.New(logs.Handler()).Info("stopping on SIGTERM")
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, logs.Close(flushCtx))

	assert.Contains(t, collector.received(), "stopping on SIGTERM")
}

func TestCloseIsBoundedByItsContextWhenTheCollectorHangs(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	logs, _ := newLogs(t, hung.URL+"/v1/logs", otellog.FormatText)

	slog.New(logs.Handler()).Info("stuck")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- logs.Close(ctx) }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("close outlived its own context")
	}
}

// OTel's default handler writes through the log package, which slog.SetDefault
// routes into the logger: a failing export would queue a record for itself.
func TestExportErrorsGoToTheLocalCopyAndNotBackIntoTheExporter(t *testing.T) {
	collector := newFakeCollector(t, http.StatusBadRequest)
	logs, out := newLogs(t, collector.endpoint(), otellog.FormatText)
	// What slog.SetDefault does to the log package, which OTel's default error
	// handler writes through.
	prevFlags := log.Flags()
	log.SetFlags(0)
	log.SetOutput(slog.NewLogLogger(logs.Handler(), slog.LevelError).Writer())
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(prevFlags)
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	})

	// A failed background export reaches OTel's handler; Close's own flush
	// failure is returned to the caller instead, so is not the way to get one.
	otel.Handle(errors.New("collector unreachable"))
	_ = logs.Close(t.Context())

	assert.Contains(t, out.String(), "log export failed")
	assert.Contains(t, out.String(), "collector unreachable")
	assert.NotContains(t, collector.received(), "log export failed")
}

// The policy sits in front of both destinations, with one key: what must not
// be logged is in neither copy, and a hashed email is the same pseudonym in
// both.
func TestThePolicyAppliesToBothCopies(t *testing.T) {
	const (
		fixtureEmail = "ann.patient@example.com"
		fixtureIP    = "203.0.113.77"
		fixtureTitle = "Quarterly oncology review for Ann Patient"
	)
	collector := newFakeCollector(t, http.StatusOK)
	logs, out := newLogs(t, collector.endpoint(), otellog.FormatText)

	slog.New(logs.Handler()).Error("update", "client_ip", fixtureIP, "email", fixtureEmail, "body", "form=secret",
		"err", errors.New(`no ticket titled "`+fixtureTitle+`"`))
	require.NoError(t, logs.Close(t.Context()))

	require.Contains(t, collector.received(), "203.0.113.0/24", "something was exported")
	for name, got := range map[string]string{"local": out.String(), "exported": collector.received()} {
		assert.NotContains(t, got, fixtureEmail, name)
		assert.NotContains(t, got, fixtureIP, name)
		assert.NotContains(t, got, "Ann Patient", name)
		assert.NotContains(t, got, "form=secret", name)
		assert.Contains(t, got, "203.0.113.0/24", name)
		assert.Contains(t, got, "no ticket titled", name)
	}
	pseudonym := regexpHash.FindString(out.String())
	require.NotEmpty(t, pseudonym)
	assert.Contains(t, collector.received(), pseudonym, "one hash key for both copies")
}

func TestDevMailHandlerIsUnfiltered(t *testing.T) {
	var out bytes.Buffer

	slog.New(otellog.DevMailHandler(&out, otellog.FormatText)).Info("email", "to", "ann@example.com", "body", "https://app/activate?token=abc")

	assert.Contains(t, out.String(), "ann@example.com")
	assert.Contains(t, out.String(), "token=abc")
}

var regexpHash = regexp.MustCompile(`h:[0-9a-f]{16}`)

// A collector that is restarting refuses connections; the batch sent meanwhile
// must be retried, not dropped.
func TestRecordsSentWhileTheCollectorIsDownArriveWhenItReturns(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close(), "nothing listens: connections are refused")

	var out bytes.Buffer
	logs, err := otellog.New(&otellog.Options{
		ServiceName:  "test-service",
		OTLPEndpoint: "http://" + addr + "/v1/logs",
		Output:       &out,
		Export:       otellog.ExportOptions{RetryInitialInterval: 10 * time.Millisecond, RetryMaxInterval: 20 * time.Millisecond},
	})
	require.NoError(t, err)
	slog.New(logs.Handler()).Info("sent during the outage")

	var mu sync.Mutex
	var got []byte
	// The batch is first sent when Close flushes it. The collector returns
	// after that attempt is refused; a shorter wait still passes, it just
	// proves less.
	returned := make(chan *httptest.Server, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		back, err := net.Listen("tcp", addr)
		if err != nil {
			returned <- nil
			return
		}
		srv := &httptest.Server{Listener: back, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			got = append(got, body...)
			mu.Unlock()
		})}}
		srv.Start()
		returned <- srv
	}()

	require.NoError(t, logs.Close(t.Context()))
	srv := <-returned
	require.NotNil(t, srv, "the collector's address was taken")
	defer srv.Close()
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, string(got), "sent during the outage")
}

// A collector that never returns must not hold a batch for ever: past
// Export.Timeout it is dropped and the failure reaches the local copy.
func TestABatchIsDroppedOnceTheExportTimeoutPasses(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	var out bytes.Buffer
	logs, err := otellog.New(&otellog.Options{
		ServiceName:  "test-service",
		OTLPEndpoint: "http://" + addr + "/v1/logs",
		Output:       &out,
		Export: otellog.ExportOptions{
			RetryInitialInterval: 5 * time.Millisecond,
			RetryMaxInterval:     10 * time.Millisecond,
			Timeout:              100 * time.Millisecond,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {})) })
	slog.New(logs.Handler()).Info("never delivered")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.Error(t, logs.Close(ctx))
	assert.NotErrorIs(t, ctx.Err(), context.DeadlineExceeded, "gave up on its own timeout, not the caller's")
}

type kindName string

type kindCount int

type kindFlag bool

type kindStringer int

func (k kindStringer) String() string { return "stringer-" + string(rune('0'+int(k))) }

type kindValuer struct{}

func (kindValuer) LogValue() slog.Value { return slog.StringValue("valued") }

type kindMarshaler struct{ fail bool }

func (k kindMarshaler) MarshalText() ([]byte, error) {
	if k.fail {
		return nil, errors.New("boom")
	}
	return []byte("marshaled"), nil
}

type kindNilStringer struct{ name string }

func (k *kindNilStringer) String() string { return k.name }

func TestNamedTypesReachTheCollectorAsPlainValues(t *testing.T) {
	collector := newFakeCollector(t, http.StatusOK)
	logs, _ := newLogs(t, collector.endpoint(), otellog.FormatText)

	var nilStringer *kindNilStringer
	slog.New(logs.Handler()).With("bound", kindName("with")).Info("typed",
		"name", kindName("merge"), "count", kindCount(7), "flag", kindFlag(true),
		"stringer", kindStringer(3), "valuer", kindValuer{}, "nilptr", nilStringer,
		"marshaler", kindMarshaler{}, "failing", kindMarshaler{fail: true},
		slog.Group("g", "inner", kindName("deep")))
	require.NoError(t, logs.Close(t.Context()))

	got := map[string]string{}
	for _, body := range collector.requests() {
		var req collogs.ExportLogsServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					for _, kv := range lr.Attributes {
						got[kv.Key] = kv.Value.String()
					}
				}
			}
		}
	}
	assert.Equal(t, `string_value:"merge"`, got["name"])
	assert.Equal(t, `string_value:"with"`, got["bound"])
	assert.Equal(t, `int_value:7`, got["count"])
	assert.Equal(t, `bool_value:true`, got["flag"])
	assert.Equal(t, `string_value:"stringer-3"`, got["stringer"])
	assert.Equal(t, `string_value:"valued"`, got["valuer"])
	assert.NotContains(t, got["nilptr"], "unhandled")
	assert.Equal(t, `string_value:"marshaled"`, got["marshaler"])
	assert.Equal(t, `string_value:"!ERROR:boom"`, got["failing"])
	assert.Contains(t, got["g"], `deep`)
	assert.NotContains(t, collector.received(), "unhandled")
}

func TestNamedTypesInADerivedGroupReachTheCollectorAsPlainValues(t *testing.T) {
	collector := newFakeCollector(t, http.StatusOK)
	logs, _ := newLogs(t, collector.endpoint(), otellog.FormatText)

	slog.New(logs.Handler()).WithGroup("g").With("bound", kindName("a")).Info("grouped", "rec", kindName("b"))
	require.NoError(t, logs.Close(t.Context()))

	var req collogs.ExportLogsServiceRequest
	require.Len(t, collector.requests(), 1)
	require.NoError(t, proto.Unmarshal(collector.requests()[0], &req))
	got := map[string]string{}
	for _, kv := range req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes {
		if kv.Key == "g" {
			for _, inner := range kv.Value.GetKvlistValue().Values {
				got[inner.Key] = inner.Value.GetStringValue()
			}
		}
	}
	assert.Equal(t, map[string]string{"bound": "a", "rec": "b"}, got)
}
