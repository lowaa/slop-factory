// Package otelproxy is the browser's only path to an app's log and trace
// stores for the frontend's own OTLP/JSON export. It is an
// unauthenticated-capable write reachable from any page, so it trusts nothing
// in the request:
//
//   - rate limited per client address (go-auth's clientip, so only the
//     configured trusted proxies' X-Forwarded-For is believed), with a
//     bounded key table;
//   - the body is size limited before it is read, and the number of
//     in-flight forwards is capped, so a flood can't be amplified into the
//     store;
//   - nothing is passed through: telemetry.Policy rebuilds the payload from
//     the records and attributes its tiers permit, with the service name
//     forced, so a caller can neither forge another service's logs nor
//     smuggle attributes, resource fields or timestamps.
//
// The app supplies the policies, who the caller is, and the route's
// authentication metadata; the rules (go-privacy/README.md §8) are not
// per-app.
package otelproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-privacy/telemetry"
)

// Encoding is how a filtered export is sent to the store.
type Encoding int

const (
	// JSON forwards the filtered OTLP/JSON as it is (an OpenTelemetry
	// collector's OTLP/HTTP receiver takes it).
	JSON Encoding = iota
	// Protobuf re-encodes it as OTLP protobuf, the only encoding VictoriaLogs
	// accepts on its OTLP endpoint, which the browser's JSON exporter can't
	// send. protojson refuses anything that isn't a valid OTLP message, so
	// it is a second check on the filter's output.
	Protobuf
)

const (
	// A batch of console-level error records is a few KiB; this bounds the
	// worst case one rate-limited client can push through.
	DefaultMaxBodyBytes = 128 * 1024

	forwardTimeout      = 5 * time.Second
	DefaultMaxForwards  = 16
	maxCollectorReply   = 64 * 1024
	retryAfterSeconds   = "60"
	unknownClient       = "unknown"
	logsOperationID     = "exportOtelLogs"
	tracesOperationID   = "exportOtelTraces"
	protobufContentType = "application/x-protobuf"
	jsonContentType     = "application/json"
)

// Options configure a Handler.
type Options struct {
	// LogsEndpoint is the store's full OTLP/HTTP logs URL. Empty accepts and
	// discards, as the app's own export does when none is configured.
	LogsEndpoint string
	// TracesEndpoint is the same for traces.
	TracesEndpoint string
	// Encoding is how logs are sent; traces are always JSON.
	Encoding Encoding

	Logs *telemetry.Policy
	// Spans enables the traces route.
	Spans *telemetry.SpanPolicy

	Limit *ratelimit.Config
	// MaxBodyBytes defaults to DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// MaxForwards is how many forwards may be in flight; default
	// DefaultMaxForwards.
	MaxForwards int

	// Identify says who is sending, from what the app's authentication left
	// on the context. The payload can't be trusted to say.
	Identify func(ctx context.Context) *telemetry.Caller
	// ConsentUnknownError is what a batch gets when the sender's credential
	// was refused (Caller.ConsentUnknown) and the batch holds a record that
	// needs consent: its grant would be read for the wrong person. Nil drops
	// those records and goes on with the rest.
	ConsentUnknownError func() error

	// Client posts to the endpoints; nil means a client with a timeout that
	// does not follow redirects.
	Client *http.Client
	Logger slog.Handler
}

// Handler forwards policy-filtered exports to the stores.
type Handler struct {
	opts     Options
	limiter  *ratelimit.Limiter
	client   *http.Client
	logger   *slog.Logger
	inflight chan struct{}
}

// NewHandler checks opts, so a handler that can never work is found at
// startup.
func NewHandler(opts *Options) (*Handler, error) {
	if opts.Logs == nil || opts.Identify == nil || opts.Limit == nil {
		return nil, errors.New("otelproxy: needs a logs policy, an Identify and a rate limit")
	}
	o := *opts
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if o.MaxForwards <= 0 {
		o.MaxForwards = DefaultMaxForwards
	}
	client := o.Client
	if client == nil {
		client = &http.Client{
			Timeout:       forwardTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	logHandler := o.Logger
	if logHandler == nil {
		logHandler = slog.DiscardHandler
	}
	return &Handler{
		opts:     o,
		limiter:  ratelimit.New(o.Limit),
		client:   client,
		logger:   slog.New(logHandler),
		inflight: make(chan struct{}, o.MaxForwards),
	}, nil
}

// Routes says where the handler is mounted.
type Routes struct {
	LogsPath string
	// TracesPath is mounted only when the handler has a span policy.
	TracesPath string
	// Metadata is the app's authentication marker for the routes (the
	// signed-in user is wanted when there is one, but none is required).
	Metadata map[string]any
	// Hidden keeps the routes out of the OpenAPI document: they are the OTel
	// exporter's wire format, not part of a typed client.
	Hidden bool
}

// Register mounts the export routes on api.
func (h *Handler) Register(api huma.API, r *Routes) {
	huma.Register(api, huma.Operation{
		OperationID:  logsOperationID,
		Method:       http.MethodPost,
		Path:         r.LogsPath,
		Summary:      "Forward the frontend's OTel log export to the collector",
		MaxBodyBytes: h.opts.MaxBodyBytes,
		Hidden:       r.Hidden,
		Metadata:     r.Metadata,
		Middlewares:  huma.Middlewares{h.rateLimit(api)},
	}, h.exportLogs)
	if h.opts.Spans == nil {
		return
	}
	known := func(route string) bool {
		_, ok := api.OpenAPI().Paths[route]
		return ok
	}
	huma.Register(api, huma.Operation{
		OperationID:  tracesOperationID,
		Method:       http.MethodPost,
		Path:         r.TracesPath,
		Summary:      "Forward the frontend's spans for failed requests to the collector",
		MaxBodyBytes: h.opts.MaxBodyBytes,
		Hidden:       r.Hidden,
		Metadata:     r.Metadata,
		Middlewares:  huma.Middlewares{h.rateLimit(api)},
	}, func(ctx context.Context, in *ExportInput) (*ExportOutput, error) {
		return h.exportTraces(ctx, in, known)
	})
}

func (h *Handler) rateLimit(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		// A request whose address can't be worked out shares one allowance
		// rather than having none.
		key := unknownClient
		if addr := clientip.FromContext(ctx.Context()); addr.IsValid() {
			key = clientip.RateKey(addr)
		}
		if !h.limiter.Allow(key) {
			ctx.SetHeader("Retry-After", retryAfterSeconds)
			_ = huma.WriteErr(api, ctx, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next(ctx)
	}
}

// ExportInput's RawBody carries no contentType tag on purpose: tagging it
// application/json makes huma validate the parsed body against a binary
// string schema and 422 every request. The body is parsed by hand in the
// policies instead.
type ExportInput struct {
	RawBody []byte
}

type ExportOutput struct{}

func (h *Handler) caller(ctx context.Context) *telemetry.Caller {
	if c := h.opts.Identify(ctx); c != nil {
		return c
	}
	return &telemetry.Caller{}
}

func (h *Handler) exportLogs(ctx context.Context, in *ExportInput) (*ExportOutput, error) {
	caller := h.caller(ctx)
	res, err := h.opts.Logs.Apply(ctx, in.RawBody, caller)
	if err != nil {
		if errors.Is(err, telemetry.ErrMalformed) {
			return nil, huma.Error400BadRequest("malformed OTLP log payload")
		}
		h.logger.Error("otelproxy: apply policy", "err", err)
		return nil, huma.Error500InternalServerError("otel export failed")
	}
	if caller.ConsentUnknown && res.ConsentGated > 0 && h.opts.ConsentUnknownError != nil {
		return nil, h.opts.ConsentUnknownError()
	}
	// Counts only, never a record's content or tier text: it is the
	// caller's, and a log line must not be a way to write to our logs.
	if res.StrippedAttributes+res.TotalDropped() > 0 {
		h.logger.Debug("otelproxy: filtered export",
			"forwarded", res.Forwarded, "stripped_attributes", res.StrippedAttributes,
			"dropped_no_tier", res.DroppedNoTier, "dropped_unknown_tier", res.DroppedUnknownTier,
			"dropped_severity", res.DroppedSeverity, "dropped_consent", res.DroppedConsent,
			"dropped_malformed", res.DroppedMalformed, "dropped_over_limit", res.DroppedOverLimit,
			"dropped_mislabelled", res.DroppedMislabelled)
	}
	if res.Body == nil || h.opts.LogsEndpoint == "" {
		return &ExportOutput{}, nil
	}
	return h.forward(ctx, h.opts.LogsEndpoint, res.Body, func() proto.Message { return new(collogs.ExportLogsServiceRequest) })
}

func (h *Handler) exportTraces(ctx context.Context, in *ExportInput, known telemetry.RouteKnown) (*ExportOutput, error) {
	res, err := h.opts.Spans.Apply(in.RawBody, known)
	if err != nil {
		if errors.Is(err, telemetry.ErrMalformedSpans) {
			return nil, huma.Error400BadRequest("malformed OTLP trace payload")
		}
		h.logger.Error("otelproxy: apply span policy", "err", err)
		return nil, huma.Error500InternalServerError("otel export failed")
	}
	if res.Dropped > 0 {
		h.logger.Debug("otelproxy: dropped spans", "dropped", res.Dropped, "forwarded", res.Forwarded)
	}
	if res.Body == nil || h.opts.TracesEndpoint == "" {
		return &ExportOutput{}, nil
	}
	// Always JSON: OTLP/JSON writes trace and span ids as hex, which
	// protojson would read as base64, so the protobuf re-encoding is for logs
	// only.
	return h.forward(ctx, h.opts.TracesEndpoint, res.Body, nil)
}

func (h *Handler) forward(ctx context.Context, endpoint string, jsonBody []byte, newMsg func() proto.Message) (*ExportOutput, error) {
	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	default:
		return nil, huma.Error503ServiceUnavailable("otel export busy")
	}
	if err := h.post(ctx, endpoint, jsonBody, newMsg); err != nil {
		h.logger.Error("otelproxy: forward to store", "err", err)
		return nil, huma.Error502BadGateway("otel export failed")
	}
	return &ExportOutput{}, nil
}

func (h *Handler) post(ctx context.Context, endpoint string, jsonBody []byte, newMsg func() proto.Message) error {
	body, contentType := jsonBody, jsonContentType
	if h.opts.Encoding == Protobuf && newMsg != nil {
		msg := newMsg()
		if err := protojson.Unmarshal(jsonBody, msg); err != nil {
			return err
		}
		var err error
		if body, err = proto.Marshal(msg); err != nil {
			return err
		}
		contentType = protobufContentType
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCollectorReply))
	if resp.StatusCode >= 300 {
		return errors.New("store answered " + resp.Status)
	}
	return nil
}
