package scriptedmodel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type sseEventForTest struct {
	event string
	data  map[string]any
}

// parseSSE reads a stream of "event:" + "data:" blocks.
func parseSSE(t *testing.T, raw []byte) []sseEventForTest {
	t.Helper()
	var events []sseEventForTest
	for _, block := range strings.Split(strings.TrimSpace(string(raw)), "\n\n") {
		lines := strings.Split(block, "\n")
		require.Len(t, lines, 2, block)
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &data))
		events = append(events, sseEventForTest{event: strings.TrimPrefix(lines[0], "event: "), data: data})
	}
	return events
}

// parseDataLines reads a stream of "data:" blocks; "[DONE]" stays a string.
func parseDataLines(t *testing.T, raw []byte) []any {
	t.Helper()
	var out []any
	for _, block := range strings.Split(strings.TrimSpace(string(raw)), "\n\n") {
		require.True(t, strings.HasPrefix(block, "data: "), block)
		payload := strings.TrimPrefix(block, "data: ")
		if payload == "[DONE]" {
			out = append(out, payload)
			continue
		}
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &v))
		out = append(out, v)
	}
	return out
}

func toJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var v map[string]any
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func mustParse(t *testing.T, a adapter, r *http.Request, body any) *Request {
	t.Helper()
	if r == nil {
		r = httptest.NewRequest(http.MethodPost, "/", nil)
	}
	req, err := a.parse(r, toJSON(t, body))
	require.NoError(t, err)
	return req
}

func encodeTurn(a adapter, turn *Turn, req *Request, id string) []byte {
	return bytes.Join(a.encode(turn, req, id), nil)
}

// path walks a decoded JSON value: string keys index objects, ints arrays.
func path(t *testing.T, v any, keys ...any) any {
	t.Helper()
	for _, k := range keys {
		switch key := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			require.True(t, ok, "not an object at %v: %v", key, v)
			v = m[key]
		case int:
			a, ok := v.([]any)
			require.True(t, ok, "not an array at %v: %v", key, v)
			require.Less(t, key, len(a))
			v = a[key]
		}
	}
	return v
}

func call(name string, input map[string]any) *ToolCall { return &ToolCall{Name: name, Input: input} }
