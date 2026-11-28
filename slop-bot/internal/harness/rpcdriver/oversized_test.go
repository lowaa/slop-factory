package rpcdriver

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// endCodec's turn start is a "start" line, and its turn end
// {"type":"end",...}, which may be too long to read.
type endCodec struct{ lineCodec }

func (endCodec) Decode(line []byte, _ *Wire) ([]harness.Event, error) {
	if string(line) == "start" {
		return []harness.Event{{Kind: harness.TurnStarted}}, nil
	}
	return nil, nil
}

func turnKinds(c *Conversation) []harness.EventKind {
	var out []harness.EventKind
	for ev := range c.Events() {
		if ev.Kind == harness.TurnStarted || ev.Kind == harness.TurnEnded {
			out = append(out, ev.Kind)
		}
	}
	return out
}

func (endCodec) Oversized(prefix []byte, _ *Wire) ([]harness.Event, error) {
	var rec struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(PrefixObject(prefix), &rec); err != nil {
		return nil, err
	}
	if rec.Type != "end" {
		return nil, nil
	}
	return []harness.Event{{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}}, nil
}

// A turn end too long to read whole (a result that repeats a huge output)
// still ends the turn: the codec gets the line's start.
func TestAnOversizedTurnEndStillEndsTheTurn(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: endCodec{}, MaxLine: 100,
		Spec: &harness.LaunchSpec{Path: sh, Args: []string{"-c",
			`echo ready; echo start; printf '{"type":"end","result":"'; i=0; while [ $i -lt 50 ]; do printf xxxxxxxx; i=$((i+1)); done; echo '"}'`},
			Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.proc.Kill() })

	assert.Equal(t, []harness.EventKind{harness.TurnStarted, harness.TurnEnded}, turnKinds(c))
}

func TestPrefixObjectIsTheWholeFieldsBeforeTheCut(t *testing.T) {
	t.Parallel()
	got := PrefixObject([]byte(`{"type":"result","subtype":"success","n":{"a":1},"result":"very lo`))
	assert.JSONEq(t, `{"type":"result","subtype":"success","n":{"a":1}}`, string(got))
	assert.Nil(t, PrefixObject([]byte(`not json`)))
	assert.Nil(t, PrefixObject([]byte(`{"result":"cut`)))
}
