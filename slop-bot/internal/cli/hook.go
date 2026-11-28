package cli

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
)

// hookAskTimeout bounds the hook's question to the daemon, which answers
// from memory and state.db; past it the hook decides alone rather than
// holding the agent's tool call.
const hookAskTimeout = 10 * time.Second

// hookCmd is what harnesses run from their hooks. It isn't an agent verb:
// the harness runs it, so it stays out of the briefing's CLI reference.
func (a *app) hookCmd() *cobra.Command {
	c := &cobra.Command{Use: "hook", Short: "Run from a harness's hooks (not for direct use)", Hidden: true}
	c.AddCommand(&cobra.Command{
		Use:   "pre-tool",
		Short: "The merge guard: a PreToolUse hook that refuses unauthorised merges",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stdin := a.deps.Stdin
			if stdin == nil {
				stdin = strings.NewReader("")
			}
			git, err := gitx.New(&gitx.Options{Bin: "git", Logger: slog.New(slog.DiscardHandler)})
			var cl *mergeguard.Classifier
			if err == nil {
				cl = mergeguard.NewClassifier(git)
			} else {
				// No git: the push checks lose the current branch, the
				// rest still works.
				cl = mergeguard.NewClassifier(nil)
			}
			return mergeguard.RunHook(cmd.Context(), &mergeguard.HookOptions{
				Ask: func(ctx context.Context, call *api.PreTool) (*api.PreToolVerdict, error) {
					c, err := a.client()
					if err != nil {
						return nil, err
					}
					ctx, cancel := context.WithTimeout(ctx, hookAskTimeout)
					defer cancel()
					var v api.PreToolVerdict
					if err := c.Do(ctx, http.MethodPost, api.PathPreTool, call, &v); err != nil {
						return nil, err
					}
					return &v, nil
				},
				Classifier: cl,
				Stdin:      stdin, Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			})
		},
	})
	c.AddCommand(a.hookTUICmd())
	return c
}
