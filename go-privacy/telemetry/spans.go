package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"
)

// A trace export is never forwarded, only rebuilt. Where logs are filtered
// (Policy) because their content is the app's to choose, the only
// span the browser may send is one for a failed API request, so the proxy
// reads exactly the fields of that span, checks each against what it can be,
// and writes a new document from them. Anything else in the payload -- events,
// links, trace state, a parent, a span name, extra attributes, resource
// attributes -- is not read and so cannot be carried through, whatever the
// collector's parser would make of it. Only the canonical lowerCamelCase
// field names are read: a snake_case or duplicated spelling is simply not
// seen.

// maxSpansPerExport bounds one request. A page reports a failed request at
// most a few times a minute (js-components/logging's SpanReporter).
const maxSpansPerExport = 20

// maxSpanDuration is generous for a request, and keeps a client's clock from
// producing a span that stretches across the store's time axis.
const maxSpanDuration = uint64(time.Hour)

var (
	traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDPattern  = regexp.MustCompile(`^[0-9a-f]{16}$`)
	errorCodeRe    = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	allZeroPattern = regexp.MustCompile(`^0+$`)
)

var httpMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

const (
	spanKindClient  = 3
	statusCodeError = 2
)

var errSpansShape = errors.New("telemetry: not an OTLP/JSON traces payload")

// SpanPolicy rebuilds a trace export from the failed-request spans in it.
// Immutable once built, so safe for concurrent use.
type SpanPolicy struct {
	serviceName string
}

// NewSpanPolicy forces serviceName as the service of whatever it forwards.
func NewSpanPolicy(serviceName string) (*SpanPolicy, error) {
	if serviceName == "" {
		return nil, errors.New("telemetry: span policy has no service name")
	}
	return &SpanPolicy{serviceName: serviceName}, nil
}

// SpanResult is what Apply let through, and what it did not.
type SpanResult struct {
	// Body is the OTLP/JSON TracesData to forward; nil when no span passed.
	Body               []byte
	Forwarded, Dropped int
}

// RouteKnown says whether a route template is one of this API's own. The
// browser's template comes from the same OpenAPI document, so a span for a
// route that does not exist is not from this app. Nil knows none, so no span
// is forwarded.
type RouteKnown func(route string) bool

type spansRewriter struct {
	serviceName string
	routeKnown  RouteKnown

	kept, dropped int
}

// ErrMalformedSpans is a body that is not an OTLP/JSON traces document.
var ErrMalformedSpans = errSpansShape

// Apply rebuilds body, which is not forwarded as sent.
func (p *SpanPolicy) Apply(body []byte, known RouteKnown) (*SpanResult, error) {
	r := &spansRewriter{serviceName: p.serviceName, routeKnown: known}
	out, err := r.rewriteSpans(body)
	if err != nil {
		return nil, err
	}
	return &SpanResult{Body: out, Forwarded: r.kept, Dropped: r.dropped}, nil
}

type failedRequestSpan struct {
	traceID, spanID string
	start, end      uint64
	method, route   string
	status          int64
	code            string
}

// rewriteSpans returns a TracesData document holding one span for each span of
// body that is a well-formed failed-request span, and an error if body is not
// a TracesData document at all.
func (r *spansRewriter) rewriteSpans(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("%w: %v", errSpansShape, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data after the JSON document", errSpansShape)
	}
	resourceSpans, ok := root["resourceSpans"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: missing resourceSpans array", errSpansShape)
	}

	var spans []any
	for _, rs := range resourceSpans {
		rsMap, _ := rs.(map[string]any)
		scopeSpans, _ := rsMap["scopeSpans"].([]any)
		for _, ss := range scopeSpans {
			ssMap, _ := ss.(map[string]any)
			items, _ := ssMap["spans"].([]any)
			for _, item := range items {
				if len(spans) >= maxSpansPerExport {
					r.dropped++
					continue
				}
				span, ok := r.readSpan(item)
				if !ok {
					r.dropped++
					continue
				}
				r.kept++
				spans = append(spans, span.build())
			}
		}
	}
	if len(spans) == 0 {
		return nil, nil
	}
	return json.Marshal(map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{"attributes": []any{stringKV("service.name", r.serviceName)}},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": r.serviceName},
				"spans": spans,
			}},
		}},
	})
}

func (r *spansRewriter) readSpan(raw any) (failedRequestSpan, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return failedRequestSpan{}, false
	}
	var s failedRequestSpan
	s.traceID, _ = m["traceId"].(string)
	s.spanID, _ = m["spanId"].(string)
	if !traceIDPattern.MatchString(s.traceID) || allZeroPattern.MatchString(s.traceID) ||
		!spanIDPattern.MatchString(s.spanID) || allZeroPattern.MatchString(s.spanID) {
		return s, false
	}
	// Only a failed request's span is wanted: the browser sends it as a client
	// span with an error status, so one that says otherwise is not from it.
	if !isNumber(m["kind"], spanKindClient) || !isNumber(asMap(m["status"])["code"], statusCodeError) {
		return s, false
	}
	var startOK, endOK bool
	s.start, startOK = nanos(m["startTimeUnixNano"])
	s.end, endOK = nanos(m["endTimeUnixNano"])
	if !startOK || !endOK || s.end < s.start || s.end-s.start > maxSpanDuration {
		return s, false
	}

	attrs, _ := m["attributes"].([]any)
	seen := map[string]bool{}
	for _, a := range attrs {
		kv, _ := a.(map[string]any)
		key, _ := kv["key"].(string)
		val, _ := kv["value"].(map[string]any)
		// A key given twice is ambiguous: no reading of it is safe to pick.
		if seen[key] {
			return s, false
		}
		seen[key] = true
		switch key {
		case "http.request.method":
			s.method, _ = val["stringValue"].(string)
		case "http.route":
			s.route, _ = val["stringValue"].(string)
		case "error.code":
			s.code, _ = val["stringValue"].(string)
		case "http.response.status_code":
			// Present but not a number is malformed, not a missing status:
			// a missing one means a network failure, which is forwarded.
			n, ok := intAttr(val["intValue"])
			if !ok {
				return s, false
			}
			s.status = n
		}
	}
	if !httpMethods[s.method] || r.routeKnown == nil || !r.routeKnown(s.route) {
		return s, false
	}
	// Optional, but an invalid one is dropped rather than kept: it would be
	// free text in the store.
	if s.code != "" && !errorCodeRe.MatchString(s.code) {
		s.code = ""
	}
	// A response that was a success, or no status code at all, is not a
	// failure (a network failure has no response, and so none).
	if s.status != 0 && (s.status < 400 || s.status > 599) {
		return s, false
	}
	return s, true
}

func (s failedRequestSpan) build() map[string]any {
	attrs := []any{stringKV("http.request.method", s.method), stringKV("http.route", s.route)}
	if s.status != 0 {
		attrs = append(attrs, map[string]any{"key": "http.response.status_code", "value": map[string]any{"intValue": strconv.FormatInt(s.status, 10)}})
	}
	if s.code != "" {
		attrs = append(attrs, stringKV("error.code", s.code))
	}
	return map[string]any{
		"traceId":           s.traceID,
		"spanId":            s.spanID,
		"name":              s.method + " " + s.route,
		"kind":              spanKindClient,
		"startTimeUnixNano": strconv.FormatUint(s.start, 10),
		"endTimeUnixNano":   strconv.FormatUint(s.end, 10),
		"attributes":        attrs,
		"status":            map[string]any{"code": statusCodeError},
	}
}

// nanos reads a unix-nanosecond timestamp, which OTLP/JSON writes as a string
// of digits.
func nanos(v any) (uint64, bool) {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case json.Number:
		s = t.String()
	default:
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// isNumber reports whether v is the JSON number want.
func isNumber(v any, want int64) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := n.Int64()
	return err == nil && i == want
}

func intAttr(v any) (int64, bool) {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case json.Number:
		s = t.String()
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func stringKV(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}
