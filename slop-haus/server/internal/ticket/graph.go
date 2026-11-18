package ticket

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The blocking graph. "A is blocked by B" is the same edge the scheduler
// already uses to decide what is claimable -- ticket_deps -- so this is a
// view over existing data rather than a parallel concept. A ticket may be
// blocked by any number of others.
//
// Everything a caller needs to *draw* the graph is computed here: layers,
// transitive blockers, and how much each ticket is holding up. The browser
// gets a finished answer rather than a pile of edges and a traversal to
// write, which also means one definition of "blocked" rather than a second
// one in TypeScript that can drift from the claim path's.

// GraphNode is a ticket and its position in the blocking graph.
type GraphNode struct {
	ID       int64          `json:"id"`
	Title    string         `json:"title"`
	Status   Status         `json:"status"`
	Priority model.Priority `json:"priority"`

	// BlockedBy and Blocks are the direct edges, in both directions.
	BlockedBy []int64 `json:"blocked_by"`
	Blocks    []int64 `json:"blocks"`

	// Blocked reports whether anything is actually holding this ticket up
	// right now. It uses the same rule as the claim path -- a blocker
	// counts only while it is non-terminal -- so a ticket whose blockers
	// are all done or cancelled reads as unblocked here and is genuinely
	// claimable, rather than the two disagreeing.
	Blocked bool `json:"blocked"`

	// TransitiveBlockers is every non-terminal ticket upstream of this
	// one, not just the direct ones. It answers "what actually has to
	// happen before this can start", which for anything past the first
	// layer is not a question a human should have to trace by eye.
	TransitiveBlockers []int64 `json:"transitive_blockers"`

	// Unblocks is how many unfinished tickets are waiting on this one,
	// directly or further downstream -- what finishing it would free. It
	// is the number worth sorting by when deciding what to do next.
	Unblocks int `json:"unblocks"`

	// Layer is the ticket's depth in the graph: 0 for anything with no
	// unfinished blockers, otherwise one past its deepest blocker.
	// Rendering a readable graph is then a matter of drawing columns.
	Layer int `json:"layer"`

	// Hidden marks a ticket the filter did not match that is here anyway,
	// because it holds up a ticket that did. It stands in for the blocker
	// so the shown ticket is not drawn as free to start when it is not.
	Hidden bool `json:"hidden"`
}

// GraphEdge is one blocking relationship, named from the reader's point of
// view: Blocker holds up Blocked.
type GraphEdge struct {
	Blocker int64 `json:"blocker"`
	Blocked int64 `json:"blocked"`
}

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// Layers lists node ids by depth, so a caller can lay the graph out
	// without computing a topological order itself.
	Layers [][]int64 `json:"layers"`
	// Truncated reports that more tickets matched than the limit allowed;
	// the newest were kept.
	Truncated bool `json:"truncated"`
}

const (
	DefaultGraphLimit = 1000
	MaxGraphLimit     = 2000
)

// GraphQuery says which tickets the graph is for. The zero value is every
// ticket, without a limit, as the per-ticket views need.
type GraphQuery struct {
	Filter ListFilter
	// Limit caps how many matching tickets are drawn; zero is no cap.
	Limit int
}

// GraphInput is the raw material Service.Graph works from: every ticket,
// and every blocking edge between them.
type GraphInput struct {
	Tickets []Ticket
	Edges   []GraphEdge
	// Hidden are the tickets that are only here as a shown ticket's
	// unfinished blockers.
	Hidden map[int64]bool
	// Unblocks, when set, is each ticket's count over the whole board. A
	// filtered input lacks the dependants the filter hid, so counting from
	// it would understate what finishing a ticket frees.
	Unblocks  map[int64]int
	Truncated bool
}

// buildGraph turns tickets and edges into the finished view.
//
// The graph is acyclic by construction -- AddDependency refuses an edge
// that would close a loop -- but this must not *depend* on that, because a
// row inserted by hand or by a future code path would otherwise hang the
// server rather than render oddly. The layering below is Kahn's algorithm,
// which terminates either way, and anything left unresolved is placed in a
// final layer rather than dropped.
func buildGraph(in GraphInput) Graph {
	nodes := make(map[int64]*GraphNode, len(in.Tickets))
	order := make([]int64, 0, len(in.Tickets))
	terminal := make(map[int64]bool, len(in.Tickets))

	for i := range in.Tickets {
		t := &in.Tickets[i]
		nodes[t.ID] = &GraphNode{
			ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority,
			BlockedBy: []int64{}, Blocks: []int64{}, TransitiveBlockers: []int64{},
			Hidden: in.Hidden[t.ID],
		}
		order = append(order, t.ID)
		terminal[t.ID] = t.Status.IsTerminal()
	}

	edges := make([]GraphEdge, 0, len(in.Edges))
	for _, e := range in.Edges {
		blocked, okBlocked := nodes[e.Blocked]
		blocker, okBlocker := nodes[e.Blocker]
		if !okBlocked || !okBlocker {
			continue
		}
		blocked.BlockedBy = append(blocked.BlockedBy, e.Blocker)
		blocker.Blocks = append(blocker.Blocks, e.Blocked)
		edges = append(edges, e)
	}

	for _, id := range order {
		n := nodes[id]
		n.TransitiveBlockers = transitiveBlockers(nodes, terminal, id)
		n.Blocked = len(n.TransitiveBlockers) > 0
	}

	// Unblocks is the exact inverse of TransitiveBlockers rather than its
	// own walk downstream, so the two cannot disagree: a ticket that does
	// not list this one as a blocker is not one finishing it would free.
	// That rules out tickets already past a finished blocker, and finished
	// tickets, neither of which is waiting on anything.
	for _, id := range order {
		if terminal[id] {
			continue
		}
		for _, blocker := range nodes[id].TransitiveBlockers {
			nodes[blocker].Unblocks++
		}
	}
	for id, n := range in.Unblocks {
		if node, ok := nodes[id]; ok {
			node.Unblocks = n
		}
	}

	layers := assignLayers(nodes, terminal, order)

	out := Graph{Nodes: make([]GraphNode, 0, len(order)), Edges: edges, Layers: layers, Truncated: in.Truncated}
	for _, id := range order {
		out.Nodes = append(out.Nodes, *nodes[id])
	}
	return out
}

// transitiveBlockers walks upstream, counting only non-terminal tickets: a
// finished blocker holds up nothing, and neither does anything behind it.
func transitiveBlockers(nodes map[int64]*GraphNode, terminal map[int64]bool, start int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}

	var walk func(int64)
	walk = func(id int64) {
		for _, blocker := range nodes[id].BlockedBy {
			if seen[blocker] || terminal[blocker] {
				continue
			}
			seen[blocker] = true
			out = append(out, blocker)
			walk(blocker)
		}
	}
	walk(start)
	return out
}

// assignLayers puts each ticket one past its deepest unfinished blocker.
//
// Terminal blockers are ignored, so finishing a ticket visibly moves what
// it was holding up to the front rather than leaving it stranded behind a
// done row.
func assignLayers(nodes map[int64]*GraphNode, terminal map[int64]bool, order []int64) [][]int64 {
	remaining := make(map[int64]int, len(order))
	for _, id := range order {
		count := 0
		for _, blocker := range nodes[id].BlockedBy {
			if !terminal[blocker] {
				count++
			}
		}
		remaining[id] = count
	}

	layers := [][]int64{}
	placed := make(map[int64]bool, len(order))
	for len(placed) < len(order) {
		layer := []int64{}
		for _, id := range order {
			if !placed[id] && remaining[id] == 0 {
				layer = append(layer, id)
			}
		}
		if len(layer) == 0 {
			// Only reachable if the data contains a cycle, which
			// AddDependency prevents. Place the rest together rather than
			// looping forever -- a graph that renders oddly beats a
			// request that never returns.
			rest := []int64{}
			for _, id := range order {
				if !placed[id] {
					rest = append(rest, id)
					placed[id] = true
				}
			}
			layers = append(layers, rest)
			break
		}
		for _, id := range layer {
			placed[id] = true
			nodes[id].Layer = len(layers)
		}
		for _, id := range layer {
			for _, blocked := range nodes[id].Blocks {
				if !terminal[id] {
					remaining[blocked]--
				}
			}
		}
		layers = append(layers, layer)
	}
	return layers
}

// BlockerCandidate is a ticket that may be added as a blocker of another:
// enough to label it in a picker, not the whole ticket.
type BlockerCandidate struct {
	ID       int64          `json:"id"`
	Title    string         `json:"title"`
	Status   Status         `json:"status"`
	Priority model.Priority `json:"priority"`
}

// blockerCandidates lists what can be added as a blocker of ticketID, and
// reports whether the ticket exists at all.
//
// Three groups are left out, for two different reasons. The ticket itself
// and anything downstream of it are excluded because AddDependency would
// refuse the edge as a cycle; tickets that already block it are excluded
// because the edge is already there. Finished tickets are excluded on
// different grounds: blocked-ness is a predicate, so that edge is accepted
// and then holds nothing up -- offered in a picker it reads as an action
// that did nothing.
//
// The downstream walk ignores status, which the terminal-pruned
// TransitiveBlockers does not, and that difference is the point: the SQL
// cycle check ignores status too. Reusing TransitiveBlockers here would
// offer a candidate whose only route back to this ticket runs through a
// finished one -- and the API would then refuse it, which is exactly what
// a candidate list is for avoiding.
func blockerCandidates(in GraphInput, ticketID int64) ([]BlockerCandidate, bool) {
	blocks := map[int64][]int64{}
	alreadyBlocking := map[int64]bool{}
	for _, e := range in.Edges {
		blocks[e.Blocker] = append(blocks[e.Blocker], e.Blocked)
		if e.Blocked == ticketID {
			alreadyBlocking[e.Blocker] = true
		}
	}

	// Everything that already reaches ticketID by waiting on it, however
	// far back, plus ticketID itself.
	downstream := map[int64]bool{ticketID: true}
	var walk func(int64)
	walk = func(id int64) {
		for _, blocked := range blocks[id] {
			if downstream[blocked] {
				continue
			}
			downstream[blocked] = true
			walk(blocked)
		}
	}
	walk(ticketID)

	candidates := []BlockerCandidate{}
	found := false
	for i := range in.Tickets {
		t := &in.Tickets[i]
		if t.ID == ticketID {
			found = true
			continue
		}
		if downstream[t.ID] || alreadyBlocking[t.ID] || t.Status.IsTerminal() {
			continue
		}
		candidates = append(candidates, BlockerCandidate{
			ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority,
		})
	}
	return candidates, found
}
