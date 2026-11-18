package manifest_test

import (
	"context"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const monoRemote = "github.com/acme/mono"

func (f *tenantFixture) adopt(repos ...manifest.Repo) {
	f.t.Helper()
	_, err := manifest.NewPostgresStore(f.db).Put(f.ctx, repos, f.owner.ID)
	require.NoError(f.t, err)
}

func (f *tenantFixture) newTicket(title string, paths ...string) *ticket.Ticket {
	f.t.Helper()
	ps := make([]model.Path, len(paths))
	for i, p := range paths {
		ps[i] = model.Path{Path: p}
	}
	tk, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: title, Paths: ps})
	require.NoError(f.t, err)
	return tk
}

func (f *tenantFixture) pathsOf(id int64) []string {
	f.t.Helper()
	tk, err := f.tickets.Get(f.ctx, id)
	require.NoError(f.t, err)
	out := make([]string, len(tk.Paths))
	for i, p := range tk.Paths {
		out[i] = p.Path
	}
	sort.Strings(out)
	return out
}

func (f *tenantFixture) intakeSource(name string, paths ...string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(f.ctx, `
		INSERT INTO intake_sources (id, name, adapter, trust, auth_kind, default_status, default_priority, max_priority, paths)
		VALUES ($1, $2, 'native', 'trusted', 'bearer', 'open', 'normal', 'normal', $3)`, id, name, paths)
	require.NoError(f.t, err)
	return id
}

func (f *tenantFixture) intakePaths(id uuid.UUID) []string {
	f.t.Helper()
	var paths []string
	require.NoError(f.t, f.db.QueryRow(f.ctx, `SELECT paths FROM intake_sources WHERE id = $1`, id).Scan(&paths))
	return paths
}

func (f *tenantFixture) manifestPaths() []string {
	f.t.Helper()
	m, err := f.svc.Get(f.ctx)
	require.NoError(f.t, err)
	var out []string
	for _, r := range m.Repos {
		out = append(out, r.Path)
	}
	return out
}

func (f *tenantFixture) cancel(id int64) {
	f.t.Helper()
	st := ticket.StatusCancelled
	_, err := f.tickets.Update(f.ctx, id, &ticket.UpdateParams{Status: &st})
	require.NoError(f.t, err)
}

func TestMove_RewritesNonTerminalTicketsAndIntakeAndNothingElse(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote, BaseBranch: "slop-haus-x"})

	open := a.newTicket("open", "old/slop-haus/server/", "old/docs/readme.md")
	// A second attempt shares the ticket's scopes, so is covered with them.
	_, err := a.tickets.AddAttempt(a.ctx, a.owner.ID, open.ID, nil)
	require.NoError(t, err)
	dead := a.newTicket("cancelled", "old/slop-haus/old/")
	a.cancel(dead.ID)
	src := a.intakeSource("alerts", "old/slop-haus/deploy/")

	plan, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"mono"}, a.manifestPaths())
	m, _ := a.svc.Get(a.ctx)
	assert.Equal(t, "slop-haus-x", m.Repos[0].BaseBranch, "the repo's other settings move with it")
	assert.Equal(t, []string{"mono/docs/readme.md", "mono/slop-haus/server/"}, a.pathsOf(open.ID))
	assert.Equal(t, []string{"old/slop-haus/old/"}, a.pathsOf(dead.ID), "a terminal ticket keeps the scope it finished with")
	assert.Equal(t, []string{"mono/slop-haus/deploy/"}, a.intakePaths(src))

	require.Len(t, plan.Tickets, 1)
	assert.Equal(t, open.ID, plan.Tickets[0].ID)
	require.Len(t, plan.IntakeSources, 1)

	comments, err := a.tickets.ListComments(a.ctx, open.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, a.owner.ID, *comments[0].AuthorUserID, "recorded as the owner who moved it")
	assert.Contains(t, comments[0].Body, "`old/slop-haus/server/` -> `mono/slop-haus/server/`")
}

func TestMove_DryRunListsEverythingAndChangesNothing(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	tk := a.newTicket("t", "old/slop-haus/x.go")
	src := a.intakeSource("s", "old/slop-haus/")

	plan, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, true)
	require.NoError(t, err)

	assert.Equal(t, []manifest.MovedPath{{From: "old/slop-haus/x.go", To: "mono/slop-haus/x.go"}}, plan.Tickets[0].Paths)
	assert.Equal(t, []manifest.MovedPath{{From: "old/slop-haus/", To: "mono/slop-haus/"}}, plan.IntakeSources[0].Paths)
	assert.Equal(t, "mono", plan.Repos[0].Path)
	assert.Equal(t, []string{"old"}, a.manifestPaths())
	assert.Equal(t, []string{"old/slop-haus/x.go"}, a.pathsOf(tk.ID))
	assert.Equal(t, []string{"old/slop-haus/"}, a.intakePaths(src))
	comments, err := a.tickets.ListComments(a.ctx, tk.ID)
	require.NoError(t, err)
	assert.Empty(t, comments)
}

// The point of rewriting in one step: scopes that exclude each other before
// the move still do after it.
func TestMove_TicketsThatConflictedStillConflict(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	wide := a.newTicket("wide", "old/slop-haus/server/")
	narrow := a.newTicket("narrow", "old/slop-haus/server/internal/x.go")
	other := a.newTicket("other", "old/slop-haus/frontend/")

	_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"mono/slop-haus/server/"}, a.pathsOf(wide.ID))
	assert.True(t, scope.Conflicts(a.pathsOf(wide.ID)[0], a.pathsOf(narrow.ID)[0]))
	assert.False(t, scope.Conflicts(a.pathsOf(wide.ID)[0], a.pathsOf(other.ID)[0]))
}

func TestMove_LeavesNestedReposTickets(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote}, manifest.Repo{Path: "deploy", Remote: "github.com/acme/deploy"})
	inDeploy := a.newTicket("deploy", "deploy/envs/")
	inRepo := a.newTicket("moved", "old/app/")

	_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"deploy/envs/"}, a.pathsOf(inDeploy.ID))
	assert.Equal(t, []string{"mono/app/"}, a.pathsOf(inRepo.ID))
}

func TestMove_RefusedWhileATicketUnderTheRepoIsLeased(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote}, manifest.Repo{Path: "deploy", Remote: "github.com/acme/deploy"})
	worker := a.newAgent("worker", nil)
	held := a.newTicket("held", "old/slop-haus/x/")
	idle := a.newTicket("idle", "old/slop-haus/y/")
	elsewhere := a.newTicket("elsewhere", "deploy/z/")
	_, err := a.tickets.Claim(a.ctx, worker.ID, &held.ID)
	require.NoError(t, err)

	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	var blocked *manifest.MoveBlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, []manifest.MoveBlocker{{TicketID: held.ID, Title: "held", Reason: manifest.BlockerLease}}, blocked.Blockers)
	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, true)
	require.ErrorIs(t, err, manifest.ErrMoveBlocked, "a dry run says so too")
	assert.Equal(t, []string{"old/slop-haus/y/"}, a.pathsOf(idle.ID), "nothing was rewritten")
	assert.Equal(t, []string{"old", "deploy"}, a.manifestPaths())

	// A lease on a ticket of another repo does not stop this one.
	_, err = a.tickets.Release(a.ctx, worker.ID, "done for now", nil)
	require.NoError(t, err)
	_, err = a.tickets.Claim(a.ctx, worker.ID, &elsewhere.ID)
	require.NoError(t, err)
	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err)
}

func TestMove_RefusedWhileATicketUnderTheRepoHoldsTheIntegrationLock(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	worker := a.newAgent("worker", nil)
	held := a.newTicket("held", "old/slop-haus/x/")
	_, err := a.tickets.Claim(a.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	require.NoError(t, a.tickets.AcquireIntegrationLock(a.ctx, worker.ID))

	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	var blocked *manifest.MoveBlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, manifest.BlockerLock, blocked.Blockers[0].Reason)
	assert.Contains(t, err.Error(), "#")
}

func TestMove_RefusesWhatTheManifestWouldNot(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "app", Remote: monoRemote}, manifest.Repo{Path: "deploy", Remote: "github.com/acme/deploy"})

	for name, req := range map[string]manifest.MoveRequest{
		"unknown repo":   {From: "nope", To: "x"},
		"collision":      {From: "app", To: "deploy"},
		"same path":      {From: "app", To: "app"},
		"nested path":    {From: "app", To: "a/b"},
		"climbing path":  {From: "app", To: ".."},
		"empty new path": {From: "app", To: ""},
	} {
		_, err := a.svc.Move(a.ctx, a.owner, req, false)
		assert.ErrorIs(t, err, manifest.ErrInvalid, name)
	}
	assert.Equal(t, []string{"app", "deploy"}, a.manifestPaths())

	_, err := a.svc.Move(a.ctx, a.member, manifest.MoveRequest{From: "app", To: "x"}, false)
	assert.ErrorIs(t, err, manifest.ErrNotOwner)
}

func TestMove_NeedsAManifest(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "x"}, false)
	assert.ErrorIs(t, err, manifest.ErrInvalid)
}

func TestMove_AnotherTenantsTicketsAreOutOfReach(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	b.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	other := b.newTicket("b", "old/slop-haus/x/")

	_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"old/slop-haus/x/"}, b.pathsOf(other.ID))
}

func TestMoveRoute(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	worker := a.newAgent("worker", nil)
	tk := a.newTicket("t", "old/slop-haus/x/")

	rec := call(a.server(a.member), http.MethodPost, "/api/workspace/manifest/move", `{"from_path":"old","to_path":"mono"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = call(a.server(a.owner), http.MethodPost, "/api/workspace/manifest/move", `{"from_path":"old","to_path":"mono","dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"applied":false`)
	assert.Contains(t, rec.Body.String(), `mono/slop-haus/x/`)

	_, err := a.tickets.Claim(a.ctx, worker.ID, &tk.ID)
	require.NoError(t, err)
	rec = call(a.server(a.owner), http.MethodPost, "/api/workspace/manifest/move", `{"from_path":"old","to_path":"mono"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "#")

	rec = call(a.server(a.owner), http.MethodPost, "/api/workspace/manifest/move", `{"from_path":"nope","to_path":"x"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestMove_ADoneTicketKeepsItsScopeToo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	done := a.newTicket("done", "old/slop-haus/old/")
	st := ticket.StatusDone
	_, err := a.tickets.Update(a.ctx, done.ID, &ticket.UpdateParams{Status: &st})
	require.NoError(t, err)

	plan, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err)
	assert.Empty(t, plan.Tickets)
	assert.Equal(t, []string{"old/slop-haus/old/"}, a.pathsOf(done.ID))
}

func TestMove_ALeaseThatExpiredDoesNotHoldTheMove(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	worker := a.newAgent("worker", nil)
	held := a.newTicket("held", "old/slop-haus/x/")
	_, err := a.tickets.Claim(a.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	_, err = a.db.Exec(a.ctx, `UPDATE leases SET expires_at = now() - interval '1 hour' WHERE ticket_id = $1`, held.ID)
	require.NoError(t, err)

	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.NoError(t, err, "the ticket package no longer counts it as held, and neither does the move")
}

func TestMove_RefusesToOverlapAnotherReposTickets(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote}, manifest.Repo{Path: "app", Remote: "github.com/acme/app"})
	rootTicket := a.newTicket("root", "new/x/")
	inApp := a.newTicket("app", "app/x/")

	_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "app", To: "new"}, false)
	require.ErrorIs(t, err, manifest.ErrInvalid)
	assert.Contains(t, err.Error(), "overlap")
	assert.Equal(t, []string{"app/x/"}, a.pathsOf(inApp.ID))
	assert.Equal(t, []string{"new/x/"}, a.pathsOf(rootTicket.ID))
}

// A write that fails late undoes the early ones.
func TestMove_ALateFailureLeavesNothingChanged(t *testing.T) {
	// As the schema's owner, which alone may add a trigger.
	owner, err := db.New(context.Background(), dbtest.WithOwnerDSN(t))
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	a := (&env{t: t, database: owner}).tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	tk := a.newTicket("t", "old/slop-haus/x/")
	src := a.intakeSource("s", "old/slop-haus/")
	// The comment is written after the paths and intake sources.
	_, err = a.db.Global().Exec(context.Background(), `
		CREATE FUNCTION refuse_comment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'no comments'; END $$;
		CREATE TRIGGER refuse_comment BEFORE INSERT ON ticket_comments FOR EACH ROW EXECUTE FUNCTION refuse_comment();`)
	require.NoError(t, err)

	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
	require.Error(t, err)
	assert.Equal(t, []string{"old"}, a.manifestPaths())
	assert.Equal(t, []string{"old/slop-haus/x/"}, a.pathsOf(tk.ID))
	assert.Equal(t, []string{"old/slop-haus/"}, a.intakePaths(src))
}

// A status change that wins the ticket's row before the move reads it is
// seen: the move leaves a ticket that has gone terminal as it finished.
func TestMove_ATicketCancelledWhileTheMoveWaitsIsLeftAlone(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "old", Remote: monoRemote})
	racing := a.newTicket("racing", "old/slop-haus/x/")
	other := a.newTicket("other", "old/slop-haus/y/")

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.db.InTx(a.ctx, func(q db.Querier) error {
			if _, err := q.Exec(a.ctx, `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, racing.ID); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	moved := make(chan error, 1)
	go func() {
		_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "old", To: "mono"}, false)
		moved <- err
	}()
	// Let go only once the move is queued behind the ticket's row lock.
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, a.db.Global().QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%FOR UPDATE%'`).Scan(&waiting))
		return waiting > 0
	}, 10*time.Second, 10*time.Millisecond)
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-moved)

	assert.Equal(t, []string{"old/slop-haus/x/"}, a.pathsOf(racing.ID), "it was cancelled before the move read it")
	assert.Equal(t, []string{"mono/slop-haus/y/"}, a.pathsOf(other.ID))
}
