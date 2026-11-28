package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// agentToken records a live agent t1-aaaaaa on ticket 1 and returns its
// token.
func agentToken(t *testing.T, d *Daemon) string {
	t.Helper()
	token, err := control.NewToken()
	require.NoError(t, err)
	_, err = d.state.Agents.Create(context.Background(), &state.NewAgent{
		ID: "t1-aaaaaa", Ticket: "1", Harness: "claude-code", Driver: "stream-json",
		Pool: state.PoolNormal, State: state.StateStarting, TokenHash: control.HashToken(token),
	})
	require.NoError(t, err)
	return token
}

// rawCall sends body to method path as token ("" for the user), and returns
// the response.
func rawCall(t *testing.T, p *paths.Paths, token, method, path, body string) *http.Response {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: control.Dialer(p.Socket())}}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://slop-bot"+path, bytes.NewBufferString(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// The user's routes refuse an agent's token: each would show it other
// agents' work (their questions, other tickets' discussions, transcripts)
// or let it act as the user.
func TestUserOnlyRoutesRefuseAnAgent(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	token := agentToken(t, d)
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/asks", ``},
		{http.MethodGet, "/v1/discussions", ``},
		{http.MethodGet, "/v1/inbox?log", ``},
		{http.MethodPost, "/v1/autonomy", `{"enabled":true}`},
		{http.MethodGet, "/v1/transcript/t1-aaaaaa", ``},
		{http.MethodGet, "/v1/watch/t1-aaaaaa", ``},
		{http.MethodGet, "/v1/worktrees", ``},
		{http.MethodPost, "/v1/worktrees/1/clean", `{}`},
		{http.MethodPost, "/v1/agents/t1-aaaaaa/attach", `{"holder":"h"}`},
		{http.MethodPost, "/v1/agents/t1-aaaaaa/attach/launched", `{"holder":"h","pid":2}`},
		{http.MethodPost, "/v1/agents/t1-aaaaaa/handback", `{"holder":"h"}`},
		{http.MethodPost, "/v1/agents/t1-aaaaaa/detach", ``},
	} {
		assert.Equal(t, http.StatusForbidden, rawCall(t, p, token, r.method, r.path, r.body).StatusCode, "%s %s", r.method, r.path)
	}
	assert.Equal(t, http.StatusOK, rawCall(t, p, "", http.MethodGet, "/v1/asks", "").StatusCode, "the user's own")
	assert.Equal(t, http.StatusOK, rawCall(t, p, "", http.MethodGet, "/v1/discussions", "").StatusCode, "the user's own")
}

// readEvents reads the whole event log as token sees it.
func readEvents(t *testing.T, p *paths.Paths, token string) []map[string]any {
	t.Helper()
	resp := rawCall(t, p, token, http.MethodGet, "/v1/events", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out []map[string]any
	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var e map[string]any
		require.NoError(t, dec.Decode(&e))
		out = append(out, e)
	}
	return out
}

// An agent following the log sees that things happened to other agents,
// never what they said or what was said to them, and types not known to be
// safe not at all. Its own events it sees whole.
func TestAnAgentSeesOtherAgentsEventsRedacted(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	token := agentToken(t, d)
	for _, e := range []*state.NewEvent{
		{Type: "agent.asked", Agent: "t2-bbbbbb", Ticket: "2", Payload: []byte(`{"question":"the secret plan?"}`)},
		{Type: "user.told", Agent: "t2-bbbbbb", Payload: []byte(`{"body":"psst"}`)},
		{Type: "harness.text", Agent: "t2-bbbbbb", Payload: []byte(`{"text":"thinking"}`)},
		{Type: "merge.refused", Agent: "t2-bbbbbb", Payload: []byte(`{"kind":"push","detail":"git push origin main"}`)},
		{Type: "some.future_event", Agent: "t2-bbbbbb", Payload: []byte(`{"detail":"x"}`)},
		{Type: "message.queued", Agent: "t1-aaaaaa", Payload: []byte(`{"to":"t1-aaaaaa","from":"user"}`)},
	} {
		_, err := d.state.Events.Append(context.Background(), e)
		require.NoError(t, err)
	}

	got := readEvents(t, p, token)
	var types []string
	for _, e := range got {
		types = append(types, e["type"].(string))
		if e["agent"] != "t1-aaaaaa" {
			assert.Nil(t, e["payload"], "%s's payload is another agent's", e["type"])
		}
	}
	assert.Equal(t, []string{"agent.asked", "user.told", "message.queued"}, types)
	assert.NotNil(t, got[len(got)-1]["payload"], "its own events are whole")

	assert.Len(t, readEvents(t, p, ""), 6, "the user sees everything")
}
