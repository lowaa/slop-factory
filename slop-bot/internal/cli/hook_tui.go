package cli

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
)

// hookTUICmd is `slop-bot hook tui <session> <event>`, the hook a TUI
// harness's profile installs for each of its hook events: it relays the
// payload on stdin to the daemon, which hands it to the session's
// conversation, and prints the reply for the harness to read.
//
// When the daemon can't answer (it is restarting, say), it answers for it
// with the profile's offline reply: the harness's defaults, but a merge the
// guard's classifier recognises is still refused. It fails, printing
// nothing, only when it can't do that either, and the profile's own
// fallback (agy's `|| printf ...`) applies.
func (a *app) hookTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui <session> <event>",
		Short: "Relay a TUI harness's hook to its conversation in the daemon",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, event := args[0], args[1]
			payload := []byte{}
			if a.deps.Stdin != nil {
				var err error
				if payload, err = io.ReadAll(a.deps.Stdin); err != nil {
					return err
				}
			}
			reply, err := a.relayHook(cmd.Context(), session, event, payload)
			if err != nil {
				p, ok := daemon.TUIProfile(session)
				if !ok {
					return err
				}
				if reply, err = p.Offline(cmd.Context(), event, payload, offlineGuard(err)); err != nil {
					return err
				}
			}
			_, err = cmd.OutOrStdout().Write(reply)
			return err
		},
	}
}

// relayHook puts the hook to the daemon, as the agent.
func (a *app) relayHook(ctx context.Context, session, event string, payload []byte) ([]byte, error) {
	if a.deps.Token == "" {
		return nil, errors.New("hook tui: no agent token: it runs from an agent's session")
	}
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, hookAskTimeout)
	defer cancel()
	var out api.HookTUIReply
	if err := c.Do(ctx, http.MethodPost, api.PathHookTUI, &api.HookTUI{Session: session, Event: event, Payload: payload}, &out); err != nil {
		return nil, err
	}
	return out.Reply, nil
}

// offlineGuard is the merge guard without the daemon: a merge is refused,
// since nothing can say it's authorised, and everything else may run.
func offlineGuard(why error) tuidriver.Guard {
	cl := offlineClassifier()
	return func(ctx context.Context, call *tuidriver.ToolCall) string {
		v := mergeguard.Unreachable(ctx, cl, &mergeguard.Call{Tool: call.Name, Command: call.Command, Cwd: call.Cwd}, why)
		if v.Allow {
			return ""
		}
		return v.Reason
	}
}
