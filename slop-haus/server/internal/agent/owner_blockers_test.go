package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// events is the audit lines with the given action, decoded.
func (b *syncBuffer) events(t *testing.T, action string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if m["action"] == action {
			out = append(out, m)
		}
	}
	return out
}

// ownerBoundFixture wires the real resolver into the ticket service the way
// the server does: every rule here lives in queries and constraints, so the
// real stores are the point.
type ownerBoundFixture struct {
	*conductorFixture
}

func newOwnerBoundFixture(t *testing.T) *ownerBoundFixture {
	t.Helper()
	f := newConductorFixture(t)
	roles := tenantRoles{user.NewPostgresStore(f.database)}
	generous := &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
	grants := oauth.NewService(oauth.NewPostgresStore(f.database), f.agents, &oauth.Config{
		Issuer: "http://x", RegisterLimit: generous, DeviceLookupLimit: generous, DeviceStartLimit: generous,
	}, nil, slog.DiscardHandler)
	f.owners.resolver = agent.NewOwnerResolver(f.agents, f.conductors, roles, grants, userLabels{user.NewPostgresStore(f.database)})
	return &ownerBoundFixture{f}
}

type userLabels struct{ store *user.PostgresStore }

func (l userLabels) DisplayName(ctx context.Context, id uuid.UUID) (string, error) {
	u, err := l.store.FindByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.Label(), nil
}

func (f *ownerBoundFixture) console(t *testing.T, name string) *agent.Agent {
	t.Helper()
	created, err := f.agents.CreateKind(f.ctx, name, agent.KindConsole)
	require.NoError(t, err)
	return created
}

func (f *ownerBoundFixture) grant(t *testing.T, a *agent.Agent, approver *authn.User, scope string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var by *uuid.UUID
	if approver != nil {
		by = &approver.ID
	}
	_, err := f.database.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, approved_by, scope)
		VALUES ($1, $2, $3, 'slop-haus-workspace', 'r', $4, $5)`, id, f.tenant, a.ID, by, scope)
	require.NoError(t, err)
	return id
}

func (f *ownerBoundFixture) revoke(t *testing.T, grant uuid.UUID) {
	t.Helper()
	_, err := f.database.Global().Exec(context.Background(), `UPDATE global_oauth_grants SET revoked_at = now() WHERE id = $1`, grant)
	require.NoError(t, err)
}

func (f *ownerBoundFixture) ticket(t *testing.T, title string) *ticket.Ticket {
	t.Helper()
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: title, Status: ticket.StatusOpen})
	require.NoError(t, err)
	return created
}

// heldBy is a ticket another agent is working.
func (f *ownerBoundFixture) heldBy(t *testing.T, title string) (*ticket.Ticket, *agent.Agent) {
	t.Helper()
	worker, err := f.agents.Create(f.ctx, "worker-"+uuid.NewString()[:8])
	require.NoError(t, err)
	held := f.ticket(t, title)
	_, err = f.tickets.Claim(f.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	return held, worker
}

func (f *ownerBoundFixture) email(t *testing.T, u *authn.User) string {
	t.Helper()
	var email string
	require.NoError(t, f.database.Global().QueryRow(context.Background(), `SELECT email FROM global_users WHERE id = $1`, u.ID).Scan(&email))
	return email
}

func (f *ownerBoundFixture) setDisplayName(t *testing.T, u *authn.User, name string) {
	t.Helper()
	_, err := f.database.Global().Exec(context.Background(), `UPDATE global_users SET display_name = $2 WHERE id = $1`, u.ID, name)
	require.NoError(t, err)
}

func (f *ownerBoundFixture) comments(t *testing.T, id int64) []ticket.Comment {
	t.Helper()
	got, err := f.tickets.ListComments(f.ctx, id)
	require.NoError(t, err)
	return got
}

func TestOwnerBound_ConductorAddsAndRemovesABlockerOnATicketAnotherAgentHolds(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	held, worker := f.heldBy(t, "Being built")
	blocker := f.ticket(t, "Land first")

	change, err := f.tickets.AgentAddDependency(f.ctx, c.ID, held.ID, blocker.ID)

	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Equal(t, []int64{blocker.ID}, change.Ticket.DependsOn)
	assert.Equal(t, ticket.StatusInProgress, change.Ticket.Status, "the holder keeps the lease: blockers gate claims")
	lease, err := f.tickets.LeaseFor(f.ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, lease, "the worker still holds it")

	change, err = f.tickets.AgentRemoveDependency(f.ctx, c.ID, held.ID, blocker.ID)

	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Empty(t, change.Ticket.DependsOn)
}

func TestOwnerBound_AddingTheFirstBlockerBlocksAnOpenTicketAndRemovingTheLastUnblocksIt(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	tk, blocker := f.ticket(t, "Waits"), f.ticket(t, "Blocker")

	change, err := f.tickets.AgentAddDependency(f.ctx, c.ID, tk.ID, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, change.Ticket.Status)

	change, err = f.tickets.AgentRemoveDependency(f.ctx, c.ID, tk.ID, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, change.Ticket.Status)
}

func TestOwnerBound_AttributionNamesTheAgentAndTheUserInCommentAndAudit(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "my-conductor")
	other := f.register(t, f.other, "their-conductor")
	tk, blocker := f.ticket(t, "Waits"), f.ticket(t, "Blocker")
	f.setDisplayName(t, f.owner, "Olive Owner")
	// f.other has no display name: the comment must not fall back to the address.
	ownerLabel := map[uuid.UUID]string{f.owner.ID: "Olive Owner", f.other.ID: "a member"}

	_, err := f.tickets.AgentAddDependency(f.ctx, c.ID, tk.ID, blocker.ID)
	require.NoError(t, err)
	_, err = f.tickets.AgentRemoveDependency(f.ctx, c.ID, tk.ID, blocker.ID)
	require.NoError(t, err)
	_, err = f.tickets.AgentAddDependency(f.ctx, other.ID, tk.ID, blocker.ID)
	require.NoError(t, err)

	comments := f.comments(t, tk.ID)
	require.Len(t, comments, 3)
	for i, want := range []struct {
		agent *agent.Agent
		user  *authn.User
		text  string
	}{{c, f.owner, "Blocked on #"}, {c, f.owner, "No longer blocked on #"}, {other, f.other, "Blocked on #"}} {
		require.NotNil(t, comments[i].AuthorAgentID)
		assert.Equal(t, want.agent.ID, *comments[i].AuthorAgentID)
		assert.Nil(t, comments[i].AuthorUserID, "the comment is the agent's; the person is named in it")
		assert.Contains(t, comments[i].Body, want.text)
		assert.Contains(t, comments[i].Body, "by agent "+want.agent.Name+" for "+ownerLabel[want.user.ID])
		assert.NotContains(t, comments[i].Body, f.email(t, want.user), "an address never goes in a ticket comment")
	}

	added := f.auditLog.events(t, "owner_bound_blocker_changed")
	require.Len(t, added, 3)
	assert.Equal(t, map[string]any{
		"agent_id": c.ID.String(), "on_behalf_of_user_id": f.owner.ID.String(), "tenant_id": f.tenant.String(),
		"ticket": float64(tk.ID), "blocker": float64(blocker.ID), "op": "add",
	}, pick(added[0], "agent_id", "on_behalf_of_user_id", "tenant_id", "ticket", "blocker", "op"))
	assert.Equal(t, "remove", added[1]["op"])
	assert.Equal(t, f.other.ID.String(), added[2]["on_behalf_of_user_id"], "each acts for its own owner")
}

func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func TestOwnerBound_ANoOpChangeIsNotAudited(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	tk, blocker := f.ticket(t, "Waits"), f.ticket(t, "Blocker")

	change, err := f.tickets.AgentRemoveDependency(f.ctx, c.ID, tk.ID, blocker.ID)

	require.NoError(t, err)
	assert.False(t, change.Changed)
	assert.Empty(t, f.auditLog.events(t, "owner_bound_blocker_changed"))
}

func TestOwnerBound_ConductorRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(t *testing.T, f *ownerBoundFixture, c *agent.Agent)
	}{
		"disabled": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			_, err := f.conductors.SetDisabled(f.ctx, f.owner, c.ID, true)
			require.NoError(t, err)
		}},
		// Cleared again below so it is the membership check that refuses,
		// not the disabling that removing an owner does.
		"owner left the tenant": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.removeMember(t, f.owner)
			_, err := f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = NULL WHERE id = $1`, c.ID)
			require.NoError(t, err)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerBoundFixture(t)
			c := f.register(t, f.owner, "conductor")
			held, _ := f.heldBy(t, "Held")
			blocker := f.ticket(t, "Blocker")
			tc.prepare(t, f, c)

			_, err := f.tickets.AgentAddDependency(f.ctx, c.ID, held.ID, blocker.ID)

			assert.ErrorIs(t, err, ticket.ErrOwnerBoundRefused)
			after, err := f.tickets.Get(f.ctx, held.ID)
			require.NoError(t, err)
			assert.Empty(t, after.DependsOn)
			assert.Empty(t, f.auditLog.events(t, "owner_bound_blocker_changed"))
			assert.Len(t, f.auditLog.events(t, "owner_bound_blocker_refused"), 1)
		})
	}
}

func TestOwnerBound_ConsoleNeedsExactlyOnePersonsLiveFullAccessGrant(t *testing.T) {
	type setup func(t *testing.T, f *ownerBoundFixture, c *agent.Agent)
	for name, tc := range map[string]struct {
		prepare setup
		allowed bool
		user    func(f *ownerBoundFixture) *authn.User
	}{
		"live grant": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "agent")
		}, true, func(f *ownerBoundFixture) *authn.User { return f.owner }},
		"two live grants from the same person": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "agent")
			f.grant(t, c, f.owner, "agent")
		}, true, func(f *ownerBoundFixture) *authn.User { return f.owner }},
		"a read-only grant beside the full one is not counted": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "agent")
			f.grant(t, c, f.other, "read")
		}, true, func(f *ownerBoundFixture) *authn.User { return f.owner }},
		"no grant": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {}, false, nil},
		"revoked grant": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.revoke(t, f.grant(t, c, f.owner, "agent"))
		}, false, nil},
		"read-only grant only": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "read")
		}, false, nil},
		"ambiguous: two people approved live grants": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "agent")
			f.grant(t, c, f.other, "agent")
		}, false, nil},
		"grant with no recorded approver": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, nil, "agent")
		}, false, nil},
		"approver left the tenant": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "agent")
			f.removeMember(t, f.owner)
		}, false, nil},
		"disabled": {func(t *testing.T, f *ownerBoundFixture, c *agent.Agent) {
			f.grant(t, c, f.owner, "agent")
			_, err := f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, c.ID)
			require.NoError(t, err)
		}, false, nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerBoundFixture(t)
			c := f.console(t, "console")
			held, _ := f.heldBy(t, "Held")
			blocker := f.ticket(t, "Blocker")
			tc.prepare(t, f, c)

			change, err := f.tickets.AgentAddDependency(f.ctx, c.ID, held.ID, blocker.ID)

			if !tc.allowed {
				assert.ErrorIs(t, err, ticket.ErrOwnerBoundRefused)
				return
			}
			require.NoError(t, err)
			assert.True(t, change.Changed)
			events := f.auditLog.events(t, "owner_bound_blocker_changed")
			require.Len(t, events, 1)
			assert.Equal(t, tc.user(f).ID.String(), events[0]["on_behalf_of_user_id"])
		})
	}
}

// A console flag turned off leaves a plain worker, whose rule is the old one,
// even while a grant is live.
func TestOwnerBound_AgentThatIsNeitherAConsoleNorAConductorKeepsTheWorkerRule(t *testing.T) {
	f := newOwnerBoundFixture(t)
	w, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	f.grant(t, w, f.owner, "agent")
	held, _ := f.heldBy(t, "Someone else's")
	mine := f.ticket(t, "Mine")
	_, err = f.tickets.Claim(f.ctx, w.ID, &mine.ID)
	require.NoError(t, err)
	blocker := f.ticket(t, "Blocker")

	_, err = f.tickets.AgentAddDependency(f.ctx, w.ID, held.ID, blocker.ID)
	assert.ErrorIs(t, err, ticket.ErrBlockersNotEditable, "not the ticket it holds, nor one it filed")
	_, err = f.tickets.AgentAddDependency(f.ctx, w.ID, mine.ID, blocker.ID)
	require.NoError(t, err, "its own held ticket, as before")
	assert.Empty(t, f.auditLog.events(t, "owner_bound_blocker_changed"), "a worker's edit is not an owner-bound one")
	comments := f.comments(t, mine.ID)
	require.Len(t, comments, 1)
	assert.NotContains(t, comments[0].Body, " for ")

	_, err = f.tickets.AgentAddDependency(f.ctx, w.ID, f.ticket(t, "Not filed by it").ID, blocker.ID)
	assert.ErrorIs(t, err, ticket.ErrBlockersNotEditable)
}

func TestOwnerBound_ARemovalOfAnEdgeAHumanAddedIsAllowed(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	tk, blocker := f.ticket(t, "Waits"), f.ticket(t, "Blocker")
	_, err := f.tickets.AddDependency(f.ctx, ticket.UserAuthor(f.owner.ID), tk.ID, blocker.ID)
	require.NoError(t, err)

	change, err := f.tickets.AgentRemoveDependency(f.ctx, c.ID, tk.ID, blocker.ID)

	require.NoError(t, err)
	assert.True(t, change.Changed)
}

func TestOwnerBound_AnyStatusIsAllowed(t *testing.T) {
	for _, status := range []ticket.Status{ticket.StatusPreparing, ticket.StatusBlocked, ticket.StatusDiscussion, ticket.StatusReview, ticket.StatusParked, ticket.StatusDone, ticket.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			f := newOwnerBoundFixture(t)
			c := f.register(t, f.owner, "conductor")
			tk, blocker := f.ticket(t, "Target"), f.ticket(t, "Blocker")
			_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = $2 WHERE id = $1`, tk.ID, string(status))
			require.NoError(t, err)

			change, err := f.tickets.AgentAddDependency(f.ctx, c.ID, tk.ID, blocker.ID)

			require.NoError(t, err)
			assert.True(t, change.Changed)
			assert.Equal(t, []int64{blocker.ID}, change.Ticket.DependsOn)
			persisted, err := f.tickets.Get(f.ctx, tk.ID)
			require.NoError(t, err)
			assert.Equal(t, []int64{blocker.ID}, persisted.DependsOn)
			assert.Len(t, f.comments(t, tk.ID), 1)
		})
	}
}

// Ticket numbers are per tenant, so another tenant's ticket cannot be named
// at all: a number is read in the caller's tenant, and one that exists only
// elsewhere is not found.
func TestOwnerBound_AnotherTenantsTicketIsNeverReachedOrChanged(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	mine := f.ticket(t, "Mine")
	elsewhere := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	var theirs []*ticket.Ticket
	for _, title := range []string{"Theirs 1", "Theirs 2", "Theirs 3"} {
		created, err := f.tickets.Create(elsewhere, &ticket.CreateParams{Title: title, Status: ticket.StatusOpen})
		require.NoError(t, err)
		theirs = append(theirs, created)
	}
	onlyTheirs := theirs[2].ID
	require.NotEqual(t, mine.ID, onlyTheirs)

	_, err := f.tickets.AgentAddDependency(f.ctx, c.ID, onlyTheirs, mine.ID)
	assert.ErrorIs(t, err, ticket.ErrNotFound, "the ticket")
	_, err = f.tickets.AgentAddDependency(f.ctx, c.ID, mine.ID, onlyTheirs)
	assert.ErrorIs(t, err, ticket.ErrNotFound, "the blocker")

	// A number both tenants have is read in the caller's own.
	other := f.ticket(t, "Mine too")
	require.Equal(t, theirs[1].ID, other.ID)
	_, err = f.tickets.AgentAddDependency(f.ctx, c.ID, other.ID, mine.ID)
	require.NoError(t, err)
	for _, tk := range theirs {
		after, err := f.tickets.Get(elsewhere, tk.ID)
		require.NoError(t, err)
		assert.Empty(t, after.DependsOn)
		assert.Equal(t, ticket.StatusOpen, after.Status)
	}
}

func TestOwnerBound_StillRefusesACycle(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	a, b := f.ticket(t, "A"), f.ticket(t, "B")
	_, err := f.tickets.AgentAddDependency(f.ctx, c.ID, a.ID, b.ID)
	require.NoError(t, err)

	_, err = f.tickets.AgentAddDependency(f.ctx, c.ID, b.ID, a.ID)
	assert.ErrorIs(t, err, ticket.ErrDependencyCycle)
	_, err = f.tickets.AgentAddDependency(f.ctx, c.ID, a.ID, a.ID)
	assert.ErrorIs(t, err, ticket.ErrDependencyCycle)
}

// The direct tier is blockers and nothing else: what the person keeps
// (approvals, review decisions, requires_approval, scopes, who holds a ticket)
// is untouched by it.
func TestOwnerBound_ChangesNothingButTheBlockerAndItsComment(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	held, worker := f.heldBy(t, "Held")
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET requires_approval = true WHERE id = $1`, held.ID)
	require.NoError(t, err)
	blocker := f.ticket(t, "Blocker")
	snapshot := func() map[string]any {
		var row map[string]any
		var title, body, status, priority string
		var requires, needs bool
		var holder uuid.UUID
		var paths int
		require.NoError(t, f.database.QueryRow(f.ctx, `
			SELECT t.title, t.body, t.status, t.priority, t.requires_approval, t.needs_discussion, l.agent_id,
			       (SELECT count(*) FROM ticket_paths p WHERE p.ticket_id = t.id)
			  FROM tickets t JOIN leases l ON l.ticket_id = t.id WHERE t.id = $1`, held.ID).
			Scan(&title, &body, &status, &priority, &requires, &needs, &holder, &paths))
		row = map[string]any{"title": title, "body": body, "status": status, "priority": priority,
			"requires": requires, "needs": needs, "holder": holder, "paths": paths}
		return row
	}
	before := snapshot()

	_, err = f.tickets.AgentAddDependency(f.ctx, c.ID, held.ID, blocker.ID)
	require.NoError(t, err)
	_, err = f.tickets.AgentRemoveDependency(f.ctx, c.ID, held.ID, blocker.ID)
	require.NoError(t, err)

	assert.Equal(t, before, snapshot())
	assert.Equal(t, worker.ID, before["holder"])
	assert.Len(t, f.comments(t, held.ID), 2, "the only other trace is the two blocker comments")
}

// Author's exported fields are all a caller outside the ticket package can
// set. The marker for an owner-bound edit is not one of them, so no other
// package can claim the direct blocker powers by building an Author.
func TestOwnerBound_AuthorCannotBeForgedFromOutsideTheTicketPackage(t *testing.T) {
	typ := reflect.TypeOf(ticket.Author{})
	var exported []string
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).IsExported() {
			exported = append(exported, typ.Field(i).Name)
		}
	}
	sort.Strings(exported)
	assert.Equal(t, []string{"AgentID", "UserID"}, exported,
		"a new exported Author field must not be able to widen what an agent's blocker edit may do")

	// The agent-facing forms are the only ones that resolve an owner.
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "conductor")
	held, _ := f.heldBy(t, "Held")
	blocker := f.ticket(t, "Blocker")
	_, err := f.tickets.AddDependency(f.ctx, ticket.AgentAuthor(c.ID), held.ID, blocker.ID)
	assert.ErrorIs(t, err, ticket.ErrBlockersNotEditable, "the generic entry point stays the worker rule, even for a conductor")
}
