package ticket_test

import (
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// A person's paths edit on a ticket that holds its scope -- leased, or in
// review -- is checked against every other held scope, the rule a claim and
// a scope extension already apply, and refused whole on a conflict. A
// ticket holding nothing is not checked: a claim checks it when it is
// taken.

func (f *fixture) editPaths(t *testing.T, id int64, paths ...string) (*ticket.Ticket, error) {
	t.Helper()
	return f.svc.Update(f.ctx, id, &ticket.UpdateParams{Paths: scopes(paths...)})
}

func scopes(paths ...string) []model.Path {
	out := make([]model.Path, 0, len(paths))
	for _, p := range paths {
		out = append(out, model.Path{Path: p})
	}
	return out
}

// pathSources is a ticket's scope as path -> source.
func pathSources(tk *ticket.Ticket) map[string]model.PathSource {
	out := make(map[string]model.PathSource, len(tk.Paths))
	for _, p := range tk.Paths {
		out[p.Path] = p.Source
	}
	return out
}

func pathNames(tk *ticket.Ticket) []string {
	out := make([]string, 0, len(tk.Paths))
	for _, p := range tk.Paths {
		out = append(out, p.Path)
	}
	sort.Strings(out)
	return out
}

// holding makes a gated ticket with paths, claimed by a new agent, and
// sends it to review if inReview. Gated so the review is a real one.
func (f *fixture) holding(t *testing.T, name string, inReview bool, paths ...string) (*ticket.Ticket, uuid.UUID) {
	t.Helper()
	tk := f.gated(t, name, paths...)
	a := f.agent(t, name)
	claimed, err := f.svc.Claim(f.ctx, a, &tk.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket, "the setup's own claim")
	if inReview {
		f.review(t, a)
	}
	return f.get(t, tk.ID), a
}

func requireScopeConflict(t *testing.T, err error, edited, holder int64, heldPath string) {
	t.Helper()
	require.ErrorIs(t, err, ticket.ErrScopeConflict)
	var conflict *ticket.ScopeConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, edited, conflict.Blocked.TicketID)
	assert.Equal(t, holder, conflict.Blocked.ConflictingTicketID)
	assert.Equal(t, heldPath, conflict.Blocked.ConflictingPath)
}

func TestUpdate_APathsEditOverlappingAnotherHeldScopeIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		editedInReview, otherIn bool
	}{
		{name: "held over held"},
		{name: "held over in review", otherIn: true},
		{name: "in review over held", editedInReview: true},
		{name: "in review over in review", editedInReview: true, otherIn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			other, _ := f.holding(t, "other", tc.otherIn, "a/")
			edited, _ := f.holding(t, "edited", tc.editedInReview, "b/")

			high := model.PriorityHigh
			_, err := f.svc.Update(f.ctx, edited.ID, &ticket.UpdateParams{
				Paths: scopes("b/", "a/x.go"), Priority: &high,
			})

			requireScopeConflict(t, err, edited.ID, other.ID, "a/")
			got := f.get(t, edited.ID)
			assert.Equal(t, []string{"b/"}, pathNames(got), "the paths are left as they were")
			assert.Equal(t, model.PriorityNormal, got.Priority, "and nothing else in the edit is written")
			assert.Equal(t, edited.Status, got.Status)
		})
	}
}

// Over the wire the refusal is a 409 that names what stood in the way.
func TestUpdate_ARefusedPathsEditIsAConflictOverTheWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other, _ := f.holding(t, "other", false, "a/")
	edited, _ := f.holding(t, "edited", false, "b/")

	rec := send(t, f.boardMux(), http.MethodPatch, fmt.Sprintf("/api/tickets/%d", edited.ID), `{"paths": [{"path": "a/x.go"}]}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), fmt.Sprintf("held by #%d", other.ID))
	assert.Equal(t, []string{"b/"}, pathNames(f.get(t, edited.ID)))
}

// Its own scope is no conflict, and nor is a widening clear of everyone
// else's.
func TestUpdate_APathsEditOnAHeldTicketClearOfOtherScopesIsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.holding(t, "other", false, "a/")
	edited, _ := f.holding(t, "edited", false, "b/")

	got, err := f.editPaths(t, edited.ID, "b/x/", "c/")
	require.NoError(t, err)
	assert.Equal(t, []string{"b/x/", "c/"}, pathNames(got))
}

// A ticket nobody holds is not checked: whoever claims it is, then.
func TestUpdate_APathsEditOnATicketHoldingNoScopeIsNotChecked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		make func(t *testing.T, f *fixture, other *ticket.Ticket) *ticket.Ticket
	}{{
		name: "open",
		make: func(t *testing.T, f *fixture, _ *ticket.Ticket) *ticket.Ticket { return f.ticket(t, "Open", "b/") },
	}, {
		name: "preparing",
		make: func(t *testing.T, f *fixture, _ *ticket.Ticket) *ticket.Ticket {
			return f.preparing(t, "Preparing", "b/")
		},
	}, {
		name: "blocked",
		make: func(t *testing.T, f *fixture, other *ticket.Ticket) *ticket.Ticket {
			created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
				Title: "Blocked", Paths: scopes("b/"), BlockedOn: []int64{other.ID}, CreatedByUserID: &f.human,
			})
			require.NoError(t, err)
			require.Equal(t, ticket.StatusBlocked, created.Status)
			return created
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			other, _ := f.holding(t, "other", false, "a/")
			edited := tc.make(t, f, other)

			got, err := f.editPaths(t, edited.ID, "a/x.go")
			require.NoError(t, err)
			assert.Equal(t, []string{"a/x.go"}, pathNames(got))
		})
	}
}

// An edit that moves a ticket and sets its paths is checked as the ticket
// lands: into review, it is checked as holding its scope; out of a lease,
// as holding nothing.
func TestUpdate_AStatusAndPathsEditIsCheckedAsTheTicketLands(t *testing.T) {
	t.Parallel()
	t.Run("into review", func(t *testing.T) {
		f := newFixture(t)
		other, _ := f.holding(t, "other", false, "a/")
		edited := f.ticket(t, "Open", "b/")
		review := ticket.StatusReview

		_, err := f.svc.Update(f.ctx, edited.ID, &ticket.UpdateParams{Status: &review, Paths: scopes("a/x.go")})

		requireScopeConflict(t, err, edited.ID, other.ID, "a/")
		got := f.get(t, edited.ID)
		assert.Equal(t, ticket.StatusOpen, got.Status, "the move is refused with the paths")
		assert.Equal(t, []string{"b/"}, pathNames(got))
	})
	t.Run("out of a lease", func(t *testing.T) {
		f := newFixture(t)
		f.holding(t, "other", false, "a/")
		edited, _ := f.holding(t, "edited", false, "b/")
		preparing := ticket.StatusPreparing

		got, err := f.svc.Update(f.ctx, edited.ID, &ticket.UpdateParams{Status: &preparing, Paths: scopes("a/x.go")})

		require.NoError(t, err)
		assert.Equal(t, ticket.StatusPreparing, got.Status)
		assert.Equal(t, []string{"a/x.go"}, pathNames(got))
	})
}

// A person's paths edit replaces the paths filed, not those an agent was
// granted mid-ticket: those are a fact about the work under way.
func TestUpdate_APathsEditKeepsExtendedPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	held, a := f.holding(t, "held", false, "b/")
	extended, err := f.svc.ExtendScope(f.ctx, a, scopes("c/"))
	require.NoError(t, err)
	require.True(t, extended.Granted)

	got, err := f.editPaths(t, held.ID, "d/")
	require.NoError(t, err)
	assert.Equal(t, map[string]model.PathSource{
		"c/": model.PathSourceExtended,
		"d/": model.PathSourceFiled,
	}, pathSources(got))
}

// Naming a kept extended path in the edit leaves it one row, still
// extended: a client sending back the scope it read must not turn an
// agent's grant into a filed path the next edit would drop.
func TestUpdate_APathsEditNamingAnExtendedPathKeepsItExtended(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	held, a := f.holding(t, "held", false, "b/")
	extended, err := f.svc.ExtendScope(f.ctx, a, scopes("c/"))
	require.NoError(t, err)
	require.True(t, extended.Granted)

	got, err := f.editPaths(t, held.ID, "c/", "e/")
	require.NoError(t, err)
	require.Len(t, got.Paths, 2, "c/ is one row, not two")
	assert.Equal(t, map[string]model.PathSource{
		"c/": model.PathSourceExtended,
		"e/": model.PathSourceFiled,
	}, pathSources(got))
}

// The check is on the scope the ticket would hold, kept extended paths
// included: a ticket released with an extended path, whose path another
// agent has since claimed, cannot be moved into review over it.
func TestUpdate_AKeptExtendedPathIsCheckedToo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	edited, y := f.holding(t, "edited", false, "b/")
	extended, err := f.svc.ExtendScope(f.ctx, y, scopes("c/"))
	require.NoError(t, err)
	require.True(t, extended.Granted)
	_, err = f.svc.Release(f.ctx, y, "handing back", nil)
	require.NoError(t, err)
	other, _ := f.holding(t, "other", false, "c/x.go")

	review := ticket.StatusReview
	_, err = f.svc.Update(f.ctx, edited.ID, &ticket.UpdateParams{Status: &review, Paths: scopes("d/")})

	requireScopeConflict(t, err, edited.ID, other.ID, "c/x.go")
	assert.Equal(t, []string{"b/", "c/"}, pathNames(f.get(t, edited.ID)))
}

// An empty list is a request to clear the scope, not an absent field: the
// two have to stay distinguishable over the wire, where a missing `paths`
// leaves the scope alone.
func TestUpdateTicket_EmptyPathsClearsScopeAndAbsentLeavesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	created := f.ticket(t, "Scoped", "app/src/", "app/docs/")
	url := fmt.Sprintf("/api/tickets/%d", created.ID)

	rec := send(t, mux, http.MethodPatch, url, `{"priority": "high"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, decodeTicket(t, rec.Body.Bytes()).Paths, 2, "no paths field leaves the scope alone")

	rec = send(t, mux, http.MethodPatch, url, `{"paths": []}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decodeTicket(t, rec.Body.Bytes()).Paths)
	assert.Empty(t, f.get(t, created.ID).Paths, "the scope is cleared in the database")
}

// A person's edit of a held ticket's scope is recorded as a comment, so the
// agent holding it can read what changed. An agent's own scope changes and
// an edit that changes nothing leave no comment, and an extended path, the
// agent's grant, is neither in the comment nor taken away.
func TestUpdate_APersonsPathsEditIsRecordedAsAComment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	held, a := f.holding(t, "held", false, "a/")
	_, err := f.svc.ExtendScope(f.ctx, a, scopes("ext/"))
	require.NoError(t, err)
	byPerson := ticket.WithUser(f.ctx, f.human)
	count := func() int { return len(f.comments(t, held.ID)) }
	start := count()

	_, err = f.svc.Update(byPerson, held.ID, &ticket.UpdateParams{Paths: scopes("a/")})
	require.NoError(t, err)
	require.Equal(t, start, count(), "an edit that changes nothing writes none")

	_, err = f.svc.Update(byPerson, held.ID, &ticket.UpdateParams{Paths: scopes("a/", "ext/")})
	require.NoError(t, err)
	require.Equal(t, start, count(), "resending the agent's extended path changes nothing")

	_, err = f.svc.Update(f.ctx, held.ID, &ticket.UpdateParams{Paths: scopes("a/", "c/")})
	require.NoError(t, err)
	require.Equal(t, start, count(), "an edit with no person behind it writes none")

	_, err = f.svc.Update(byPerson, held.ID, &ticket.UpdateParams{Paths: scopes("b/")})
	require.NoError(t, err)

	comments := f.comments(t, held.ID)
	require.Len(t, comments, start+1, "a person's changed edit wrote one")
	last := comments[len(comments)-1]
	assert.Equal(t, "Path scopes changed by a person. Was: a/, c/. Now: b/.", last.Body)
	require.NotNil(t, last.AuthorUserID)
	assert.Equal(t, f.human, *last.AuthorUserID)
	assert.Equal(t, []string{"b/", "ext/"}, pathNames(f.get(t, held.ID)))
}
