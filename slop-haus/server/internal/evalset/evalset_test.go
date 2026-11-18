package evalset_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/evalset"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	prHost  = "github.com"
	headOne = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type placedIn struct{ repos []scope.WorkspaceRepo }

func (p *placedIn) Place(context.Context, []string) (*ticket.Placement, error) {
	return &ticket.Placement{Declared: true, All: p.repos, Repos: p.repos}, nil
}

// tenant is one tenant with a real ticket service (replays served by a fake
// code host) and an eval set service over it.
type tenant struct {
	t       *testing.T
	db      *db.DB
	ctx     context.Context
	who     *authn.User
	tickets *ticket.Service
	agents  *agent.Service
	svc     *evalset.Service
	store   *evalset.PostgresStore
}

func newTenant(t *testing.T, database *db.DB, batchMax int, opts ...ticket.ServiceOption) *tenant {
	t.Helper()
	id := dbtest.NewTenant(t, database)
	host := codehosttest.NewLinkHost(prHost)
	host.HeadOf = map[codehost.Repo]*codehost.BranchHead{{Owner: "acme", Name: "app"}: {Branch: "main", SHA: headOne}}
	ws := &placedIn{repos: []scope.WorkspaceRepo{{Path: "/", Remote: "github.com/acme/app"}}}
	opts = append([]ticket.ServiceOption{
		ticket.WithCodeHosts(codehosttest.Registry(host)), ticket.WithWorkspace(ws), ticket.WithReplayBatchMax(batchMax),
	}, opts...)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler, opts...)
	store := evalset.NewPostgresStore(database)
	return &tenant{
		t: t, db: database, ctx: db.WithTenant(context.Background(), id),
		who:     &authn.User{ID: dbtest.NewUser(t, database), TenantID: id},
		tickets: tickets, agents: agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler),
		svc: evalset.NewService(store, tickets, true, slog.DiscardHandler), store: store,
	}
}

func (n *tenant) create(title string) *ticket.Ticket {
	n.t.Helper()
	tk, err := n.tickets.Create(n.ctx, &ticket.CreateParams{Title: title, Body: "body", Paths: []model.Path{{Path: "a/"}}})
	require.NoError(n.t, err)
	return tk
}

// done is a ticket that can be replayed: claimed and finished by an agent.
func (n *tenant) done(title string) *ticket.Ticket {
	n.t.Helper()
	tk := n.create(title)
	a, err := n.agents.Create(n.ctx, "agent-"+uuid.NewString()[:8])
	require.NoError(n.t, err)
	claimed, err := n.tickets.Claim(n.ctx, a.ID, nil)
	require.NoError(n.t, err)
	require.Equal(n.t, tk.ID, claimed.Ticket.ID)
	_, err = n.tickets.Done(n.ctx, a.ID, &ticket.DoneParams{Summary: "Done."})
	require.NoError(n.t, err)
	return tk
}

func (n *tenant) replays(ticketID int64) int {
	n.t.Helper()
	var c int
	require.NoError(n.t, n.db.QueryRow(n.ctx, `SELECT count(*) FROM attempts WHERE ticket_id = $1 AND replay`, ticketID).Scan(&c))
	return c
}

func (n *tenant) pinned(name string, ids ...int64) *evalset.Set {
	n.t.Helper()
	set, err := n.svc.Create(n.ctx, n.who, &evalset.Definition{Name: name, TicketIDs: append([]int64{}, ids...)})
	require.NoError(n.t, err)
	return set
}

var defaultVariant = []*uuid.UUID{nil}

func TestStore_TenantsDoNotSeeEachOthersSets(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newTenant(t, database, 10), newTenant(t, database, 10)
	set := a.pinned("bench", 1)

	listed, err := b.svc.List(b.ctx, b.who)
	require.NoError(t, err)
	assert.Empty(t, listed)
	_, err = b.svc.Get(b.ctx, b.who, set.ID)
	assert.ErrorIs(t, err, evalset.ErrNotFound)
	_, err = b.svc.Replace(b.ctx, b.who, set.ID, &evalset.Definition{Name: "stolen", TicketIDs: []int64{}})
	assert.ErrorIs(t, err, evalset.ErrNotFound)
	assert.ErrorIs(t, b.svc.Delete(b.ctx, b.who, set.ID), evalset.ErrNotFound)
	_, err = b.svc.History(b.ctx, b.who, set.ID)
	assert.ErrorIs(t, err, evalset.ErrNotFound)

	other := b.pinned("bench", 2)
	assert.NotEqual(t, set.ID, other.ID, "names are unique per tenant, not across them")
	still, err := a.svc.Get(a.ctx, a.who, set.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, still.TicketIDs)
}

func TestStore_AFilterOrAPinnedListNeverBothAndNeverNeither(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	for name, def := range map[string]*evalset.Definition{
		"both":    {Name: "x", Filter: &evalset.Filter{}, TicketIDs: []int64{1}},
		"neither": {Name: "x"},
	} {
		_, err := n.svc.Create(n.ctx, n.who, def)
		assert.ErrorIs(t, err, evalset.ErrInvalid, name)
	}
	// The table says the same to a writer that skips the service.
	_, err := n.db.Exec(n.ctx, `INSERT INTO eval_sets (name, filter, ticket_ids) VALUES ('raw', '{}', '{1}')`)
	assert.Error(t, err)
	_, err = n.db.Exec(n.ctx, `INSERT INTO eval_sets (name) VALUES ('raw')`)
	assert.Error(t, err)
}

func TestStore_NamesAreUniquePerTenant(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	first := n.pinned("bench")
	_, err := n.svc.Create(n.ctx, n.who, &evalset.Definition{Name: " bench ", TicketIDs: []int64{}})
	assert.ErrorIs(t, err, evalset.ErrNameTaken)
	second := n.pinned("other")

	_, err = n.svc.Replace(n.ctx, n.who, second.ID, &evalset.Definition{Name: "bench", TicketIDs: []int64{}})
	assert.ErrorIs(t, err, evalset.ErrNameTaken)
	_, err = n.svc.Replace(n.ctx, n.who, first.ID, &evalset.Definition{Name: "bench", Description: "now described", TicketIDs: []int64{}})
	require.NoError(t, err, "keeping its own name is not a clash")
}

func TestPinnedList_SurvivesLaterTicketEdits(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	tk := n.create("Original title")
	set := n.pinned("bench", tk.ID, tk.ID)
	assert.Equal(t, []int64{tk.ID}, set.TicketIDs, "duplicates are dropped")

	title := "Edited title"
	_, err := n.tickets.Edit(n.ctx, tk.ID, n.who.ID, &ticket.EditParams{Title: &title})
	require.NoError(t, err)
	_, err = n.tickets.Update(n.ctx, tk.ID, &ticket.UpdateParams{Paths: []model.Path{{Path: "b/"}}})
	require.NoError(t, err)

	got, err := n.svc.Get(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{tk.ID}, got.TicketIDs)
	ids, err := n.svc.Resolve(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{tk.ID}, ids)
}

func TestPinMatches_ResolvesTheFilterOnceAndKeepsTheResult(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	first, second := n.create("Alpha parser"), n.create("Beta parser")
	n.create("Unrelated")
	set, err := n.svc.Create(n.ctx, n.who, &evalset.Definition{Name: "parsers", Filter: &evalset.Filter{Q: "parser"}})
	require.NoError(t, err)

	pinned, err := n.svc.PinMatches(n.ctx, n.who, set.ID)

	require.NoError(t, err)
	assert.Nil(t, pinned.Filter)
	assert.ElementsMatch(t, []int64{first.ID, second.ID}, pinned.TicketIDs)
	n.create("Gamma parser")
	ids, err := n.svc.Resolve(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{first.ID, second.ID}, ids, "a later match is not in the pinned set")
}

func TestFilterSet_FollowsTheBoardUntilPinned(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	set, err := n.svc.Create(n.ctx, n.who, &evalset.Definition{Name: "parsers", Filter: &evalset.Filter{Q: "parser"}})
	require.NoError(t, err)
	ids, err := n.svc.Resolve(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Empty(t, ids)

	tk := n.create("A parser")

	ids, err = n.svc.Resolve(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{tk.ID}, ids)
}

func TestAnAgentIsRefusedEverywhere(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	set := n.pinned("bench")
	id := set.ID
	def := &evalset.Definition{Name: "x", TicketIDs: []int64{}}
	run := &evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1}

	_, listErr := n.svc.List(n.ctx, nil)
	_, getErr := n.svc.Get(n.ctx, nil, id)
	_, createErr := n.svc.Create(n.ctx, nil, def)
	_, replaceErr := n.svc.Replace(n.ctx, nil, id, def)
	deleteErr := n.svc.Delete(n.ctx, nil, id)
	_, pinErr := n.svc.PinMatches(n.ctx, nil, id)
	_, runErr := n.svc.Run(n.ctx, nil, id, run)
	_, historyErr := n.svc.History(n.ctx, nil, id)

	for name, err := range map[string]error{
		"list": listErr, "get": getErr, "create": createErr, "replace": replaceErr,
		"delete": deleteErr, "pin": pinErr, "run": runErr, "history": historyErr,
	} {
		assert.ErrorIs(t, err, authn.ErrNotPerson, name)
	}
	listed, err := n.svc.List(n.ctx, n.who)
	require.NoError(t, err)
	assert.Len(t, listed, 1, "nothing the agent tried changed the set")
}

func TestRun_CreatesOneBatchThroughTheTicketServiceAndRecordsIt(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	a, b := n.done("First"), n.done("Second")
	set := n.pinned("bench", a.ID, b.ID)
	variantID := uuid.New()

	res, err := n.svc.Run(n.ctx, n.who, set.ID, &evalset.RunRequest{VersionIDs: defaultVariant, Runs: 2, ConfirmAttempts: 4})

	require.NoError(t, err)
	assert.Empty(t, res.Batch.Refused)
	require.Len(t, res.Batch.Added, 2)
	var inBatch int
	require.NoError(t, n.db.QueryRow(n.ctx, `SELECT count(*) FROM attempts WHERE replay_batch_id = $1 AND replay`, res.Batch.BatchID).Scan(&inBatch))
	assert.Equal(t, 4, inBatch, "every replay is in the one batch")
	assert.Equal(t, 2, n.replays(a.ID))

	history, err := n.svc.History(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, res.Batch.BatchID, history[0].BatchID)
	assert.Equal(t, 2, history[0].TicketCount)
	assert.Equal(t, 2, history[0].Runs)
	assert.Equal(t, defaultVariant, history[0].VersionIDs, "the default variant is a null entry")
	assert.Equal(t, n.who.ID, *history[0].CreatedBy)

	// A named variant that does not exist is the ticket service's refusal,
	// reported per ticket; nothing is recorded for a batch that added nothing.
	res, err = n.svc.Run(n.ctx, n.who, set.ID, &evalset.RunRequest{VersionIDs: []*uuid.UUID{&variantID}, Runs: 1, ConfirmAttempts: 2})
	require.NoError(t, err)
	assert.Empty(t, res.Batch.Added)
	assert.Len(t, res.Batch.Refused, 2)
	assert.Nil(t, res.History)
	history, err = n.svc.History(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Len(t, history, 1)
}

func TestRun_ReportsTicketsThatCannotBeReplayedAndAddsTheRest(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	ok := n.done("Replayable")
	open := n.create("Never claimed")
	set := n.pinned("bench", ok.ID, open.ID, 999999)

	res, err := n.svc.Run(n.ctx, n.who, set.ID, &evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1, ConfirmAttempts: 3})

	require.NoError(t, err)
	require.Len(t, res.Batch.Added, 1)
	assert.Equal(t, ok.ID, res.Batch.Added[0].TicketID)
	reasons := map[int64]string{}
	for _, r := range res.Batch.Refused {
		reasons[r.TicketID] = r.Reason
	}
	require.Len(t, reasons, 2, "none is dropped silently")
	assert.Contains(t, reasons[open.ID], "only a done or parked ticket")
	assert.Contains(t, reasons, int64(999999))
	assert.Equal(t, 0, n.replays(open.ID))
}

func TestRun_TheTicketServicesCapAndConfirmationApplyUnchanged(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 3)
	a, b := n.done("First"), n.done("Second")
	set := n.pinned("bench", a.ID, b.ID)
	cases := []struct {
		name string
		req  evalset.RunRequest
		want error
	}{
		{"over the per-batch maximum", evalset.RunRequest{VersionIDs: defaultVariant, Runs: 2, ConfirmAttempts: 4}, ticket.ErrReplayBatchTooLarge},
		{"a confirmation naming another count", evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1, ConfirmAttempts: 1}, ticket.ErrReplayBatchUnconfirmed},
		{"no confirmation", evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1}, ticket.ErrReplayBatchUnconfirmed},
		{"no variants", evalset.RunRequest{Runs: 1}, ticket.ErrReplayBatchShape},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := n.svc.Run(n.ctx, n.who, set.ID, &c.req)

			require.ErrorIs(t, err, c.want)
			assert.Zero(t, n.replays(a.ID)+n.replays(b.ID), "a refused run adds nothing")
		})
	}
	history, err := n.svc.History(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Empty(t, history)
}

func TestRun_RefusesAnEmptySetAndAServerWithoutReplays(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	empty := n.pinned("empty")
	req := &evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1}
	_, err := n.svc.Run(n.ctx, n.who, empty.ID, req)
	assert.ErrorIs(t, err, evalset.ErrEmpty)

	off := evalset.NewService(n.store, n.tickets, false, slog.DiscardHandler)
	_, err = off.Run(n.ctx, n.who, empty.ID, req)
	assert.ErrorIs(t, err, evalset.ErrReplaysDisabled)
}

func TestDelete_TakesTheSetsHistoryWithIt(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	tk := n.done("Only")
	set := n.pinned("bench", tk.ID)
	_, err := n.svc.Run(n.ctx, n.who, set.ID, &evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1, ConfirmAttempts: 1})
	require.NoError(t, err)

	require.NoError(t, n.svc.Delete(n.ctx, n.who, set.ID))

	var rows int
	require.NoError(t, n.db.QueryRow(n.ctx, `SELECT count(*) FROM eval_set_batches`).Scan(&rows))
	assert.Zero(t, rows)
}

func TestCreate_ValidatesTheDefinition(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	long := make([]byte, evalset.MaxNameLen+1)
	for i := range long {
		long[i] = 'x'
	}
	for name, def := range map[string]*evalset.Definition{
		"no name":         {TicketIDs: []int64{}},
		"a long name":     {Name: string(long), TicketIDs: []int64{}},
		"a control name":  {Name: "a\x00b", TicketIDs: []int64{}},
		"a bad ticket id": {Name: "x", TicketIDs: []int64{0}},
		"a bad status":    {Name: "x", Filter: &evalset.Filter{Statuses: []string{"nonsense"}}},
		"a bad date":      {Name: "x", Filter: &evalset.Filter{ClosedFrom: "yesterday"}},
	} {
		_, err := n.svc.Create(n.ctx, n.who, def)
		assert.ErrorIs(t, err, evalset.ErrInvalid, name)
	}
}

func TestRun_RecordsTheVariantsTheTicketServiceRanOnce(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	set := n.pinned("bench", n.done("Only").ID)

	res, err := n.svc.Run(n.ctx, n.who, set.ID, &evalset.RunRequest{VersionIDs: []*uuid.UUID{nil, nil}, Runs: 1, ConfirmAttempts: 1})

	require.NoError(t, err)
	assert.Equal(t, defaultVariant, res.History.VersionIDs)
}

func TestRun_ASetOverTheTicketServicesTicketLimitIsRefusedWithoutAddingAnything(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10, ticket.WithBulkMax(1))
	a, b := n.done("First"), n.done("Second")
	set := n.pinned("bench", a.ID, b.ID)

	_, err := n.svc.Run(n.ctx, n.who, set.ID, &evalset.RunRequest{VersionIDs: defaultVariant, Runs: 1, ConfirmAttempts: 2})

	require.ErrorIs(t, err, ticket.ErrBatchTooLarge)
	assert.Zero(t, n.replays(a.ID)+n.replays(b.ID))
}

func TestPinMatches_DoesNotOverwriteAnEditMadeAfterItRead(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	set, err := n.svc.Create(n.ctx, n.who, &evalset.Definition{Name: "parsers", Filter: &evalset.Filter{Q: "parser"}})
	require.NoError(t, err)
	_, err = n.svc.Replace(n.ctx, n.who, set.ID, &evalset.Definition{Name: "renamed", Filter: &evalset.Filter{Q: "other"}})
	require.NoError(t, err)

	// What a pin that read the set before that edit would write.
	_, err = n.store.Replace(n.ctx, set.ID, &evalset.Definition{Name: set.Name, TicketIDs: []int64{1}}, &set.UpdatedAt)

	require.ErrorIs(t, err, evalset.ErrChanged)
	got, err := n.svc.Get(n.ctx, n.who, set.ID)
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, "other", got.Filter.Q)
	_, err = n.store.Replace(n.ctx, uuid.New(), &evalset.Definition{Name: "x", TicketIDs: []int64{}}, &set.UpdatedAt)
	assert.ErrorIs(t, err, evalset.ErrNotFound, "a missing set is not a change")
}

// limited answers a filter with as many tickets as the limit allows, up to
// matching, like a board with that many matches.
type limited struct{ matching int }

func (l *limited) List(_ context.Context, f *ticket.ListFilter) ([]ticket.Ticket, error) {
	n := min(l.matching, f.Limit)
	out := make([]ticket.Ticket, n)
	for i := range out {
		out[i].ID = int64(i + 1)
	}
	return out, nil
}

func (l *limited) AddReplayBatch(context.Context, uuid.UUID, *ticket.ReplayBatchRequest) (*ticket.ReplayBatchResult, error) {
	return nil, assert.AnError
}

func TestPinMatches_RefusesAFilterMatchingMoreThanASetHolds(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	for matching, wantErr := range map[int]bool{evalset.MaxTickets: false, evalset.MaxTickets + 1: true} {
		svc := evalset.NewService(n.store, &limited{matching: matching}, true, slog.DiscardHandler)
		set, err := svc.Create(n.ctx, n.who, &evalset.Definition{Name: fmt.Sprint("big-", matching), Filter: &evalset.Filter{}})
		require.NoError(t, err)

		pinned, err := svc.PinMatches(n.ctx, n.who, set.ID)

		if wantErr {
			require.ErrorIs(t, err, evalset.ErrInvalid)
			got, err := svc.Get(n.ctx, n.who, set.ID)
			require.NoError(t, err)
			assert.NotNil(t, got.Filter, "a refused pin leaves the filter")
		} else {
			require.NoError(t, err)
			assert.Len(t, pinned.TicketIDs, matching)
		}
	}
	_, err := n.svc.Create(n.ctx, n.who, &evalset.Definition{Name: "huge", TicketIDs: make([]int64, evalset.MaxTickets+1)})
	assert.ErrorIs(t, err, evalset.ErrInvalid)
}
