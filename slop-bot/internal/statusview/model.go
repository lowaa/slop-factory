package statusview

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

// SnapshotMsg carries a fresh GET /v1/status into the program.
type SnapshotMsg struct{ Status *api.Status }

// Options configure a Model.
type Options struct {
	Clock clock.Clock
}

// Model is the read-only status table. It only paginates: no selection,
// sorting or filtering.
type Model struct {
	clock  clock.Clock
	status *api.Status
	width  int
	height int
	page   int
}

// New builds a Model; it shows a waiting line until the first snapshot.
func New(opts *Options) *Model {
	return &Model{clock: opts.Clock}
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case SnapshotMsg:
		m.status = msg.Status
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "right", "pgdown":
			m.page++
		case "left", "pgup":
			m.page--
		}
	}
	m.page = min(max(m.page, 0), m.pages()-1)
	return m, nil
}

// Page is the current zero-based page.
func (m *Model) Page() int { return m.page }

// Pages is how many pages the current snapshot needs, at least one.
func (m *Model) Pages() int { return m.pages() }

// Rows are the current page's cells, sorted by ticket id.
func (m *Model) Rows() [][]string {
	all := m.allRows()
	lo := min(m.page*m.pageSize(), len(all))
	hi := min(lo+m.pageSize(), len(all))
	return all[lo:hi]
}

// chrome is the lines that aren't table rows: the header line, the
// table's column header and the footer.
const chrome = 3

func (m *Model) pageSize() int { return max(m.height-chrome, 1) }

func (m *Model) pages() int {
	n := 0
	if m.status != nil {
		n = len(m.status.Agents)
	}
	return max((n+m.pageSize()-1)/m.pageSize(), 1)
}

var headers = []string{"TICKET", "TITLE", "STATUS", "AGENT", "HARNESS", "MODEL", "POOL", "BLOCKERS", "NOTE"}

// flexible columns absorb a narrow terminal first.
var flexible = map[int]bool{1: true, 8: true}

func (m *Model) allRows() [][]string {
	if m.status == nil {
		return nil
	}
	agents := append([]api.AgentStatus(nil), m.status.Agents...)
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Ticket < agents[j].Ticket })
	now := m.clock.Now()
	rows := make([][]string, 0, len(agents))
	for i := range agents {
		a := &agents[i]
		rows = append(rows, []string{
			a.Ticket, a.Title, dash(a.TicketStatus),
			// The time in state is as of the last event: the TUI never polls,
			// so it can read stale until something happens.
			a.StateLabel() + " " + humanDuration(now.Sub(a.Since)),
			a.Harness, modelEffort(a), a.Pool, dash(strings.Join(a.Blockers, ",")), dash(a.Note),
		})
	}
	return rows
}

func (m *Model) View() tea.View {
	if m.status == nil {
		return tea.NewView("waiting for the daemon's status...")
	}
	rows := m.Rows()
	avail := m.width - 2*len(headers)
	if m.width == 0 {
		avail = 1 << 20 // no size yet: don't squeeze
	}
	cols := fitColumns(headers, m.allRows(), avail)
	tcols := make([]table.Column, len(cols))
	for i, w := range cols {
		tcols[i] = table.Column{Title: headers[i], Width: w}
	}
	styles := table.DefaultStyles()
	// Read-only: nothing is selected, so nothing is highlighted.
	styles.Selected = lipgloss.NewStyle()
	trows := make([]table.Row, len(rows))
	for i, r := range rows {
		trows[i] = r
	}
	// The viewport is clipped to its width, and its height excludes the column header.
	t := table.New(table.WithColumns(tcols), table.WithRows(trows), table.WithStyles(styles),
		table.WithWidth(sum(cols)+2*len(cols)), table.WithHeight(m.pageSize()+1), table.WithFocused(false))
	footer := fmt.Sprintf("page %d/%d  ←/→ page  q quit", m.page+1, m.pages())
	v := tea.NewView(strings.Join([]string{m.headerLine(), t.View(), footer}, "\n"))
	v.AltScreen = true
	return v
}

func (m *Model) headerLine() string {
	st := m.status
	var pools []string
	for _, p := range st.Pools {
		name := p.Pool
		if name == "normal" {
			name = "pool"
		}
		pools = append(pools, fmt.Sprintf("%s %d/%d", name, p.Active, p.Max))
	}
	if len(pools) == 0 {
		pools = []string{"pool n/a"}
	}
	autonomy := "autonomy off"
	if st.Autonomy {
		autonomy = "autonomy on"
	}
	plan := "plan: n/a"
	if len(st.Limits) > 0 {
		var ls []string
		for _, l := range st.Limits {
			name := l.Harness
			if l.Window != "" {
				name += " " + l.Window
			}
			s := fmt.Sprintf("%s %.0f%%", name, l.UsedPercent)
			if !l.ResetsAt.IsZero() {
				s += " resets in " + humanDuration(l.ResetsAt.Sub(m.clock.Now()))
			}
			if l.Held {
				s += " HELD"
			}
			ls = append(ls, s)
		}
		plan = "plan: " + strings.Join(ls, ", ")
	}
	return fmt.Sprintf("%s | %s, agent-filed %d/%d | %s", strings.Join(pools, ", "), autonomy, st.Outstanding, st.Budget, plan)
}

func modelEffort(a *api.AgentStatus) string {
	switch {
	case a.Model != "" && a.Effort != "":
		return a.Model + "/" + a.Effort
	case a.Model != "":
		return a.Model
	}
	return dash(a.Effort)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
