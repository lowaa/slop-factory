package telemetry_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/telemetry"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
	// A route of the test mux (the proxy checks templates against the API it is
	// registered on).
	knownRoute = "/api/otel/v1/logs"
)

func spanStrAttr(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

func goodSpan() map[string]any {
	return map[string]any{
		"traceId":           testTraceID,
		"spanId":            testSpanID,
		"name":              "POST " + knownRoute,
		"kind":              3,
		"startTimeUnixNano": "1700000000000000000",
		"endTimeUnixNano":   "1700000000250000000",
		"attributes": []any{
			spanStrAttr("http.request.method", "POST"),
			spanStrAttr("http.route", knownRoute),
			map[string]any{"key": "http.response.status_code", "value": map[string]any{"intValue": "500"}},
			spanStrAttr("error.code", "internal_error"),
		},
		"status": map[string]any{"code": 2},
	}
}

func tracesPayload(spans ...map[string]any) string {
	items := make([]any, len(spans))
	for i, s := range spans {
		items[i] = s
	}
	body, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{spanStrAttr("service.name", "example-api"), spanStrAttr("user.email", "jane@example.com")}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "x"}, "spans": items}},
	}}})
	return string(body)
}

// exportTraces applies the policy to payload and returns what it would
// forward (nil when nothing passes) and the error.
func exportTraces(t *testing.T, payload string) (error, map[string]any) {
	t.Helper()
	p, err := telemetry.NewSpanPolicy("example-frontend")
	require.NoError(t, err)
	res, err := p.Apply([]byte(payload), func(route string) bool { return route == knownRoute })
	if err != nil {
		return err, nil
	}
	if res.Body == nil {
		return nil, nil
	}
	var got map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &got))
	return nil, got
}

func forwardedSpans(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	require.NotNil(t, doc, "nothing reached the collector")
	rs := doc["resourceSpans"].([]any)
	require.Len(t, rs, 1)
	ss := rs[0].(map[string]any)["scopeSpans"].([]any)
	require.Len(t, ss, 1)
	var out []map[string]any
	for _, s := range ss[0].(map[string]any)["spans"].([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

func attrMap(span map[string]any) map[string]any {
	m := map[string]any{}
	for _, a := range span["attributes"].([]any) {
		kv := a.(map[string]any)
		v := kv["value"].(map[string]any)
		for _, val := range v {
			m[kv["key"].(string)] = val
		}
	}
	return m
}

func TestSpans_ForwardsAFailedRequestSpan(t *testing.T) {
	err, doc := exportTraces(t, tracesPayload(goodSpan()))

	require.NoError(t, err)
	spans := forwardedSpans(t, doc)
	require.Len(t, spans, 1)
	assert.Equal(t, testTraceID, spans[0]["traceId"])
	assert.Equal(t, testSpanID, spans[0]["spanId"])
	assert.Equal(t, "POST "+knownRoute, spans[0]["name"])
	assert.Equal(t, map[string]any{
		"http.request.method":       "POST",
		"http.route":                knownRoute,
		"http.response.status_code": "500",
		"error.code":                "internal_error",
	}, attrMap(spans[0]))
	assert.EqualValues(t, 3, spans[0]["kind"])
	assert.Equal(t, "1700000000000000000", spans[0]["startTimeUnixNano"])
	assert.Equal(t, "1700000000250000000", spans[0]["endTimeUnixNano"])
	assert.Equal(t, map[string]any{"code": float64(2)}, spans[0]["status"])
}

func TestSpans_ForcesServiceNameAndDropsResourceAttributes(t *testing.T) {
	_, doc := exportTraces(t, tracesPayload(goodSpan()))

	resource := doc["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	assert.Equal(t, []any{spanStrAttr("service.name", "example-frontend")}, resource["attributes"])
	scope := doc["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["scope"]
	assert.Equal(t, map[string]any{"name": "example-frontend"}, scope, "the caller's scope name is not kept")
	assert.NotContains(t, mustJSON(t, doc), "jane@example.com")
}

func TestSpans_RebuildsSpanFromAllowlistedFieldsOnly(t *testing.T) {
	span := goodSpan()
	span["name"] = "Jane Doe hemoglobin 13.5"
	span["parentSpanId"] = "1111111111111111"
	span["traceState"] = "vendor=Jane"
	span["events"] = []any{map[string]any{"name": "Jane Doe"}}
	span["links"] = []any{map[string]any{"traceId": testTraceID}}
	span["trace_id"] = "ffffffffffffffffffffffffffffffff"
	span["attributes"] = append(span["attributes"].([]any),
		spanStrAttr("user.id", "u-123"),
		spanStrAttr("url.full", "https://app.test/api/things/42?name=Jane"),
		spanStrAttr("note", "Jane Doe"),
	)

	_, doc := exportTraces(t, tracesPayload(span))

	spans := forwardedSpans(t, doc)
	require.Len(t, spans, 1)
	out := mustJSON(t, doc)
	for _, leaked := range []string{"Jane", "13.5", "u-123", "1111111111111111", "ffffffff", "vendor", "events", "links", "parentSpanId", "traceState", "/42"} {
		assert.NotContains(t, out, leaked)
	}
	assert.Equal(t, "POST "+knownRoute, spans[0]["name"], "the name is rebuilt from the method and route")
	assert.Len(t, attrMap(spans[0]), 4)
}

func TestSpans_DropsSpansThatAreNotFailedRequestSpans(t *testing.T) {
	mutate := func(name string, f func(map[string]any)) (string, map[string]any) {
		s := goodSpan()
		f(s)
		return name, s
	}
	setAttr := func(key, val string) func(map[string]any) {
		return func(s map[string]any) {
			attrs := s["attributes"].([]any)
			for i, a := range attrs {
				if a.(map[string]any)["key"] == key {
					attrs[i] = spanStrAttr(key, val)
				}
			}
		}
	}
	cases := map[string]map[string]any{}
	for _, c := range []func() (string, map[string]any){
		func() (string, map[string]any) {
			return mutate("short trace id", func(s map[string]any) { s["traceId"] = "abc" })
		},
		func() (string, map[string]any) {
			return mutate("zero trace id", func(s map[string]any) { s["traceId"] = strings.Repeat("0", 32) })
		},
		func() (string, map[string]any) {
			return mutate("uppercase span id", func(s map[string]any) { s["spanId"] = "00F067AA0BA902B7" })
		},
		func() (string, map[string]any) {
			return mutate("end before start", func(s map[string]any) { s["endTimeUnixNano"] = "1" })
		},
		func() (string, map[string]any) {
			return mutate("span of a day", func(s map[string]any) { s["endTimeUnixNano"] = "1700086400000000000" })
		},
		func() (string, map[string]any) {
			return mutate("unknown route", setAttr("http.route", "/api/not-ours"))
		},
		func() (string, map[string]any) {
			return mutate("a url, not a template", setAttr("http.route", "https://app.test/api/things/42?x=1"))
		},
		func() (string, map[string]any) {
			return mutate("a success status code", func(s map[string]any) {
				s["attributes"] = append(s["attributes"].([]any)[:2], map[string]any{"key": "http.response.status_code", "value": map[string]any{"intValue": "200"}})
			})
		},
		func() (string, map[string]any) {
			return mutate("no error status", func(s map[string]any) { delete(s, "status") })
		},
		func() (string, map[string]any) {
			return mutate("an OK span status", func(s map[string]any) { s["status"] = map[string]any{"code": 1} })
		},
		func() (string, map[string]any) {
			return mutate("a server span kind", func(s map[string]any) { s["kind"] = 2 })
		},
		func() (string, map[string]any) { return mutate("odd method", setAttr("http.request.method", "BREW")) },
		func() (string, map[string]any) {
			return mutate("a status code that is text", func(s map[string]any) {
				s["attributes"] = append(s["attributes"].([]any)[:2], spanStrAttr("http.response.status_code", "500"))
			})
		},
		func() (string, map[string]any) {
			return mutate("duplicate key", func(s map[string]any) {
				s["attributes"] = append(s["attributes"].([]any), spanStrAttr("http.route", knownRoute))
			})
		},
	} {
		name, s := c()
		cases[name] = s
	}
	for name, span := range cases {
		t.Run(name, func(t *testing.T) {
			err, doc := exportTraces(t, tracesPayload(span))
			assert.NoError(t, err)
			assert.Nil(t, doc, "a span that is not a well-formed failed-request span must not reach the collector")
		})
	}
}

func TestSpans_FreeTextErrorCodeIsDropped(t *testing.T) {
	span := goodSpan()
	for i, a := range span["attributes"].([]any) {
		if a.(map[string]any)["key"] == "error.code" {
			span["attributes"].([]any)[i] = spanStrAttr("error.code", "Jane Doe, 13.5 g/dL")
		}
	}

	_, doc := exportTraces(t, tracesPayload(span))

	spans := forwardedSpans(t, doc)
	assert.NotContains(t, attrMap(spans[0]), "error.code")
	assert.NotContains(t, mustJSON(t, doc), "Jane")
}

func TestSpans_CapsSpansPerExport(t *testing.T) {
	var spans []map[string]any
	for i := 0; i < 30; i++ {
		spans = append(spans, goodSpan())
	}

	_, doc := exportTraces(t, tracesPayload(spans...))

	assert.Len(t, forwardedSpans(t, doc), 20)
}

func TestSpans_MalformedPayloadRejected(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":   "nope",
		"not traces": `{"resourceLogs":[]}`,
		"trailing":   tracesPayload(goodSpan()) + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			err, doc := exportTraces(t, payload)
			assert.ErrorIs(t, err, telemetry.ErrMalformedSpans)
			assert.Nil(t, doc)
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestSpans_NetworkFailureSpanWithoutStatusCodeIsForwarded(t *testing.T) {
	span := goodSpan()
	span["attributes"] = span["attributes"].([]any)[:2]

	err, doc := exportTraces(t, tracesPayload(span))

	require.NoError(t, err)
	assert.Len(t, forwardedSpans(t, doc), 1)
}

func TestSpans_ResultCounts(t *testing.T) {
	p, err := telemetry.NewSpanPolicy("svc")
	require.NoError(t, err)
	known := func(route string) bool { return route == knownRoute }

	var many []map[string]any
	for i := 0; i < 30; i++ {
		many = append(many, goodSpan())
	}
	res, err := p.Apply([]byte(tracesPayload(many...)), known)
	require.NoError(t, err)
	assert.Equal(t, 20, res.Forwarded)
	assert.Equal(t, 10, res.Dropped)

	bad := goodSpan()
	bad["traceId"] = "abc"
	res, err = p.Apply([]byte(tracesPayload(goodSpan(), bad)), known)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Forwarded)
	assert.Equal(t, 1, res.Dropped)

	res, err = p.Apply([]byte(tracesPayload(goodSpan())), nil)
	require.NoError(t, err)
	assert.Nil(t, res.Body, "with no route known, no span is")
}
