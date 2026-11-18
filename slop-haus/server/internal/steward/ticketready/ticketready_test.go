package ticketready_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/ticketready"
)

// fake decides nothing: it returns what the test set.
type fake struct {
	paths     []string
	body      string
	blockers  []int64
	protected map[string]string
	// failAt names the read that fails; the reads before it succeed.
	failAt string
}

func (f *fake) fail(read string) error {
	if f.failAt == read {
		return errors.New(read + " failed")
	}
	return nil
}

func (f *fake) Revision(context.Context, int64) (int, error) { return 1, f.fail("Revision") }
func (f *fake) Paths(context.Context, int64) ([]string, error) {
	return f.paths, f.fail("Paths")
}
func (f *fake) Status(context.Context, int64) (string, error) { return "preparing", f.fail("Status") }
func (f *fake) Body(context.Context, int64) (string, error)   { return f.body, f.fail("Body") }
func (f *fake) Blockers(context.Context, int64) ([]int64, error) {
	return f.blockers, f.fail("Blockers")
}
func (f *fake) Protected(context.Context, []string) (map[string]string, error) {
	return f.protected, f.fail("Protected")
}

const goodBody = "Build part of #1193.\n\nDo: add the category.\nTests: table tests.\nBlocked on #1194."

func good() *fake {
	return &fake{paths: []string{"a/b/"}, body: goodBody, blockers: []int64{1194}}
}

func run(t *testing.T, f *fake) map[string]steward.Evidence {
	out := map[string]steward.Evidence{}
	for _, c := range checksOf(f) {
		ev, err := c.Run(context.Background(), &steward.Subject{TicketID: 1})
		require.NoError(t, err)
		assert.Equal(t, c.Name(), ev.Check)
		out[c.Name()] = ev
	}
	return out
}

func TestAllChecksPass(t *testing.T) {
	for name, ev := range run(t, good()) {
		assert.True(t, ev.OK, "%s: %s", name, ev.Detail)
		assert.NotEmpty(t, ev.Detail, name)
	}
}

func TestCategoryNamesChecksThatExist(t *testing.T) {
	cat, ok := steward.Lookup(ticketready.CategoryID)
	require.True(t, ok, "ticket_ready is on the allowlist")
	assert.Equal(t, steward.ClassJudgment, cat.Class)
	assert.Equal(t, []string{steward.KindReady}, cat.Kinds)
	assert.ElementsMatch(t, []string{
		steward.CheckTicketHasBrief, steward.CheckBlockersRecorded, steward.CheckTicketPreparing,
		steward.CheckTicketRevUnchanged, steward.CheckNoNewPaths, steward.CheckNoProtectedPath,
		steward.CheckNoHumanOnlyMarker,
	}, cat.Checks)
	assert.Equal(t, "ticket_ready", cat.ID)
	own := run(t, good())
	for _, name := range cat.Checks {
		_, mine := own[name]
		_, theirs := map[string]bool{
			steward.CheckTicketPreparing: true, steward.CheckTicketRevUnchanged: true,
			steward.CheckNoNewPaths: true, steward.CheckNoHumanOnlyMarker: true,
		}[name]
		assert.True(t, mine || theirs, name)
	}
}

func TestBriefFindings(t *testing.T) {
	for _, c := range []struct {
		name    string
		mutate  func(*fake)
		missing string
	}{
		{"no paths", func(f *fake) { f.paths = nil }, "paths"},
		{"no Do", func(f *fake) { f.body = "Tests: yes" }, "Do"},
		{"no tests", func(f *fake) { f.body = "Do: it" }, "tests"},
		{"Do inside a word is not a Do", func(f *fake) { f.body = "Done soon. tests" }, "Do"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := good()
			c.mutate(f)
			ev := run(t, f)[steward.CheckTicketHasBrief]
			assert.False(t, ev.OK)
			assert.Contains(t, ev.Detail, "missing "+c.missing)
		})
	}
}

func TestBlockersRecorded(t *testing.T) {
	f := good()
	f.blockers = nil
	ev := run(t, f)[steward.CheckBlockersRecorded]
	assert.False(t, ev.OK)
	assert.Contains(t, ev.Detail, "#1194")

	f = good()
	f.blockers = []int64{5}
	ev = run(t, f)[steward.CheckBlockersRecorded]
	assert.False(t, ev.OK, "an unrelated blocker does not cover #1194")

	f = good()
	f.body = "Do: x. tests.\nBlocked on #12 and #13."
	f.blockers = []int64{12}
	ev = run(t, f)[steward.CheckBlockersRecorded]
	assert.False(t, ev.OK)
	assert.Contains(t, ev.Detail, "#13")
	assert.NotContains(t, ev.Detail, "#12")
	f.blockers = []int64{12, 13}
	assert.True(t, run(t, f)[steward.CheckBlockersRecorded].OK)

	f = good()
	f.body = "Do: x. tests. Part of #5."
	assert.True(t, run(t, f)[steward.CheckBlockersRecorded].OK, "a mention is not a dependency")
}

func TestProtectedPath(t *testing.T) {
	f := good()
	f.protected = map[string]string{"a/b/": "deploy"}
	ev := run(t, f)[steward.CheckNoProtectedPath]
	assert.False(t, ev.OK)
	assert.Contains(t, ev.Detail, "a/b/ (deploy)")

	f = good()
	f.paths = nil
	ev = run(t, f)[steward.CheckNoProtectedPath]
	assert.False(t, ev.OK, "no declared paths counts as protected")
}

func TestReadErrorsAreReturned(t *testing.T) {
	for _, c := range []struct{ check, read string }{
		{steward.CheckTicketHasBrief, "Paths"}, {steward.CheckTicketHasBrief, "Body"},
		{steward.CheckBlockersRecorded, "Body"}, {steward.CheckBlockersRecorded, "Blockers"},
		{steward.CheckNoProtectedPath, "Paths"}, {steward.CheckNoProtectedPath, "Protected"},
	} {
		t.Run(c.check+"/"+c.read, func(t *testing.T) {
			f := good()
			f.failAt = c.read
			for _, ck := range checksOf(f) {
				if ck.Name() != c.check {
					continue
				}
				_, err := ck.Run(context.Background(), &steward.Subject{TicketID: 1})
				assert.ErrorContains(t, err, c.read+" failed")
			}
		})
	}
}

func TestComparator(t *testing.T) {
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start.Add(48 * time.Hour))
	cmp := agreement.SameActionOrExpired(clk, 24*time.Hour)
	dec := func(d steward.Decision) *steward.Decision { return &d }
	now := start
	for _, c := range []struct {
		name string
		rec  steward.Record
		want agreement.Outcome
	}{
		{"opened unchanged", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionApproved)}, agreement.Agreed},
		{"edited first", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionEdited)}, agreement.Disagreed},
		{"rejected", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionRejected)}, agreement.Disagreed},
		{"left past the window", steward.Record{Verdict: steward.VerdictWouldVeto, CreatedAt: start}, agreement.Disagreed},
		{"within the window", steward.Record{Verdict: steward.VerdictWouldVeto, CreatedAt: start.Add(40 * time.Hour)}, agreement.Undecided},
		{"voided is no decision", steward.Record{Verdict: steward.VerdictWouldVeto, CreatedAt: start, VoidedAt: &now}, agreement.Undecided},
		{"needs_human claims nothing", steward.Record{Verdict: steward.VerdictNeedsHuman, CreatedAt: start}, agreement.Undecided},
		{"veto beats an approval", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionApproved), VetoedAt: &now}, agreement.Disagreed},
		{"veto within the window", steward.Record{Verdict: steward.VerdictWouldVeto, CreatedAt: start.Add(40 * time.Hour), VetoedAt: &now}, agreement.Disagreed},
		{"undo beats an approval", steward.Record{Verdict: steward.VerdictWouldAuto, HumanDecision: dec(steward.DecisionApproved), UndoneAt: &now}, agreement.Disagreed},
		{"withdrawn", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionWithdrawn)}, agreement.Undecided},
	} {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, cmp(&c.rec)) })
	}
}

func TestCategoryRetiresOpenTicket(t *testing.T) {
	assert.True(t, steward.Retired("open_ticket"))
}

func TestComparatorIsTheRegisteredOne(t *testing.T) {
	assert.True(t, agreement.Registered(ticketready.CategoryID))
	old := &steward.Record{Verdict: steward.VerdictWouldVeto, CreatedAt: time.Now().Add(-30 * 24 * time.Hour)}
	assert.Equal(t, agreement.Disagreed, agreement.ComparatorFor(ticketready.CategoryID)(old), "left unopened past the window")
}

func checksOf(f *fake) []steward.Check {
	return []steward.Check{ticketready.TicketHasBrief(f), ticketready.BlockersRecorded(f), ticketready.NoProtectedPath(f, f)}
}
