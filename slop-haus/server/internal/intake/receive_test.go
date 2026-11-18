package intake_test

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// What a delivery files, and what the source's own config -- not the
// payload -- decides about it.

func TestOpen_FilesATicketWithTheSendersWordsKeptApart(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(trusted))

	rec := f.send(s.post(event("open", "k1", map[string]any{
		"body": "the printer is on fire", "link": "https://desk.example/k1", "context": map[string]any{"floor": 3},
	})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := f.ticketFor(s, "k1")
	require.NotNil(t, got)
	assert.Equal(t, "[desk] external report", got.Title, "slop-haus's own wording: titles reach agents outside any fence")
	require.NotNil(t, got.Intake)
	assert.Equal(t, "Printer on fire", got.Intake.Title, "the sender's title, for a person's list")
	assert.NotContains(t, got.Body, "on fire", "the sender's text is never in the ticket's own body")
	assert.Contains(t, got.Body, "external content")
	events := f.external(s, got.ID)
	require.Len(t, events, 1)
	assert.Equal(t, "the printer is on fire", events[0].Body)
	assert.Equal(t, "https://desk.example/k1", events[0].Link)
	assert.JSONEq(t, `{"floor":3}`, string(events[0].Context))
	assert.Equal(t, "desk", events[0].SourceName)
	assert.Equal(t, "k1", events[0].Key)
	assert.Nil(t, got.CreatedByUserID)
	assert.Nil(t, got.CreatedByAgentID)
}

func TestStatusGuard_AnUntrustedSourceIsAlwaysPreparing(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())

	rec := f.send(s.post(event("open", "asked", map[string]any{"status": "open"})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "preparing")
	assert.Equal(t, ticket.StatusPreparing, f.ticketFor(s, "asked").Status, "a request for open is downgraded, not rejected")

	rec = f.send(s.post(event("open", "plain", nil)))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ticket.StatusPreparing, f.ticketFor(s, "plain").Status)

	downgraded := map[string]bool{}
	for _, d := range f.deliveries(f.a) {
		downgraded[d.Key] = d.Downgraded
	}
	assert.True(t, downgraded["asked"], "the downgrade is recorded on the delivery")
	assert.False(t, downgraded["plain"], "no request, nothing to downgrade")
	assert.Contains(t, f.logs.String(), "action=intake_downgraded", "and in the audit log")
}

func TestStatusGuard_NotBypassedByUpdateWithNoTicket(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())

	rec := f.send(s.post(event("update", "lost-open", map[string]any{"title": "late", "status": "open"})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := f.ticketFor(s, "lost-open")
	require.NotNil(t, got, "an update with no ticket is treated as an open")
	assert.Equal(t, ticket.StatusPreparing, got.Status)
}

func TestStatusGuard_NotBypassedByTheReopenPath(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(func(sp *intake.Spec) { sp.ReopenPolicy = intake.ReopenExisting }))
	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "k", nil))).Code)
	first := f.ticketFor(s, "k")
	f.setStatus(f.a, first.ID, ticket.StatusDone)

	rec := f.send(s.post(event("open", "k", map[string]any{"status": "open"})).header("X-Slophaus-Delivery", "second"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := f.ticketFor(s, "k")
	assert.Equal(t, first.ID, got.ID, "reopened rather than refiled")
	assert.Equal(t, ticket.StatusPreparing, got.Status, "an untrusted source cannot reopen into the queue")
	assert.Nil(t, got.ClosedAt)
}

func TestStatusGuard_ATrustedSourceGetsWhatItAsksFor(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen }))

	f.send(s.post(event("open", "default", nil)))
	f.send(s.post(event("open", "open", map[string]any{"status": "open"})))
	f.send(s.post(event("open", "prep", map[string]any{"status": "preparing"})))

	assert.Equal(t, ticket.StatusOpen, f.ticketFor(s, "default").Status, "the source's default applies when the payload asks nothing")
	assert.Equal(t, ticket.StatusOpen, f.ticketFor(s, "open").Status)
	assert.Equal(t, ticket.StatusPreparing, f.ticketFor(s, "prep").Status)
	for _, d := range f.deliveries(f.a) {
		assert.False(t, d.Downgraded)
	}
}

func TestStatusGuard_TheSchemaRefusesAnUntrustedOpenDefault(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Create(f.a.ctx, f.a.who(), spec(func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen }))
	assert.ErrorIs(t, err, intake.ErrInvalidSource)

	_, err = f.database.Exec(f.a.ctx, `
		INSERT INTO intake_sources (id, name, adapter, trust, auth_kind, default_status, default_priority, max_priority)
		VALUES (gen_random_uuid(), 'sneaky', 'native', 'untrusted', 'bearer', 'open', 'normal', 'normal')`)
	assert.Error(t, err, "and so does the database, whatever the code does")
}

func TestPayload_CannotSetWhatOnlyTheSourceConfigSets(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(func(sp *intake.Spec) {
		sp.Paths = []string{"infra/"}
	}))
	for _, field := range []string{"roles", "role_match", "paths", "blockers", "assignment", "scope", "needs_discussion", "requires_approval"} {
		t.Run(field, func(t *testing.T) {
			rec := f.send(s.post(event("open", "x-"+field, map[string]any{field: []string{"admin"}})))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Nil(t, f.ticketFor(s, "x-"+field))
		})
	}
	for _, status := range []string{"in_progress", "done", "cancelled", "review"} {
		rec := f.send(s.post(event("open", "st-"+status, map[string]any{"status": status})))
		assert.Equal(t, http.StatusBadRequest, rec.Code, status)
		assert.Nil(t, f.ticketFor(s, "st-"+status))
	}
	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "fine", nil))).Code)
	got := f.ticketFor(s, "fine")
	require.Len(t, got.Paths, 1)
	assert.Equal(t, "infra/", got.Paths[0].Path, "paths come from the source")
}

func TestOpen_RolesAndPathsComeFromTheSource(t *testing.T) {
	f := newFixture(t)
	_, err := f.database.Exec(f.a.ctx, `INSERT INTO agent_roles (id, name) VALUES (gen_random_uuid(), 'deploy'), (gen_random_uuid(), 'ops')`)
	require.NoError(t, err)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) {
		sp.RoleNames = []string{"deploy", "ops"}
		sp.RoleMatch = model.RoleMatchAll
		sp.Paths = []string{"slop-haus/server/"}
	}))
	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "r", nil))).Code)
	got := f.ticketFor(s, "r")
	require.NotNil(t, got)
	names := []string{}
	for _, r := range got.RequiredRoles {
		names = append(names, r.Name)
	}
	assert.ElementsMatch(t, []string{"deploy", "ops"}, names)
	assert.Equal(t, model.RoleMatchAll, got.RoleMatch)
	require.Len(t, got.Paths, 1)
	assert.Equal(t, "slop-haus/server/", got.Paths[0].Path)
}

func TestOpen_ARoleDeletedSinceFailsTheDeliveryRatherThanLooseningIt(t *testing.T) {
	f := newFixture(t)
	_, err := f.database.Exec(f.a.ctx, `INSERT INTO agent_roles (id, name) VALUES (gen_random_uuid(), 'deploy')`)
	require.NoError(t, err)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) { sp.RoleNames = []string{"deploy"} }))
	_, err = f.database.Exec(f.a.ctx, `DELETE FROM agent_roles WHERE name = 'deploy'`)
	require.NoError(t, err)

	rec := f.send(s.post(event("open", "r", nil)))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Nil(t, f.ticketFor(s, "r"), "no ticket claimable by anyone")
	ds := f.deliveries(f.a)
	require.Len(t, ds, 1)
	assert.Equal(t, intake.OutcomeError, ds[0].Outcome)
	assert.Contains(t, ds[0].Detail, "role")
}

func TestPriority_IsARequestCappedAtTheSourcesMax(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(func(sp *intake.Spec) {
		sp.DefaultPriority, sp.MaxPriority = model.PriorityLow, model.PriorityHigh
	}))
	f.send(s.post(event("open", "none", nil)))
	f.send(s.post(event("open", "urgent", map[string]any{"priority": "urgent"})))
	f.send(s.post(event("open", "normal", map[string]any{"priority": "normal"})))

	assert.Equal(t, model.PriorityLow, f.ticketFor(s, "none").Priority)
	assert.Equal(t, model.PriorityHigh, f.ticketFor(s, "urgent").Priority, "capped, not rejected")
	assert.Equal(t, model.PriorityNormal, f.ticketFor(s, "normal").Priority)
}

// Correlation of (source, key) to tickets.

func TestOpen_AnOpenForAKnownKeyAddsToItsTicket(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	f.send(s.post(event("open", "k", nil)))
	rec := f.send(s.post(event("open", "k", map[string]any{"body": "again"})))
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, f.allTickets(f.a), 1)
	assert.Len(t, f.external(s, f.ticketFor(s, "k").ID), 2)
}

func TestUpdate_AddsToTheTicketAndNeverMovesIt(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen }))
	f.send(s.post(event("open", "k", nil)))
	first := f.ticketFor(s, "k")

	rec := f.send(s.post(event("update", "k", map[string]any{"body": "worse", "status": "preparing", "priority": "high"})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := f.ticketFor(s, "k")
	assert.Equal(t, first.ID, got.ID)
	assert.Equal(t, ticket.StatusOpen, got.Status, "an update is a comment: no status change")
	assert.Equal(t, first.Priority, got.Priority)
	events := f.external(s, got.ID)
	require.Len(t, events, 2)
	assert.Equal(t, "update", events[1].Event)
}

func TestUpdate_WithNoTicketIsTakenAsAnOpen(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	rec := f.send(s.post(event("update", "never-opened", map[string]any{"title": "hello"})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := f.ticketFor(s, "never-opened")
	require.NotNil(t, got)
	assert.Equal(t, "[desk] external report", got.Title)
	assert.Equal(t, "hello", got.Intake.Title)
}

func TestResolve_OnlyCommentsAndNeverClosesATicket(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen }))
	f.send(s.post(event("open", "k", nil)))
	before := f.ticketFor(s, "k")

	rec := f.send(s.post(event("resolve", "k", map[string]any{"body": "all clear", "status": "preparing"})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := f.ticketFor(s, "k")
	assert.Equal(t, before.Status, got.Status)
	assert.Nil(t, got.ClosedAt)
	events := f.external(s, got.ID)
	require.Len(t, events, 2)
	assert.Equal(t, "resolve", events[1].Event)
}

func TestResolve_WithNoTicketIsIgnored(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	rec := f.send(s.post(event("resolve", "ghost", nil)))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ignored")
	assert.Empty(t, f.allTickets(f.a))
}

func TestOpen_WhenTheTicketIsDonePolicyNewFilesAnother(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(func(sp *intake.Spec) { sp.ReopenPolicy = intake.ReopenNew }))
	f.send(s.post(event("open", "k", nil)))
	first := f.ticketFor(s, "k")
	f.setStatus(f.a, first.ID, ticket.StatusDone)

	f.send(s.post(event("open", "k", nil)).header("X-Slophaus-Delivery", "again"))
	second := f.ticketFor(s, "k")
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, ticket.StatusPreparing, second.Status)
	assert.Len(t, f.allTickets(f.a), 2)

	// Later events go to the newest.
	f.send(s.post(event("update", "k", nil)).header("X-Slophaus-Delivery", "u"))
	assert.Len(t, f.external(s, second.ID), 2)
	assert.Len(t, f.external(s, first.ID), 1)
}

func TestOpen_WhenTheTicketIsDonePolicyReopenReturnsIt(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) {
		sp.ReopenPolicy = intake.ReopenExisting
		sp.DefaultStatus = ticket.StatusOpen
	}))
	f.send(s.post(event("open", "k", nil)))
	first := f.ticketFor(s, "k")
	f.setStatus(f.a, first.ID, ticket.StatusDone)

	rec := f.send(s.post(event("open", "k", nil)).header("X-Slophaus-Delivery", "again"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "reopened")

	got := f.ticketFor(s, "k")
	assert.Equal(t, first.ID, got.ID)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	assert.Nil(t, got.ClosedAt)
	assert.Len(t, f.allTickets(f.a), 1)
	assert.Len(t, f.external(s, got.ID), 2)
}

func TestOpen_ACancelledTicketCountsAsDone(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	f.send(s.post(event("open", "k", nil)))
	f.setStatus(f.a, f.ticketFor(s, "k").ID, ticket.StatusCancelled)
	f.send(s.post(event("open", "k", nil)).header("X-Slophaus-Delivery", "again"))
	assert.Len(t, f.allTickets(f.a), 2)
}

func TestCaps_BoundWhatOneSourceCanPileUp(t *testing.T) {
	f := newFixture(t) // 5 events per ticket, 3 live tickets
	s := f.source(f.a, spec())

	f.send(s.post(event("open", "k", nil)))
	for i := 0; i < 6; i++ {
		f.send(s.post(event("update", "k", map[string]any{"body": strings.Repeat("x", i+1)})))
	}
	assert.Len(t, f.external(s, f.ticketFor(s, "k").ID), 5)
	last := f.deliveries(f.a)[0]
	assert.Equal(t, intake.OutcomeIgnored, last.Outcome)

	f.send(s.post(event("open", "k2", nil)))
	f.send(s.post(event("open", "k3", nil)))
	rec := f.send(s.post(event("open", "k4", nil)))
	assert.Contains(t, rec.Body.String(), "ignored")
	assert.Nil(t, f.ticketFor(s, "k4"))
}

// Tenant isolation.

func TestIsolation_ASourceActsOnlyOnItsOwnTenantsTickets(t *testing.T) {
	f := newFixture(t)
	sa := f.source(f.a, spec())
	sb := f.source(f.b, spec())

	f.send(sa.post(event("open", "same-key", nil)))
	f.send(sb.post(event("open", "same-key", nil)))
	require.Len(t, f.allTickets(f.a), 1)
	require.Len(t, f.allTickets(f.b), 1)

	f.send(sb.post(event("resolve", "same-key", map[string]any{"body": "b only"})))
	assert.Len(t, f.external(sa, f.ticketFor(sa, "same-key").ID), 1, "tenant b's resolve never reached tenant a's ticket")
	assert.Len(t, f.external(sb, f.ticketFor(sb, "same-key").ID), 2)

	// Tenant b cannot see, rotate or revoke tenant a's source.
	sources, err := f.svc.List(f.b.ctx)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, sb.id, sources[0].ID)
	_, err = f.svc.Rotate(f.b.ctx, f.b.who(), sa.id)
	assert.ErrorIs(t, err, intake.ErrNotFound)
	assert.ErrorIs(t, f.svc.Revoke(f.b.ctx, f.b.who(), sa.id), intake.ErrNotFound)
	assert.Equal(t, http.StatusOK, f.send(sa.post(event("update", "same-key", nil))).Code, "a's source still works")
	ds := f.deliveries(f.b)
	for _, d := range ds {
		assert.Equal(t, sb.id, d.SourceID)
	}
}

// A source's paths are set by a person and a delivery cannot be refused for
// them: the sender would retry, or the alert be lost. So the one-repo rule is
// not applied to what a source files, even with a manifest the paths span.
func TestOpen_OneRepoRuleDoesNotRefuseASourcesTickets(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(func(sp *intake.Spec) { sp.Paths = []string{"app/", "deploy/"} }))
	_, err := manifest.NewPostgresStore(f.database).Put(f.a.ctx, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/app"}, {Path: "deploy", Remote: "github.com/acme/deploy"},
	}, f.a.member)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, f.send(s.post(event("open", "span", nil))).Code)
	got := f.ticketFor(s, "span")
	require.NotNil(t, got)
	assert.Len(t, got.Paths, 2)
}
