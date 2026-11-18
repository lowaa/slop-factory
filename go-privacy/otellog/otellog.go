// Package otellog builds a service's log handler: its records always go to a
// local writer, and -- when an OTLP endpoint is configured -- the same
// records are exported over OTLP/HTTP to the collector that routes them by
// tier (go-privacy/README.md, "Telemetry tiers"). Every record, to either
// destination, first passes the operational tier's attribute policy
// (go-privacy/logpolicy).
//
// Services take the handler and build their own *slog.Logger from it, rather
// than reaching for slog's package-level functions.
package otellog

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/lowaa/slop-factory/go-privacy/logpolicy"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Level is the least severe level either destination gets.
const Level = slog.LevelInfo

const (
	// TierAttribute is the record attribute the collector routes by to a log
	// store (go-privacy/README.md, "Telemetry tiers").
	TierAttribute = "privacy.tier"
	// OperationalTier is the tier of everything a service logs itself: its
	// request, error and security audit logs. A record with no tier is not
	// lost -- the collector stores it as operational too -- but it is counted
	// there as a fault, so the service says so itself.
	OperationalTier = "operational"
)

// Format is how the local copy is written.
type Format int

const (
	// FormatText is slog's key=value text.
	FormatText Format = iota
	// FormatJSON is one JSON object per record.
	FormatJSON
)

func (f Format) handler(w io.Writer, opts *slog.HandlerOptions) slog.Handler {
	if f == FormatJSON {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// Options configure New.
type Options struct {
	// ServiceName is the service.name every exported record carries, the
	// field VictoriaLogs groups a stream by.
	ServiceName string
	// OTLPEndpoint is the full URL records are POSTed to, path included
	// (the collector: http://otel-collector:4318/v1/logs): the exporter takes
	// the path literally and does not default an empty one. Empty exports
	// nothing.
	OTLPEndpoint string
	// Output is where the local copy goes.
	Output io.Writer
	// Format is how the local copy is written.
	Format Format
	// Export tunes how an unreachable collector is ridden out. Zero fields
	// take the defaults below.
	Export ExportOptions
}

// ExportOptions bound how long a batch is retried, and so how long a
// collector outage (a rollout restarts it) can last before records are lost.
type ExportOptions struct {
	// RetryInitialInterval is the first wait before a retry; it doubles up to
	// RetryMaxInterval. Default 1s.
	RetryInitialInterval time.Duration
	// RetryMaxInterval caps the wait between retries. Default 10s.
	RetryMaxInterval time.Duration
	// Timeout bounds one batch, retries included: a batch still unsent after
	// it is dropped. Default 2m. The OTel SDK's own 30s would cut the
	// exporter's retries short.
	Timeout time.Duration
	// QueueSize is how many records wait while a batch is being retried;
	// beyond it records are dropped. Default 8192.
	QueueSize int
}

func (o ExportOptions) withDefaults() ExportOptions {
	if o.RetryInitialInterval <= 0 {
		o.RetryInitialInterval = time.Second
	}
	if o.RetryMaxInterval <= 0 {
		o.RetryMaxInterval = 10 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 8192
	}
	return o
}

// Logs is the process's log handler, and what must be flushed before the
// process exits.
type Logs struct {
	handler  slog.Handler
	provider *sdklog.LoggerProvider
}

// New builds the handler. Exporting is batched in the background, so a log
// store that is down or slow never makes a caller wait on it. A collector
// that is down is retried with backoff for up to Export.Timeout per batch,
// while later records queue (up to Export.QueueSize); only past those bounds
// are records dropped.
//
// With an endpoint, New also installs OTel's process-wide error handler, so
// it is for a process's one logger, built at startup.
func New(opts *Options) (*Logs, error) {
	// One key for both destinations, so a pseudonym means the same thing in
	// the local copy as in the log store.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("otellog: hash key: %w", err)
	}
	policy := func(next slog.Handler) (slog.Handler, error) {
		return logpolicy.New(next, &logpolicy.Config{Rules: logpolicy.EmailRules(), HashKey: key})
	}
	local, err := policy(&traceHandler{Handler: opts.Format.handler(opts.Output, &slog.HandlerOptions{Level: Level})})
	if err != nil {
		return nil, fmt.Errorf("otellog: log policy: %w", err)
	}
	if opts.OTLPEndpoint == "" {
		return &Logs{handler: local}, nil
	}
	// The exporter outlives any context New could be given: the process's own
	// is cancelled by the signal that stops it.
	export := opts.Export.withDefaults()
	exporter, err := otlploghttp.New(context.Background(),
		otlploghttp.WithEndpointURL(opts.OTLPEndpoint),
		otlploghttp.WithHTTPClient(&http.Client{Transport: &retryingTransport{next: http.DefaultTransport}}),
		otlploghttp.WithRetry(otlploghttp.RetryConfig{
			Enabled:         true,
			InitialInterval: export.RetryInitialInterval,
			MaxInterval:     export.RetryMaxInterval,
			MaxElapsedTime:  export.Timeout,
		}))
	if err != nil {
		return nil, fmt.Errorf("otellog: OTLP exporter for %q: %w", opts.OTLPEndpoint, err)
	}
	// Export failures go to the local copy alone. OTel's default reports them
	// through the log package, which slog.SetDefault routes back into this
	// handler -- and so into the exporter that is failing.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		_ = local.Handle(context.Background(), errorRecord(err))
	}))
	// The bridge's own name argument only sets the instrumentation scope; the
	// resource-level service.name has to be set explicitly, or exported
	// records are attributed to "unknown_service:<binary>".
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(resource.NewSchemaless(semconv.ServiceName(opts.ServiceName))),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter,
			sdklog.WithExportTimeout(export.Timeout),
			sdklog.WithMaxQueueSize(export.QueueSize))),
	)
	exported, err := policy(tieredOTelHandler(opts.ServiceName, provider))
	if err != nil {
		return nil, fmt.Errorf("otellog: log policy: %w", err)
	}
	return &Logs{handler: &fanout{handlers: []slog.Handler{local, exported}}, provider: provider}, nil
}

// Handler is the handler every component logs through.
func (l *Logs) Handler() slog.Handler { return l.handler }

// Close flushes what is waiting to be exported, within ctx. It is not the
// process's own context: that is cancelled by the signal that is stopping the
// process, which would fail the flush at once and drop the records that say
// why it stopped.
func (l *Logs) Close(ctx context.Context) error {
	if l.provider == nil {
		return nil
	}
	return l.provider.Shutdown(ctx)
}

// DevMailHandler is where a local-development mail sink writes whole
// messages, links that sign someone in included, to w. It is unfiltered,
// because the link is what the developer is there to read, and it is never
// exported to the log store. It shares its stream with the operational local
// copy, so wherever that is collected (pod logs) the message is there too:
// the log backend is for a developer's machine.
func DevMailHandler(w io.Writer, format Format) slog.Handler {
	return format.handler(w, nil)
}

func errorRecord(err error) slog.Record {
	r := slog.NewRecord(time.Now(), slog.LevelWarn, "log export failed", 0)
	r.AddAttrs(slog.Any("err", err))
	return r
}

// tieredOTelHandler is the exported copy. The tier rides on it only: it is
// the collector's routing key, and means nothing locally.
func tieredOTelHandler(serviceName string, provider *sdklog.LoggerProvider) slog.Handler {
	tiered := otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(provider)).
		WithAttrs([]slog.Attr{slog.String(TierAttribute, OperationalTier)})
	return &minLevel{Handler: &plainAttrs{Handler: tiered}, level: Level}
}
