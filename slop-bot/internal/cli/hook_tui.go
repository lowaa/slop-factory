package cli

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

// hookTUICmd is `slop-bot hook tui <session> <event>`, the hook a TUI
// harness's profile installs for each of its hook events: it relays the
// payload on stdin to the daemon, which hands it to the session's
// conversation, and prints the reply for the harness to read.
//
// It prints nothing unless the daemon answered, and fails otherwise, so
// that the fallback the profile wraps each hook in (agy's `|| printf ...`)
// is what the harness reads while the daemon is away.
func (a *app) hookTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui <session> <event>",
		Short: "Relay a TUI harness's hook to its conversation in the daemon",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.deps.Token == "" {
				return errors.New("hook tui: no agent token: it runs from an agent's session")
			}
			payload := []byte{}
			if a.deps.Stdin != nil {
				var err error
				if payload, err = io.ReadAll(a.deps.Stdin); err != nil {
					return err
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), hookAskTimeout)
			defer cancel()
			var out api.HookTUIReply
			if err := c.Do(ctx, http.MethodPost, api.PathHookTUI, &api.HookTUI{Session: args[0], Event: args[1], Payload: payload}, &out); err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out.Reply)
			return err
		},
	}
}
