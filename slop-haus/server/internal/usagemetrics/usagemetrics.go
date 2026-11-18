// Package usagemetrics counts feature usage, in aggregate and for everyone:
// API requests by operation, and agent MCP tool calls by tool. They are
// internal metrics for supporting and improving slop-haus.
//
// Nothing here may identify a person, tenant or agent. That is what lets it
// run without consent, so it is enforced structurally: the arguments of
// record and ToolCall are the only label values there are, each drawn from a
// closed set (an operation id or tool name is one the code registered, never
// a URL or a request's own text; anything else is OperationUnmatched or
// ToolUnknown), and AllowedLabels is the one list a test holds every exported
// series to.
package usagemetrics

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	RequestsMetric     = "slophaus_operation_requests_total"
	ToolCallsMetric    = "slophaus_mcp_tool_calls_total"
	ToolDurationMetric = "slophaus_mcp_tool_call_duration_seconds"

	LabelOperation = "operation"
	LabelOutcome   = "outcome"
	LabelClient    = "client"
	LabelTool      = "tool"
	LabelAgentKind = "agent_kind"

	// OperationUnmatched is every request that reached no huma operation: a
	// 404 or 405 from the mux, the MCP and OAuth routes, a scanner. One
	// bounded value, so requests for made-up paths cannot make series.
	OperationUnmatched = "unmatched"
	// ToolUnknown is a call to a tool that is not registered.
	ToolUnknown = "unknown"
	// KindOther is an agent kind this package has no name for.
	KindOther = "other"
)

// AllowedLabels is every label name a series of this package may carry.
var AllowedLabels = []string{LabelOperation, LabelOutcome, LabelClient, LabelTool, LabelAgentKind}

// probePaths are the plain mux routes that say how the api is: the kubelet
// calls /healthz every few seconds, which would otherwise swamp the
// "unmatched" count that exists to show scanners and broken links.
var probePaths = map[string]bool{"/healthz": true, "/api/version": true}

// Client is how a request authenticated, which is all it says about who made
// it: a signed-in browser, a script holding a token, or neither.
type Client string

const (
	ClientBrowser   Client = "browser"
	ClientBearer    Client = "bearer"
	ClientAnonymous Client = "anonymous"
)

// Outcome is how an MCP tool call ended.
type Outcome string

const (
	OutcomeOK Outcome = "ok"
	// OutcomeRefused is a policy or secret refusal: the call was understood
	// and not allowed.
	OutcomeRefused Outcome = "refused"
	OutcomeError   Outcome = "error"
	// OutcomeInvalid is a call whose arguments the tool schema rejected.
	OutcomeInvalid Outcome = "invalid"
)

var outcomes = []Outcome{OutcomeOK, OutcomeRefused, OutcomeError, OutcomeInvalid}

// agentKinds are the agent kinds (agent.Kind) that call tools.
var agentKinds = []string{"worker", "steward", "conductor", "console", "supervisor", KindOther}

// Recorder counts requests and tool calls. Its zero value is not usable; use
// New.
type Recorder struct {
	requests  *prometheus.CounterVec
	tools     *prometheus.CounterVec
	durations *prometheus.HistogramVec

	mu         sync.RWMutex
	operations map[string]bool
	toolNames  map[string]bool
}

func New() *Recorder {
	return &Recorder{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: RequestsMetric,
			Help: "API requests by operation, outcome class and client type; no identifiers.",
		}, []string{LabelOperation, LabelOutcome, LabelClient}),
		tools: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: ToolCallsMetric,
			Help: "Agent MCP tool calls by tool, outcome and kind of agent; no identifiers.",
		}, []string{LabelTool, LabelOutcome, LabelAgentKind}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    ToolDurationMetric,
			Help:    "Agent MCP tool call durations by tool; no identifiers.",
			Buckets: prometheus.DefBuckets,
		}, []string{LabelTool}),
		operations: map[string]bool{},
		toolNames:  map[string]bool{},
	}
}

// Collectors are what to register for /metrics to serve the series.
func (r *Recorder) Collectors() []prometheus.Collector {
	return []prometheus.Collector{r.requests, r.tools, r.durations}
}

// SetOperations names the operations the api has registered, which are the
// only operation label values there will be. Each starts a series at zero
// (so a dashboard can tell an operation never called from one that does not
// exist, and the first call shows in increase()), and a name outside the set
// is counted as OperationUnmatched. Call it before serving.
func (r *Recorder) SetOperations(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		r.operations[id] = true
		r.requests.WithLabelValues(id, "2xx", string(ClientBrowser))
	}
}

// Known registers a tool name, and starts its series at zero as
// SetOperations does. Only registered tools are counted under their own name.
func (r *Recorder) Known(tool string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolNames[tool] = true
	r.durations.WithLabelValues(tool)
	for _, kind := range agentKinds {
		r.tools.WithLabelValues(tool, string(OutcomeOK), kind)
	}
}

// ToolCall counts one tool call. A tool or kind the recorder does not know is
// counted under ToolUnknown or KindOther.
func (r *Recorder) ToolCall(tool, agentKind string, outcome Outcome, took time.Duration) {
	r.mu.RLock()
	known := r.toolNames[tool]
	r.mu.RUnlock()
	if !known {
		tool = ToolUnknown
	}
	if !slices.Contains(agentKinds, agentKind) {
		agentKind = KindOther
	}
	if !slices.Contains(outcomes, outcome) {
		outcome = OutcomeError
	}
	r.tools.WithLabelValues(tool, string(outcome), agentKind).Inc()
	if known {
		r.durations.WithLabelValues(tool).Observe(took.Seconds())
	}
}

// record is unexported on purpose: the only caller is Middleware, whose
// operation is the matched route's id or OperationUnmatched, and whose client
// is one of the Client constants. Exporting it would let any caller put a URL
// or an id into a label.
func (r *Recorder) record(operation string, status int, client Client) {
	r.mu.RLock()
	known := r.operations[operation]
	r.mu.RUnlock()
	if !known {
		operation = OperationUnmatched
	}
	r.requests.WithLabelValues(operation, outcomeClass(status), string(client)).Inc()
}

func outcomeClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	}
	return "other"
}

func clientOf(r *http.Request) Client {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		return ClientBearer
	}
	if len(r.Cookies()) > 0 {
		return ClientBrowser
	}
	return ClientAnonymous
}

// slot carries the matched operation from huma's middleware, which knows it,
// out to the HTTP middleware, which sees the final status and every request,
// matched or not.
type slot struct{ operation string }

type slotKey struct{}

// statusWriter remembers the status written. Unwrap lets
// http.ResponseController reach the real writer's Flush and the like.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	// An informational response (103 Early Hints) is followed by the real
	// one; 101 is final, as the connection no longer speaks HTTP.
	if w.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Flush keeps the wrapper an http.Flusher whenever the writer underneath is,
// for code that type-asserts instead of using http.ResponseController.
func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware counts every request that passes through next, the ones no
// operation matched included. It must be outermost, so that a request another
// middleware refuses or a handler panics on is still counted (a panic reaching
// it counts as a 500).
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && probePaths[req.URL.Path] {
			next.ServeHTTP(w, req)
			return
		}
		s := &slot{operation: OperationUnmatched}
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			if rec := recover(); rec != nil {
				r.record(s.operation, http.StatusInternalServerError, clientOf(req))
				panic(rec)
			}
			r.record(s.operation, status, clientOf(req))
		}()
		next.ServeHTTP(sw, req.WithContext(contextWithSlot(req, s)))
	})
}

// Operation is the huma middleware that names the matched operation for
// Middleware. Register it before any other, so a request that a later one
// refuses is still attributed to the operation it was for.
func Operation(ctx huma.Context, next func(huma.Context)) {
	if s, ok := ctx.Context().Value(slotKey{}).(*slot); ok {
		if op := ctx.Operation(); op != nil && op.OperationID != "" {
			s.operation = op.OperationID
		}
	}
	next(ctx)
}

func contextWithSlot(req *http.Request, s *slot) context.Context {
	return context.WithValue(req.Context(), slotKey{}, s)
}
