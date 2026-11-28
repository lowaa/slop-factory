package statusview_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock/clocktest"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/statusview"
)

var now = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

func newModel(t *testing.T, w, h int, st *api.Status) *statusview.Model {
	t.Helper()
	m := statusview.New(&statusview.Options{Clock: clocktest.NewFixed(now)})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if st != nil {
		m.Update(statusview.SnapshotMsg{Status: st})
	}
	return m
}

func snapshot() *api.Status {
	return &api.Status{
		Autonomy: true, Budget: 10, Outstanding: 2,
		Pools: []api.PoolUsage{{Pool: "normal", Active: 3, Max: 4}, {Pool: "urgent", Active: 0, Max: 1}},
		Agents: []api.AgentStatus{
			{ID: "a2", Ticket: "2", Title: "Add the CSV export for reports", TicketStatus: "in_progress",
				State: "working", Since: now.Add(-95 * time.Second), Harness: "claude-code", Model: "opus", Effort: "high",
				Pool: "normal", Note: "writing the exporter"},
			{ID: "a1", Ticket: "1", Title: "Fix login", TicketStatus: "preparing",
				State: "waiting_user", Since: now.Add(-2 * time.Hour), Harness: "claude-code", Pool: "urgent"},
			{ID: "a3", Ticket: "3", Title: "Blocked thing", TicketStatus: "open",
				State: "idle", Since: now.Add(-10 * time.Second), Harness: "codex", Model: "gpt", Pool: "normal",
				Blockers: []string{"1", "2"}},
		},
	}
}

func many(n int) *api.Status {
	st := &api.Status{}
	for i := range n {
		st.Agents = append(st.Agents, api.AgentStatus{Ticket: fmt.Sprintf("%02d", i), Title: "t", State: "idle", Since: now})
	}
	return st
}

func TestRowsAreBuiltFromTheSnapshotSortedByTicket(t *testing.T) {
	t.Parallel()
	m := newModel(t, 200, 20, snapshot())
	assert.Equal(t, [][]string{
		{"1", "Fix login", "preparing", "waiting_user 2h0m", "claude-code", "-", "urgent", "-", "-"},
		{"2", "Add the CSV export for reports", "in_progress", "working 1m", "claude-code", "opus/high", "normal", "-", "writing the exporter"},
		{"3", "Blocked thing", "open", "idle 10s", "codex", "gpt", "normal", "1,2", "-"},
	}, m.Rows())
}

func TestPaginationStaysInBounds(t *testing.T) {
	t.Parallel()
	// Height 6 leaves 3 rows a page: 7 agents make 3 pages.
	m := newModel(t, 100, 6, many(7))
	require.Equal(t, 3, m.Pages())
	require.Len(t, m.Rows(), 3)

	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	assert.Equal(t, 0, m.Page(), "no page before the first")
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	assert.Equal(t, 2, m.Page())
	assert.Equal(t, "06", m.Rows()[0][0])
	assert.Len(t, m.Rows(), 1)
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	assert.Equal(t, 2, m.Page(), "no page past the last")
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	assert.Equal(t, 1, m.Page())

	// A shrinking snapshot pulls the page back in range.
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m.Update(statusview.SnapshotMsg{Status: many(2)})
	assert.Equal(t, 0, m.Page())
}

func TestQuitKeys(t *testing.T) {
	t.Parallel()
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		m := newModel(t, 80, 10, snapshot())
		_, cmd := m.Update(k)
		require.NotNil(t, cmd, k.String())
		assert.IsType(t, tea.QuitMsg{}, cmd(), k.String())
	}
}

func TestColumnsTruncateAtANarrowWidth(t *testing.T) {
	t.Parallel()
	st := snapshot()
	wide := ansi.Strip(newModel(t, 200, 20, st).View().Content)
	narrow := ansi.Strip(newModel(t, 70, 20, st).View().Content)
	assert.Contains(t, wide, "writing the exporter")
	assert.Contains(t, wide, "Add the CSV export for reports")
	assert.NotContains(t, narrow, "writing the exporter")
	assert.Contains(t, narrow, "…")
	for _, line := range strings.Split(narrow, "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 70, line)
	}
	assert.Contains(t, narrow, "\n 1 ", "the ticket id is never cut")
}

func TestTheHeaderShowsPoolsAutonomyAndAPlaceholderForAllowances(t *testing.T) {
	t.Parallel()
	out := newModel(t, 200, 20, snapshot()).View().Content
	assert.Contains(t, out, "pool 3/4, urgent 0/1 | autonomy on, agent-filed 2/10 | plan: n/a")

	st := snapshot()
	st.Allowances = []api.Allowance{{Harness: "claude-code", UsedPercent: 42, ResetsAt: now.Add(3 * time.Hour)}}
	out = newModel(t, 200, 20, st).View().Content
	assert.Contains(t, out, "plan: claude-code 42% resets in 3h0m")
}

func TestBeforeTheFirstSnapshot(t *testing.T) {
	t.Parallel()
	assert.Contains(t, newModel(t, 80, 10, nil).View().Content, "waiting")
}

func TestViewGolden(t *testing.T) {
	t.Parallel()
	golden.RequireEqual(t, []byte(newModel(t, 150, 12, snapshot()).View().Content))
}

func TestViewGoldenNarrow(t *testing.T) {
	t.Parallel()
	golden.RequireEqual(t, []byte(newModel(t, 72, 12, snapshot()).View().Content))
}

// The program's raw output isn't stable (it includes terminal capability
// queries answered at the terminal's pace), so teatest drives the program
// and the golden is the final model's view.
func TestProgramViewGolden(t *testing.T) {
	t.Parallel()
	m := statusview.New(&statusview.Options{Clock: clocktest.NewFixed(now)})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(150, 12))
	tm.Send(statusview.SnapshotMsg{Status: snapshot()})
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second))
	golden.RequireEqual(t, []byte(final.View().Content))
}
