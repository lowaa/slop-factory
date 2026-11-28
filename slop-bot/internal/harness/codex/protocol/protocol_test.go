package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decoders are the generated types for the recorded messages the harness
// reads: notifications by method, responses by their fixture marker
// ("<method>", see ../testdata/README.md).
var decoders = map[string]func() any{
	MethodTurnStarted:                                func() any { return &TurnStartedNotification{} },
	MethodTurnCompleted:                              func() any { return &TurnCompletedNotification{} },
	MethodItemStarted:                                func() any { return &ItemStartedNotification{} },
	MethodItemCompleted:                              func() any { return &ItemCompletedNotification{} },
	MethodThreadTokenUsageUpdated:                    func() any { return &ThreadTokenUsageUpdatedNotification{} },
	MethodAccountRateLimitsUpdated:                   func() any { return &AccountRateLimitsUpdatedNotification{} },
	"<" + MethodInitialize + ">":                     func() any { return &InitializeResponse{} },
	"<" + MethodThreadStart + ">":                    func() any { return &ThreadStartResponse{} },
	"<" + MethodThreadResume + ">":                   func() any { return &ThreadResumeResponse{} },
	"<" + MethodTurnStart + ">":                      func() any { return &TurnStartResponse{} },
	"<" + MethodTurnSteer + ">":                      func() any { return &TurnSteerResponse{} },
	"<" + MethodTurnInterrupt + ">":                  func() any { return &struct{}{} },
	"<" + MethodThreadBackgroundTerminalsClean + ">": func() any { return &struct{}{} },
}

type envelope struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// TestRecordedMessagesDecodeStrictly decodes every recorded message the
// harness reads into its generated type, refusing unknown fields: a field
// Codex sends that the schema (or the generator) left out fails here, which
// is the check that the generated types match what Codex really says.
func TestRecordedMessagesDecodeStrictly(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("../testdata/*.jsonl")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	decoded := map[string]int{}
	for _, file := range files {
		f, err := os.Open(file) // #nosec G304 -- the package's own fixtures
		require.NoError(t, err)
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for n := 1; sc.Scan(); n++ {
			var env envelope
			require.NoError(t, json.Unmarshal(sc.Bytes(), &env))
			key, body := env.Method, env.Params
			if key == "" {
				key, body = env.ID, env.Result
			}
			mk, ok := decoders[key]
			if !ok || body == nil {
				continue
			}
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			assert.NoError(t, dec.Decode(mk()), "%s:%d %s", filepath.Base(file), n, key)
			decoded[key]++
		}
		_ = f.Close()
		require.NoError(t, sc.Err())
	}
	for key := range decoders {
		assert.NotZero(t, decoded[key], "no recording of %s", key)
	}
}

func TestAStringOrArrayUnionDecodesEitherForm(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`{"name":"t","output":"text"}`, `{"name":"t","output":[{"type":"input_text","text":"x"}]}`} {
		var out TurnToolOutput
		require.NoError(t, json.Unmarshal([]byte(in), &out))
		assert.NotEmpty(t, out.Output)
		b, err := json.Marshal(&out)
		require.NoError(t, err)
		assert.JSONEq(t, in, string(b), "and it round-trips")
	}
}
