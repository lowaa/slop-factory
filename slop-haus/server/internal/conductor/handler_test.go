package conductor_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// rest is the conductor's owner's and host's routes over HTTP, against the
// real service and database.
type rest struct {
	*fixture
	api       *replica
	mine      *agent.OwnedConductor
	base      string
	hostToken string
}

func newRest(t *testing.T) *rest {
	t.Helper()
	f := newFixture(t)
	r := &rest{fixture: f, api: f.replica(t, nil, nil), mine: f.owned(t, f.owner, "c1"), hostToken: "host"}
	r.base = "/api/conductors/" + r.mine.Agent().ID.String() + "/conversations"
	r.api.users["owner"], r.api.users["other"], r.api.users["admin"] = f.owner, f.other, f.admin
	r.api.agents[r.hostToken] = r.mine.Agent()
	worker, err := f.agents.Create(f.ctx, "a-worker")
	require.NoError(t, err)
	r.api.agents["worker"] = worker
	return r
}

type conversationJSON struct {
	ID      string  `json:"id"`
	State   string  `json:"state"`
	LastSeq int64   `json:"last_seq"`
	Failure *string `json:"failure"`
	Title   *string `json:"title"`

	ContextTokens int64 `json:"context_tokens"`
	CanCompact    bool  `json:"can_compact"`

	HostOnline bool `json:"host_online"`
}

type pollJSON struct {
	Reason string `json:"reason"`
	Items  []struct {
		Seq          int64  `json:"seq"`
		Type         string `json:"type"`
		Conversation string `json:"conversation"`
		Resume       bool   `json:"resume"`
		Text         string `json:"text"`
		Turn         *int64 `json:"turn"`
		Attachments  []struct {
			ID          string `json:"id"`
			Filename    string `json:"filename"`
			ContentType string `json:"content_type"`
			SizeBytes   int64  `json:"size_bytes"`
		} `json:"attachments"`
	} `json:"items"`
	Acked int64 `json:"acked"`
}

type eventsJSON struct {
	Events []struct {
		Seq  int64  `json:"seq"`
		Kind string `json:"kind"`
		Body string `json:"body"`
	} `json:"events"`
}

func (r *rest) start(t *testing.T, body any) conversationJSON {
	t.Helper()
	rec := r.api.do("owner", http.MethodPost, r.base, body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return decode[conversationJSON](t, rec)
}

func TestTheWholeConversationOverHTTP(t *testing.T) {
	r := newRest(t)
	hostID := uuid.NewString()

	started := r.start(t, map[string]any{"message": "hello"})
	assert.Equal(t, "starting", started.State)

	// The host takes the start and the message, and acknowledges them on its next poll.
	poll := decode[pollJSON](t, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID}))
	require.Len(t, poll.Items, 2)
	assert.Equal(t, "work", poll.Reason)
	assert.Equal(t, "start", poll.Items[0].Type)
	assert.Equal(t, started.ID, poll.Items[0].Conversation)
	assert.False(t, poll.Items[0].Resume)
	assert.Equal(t, "message", poll.Items[1].Type)
	assert.Equal(t, "hello", poll.Items[1].Text)

	rec := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/conversations/"+started.ID+"/events", map[string]any{
		"host_id": hostID,
		"events": []map[string]any{
			{"host_seq": 1, "kind": "ready", "work_seq": 1},
			{"host_seq": 2, "kind": "assistant", "turn": 1, "text": "Hi there"},
			{"host_seq": 3, "kind": "turn_end", "turn": 1},
		},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"acked":3}`, rec.Body.String())

	got := decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID, nil))
	assert.Equal(t, "ready", got.State)
	assert.Equal(t, int64(4), got.LastSeq, "the user's message, then the three events")
	events := decode[eventsJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID+"/events?after=1", nil))
	require.Len(t, events.Events, 3)
	assert.Equal(t, "ready", events.Events[0].Kind)
	assert.Equal(t, "Hi there", events.Events[1].Body)

	sent := r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/messages", map[string]any{"text": "and then?"})
	require.Equal(t, http.StatusCreated, sent.Code, sent.Body.String())
	next := decode[pollJSON](t, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll",
		map[string]any{"host_id": hostID, "ack": poll.Items[1].Seq}))
	require.Len(t, next.Items, 1)
	assert.Equal(t, "and then?", next.Items[0].Text)

	disconnected := decode[conversationJSON](t, r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/disconnect", nil))
	assert.Equal(t, "stopped", disconnected.State)
	list := decode[struct {
		Conversations []conversationJSON `json:"conversations"`
	}](t, r.api.do("owner", http.MethodGet, r.base, nil))
	require.Len(t, list.Conversations, 1, "the session stays")

	// The next message reconnects it: a start that resumes the session, then the message.
	back := r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/messages", map[string]any{"text": "i am back"})
	require.Equal(t, http.StatusCreated, back.Code, back.Body.String())
	assert.Equal(t, "starting", decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID, nil)).State)
	reconnect := decode[pollJSON](t, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll",
		map[string]any{"host_id": hostID, "ack": next.Items[0].Seq}))
	require.Len(t, reconnect.Items, 3)
	assert.Equal(t, []string{"stop", "start", "message"}, []string{reconnect.Items[0].Type, reconnect.Items[1].Type, reconnect.Items[2].Type})
	assert.True(t, reconnect.Items[1].Resume)
}

func TestAConversationReadSaysWhetherItsHostIsThere(t *testing.T) {
	r := newRest(t)
	started := r.start(t, map[string]any{})
	read := func() conversationJSON {
		return decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID, nil))
	}

	assert.False(t, read().HostOnline, "no host has polled")

	polled := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": uuid.NewString()})
	require.Equal(t, http.StatusOK, polled.Code, polled.Body.String())
	assert.True(t, read().HostOnline)
}

// Ownership is checked on every owner route: another member, and the
// tenant's own admin, are refused all of them -- an admin may disable a
// conductor, never talk through it. The rest of the check is the agent
// package's, tested there; this is that no route here forgets it.
func TestEveryOwnerRouteRefusesEveryoneButTheOwner(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})
	id := c.ID

	routes := []struct{ method, path, body string }{
		{http.MethodPost, r.base, `{}`},
		{http.MethodGet, r.base, ``},
		{http.MethodGet, r.base + "/" + id, ``},
		{http.MethodGet, r.base + "/" + id + "/events", ``},
		{http.MethodGet, r.base + "/" + id + "/events?turns=5", ``},
		{http.MethodPost, r.base + "/" + id + "/messages", `{"text":"hi"}`},
		{http.MethodPost, r.base + "/" + id + "/disconnect", ``},
		{http.MethodPost, r.base + "/" + id + "/interrupt", `{"turn":1}`},
		{http.MethodPatch, r.base + "/" + id, `{"title":"mine now"}`},
		{http.MethodGet, r.base + "/" + id + "/stream", ``},
	}
	for _, route := range routes {
		for _, who := range []string{"other", "admin"} {
			rec := r.api.do(who, route.method, route.path, route.body)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s as %s: %s", route.method, route.path, who, rec.Body.String())
		}
		for _, token := range []string{r.hostToken, "worker"} {
			rec := r.api.do(token, route.method, route.path, route.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s with an agent's credential: %s", route.method, route.path, rec.Body.String())
		}
	}

	got := decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+id, nil))
	assert.Equal(t, "starting", got.State, "and none of it touched the conversation")
	assert.Equal(t, int64(0), got.LastSeq)
	assert.Nil(t, got.Title, "and none of them renamed it")
}

func TestHostRoutesNeedAConductorsCredential(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})
	hostID := uuid.NewString()
	events := map[string]any{"host_id": hostID, "events": []map[string]any{{"host_seq": 1, "kind": "turn_end"}}}

	for name, token := range map[string]string{"a worker": "worker", "a person": "owner", "an admin": "admin"} {
		poll := r.api.do(token, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})
		release := r.api.do(token, http.MethodPost, "/api/conductor/release", map[string]any{"host_id": hostID})
		report := r.api.do(token, http.MethodPost, "/api/conductor/conversations/"+c.ID+"/events", events)
		if token == "worker" {
			assert.Equal(t, http.StatusForbidden, poll.Code, "%s polling: %s", name, poll.Body.String())
			assert.Equal(t, http.StatusForbidden, report.Code, "%s reporting: %s", name, report.Body.String())
			assert.Equal(t, http.StatusForbidden, release.Code, "%s releasing: %s", name, release.Body.String())
			continue
		}
		assert.Equal(t, http.StatusUnauthorized, poll.Code, "%s polling", name)
		assert.Equal(t, http.StatusUnauthorized, report.Code, "%s reporting", name)
		assert.Equal(t, http.StatusUnauthorized, release.Code, "%s releasing", name)
	}
	r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})
	got := decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+c.ID, nil))
	assert.Equal(t, int64(0), got.LastSeq, "no refused report reached the transcript")
}

func TestAnotherConductorsConversationIsNotFoundOverHTTP(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})
	theirs := r.owned(t, r.other, "theirs")
	otherBase := "/api/conductors/" + theirs.Agent().ID.String() + "/conversations/" + c.ID

	assert.Equal(t, http.StatusNotFound, r.api.do("other", http.MethodGet, otherBase, nil).Code)
	assert.Equal(t, http.StatusNotFound, r.api.do("other", http.MethodPost, otherBase+"/disconnect", nil).Code)
	assert.Equal(t, http.StatusNotFound, r.api.do("other", http.MethodGet, otherBase+"/stream", nil).Code)
	// The owner naming the other's conductor is refused as not its owner.
	assert.Equal(t, http.StatusForbidden, r.api.do("owner", http.MethodGet, otherBase, nil).Code)

	r.api.agents["theirs"] = theirs.Agent()
	rec := r.api.do("theirs", http.MethodPost, "/api/conductor/conversations/"+c.ID+"/events", map[string]any{
		"host_id": uuid.NewString(), "events": []map[string]any{{"host_seq": 1, "kind": "turn_end"}}})
	assert.Equal(t, http.StatusNotFound, rec.Code, "a host reports only for its own conductor's conversations")
}

func TestAnotherSessionReplacesTheLiveOneAndAMessageReconnectsADisconnectedOne(t *testing.T) {
	r := newRest(t)
	first := r.start(t, map[string]any{})

	second := r.api.do("owner", http.MethodPost, r.base, map[string]any{})
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())
	assert.Equal(t, "stopped", decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+first.ID, nil)).State,
		"the live one was replaced, not refused")

	send := r.api.do("owner", http.MethodPost, r.base+"/"+first.ID+"/messages", map[string]any{"text": "hi"})
	require.Equal(t, http.StatusCreated, send.Code, send.Body.String())
	assert.Equal(t, "starting", decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+first.ID, nil)).State)
	assert.Equal(t, "stopped", decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+decode[conversationJSON](t, second).ID, nil)).State,
		"and the other way round")
}

func TestRequestsAreValidated(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})

	for name, tc := range map[string]struct {
		got  int
		want int
	}{
		"an unknown field":    {r.api.do("owner", http.MethodPost, r.base, map[string]any{"kind": "attempt"}).Code, http.StatusUnprocessableEntity},
		"bad conductor id":    {r.api.do("owner", http.MethodGet, "/api/conductors/nope/conversations", nil).Code, http.StatusUnprocessableEntity},
		"bad conversation id": {r.api.do("owner", http.MethodGet, r.base+"/nope", nil).Code, http.StatusUnprocessableEntity},
		"empty message":       {r.api.do("owner", http.MethodPost, r.base+"/"+c.ID+"/messages", map[string]any{"text": ""}).Code, http.StatusUnprocessableEntity},
		"bad client id":       {r.api.do("owner", http.MethodPost, r.base+"/"+c.ID+"/messages", map[string]any{"text": "x", "client_id": "nope"}).Code, http.StatusUnprocessableEntity},
		"bad host id":         {r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": "nope"}).Code, http.StatusUnprocessableEntity},
		"a conversation that does not exist": {
			r.api.do("owner", http.MethodGet, r.base+"/"+uuid.NewString(), nil).Code, http.StatusNotFound},
	} {
		assert.Equal(t, tc.want, tc.got, name)
	}
}

func TestAHostThatIsNotTheCurrentOneIsRefusedWith409(t *testing.T) {
	r := newRest(t)
	r.start(t, map[string]any{})
	first, second := uuid.NewString(), uuid.NewString()
	r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": first})

	rec := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": second})

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestAGapInAHostReportIsAConflictThatSaysWhatTheServerHas(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})
	hostID := uuid.NewString()
	r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})

	rec := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/conversations/"+c.ID+"/events", map[string]any{
		"host_id": hostID, "events": []map[string]any{{"host_seq": 5, "kind": "turn_end"}}})

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "up to host_seq 0")
}

func TestAHostReleasesItsConductorOverHTTP(t *testing.T) {
	r := newRest(t)
	hostID, successor := uuid.NewString(), uuid.NewString()
	poll := func(id string) int {
		return r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": id}).Code
	}
	release := func(token, id string) int {
		return r.api.do(token, http.MethodPost, "/api/conductor/release", map[string]any{"host_id": id}).Code
	}
	require.Equal(t, http.StatusOK, poll(hostID))
	require.Equal(t, http.StatusConflict, poll(successor))

	require.Equal(t, http.StatusNoContent, release(r.hostToken, hostID))
	assert.Equal(t, http.StatusNoContent, release(r.hostToken, hostID), "saying goodbye twice is fine")
	assert.Equal(t, http.StatusOK, poll(successor), "the next process is not refused")

	assert.Equal(t, http.StatusNoContent, release(r.hostToken, hostID), "a host already replaced releases nothing")
	assert.Equal(t, http.StatusOK, poll(successor), "and the one that replaced it is still the host")
	assert.Equal(t, http.StatusUnprocessableEntity, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/release",
		map[string]any{"host_id": "nope"}).Code)
}

// A conductor's credential reaches its own queue only: with another
// conductor's host_id it releases nothing of its own.
func TestAnotherConductorsCredentialCannotReleaseThisConductorsHost(t *testing.T) {
	r := newRest(t)
	theirs := r.owned(t, r.other, "theirs")
	r.api.agents["theirs"] = theirs.Agent()
	hostID := uuid.NewString()
	require.Equal(t, http.StatusOK, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID}).Code)

	rec := r.api.do("theirs", http.MethodPost, "/api/conductor/release", map[string]any{"host_id": hostID})

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusConflict, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll",
		map[string]any{"host_id": uuid.NewString()}).Code, "the host is still the one that polled")
}

func TestAHostReportsItsWorkspaceLayoutOverHTTP(t *testing.T) {
	f := newFixture(t)
	layouts := agent.NewPostgresLayoutStore(f.database)
	api := f.replica(t, &conductor.Options{Layouts: layouts}, nil)
	mine := f.owned(t, f.owner, "c1")
	theirs := f.owned(t, f.other, "c2")
	api.agents["host"], api.agents["theirs"] = mine.Agent(), theirs.Agent()
	worker, err := f.agents.Create(f.ctx, "a-worker")
	require.NoError(t, err)
	api.agents["worker"] = worker
	api.users["owner"] = f.owner
	report := func(token string, body any) int {
		return api.do(token, http.MethodPost, "/api/conductor/layout", body).Code
	}
	body := map[string]any{
		"root":  map[string]any{"path": ".", "git": false},
		"repos": []map[string]any{{"path": "app", "git": true, "origin": "https://user:secret@example.com/org/app.git"}},
	}

	require.Equal(t, http.StatusNoContent, report("host", body))

	got, err := layouts.Layouts(f.ctx)
	require.NoError(t, err)
	require.Contains(t, got, mine.Agent().ID)
	assert.Equal(t, []agent.LayoutEntry{{Path: "app", Git: true, Origin: "example.com/org/app.git"}}, got[mine.Agent().ID].Layout.Repos,
		"credentials are stripped again on the server")
	assert.NotContains(t, got, theirs.Agent().ID, "only the caller's own conductor is written")

	body["repos"] = []map[string]any{}
	require.Equal(t, http.StatusNoContent, report("host", body))
	got, err = layouts.Layouts(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, got[mine.Agent().ID].Layout.Repos, "the latest report replaces the last")

	assert.Equal(t, http.StatusUnprocessableEntity, report("host", map[string]any{
		"root": map[string]any{"path": "elsewhere", "git": false}, "repos": []any{}}), "a root that is not the workspace root")
	assert.Equal(t, http.StatusForbidden, report("worker", body), "a worker is not a conductor")
	assert.Equal(t, http.StatusUnauthorized, report("owner", body), "nor is a person")
}

func TestALayoutIsAcceptedAndDroppedWithoutAStore(t *testing.T) {
	r := newRest(t)
	rec := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/layout", map[string]any{
		"root": map[string]any{"path": ".", "git": false}, "repos": []any{}})
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func TestAnOversizeLayoutIsRefused(t *testing.T) {
	r := newRest(t)
	root := map[string]any{"path": ".", "git": false}
	repos := make([]map[string]any, 51)
	for i := range repos {
		repos[i] = map[string]any{"path": fmt.Sprintf("r%d", i), "git": true}
	}
	post := func(root any, repos any) int {
		return r.api.do(r.hostToken, http.MethodPost, "/api/conductor/layout", map[string]any{"root": root, "repos": repos}).Code
	}
	assert.Equal(t, http.StatusUnprocessableEntity, post(root, repos), "51 repos")
	assert.Equal(t, http.StatusUnprocessableEntity, post(root, []map[string]any{{"path": "a", "git": true, "origin": strings.Repeat("x", 501)}}), "origin")
	assert.Equal(t, http.StatusUnprocessableEntity, post(map[string]any{"path": strings.Repeat("x", 256), "git": false}, []any{}), "path")
	assert.Equal(t, http.StatusNoContent, post(root, repos[:50]), "50 repos")
}
