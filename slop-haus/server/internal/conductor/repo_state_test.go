package conductor_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

const (
	headA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	headB = "b1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
)

func repoReport(seq int64, mutate func(*conductor.RepoState)) conductor.HostEvent {
	turn := int64(1)
	repo := &conductor.RepoState{
		Branch: "ticket-9-x", Head: headA, Upstream: "origin/ticket-9-x",
		Commits:      []conductor.RepoCommit{{SHA: headA, Subject: "add c"}},
		Pushes:       []conductor.Push{{Ref: "origin/ticket-9-x", To: headA}},
		ChangedPaths: []string{"slop-haus/server/internal/ticket/lease.go"}, ChangedTotal: 1,
	}
	if mutate != nil {
		mutate(repo)
	}
	return conductor.HostEvent{HostSeq: seq, Kind: conductor.EventRepoState, Turn: &turn, Repo: repo, WorkSeq: 1}
}

// holdScope has a worker claim a ticket with these paths, so that they are a
// scope an agent holds.
func (f *fixture) holdScope(t *testing.T, paths ...string) int64 {
	t.Helper()
	scopes := make([]model.Path, len(paths))
	for i, p := range paths {
		scopes[i] = model.Path{Path: p}
	}
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Held work", Status: ticket.StatusOpen, Paths: scopes})
	require.NoError(t, err)
	worker, err := f.agents.Create(f.ctx, "worker-"+uuid.NewString()[:8])
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, worker.ID, &created.ID)
	require.NoError(t, err)
	return created.ID
}

func (f *fixture) describe(t *testing.T, owned *agent.OwnedConductor, c *conductor.Conversation) *conductor.Detail {
	t.Helper()
	d, err := f.svc.Describe(f.ctx, owned, c.ID)
	require.NoError(t, err)
	return d
}

func TestAHostIsOnlineOnceItHasPolled(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")

	status, err := f.svc.HostInfo(f.ctx, owned)
	require.NoError(t, err)
	assert.False(t, status.Online, "no host has polled")
	assert.False(t, status.Supports(conductor.CapabilityInterrupt))

	h := f.host(t, owned)
	_, err = f.svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id})
	require.NoError(t, err)
	status, _ = f.svc.HostInfo(f.ctx, owned)
	assert.Equal(t, conductor.HostStatus{Online: true, Capabilities: []string{}}, status, "a host on older code reports nothing")
}

func TestAHostIsOfflineOnceItHasBeenSilentPastTheTTL(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	h := f.host(t, owned)
	_, err := f.svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id})
	require.NoError(t, err)
	impatient := f.newService(&conductor.Options{HostTTL: time.Nanosecond})
	t.Cleanup(impatient.Close)

	fresh, err := f.svc.HostInfo(f.ctx, owned)
	require.NoError(t, err)
	stale, err := impatient.HostInfo(f.ctx, owned)
	require.NoError(t, err)

	assert.True(t, fresh.Online)
	assert.False(t, stale.Online, "the same poll is long enough ago for a service that gives hosts a nanosecond")
}

func TestARepoStateIsKeptOnTheConversationAndInTheTranscript(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)

	acked := f.report(t, h, c,
		conductor.HostEvent{HostSeq: 1, Kind: conductor.EventReady, WorkSeq: 1},
		repoReport(2, nil),
		repoReport(3, func(r *conductor.RepoState) { r.Head, r.Dirty, r.DirtyCount, r.Ahead = headB, true, 2, 1 }),
	)

	assert.Equal(t, int64(3), acked)
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Repo)
	assert.Equal(t, headB, got.Repo.Head, "the newest report is the conversation's state")
	assert.True(t, got.Repo.Dirty)
	assert.Equal(t, []string{"slop-haus/server/internal/ticket/lease.go"}, got.Repo.ChangedPaths)
	events := f.transcript(t, owned, c)
	require.Len(t, events, 3)
	assert.Equal(t, conductor.EventRepoState, events[1].Kind)
	assert.Equal(t, int64(1), *events[1].Turn)
	assert.Contains(t, events[1].Body, `"head":"`+headA+`"`)
	assert.Contains(t, events[1].Body, `"pushes":[{"ref":"origin/ticket-9-x","from":"","to":"`+headA+`"}]`)
}

func TestAnEarlierSessionsLateRepoStateDoesNotReplaceTheCurrentOnes(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.report(t, h, c, conductor.HostEvent{HostSeq: 1, Kind: conductor.EventReady, WorkSeq: 1})
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "again"})
	require.NoError(t, err)
	resumed, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	require.Greater(t, resumed.StartSeq, int64(1))

	// The new session's report, then the stopped one's arriving late.
	newer := repoReport(2, func(r *conductor.RepoState) { r.Head = headB })
	newer.WorkSeq = resumed.StartSeq
	f.report(t, h, c, newer)
	f.report(t, h, c, repoReport(3, func(r *conductor.RepoState) { r.Head = headA }))

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, headB, got.Repo.Head, "the late report is in the transcript and is not the state")
	var heads []string
	for _, e := range f.transcript(t, owned, c) {
		if e.Kind == conductor.EventRepoState {
			heads = append(heads, e.Body)
		}
	}
	require.Len(t, heads, 2)
	assert.Contains(t, heads[0], headB)
	assert.Contains(t, heads[1], headA, "the late report is kept in the transcript, in the order it arrived")
}

func TestARepoStateNeedsTheSessionItIsOnAndNotOneNotYetIssued(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)

	for name, workSeq := range map[string]int64{"none": 0, "one not yet issued": 99} {
		event := repoReport(1, nil)
		event.WorkSeq = workSeq
		_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: []conductor.HostEvent{event}})
		assert.ErrorIs(t, err, conductor.ErrInvalid, name)
	}
}

func TestAConversationHasNoRepoStateBeforeTheHostReportsOne(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	assert.Nil(t, f.describe(t, owned, c).Conversation.Repo)
}

func TestARepoStateTheBoardMustNotStoreIsRefusedWholeAndStoresNothing(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	tooMany := make([]string, conductor.MaxRepoPaths+1)
	for i := range tooMany {
		tooMany[i] = "f.go"
	}

	for name, mutate := range map[string]func(*conductor.RepoState){
		"no head":            func(r *conductor.RepoState) { r.Head = "" },
		"a head that is not": func(r *conductor.RepoState) { r.Head = "HEAD; rm -rf /" },
		"too many paths":     func(r *conductor.RepoState) { r.ChangedPaths, r.ChangedTotal = tooMany, len(tooMany) },
		"an absolute path":   func(r *conductor.RepoState) { r.ChangedPaths = []string{"/etc/passwd"} },
		"an empty path":      func(r *conductor.RepoState) { r.ChangedPaths = []string{""} },
		"a path with a NUL":  func(r *conductor.RepoState) { r.ChangedPaths = []string{"a\x00b"} },
		"a long path": func(r *conductor.RepoState) {
			r.ChangedPaths = []string{strings.Repeat("a", conductor.MaxRepoPathBytes+1)}
		},
		"a total below the paths": func(r *conductor.RepoState) { r.ChangedTotal = 0 },
		"a negative count":        func(r *conductor.RepoState) { r.Ahead = -1 },
		"a bad commit":            func(r *conductor.RepoState) { r.Commits = []conductor.RepoCommit{{SHA: "xyz", Subject: "s"}} },
		"a push with no ref":      func(r *conductor.RepoState) { r.Pushes = []conductor.Push{{To: headA}} },
		"a push to nothing":       func(r *conductor.RepoState) { r.Pushes = []conductor.Push{{Ref: "origin/x", To: "zz"}} },
		"a quoted flood": func(r *conductor.RepoState) {
			// Each quote is escaped in the stored JSON: 200 paths of 200 quotes is past an event's size.
			p := strings.Repeat(`"`, conductor.MaxRepoPathBytes)
			r.ChangedPaths, r.ChangedTotal = make([]string, conductor.MaxRepoPaths), conductor.MaxRepoPaths
			for i := range r.ChangedPaths {
				r.ChangedPaths[i] = p
			}
		},
	} {
		_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: []conductor.HostEvent{repoReport(1, mutate)}})
		assert.ErrorIs(t, err, conductor.ErrInvalid, name)
	}
	_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: []conductor.HostEvent{
		{HostSeq: 1, Kind: conductor.EventRepoState, WorkSeq: 1}}})
	assert.ErrorIs(t, err, conductor.ErrInvalid, "a repo_state with no repo")

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Repo)
	assert.Empty(t, f.transcript(t, owned, c))
}

func TestPathsFullOfCharactersJSONLikesToEscapeAreStillStored(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	paths := make([]string, conductor.MaxRepoPaths)
	for i := range paths {
		paths[i] = strings.Repeat("<&>", conductor.MaxRepoPathBytes/3)
	}

	f.report(t, h, c, repoReport(1, func(r *conductor.RepoState) { r.ChangedPaths, r.ChangedTotal = paths, len(paths) }))

	assert.Len(t, f.transcript(t, owned, c), 1, "stored whole: HTML escaping would have made it six times the size")
}

func TestAScopeAnAgentHoldsThatALiveConversationChangedIsReported(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	held := f.holdScope(t, "slop-haus/server/internal/ticket/")
	c := f.start(t, owned)
	h := f.polling(t, owned)

	f.report(t, h, c, repoReport(1, func(r *conductor.RepoState) {
		r.ChangedPaths = []string{"slop-haus/server/internal/ticket/lease.go", "slop-haus/server/internal/ticket_extra/x.go", "docs/a.md"}
		r.ChangedTotal = 3
	}))

	overlaps := f.describe(t, owned, c).ScopeOverlaps
	assert.Equal(t, []ticket.HeldScopeOverlap{{
		TicketID: held, Title: "Held work", Status: ticket.StatusInProgress,
		Path: "slop-haus/server/internal/ticket/lease.go", Held: "slop-haus/server/internal/ticket/",
	}}, overlaps, "the file inside the held directory, not its lookalike or an unrelated one")
}

func TestAConversationThatChangedNothingAgentsHoldHasNoOverlap(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	f.holdScope(t, "somewhere/else/")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.report(t, h, c, repoReport(1, nil))
	assert.Empty(t, f.describe(t, owned, c).ScopeOverlaps)
}

func TestOnceTheAgentLetsGoOfTheScopeTheWarningGoes(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	held := f.holdScope(t, "slop-haus/server/internal/ticket/")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.report(t, h, c, repoReport(1, nil))
	require.Len(t, f.describe(t, owned, c).ScopeOverlaps, 1)

	done := ticket.StatusDone
	_, err := f.tickets.Update(f.ctx, held, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	assert.Empty(t, f.describe(t, owned, c).ScopeOverlaps)
}

func TestAnEndedConversationIsNotWarnedAbout(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	f.holdScope(t, "slop-haus/server/internal/ticket/")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.report(t, h, c, repoReport(1, nil))
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)

	assert.Empty(t, f.describe(t, owned, c).ScopeOverlaps)
}

// A repo state is the page's newest word on where the working copy is, so it
// is kept even when the transcript has no room left for the event it came in.
func TestARepoStatePastTheEventCapStillUpdatesTheStateAndAddsNoEvent(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.setNextSeq(t, c, conductor.MaxEventsPerConversation)

	f.report(t, h, c, repoReport(1, func(r *conductor.RepoState) { r.Head = headB }))

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Repo)
	assert.Equal(t, headB, got.Repo.Head)
	assert.Equal(t, int64(conductor.MaxEventsPerConversation), got.LastSeq, "no event was added")
}

type failingScopes struct{}

func (failingScopes) HeldScopeOverlaps(context.Context, []string) ([]ticket.HeldScopeOverlap, error) {
	return nil, errors.New("the ticket store is down")
}

// A notice that cannot be worked out must not stop the conversation showing.
func TestAConversationStillShowsWhenItsScopeOverlapsCannotBeWorkedOut(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	svc := f.newService(&conductor.Options{Scopes: failingScopes{}})
	t.Cleanup(svc.Close)
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.report(t, h, c, repoReport(1, nil))

	d, err := svc.Describe(f.ctx, owned, c.ID)

	require.NoError(t, err)
	assert.Empty(t, d.ScopeOverlaps)
	assert.NotNil(t, d.Repo)
}

func TestRemoteControlStateOverHTTP(t *testing.T) {
	r := newRest(t)
	held := r.holdScope(t, "slop-haus/server/internal/ticket/")
	hostID := uuid.NewString()
	started := r.start(t, map[string]any{})

	hostPath := "/api/conductors/" + r.mine.Agent().ID.String() + "/host"
	assert.JSONEq(t, `{"online":false}`, r.api.do("owner", http.MethodGet, hostPath, nil).Body.String())
	rec := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"online":true}`, r.api.do("owner", http.MethodGet, hostPath, nil).Body.String())
	assert.Equal(t, http.StatusForbidden, r.api.do("other", http.MethodGet, hostPath, nil).Code, "only the owner")
	assert.Equal(t, http.StatusForbidden, r.api.do("admin", http.MethodGet, hostPath, nil).Code, "not even the tenant's owner")

	rec = r.api.do(r.hostToken, http.MethodPost, "/api/conductor/conversations/"+started.ID+"/events", map[string]any{
		"host_id": hostID,
		"events": []map[string]any{
			{"host_seq": 1, "kind": "ready", "work_seq": 1},
			{"host_seq": 2, "kind": "repo_state", "turn": 1, "work_seq": 1, "repo": map[string]any{
				"branch": "ticket-9-x", "head": headA, "dirty": true, "dirty_count": 1, "upstream": "origin/ticket-9-x",
				"ahead": 1, "behind": 0,
				"commits":       []map[string]any{{"sha": headA, "subject": "add c"}},
				"pushes":        []map[string]any{{"ref": "origin/ticket-9-x", "from": "", "to": headA}},
				"changed_paths": []string{"slop-haus/server/internal/ticket/lease.go"}, "changed_total": 1,
			}},
		},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	detail := decode[struct {
		Repo *struct {
			Branch string `json:"branch"`
			Head   string `json:"head"`
			Dirty  bool   `json:"dirty"`
			Ahead  int    `json:"ahead"`
		} `json:"repo"`
		ScopeOverlaps []struct {
			TicketID int64  `json:"ticket_id"`
			Path     string `json:"path"`
			Held     string `json:"held"`
		} `json:"scope_overlaps"`
	}](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID, nil))
	require.NotNil(t, detail.Repo)
	assert.Equal(t, "ticket-9-x", detail.Repo.Branch)
	assert.True(t, detail.Repo.Dirty)
	require.Len(t, detail.ScopeOverlaps, 1)
	assert.Equal(t, held, detail.ScopeOverlaps[0].TicketID)
	assert.Equal(t, "slop-haus/server/internal/ticket/lease.go", detail.ScopeOverlaps[0].Path)

	events := decode[eventsJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID+"/events", nil))
	require.Len(t, events.Events, 2)
	assert.Equal(t, "repo_state", events.Events[1].Kind)
}

func TestARepoStateBeyondTheWireLimitsIsRefusedBeforeTheService(t *testing.T) {
	r := newRest(t)
	hostID := uuid.NewString()
	started := r.start(t, map[string]any{})
	r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})

	rec := r.api.do(r.hostToken, http.MethodPost, "/api/conductor/conversations/"+started.ID+"/events", map[string]any{
		"host_id": hostID,
		"events": []map[string]any{{"host_seq": 1, "kind": "repo_state", "work_seq": 1, "repo": map[string]any{
			"branch": "b", "head": "not-a-commit", "dirty": false, "dirty_count": 0, "upstream": "", "ahead": 0, "behind": 0,
			"commits": []any{}, "pushes": []any{}, "changed_paths": []string{}, "changed_total": 0}}},
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}
