package otellog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

func TestFanoutHandler_HandleReachesEveryChild(t *testing.T) {
	var bufA, bufB bytes.Buffer
	handler := &fanout{handlers: []slog.Handler{
		slog.NewTextHandler(&bufA, nil),
		slog.NewTextHandler(&bufB, nil),
	}}

	slog.New(handler).Info("hello", "key", "value")

	assert.Contains(t, bufA.String(), "hello")
	assert.Contains(t, bufA.String(), "key=value")
	assert.Contains(t, bufB.String(), "hello")
	assert.Contains(t, bufB.String(), "key=value")
}

func TestFanoutHandler_WithAttrsAppliesToEveryChild(t *testing.T) {
	var bufA, bufB bytes.Buffer
	handler := &fanout{handlers: []slog.Handler{
		slog.NewTextHandler(&bufA, nil),
		slog.NewTextHandler(&bufB, nil),
	}}

	slog.New(handler).With("request_id", "abc123").Info("hello")

	assert.Contains(t, bufA.String(), "request_id=abc123")
	assert.Contains(t, bufB.String(), "request_id=abc123")
}

func TestFanoutHandler_WithGroupAppliesToEveryChild(t *testing.T) {
	var bufA, bufB bytes.Buffer
	handler := &fanout{handlers: []slog.Handler{
		slog.NewTextHandler(&bufA, nil),
		slog.NewTextHandler(&bufB, nil),
	}}

	slog.New(handler).WithGroup("job").Info("hello", "id", "1")

	assert.Contains(t, bufA.String(), "job.id=1")
	assert.Contains(t, bufB.String(), "job.id=1")
}

func TestFanoutHandler_EnabledIfAnyChildEnabled(t *testing.T) {
	var buf bytes.Buffer
	handler := &fanout{handlers: []slog.Handler{
		slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}),
		slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}),
	}}

	assert.True(t, handler.Enabled(context.Background(), slog.LevelInfo))
	assert.False(t, handler.Enabled(context.Background(), slog.LevelDebug))
}

// TestFanoutHandler_HandleSkipsChildWhoseOwnLevelExcludesTheRecord proves
// each child's own Enabled is consulted independently in Handle, not just
// in the aggregate Enabled -- a child with a stricter level filter than
// another must not receive a record it wouldn't otherwise log.
func TestFanoutHandler_HandleSkipsChildWhoseOwnLevelExcludesTheRecord(t *testing.T) {
	var bufErrorOnly, bufInfo bytes.Buffer
	handler := &fanout{handlers: []slog.Handler{
		slog.NewTextHandler(&bufErrorOnly, &slog.HandlerOptions{Level: slog.LevelError}),
		slog.NewTextHandler(&bufInfo, &slog.HandlerOptions{Level: slog.LevelInfo}),
	}}

	slog.New(handler).Info("hello")

	assert.Empty(t, bufErrorOnly.String())
	assert.Contains(t, bufInfo.String(), "hello")
}

// recordingExporter keeps what the provider would have sent to the collector.
type recordingExporter struct {
	records []sdklog.Record
}

func (e *recordingExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (e *recordingExporter) Shutdown(context.Context) error   { return nil }
func (e *recordingExporter) ForceFlush(context.Context) error { return nil }

// The collector routes by this attribute: without it every server record is
// a tier fault there.
func TestTieredOTelHandler_EveryRecordIsOperational(t *testing.T) {
	exporter := &recordingExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	logger := slog.New(tieredOTelHandler("test-service", provider))

	logger.Info("request", "path", "/healthz")
	logger.With("component", "x").Error("boom")

	require.Len(t, exporter.records, 2)
	for _, record := range exporter.records {
		tiers := map[string]string{}
		record.WalkAttributes(func(kv attribute.KeyValue) bool {
			if string(kv.Key) == TierAttribute {
				tiers[string(kv.Key)] = kv.Value.AsString()
			}
			return true
		})
		assert.Equal(t, map[string]string{TierAttribute: OperationalTier}, tiers)
	}
}

// The OTLP record's own trace id is what VictoriaLogs stores as trace_id, so a
// frontend error's id finds the exported record, not only the stdout copy.
func TestTieredOTelHandler_RecordCarriesTraceIDFromContext(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
	exporter := &recordingExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))

	slog.New(tieredOTelHandler("test-service", provider)).InfoContext(ctx, "request")

	require.Len(t, exporter.records, 1)
	assert.Equal(t, traceID, exporter.records[0].TraceID())
}

func TestOTLPCopyHasTheSameLevelFloorAsStdout(t *testing.T) {
	exporter := &recordingExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	handler := tieredOTelHandler("test-service", provider)

	logger := slog.New(handler)
	logger.Debug("debug")
	logger.Info("info")

	require.Len(t, exporter.records, 1)
	assert.Equal(t, log.SeverityInfo, exporter.records[0].Severity())
	assert.False(t, handler.Enabled(context.Background(), slog.LevelDebug))
	assert.False(t, handler.WithAttrs([]slog.Attr{slog.String("a", "b")}).Enabled(context.Background(), slog.LevelDebug))
	assert.False(t, handler.WithGroup("g").Enabled(context.Background(), slog.LevelDebug))
}

type failingHandler struct{ err error }

func (h *failingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h *failingHandler) Handle(context.Context, slog.Record) error { return h.err }
func (h *failingHandler) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h *failingHandler) WithGroup(string) slog.Handler             { return h }

func TestFanoutHandler_TriesEveryChildAfterOneFails(t *testing.T) {
	boom := errors.New("stdout write failed")
	var buf bytes.Buffer
	handler := &fanout{handlers: []slog.Handler{&failingHandler{err: boom}, slog.NewTextHandler(&buf, nil)}}

	err := handler.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "still delivered", 0))

	assert.ErrorIs(t, err, boom)
	assert.Contains(t, buf.String(), "still delivered")
}
