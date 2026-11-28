package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/lifecycle"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// EventMergeRefused is recorded when the guard refuses an agent's merge.
const EventMergeRefused = "merge.refused"

// guardComponent builds the merge guard once the ticketing system and
// state.db it reads are up.
func (d *Daemon) guardComponent() lifecycle.Component {
	return &lifecycle.Func{
		N: "merge guard",
		StartFunc: func(context.Context) error {
			git, err := gitx.New(&gitx.Options{Bin: "git", Logger: d.opts.Logger})
			if err != nil {
				return err
			}
			d.guard = mergeguard.New(&mergeguard.Options{
				Classifier: mergeguard.NewClassifier(git), Tickets: d.tickets, Grants: d.state.MergeGrants, Logger: d.opts.Logger,
			})
			return nil
		},
		StopFunc: func(context.Context) error { return nil },
	}
}

// preTool is the guard's verdict on one of the calling agent's tool calls.
func (ar *agentRoutes) preTool(w http.ResponseWriter, r *http.Request) {
	var in api.PreTool
	if !decode(w, r, &in) {
		return
	}
	v := ar.d.checkTool(r.Context(), &mergeguard.Call{Tool: in.Tool, Command: in.Command, Cwd: in.Cwd})
	out := api.PreToolVerdict{Allow: v.Allow, Reason: v.Reason}
	if v.Finding != nil {
		out.Merge = string(v.Finding.Kind)
	}
	control.WriteJSON(w, http.StatusOK, out)
}

// checkTool is the guard's verdict on a tool call by the caller ctx
// carries, the refusal recorded: for `slop-bot hook pre-tool`, and for a
// TUI harness's pre-tool hook, relayed by `slop-bot hook tui`.
func (d *Daemon) checkTool(ctx context.Context, call *mergeguard.Call) *mergeguard.Verdict {
	by := control.PrincipalFrom(ctx)
	v := d.guard.Check(ctx, by, call)
	if !v.Allow {
		payload, _ := json.Marshal(map[string]string{"kind": string(v.Finding.Kind), "detail": v.Finding.Detail})
		if _, err := d.state.Events.Append(ctx, &state.NewEvent{
			Type: EventMergeRefused, Agent: by.Agent, Ticket: by.Ticket, Payload: payload,
		}); err != nil {
			d.opts.Logger.Warn("daemon: recording a refused merge", "err", err)
		}
	}
	return v
}

// tuiGuard is the TUI kit's Guard: the merge guard, as for any harness.
func (d *Daemon) tuiGuard(ctx context.Context, call *tuidriver.ToolCall) string {
	v := d.checkTool(ctx, &mergeguard.Call{Tool: call.Name, Command: call.Command, Cwd: call.Cwd})
	if v.Allow {
		return ""
	}
	return v.Reason
}

// authoriseMerge is tell's --merge: the user's go-ahead, recorded before
// the message is delivered, so the agent that reads it can merge at once.
// It returns the text to deliver: the go-ahead, then the user's own words,
// so the agent can't miss that it is authorised.
func (ar *agentRoutes) authoriseMerge(r *http.Request, in *api.Tell) (string, error) {
	if in.All {
		return "", fmt.Errorf("%w: a merge is authorised for one agent's ticket, not --all", errBadRequest)
	}
	if err := ar.d.agents.AuthoriseMerge(r.Context(), control.PrincipalFrom(r.Context()), in.To); err != nil {
		return "", err
	}
	if in.Text == "" || agents.SaysMerge(in.Text) {
		return agents.MergeGoAhead, nil
	}
	return agents.MergeGoAhead + "\n\n" + in.Text, nil
}
