package daemon

import (
	"net/http"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

var worktreeErrors = append([]control.ErrorMapping{
	{Err: agents.ErrWorktreesInUse, Status: http.StatusConflict},
	{Err: agents.ErrNoWorktrees, Status: http.StatusNotFound},
}, agentErrors...)

// worktreeRoutes lists the worktrees slop-bot recorded, and cleans up a
// ticket's on the user's say-so: the way out of one Cleanup kept.
type worktreeRoutes struct{ d *Daemon }

func (wr *worktreeRoutes) register(s *control.Server) {
	s.HandleFunc("GET "+api.PathWorktrees, wr.list)
	s.HandleFunc("POST "+api.PathWorktreesClean, wr.clean)
}

func worktreeFail(w http.ResponseWriter, err error) {
	control.WriteError(w, control.StatusFor(err, worktreeErrors), err)
}

func (wr *worktreeRoutes) list(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r, "worktrees") {
		return
	}
	entries, err := wr.d.agents.ListWorktrees(r.Context())
	if err != nil {
		worktreeFail(w, err)
		return
	}
	out := make([]api.Worktree, 0, len(entries))
	for _, e := range entries {
		wt := api.Worktree{
			Ticket: string(e.Agent.Ticket), Agent: string(e.Agent.ID), AgentState: string(e.Agent.State),
			Repo: e.Worktree.Repo, Path: e.Worktree.Path, Branch: e.Worktree.Branch,
			Status: string(e.Status), Reasons: apiReasons(e.Reasons),
		}
		if e.Err != nil {
			wt.Error = e.Err.Error()
		}
		out = append(out, wt)
	}
	control.WriteJSON(w, http.StatusOK, out)
}

func (wr *worktreeRoutes) clean(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r, "worktrees") {
		return
	}
	var in api.CleanWorktrees
	if !decode(w, r, &in) {
		return
	}
	results, err := wr.d.agents.CleanWorktrees(r.Context(), ident.TicketID(r.PathValue("ticket")), in.Force)
	if err != nil {
		worktreeFail(w, err)
		return
	}
	out := make([]api.CleanedWorktree, 0, len(results))
	for _, res := range results {
		c := api.CleanedWorktree{Repo: res.Worktree.Repo, Path: res.Worktree.Path, Removed: res.Removed, Reasons: apiReasons(res.Reasons)}
		if res.Err != nil {
			c.Error = res.Err.Error()
		}
		out = append(out, c)
	}
	control.WriteJSON(w, http.StatusOK, out)
}

func apiReasons(rs []worktree.Reason) []api.WorktreeReason {
	out := make([]api.WorktreeReason, 0, len(rs))
	for _, r := range rs {
		out = append(out, api.WorktreeReason{Kind: string(r.Kind), Detail: r.Detail})
	}
	return out
}
