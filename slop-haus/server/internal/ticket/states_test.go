package ticket

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type edge struct{ from, to Status }

func (e edge) String() string { return fmt.Sprintf("%s -> %s", e.from, e.to) }

// builtEdges are the status moves every transition builder makes, each tried
// from every status it accepts. A builder's refusal makes no move; a Change
// that leaves the status alone makes none either.
func builtEdges(t *testing.T) map[edge]string {
	t.Helper()
	user, agent := uuid.New(), uuid.New()
	blocker := int64(9)
	prURL := "https://github.com/acme/widgets/pull/12"
	pr := codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12}
	hosts := codehosttest.Registry(codehosttest.NewLinkHost("github.com"))
	cooling := CooldownPolicy{Min: time.Minute, Max: time.Hour, EscalateAfter: 3}
	escalating := CooldownPolicy{Min: time.Minute, Max: time.Hour, EscalateAfter: 1}
	yes, no := true, false
	now := time.Now()

	edges := map[edge]string{}
	record := func(name string, from Status, c *Change) {
		if c == nil {
			return
		}
		if to, ok := c.Ticket.Status.Status(); ok && to != from {
			edges[edge{from, to}] = name
		}
		if c.Ticket.Status.IsQueue() {
			// The queue is open, or blocked while a dependency is unfinished.
			for _, to := range []Status{StatusOpen, StatusBlocked} {
				if to != from {
					edges[edge{from, to}] = name
				}
			}
		}
	}
	forEachStatus := func(name string, tr ticketTransition) {
		for _, from := range model.Statuses() {
			c, err := tr(&Ticket{ID: 12, Status: from, ApprovedAt: &now, PRURL: &prURL})
			if err == nil {
				record(name, from, c)
			}
		}
	}

	held := map[string]heldExit{
		"release":           releaseExit(&ReleaseParams{Reason: "r"}, CooldownPolicy{}, CooldownStreak{}, now, &ReleaseOutcome{}),
		"release, blocker":  releaseExit(&ReleaseParams{Reason: "r", BlockedOn: &blocker}, CooldownPolicy{}, CooldownStreak{}, now, &ReleaseOutcome{}),
		"release, cooldown": releaseExit(&ReleaseParams{Reason: "r"}, cooling, CooldownStreak{}, now, &ReleaseOutcome{}),
		"release, escalate": releaseExit(&ReleaseParams{Reason: "r"}, escalating, CooldownStreak{}, now, &ReleaseOutcome{}),
		"done":              doneExit("s", &prURL),
		"discuss":           discussExit("q", "h"),
		"review":            reviewExit(prURL, "h", ""),
		"prepare":           prepareExit("r"),
		"land":              landExit("h"),
	}
	// The server holds a ticket in the merge queue, and leaves it by the same
	// exits as a worker does its own.
	for name, exit := range held {
		for _, from := range []Status{StatusInProgress, StatusInMergeQueue} {
			if name == "land" && from != StatusInProgress {
				continue
			}
			c, err := exit(&Ticket{ID: 12, Status: from}, agent)
			require.NoError(t, err, name)
			record("held exit: "+name, from, c)
		}
	}

	forEachStatus("approval", approval(user, time.Hour, nil))
	forEachStatus("changes requested", changesRequested(user, "c", time.Hour, nil))
	forEachStatus("comment answers", humanCommentEffect(nil, time.Hour))
	forEachStatus("comment sets needs_discussion", humanCommentEffect(&yes, time.Hour))
	forEachStatus("needs_discussion on", settingNeedsDiscussion(yes))
	forEachStatus("needs_discussion off", settingNeedsDiscussion(no))
	forEachStatus("merge", closeMergedFrom(hosts, pr, user, false))

	for _, from := range model.Statuses() {
		for _, to := range model.Statuses() {
			if m := MoveTo(to); m.check(from) == nil {
				record("move to "+string(to), from, m.change())
			}
		}
		if m := MoveReady(); m.check(from) == nil {
			record("ready", from, m.change())
		}
	}
	return edges
}

func tableEdges[S ~string](table map[S][]S) map[[2]S]bool {
	out := map[[2]S]bool{}
	for from, tos := range table {
		for _, to := range tos {
			out[[2]S{from, to}] = true
		}
	}
	return out
}

func TestTicketTransitionTableIsWellFormed(t *testing.T) {
	t.Parallel()
	assert.Len(t, ticketTransitions, len(model.Statuses()), "every status has a row")
	for _, from := range model.Statuses() {
		assert.NotContains(t, ticketTransitions[from], from, "%s lists itself", from)
		assert.Len(t, ticketTransitions[from], len(slices.Compact(slices.Clone(ticketTransitions[from]))), "%s repeats a target", from)
		for _, to := range ticketTransitions[from] {
			assert.True(t, to.Valid(), "%s -> %s", from, to)
		}
	}
	all := tableEdges(ticketTransitions)
	for e := range tableEdges(humanMoves) {
		assert.True(t, all[e], "human move %v is not a ticket transition", e)
	}
}

// Every status move a builder makes is in the table, and every entry of the
// table is made by a builder, is a person's free-form move, or is the merge
// queue's repair taking a queued landing into the queue.
func TestTicketTransitionTableMatchesTheBuilders(t *testing.T) {
	t.Parallel()
	built := builtEdges(t)
	table := tableEdges(ticketTransitions)
	human := tableEdges(humanMoves)

	for e, by := range built {
		assert.True(t, table[[2]Status{e.from, e.to}], "%s makes %s, which the table does not allow", by, e)
	}
	for e := range table {
		_, madeByBuilder := built[edge{e[0], e[1]}]
		madeByRepair := e == [2]Status{StatusOpen, StatusInMergeQueue}
		assert.True(t, madeByBuilder || human[e] || madeByRepair, "%s -> %s is in the table but nothing makes it", e[0], e[1])
	}
}

// A person's free-form move is the humanMoves table, and what the board
// offers (AllowedMoves) is what humanMove accepts.
func TestHumanMovesAreTheTable(t *testing.T) {
	t.Parallel()
	for _, from := range model.Statuses() {
		var allowed []Status
		for _, to := range model.Statuses() {
			err := humanMove(from, to)
			ok := to == from || slices.Contains(humanMoves[from], to)
			assert.Equal(t, ok, err == nil, "%s -> %s: %v", from, to, err)
			if err == nil {
				allowed = append(allowed, to)
			}
		}
		assert.Equal(t, allowed, AllowedMoves(from), "AllowedMoves from %s", from)
	}
}

func TestAttemptStatuses(t *testing.T) {
	t.Parallel()
	assert.Len(t, attemptTransitions, len(attemptStatuses))
	for _, from := range attemptStatuses {
		assert.True(t, from.Valid())
		assert.NotContains(t, attemptTransitions[from], from)
	}
	assert.False(t, AttemptStatus("open").Valid())
	for _, live := range []AttemptStatus{AttemptStatusQueued, AttemptStatusInProgress, AttemptStatusInMergeQueue, AttemptStatusDiscussion, AttemptStatusReview} {
		assert.True(t, live.Live(), live)
	}
	assert.False(t, AttemptStatusDone.Live())
	assert.False(t, AttemptStatusClosed.Live())
}

// An attempt follows its ticket's status (attempt_status_for_ticket), so the
// attempt table is the ticket table seen through that mapping, plus the one
// move a ticket does not make: a lease ending puts a running attempt back to
// queued, as the ticket goes back to open or blocked.
func TestAttemptTransitionsFollowTheTicketTable(t *testing.T) {
	t.Parallel()
	projected := map[[2]AttemptStatus]bool{}
	for e := range tableEdges(ticketTransitions) {
		from, to := AttemptStatusFor(e[0]), AttemptStatusFor(e[1])
		if from != to {
			projected[[2]AttemptStatus{from, to}] = true
		}
	}
	// A person closing an attempt (not its ticket) reaches closed from any
	// live status.
	for _, from := range attemptStatuses {
		if from.Live() {
			projected[[2]AttemptStatus{from, AttemptStatusClosed}] = true
		}
	}
	assert.Equal(t, projected, tableEdges(attemptTransitions))
}

func TestAttemptStatusForMatchesTheSQLFunction(t *testing.T) {
	t.Parallel()
	want := map[Status]AttemptStatus{
		StatusPreparing: AttemptStatusQueued, StatusParked: AttemptStatusQueued, StatusOpen: AttemptStatusQueued,
		StatusBlocked: AttemptStatusQueued, StatusInProgress: AttemptStatusInProgress,
		StatusDiscussion: AttemptStatusDiscussion, StatusReview: AttemptStatusReview,
		StatusInMergeQueue: AttemptStatusInMergeQueue,
		StatusDone:         AttemptStatusDone, StatusCancelled: AttemptStatusClosed,
	}
	for _, s := range model.Statuses() {
		assert.Equal(t, want[s], AttemptStatusFor(s), s)
	}
}

func TestStateDiagramsInTheDocs(t *testing.T) {
	t.Parallel()
	for _, d := range []struct{ name, diagram string }{
		{"ticket", TicketStateDiagram()},
		{"attempt", AttemptStateDiagram()},
	} {
		path := "../../STATE_MACHINES.md"
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		want, ok := WithDiagram(string(raw), d.name, d.diagram)
		require.True(t, ok, "STATE_MACHINES.md has no %s diagram markers", d.name)
		if os.Getenv("UPDATE_DOCS") != "" {
			require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
			continue
		}
		assert.Equal(t, want, string(raw), "STATE_MACHINES.md's %s diagram is stale: run with UPDATE_DOCS=1", d.name)
	}
}

func TestStateDiagramRendering(t *testing.T) {
	t.Parallel()
	d := StateDiagram([]Status{StatusOpen, StatusDone}, map[Status][]Status{StatusOpen: {StatusDone}})
	assert.Equal(t, "```mermaid\nstateDiagram-v2\n    open --> done\n```\n", d)
	doc, ok := WithDiagram("a\n<!-- state-diagram:x -->\nold\n<!-- /state-diagram:x -->\nb", "x", d)
	assert.True(t, ok)
	assert.Equal(t, "a\n<!-- state-diagram:x -->\n"+d+"<!-- /state-diagram:x -->\nb", doc)
	_, ok = WithDiagram("none", "x", d)
	assert.False(t, ok)
}
