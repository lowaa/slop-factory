package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

func newCtlServer(t *testing.T) *scriptedmodel.Server {
	t.Helper()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func ctl(t *testing.T, server *scriptedmodel.Server, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runCtl(t.Context(), append([]string{"-url", server.URL()}, args...), &out)
	return out.String(), err
}

func TestCtlLoadsReleasesAndReads(t *testing.T) {
	t.Parallel()
	server := newCtlServer(t)
	_, err := ctl(t, server, "load", writeScenario(t, `{"turns": [{"text": "late", "gate": "g"}, {"text": "two"}]}`))
	require.NoError(t, err)
	out, err := ctl(t, server, "remaining")
	require.NoError(t, err)
	assert.JSONEq(t, `{"remaining": 2}`, out)

	// An auxiliary request, then the gated one, which ctl lets through.
	auxiliary := `{"model": "m", "messages": [{"role": "user", "content": "title?"}]}`
	resp, err := http.Post(server.URL()+"/v1/messages", "application/json", strings.NewReader(auxiliary)) // #nosec G107 -- the test's own server
	require.NoError(t, err)
	_ = resp.Body.Close()
	answered := make(chan int, 1)
	go func() {
		body := `{"model": "m", "messages": [{"role": "user", "content": "go"}], "tools": [{"name": "bash"}]}`
		resp, err := http.Post(server.URL()+"/v1/messages", "application/json", strings.NewReader(body)) // #nosec G107 -- the test's own server
		if err != nil {
			answered <- 0
			return
		}
		_ = resp.Body.Close()
		answered <- resp.StatusCode
	}()
	_, err = ctl(t, server, "held", "g")
	require.NoError(t, err)
	_, err = ctl(t, server, "release", "g")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, <-answered)

	out, err = ctl(t, server, "requests")
	require.NoError(t, err)
	var all []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &all))
	assert.Len(t, all, 2)
	out, err = ctl(t, server, "requests", "-conversation")
	require.NoError(t, err)
	var conversation []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &conversation))
	require.Len(t, conversation, 1)
	assert.Equal(t, false, conversation[0]["auxiliary"])

	out, err = ctl(t, server, "errors")
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, out)
	_, err = ctl(t, server, "reset")
	require.NoError(t, err)
	assert.Zero(t, server.Remaining())
}

func TestCtlRefusesBadUsage(t *testing.T) {
	t.Parallel()
	server := newCtlServer(t)
	for _, args := range [][]string{{}, {"nope"}, {"load"}, {"release", "a", "b"}, {"requests", "-x"}, {"reset", "now"}, {"remaining", "x"}, {"requests", "extra"}} {
		_, err := ctl(t, server, args...)
		require.ErrorIs(t, err, errCtlUsage, args)
	}
}
