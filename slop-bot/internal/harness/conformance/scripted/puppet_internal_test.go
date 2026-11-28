package scripted

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// The puppet's checks without a harness: the requests are posted directly,
// as a harness would send them, so the recording's shape is under the
// test's control.
func newBarePuppet(t *testing.T) *Puppet {
	t.Helper()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	p, err := New(t, &Options{Server: server, Shell: &Shell{Tool: "Bash"}})
	require.NoError(t, err)
	return p
}

// post sends one Anthropic Messages request carrying text; withTools false
// makes it the harness's own housekeeping.
func post(t *testing.T, p *Puppet, text string, withTools bool) {
	t.Helper()
	body := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": text}}}
	if withTools {
		body["tools"] = []any{map[string]any{"name": "Bash"}}
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.server.URL()+"/v1/messages", bytes.NewReader(data))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// resumeAt marks the session that starts at recording index from as a resume.
func (p *Puppet) resumeAt(from int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur = &session{started: started{id: "c1", resumed: true}, from: from}
}

func TestRemembersSplitsTheRecordingWhereTheSessionStarted(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	require.NoError(t, p.Reply(t.Context(), "noted"))
	require.NoError(t, p.Reply(t.Context(), "KUMQUAT"))
	post(t, p, "remember KUMQUAT", true)
	// Housekeeping before the resume: it counts in the recording's index,
	// not in the conversation.
	post(t, p, "a title, please", false)
	p.resumeAt(2)
	post(t, p, "remember KUMQUAT\nwhat was the word?", true)

	require.NoError(t, p.Remembers(t.Context(), "remember KUMQUAT"))
	require.NoError(t, p.Received(t.Context(), "what was the word?", false))
	assert.ErrorContains(t, p.Remembers(t.Context(), "remember FIG"), "no earlier session")
}

func TestRemembersFailsWhenTheResumedSessionLostTheHistory(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	require.NoError(t, p.Reply(t.Context(), "noted"))
	require.NoError(t, p.Reply(t.Context(), "what word?"))
	post(t, p, "remember KUMQUAT", true)
	post(t, p, "a title, please", false)
	p.resumeAt(2)
	post(t, p, "what was the word?", true)

	assert.ErrorContains(t, p.Remembers(t.Context(), "remember KUMQUAT"), "the resumed session never sent")
}
