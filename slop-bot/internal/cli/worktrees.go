package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func (a *app) worktreesCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "worktrees",
		Short: "List every ticket's worktrees: whether each is kept, and why",
		Long: "List the worktrees slop-bot made for tickets, as recorded in its state: a live agent's are in " +
			"use; an ended agent's are kept when removing them would lose work (uncommitted or untracked files, " +
			"commits on no remote, an operation in progress, a lock, a worktree inside it), and otherwise " +
			"removable. `slop-bot worktrees clean <ticket>` removes them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var wts []api.Worktree
			if err := a.do(cmd, http.MethodGet, api.PathWorktrees, nil, &wts); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), wts)
			}
			if len(wts) == 0 {
				return a.printOK(cmd, "no worktrees recorded")
			}
			out := &errWriter{w: cmd.OutOrStdout()}
			for i := range wts {
				printWorktree(out, &wts[i])
			}
			return out.err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	c.AddCommand(a.worktreesCleanCmd())
	return c
}

func printWorktree(out *errWriter, w *api.Worktree) {
	out.printf("ticket %s, %s: %s (agent %s, %s)\n", w.Ticket, w.Repo, strings.ReplaceAll(w.Status, "_", " "), w.Agent, w.AgentState)
	out.printf("  %s", w.Path)
	if w.Branch != "" {
		out.printf(" on %s", w.Branch)
	}
	out.printf("\n")
	for _, r := range w.Reasons {
		out.printf("  %s: %s\n", r.Kind, r.Detail)
	}
	if w.Error != "" {
		out.printf("  couldn't check it: %s\n", w.Error)
	}
	switch w.Status {
	case api.WorktreeRemovable:
		out.printf("  remove it: slop-bot worktrees clean %s\n", w.Ticket)
	case api.WorktreeKept, api.WorktreeReleased, api.WorktreeUnchecked:
		out.printf("  once you've looked, discard it: slop-bot worktrees clean %s --force\n", w.Ticket)
	}
	out.printf("\n")
}

func (a *app) worktreesCleanCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "clean <ticket>",
		Short: "Remove a ticket's worktrees that hold no work; --force discards them whatever they hold",
		Long: "Remove the ticket's recorded worktrees and its local slop-bot/t<ticket>/ branches, keeping any that " +
			"would lose work, as at the ticket's end. With --force, after you confirm, they go whatever they " +
			"hold: uncommitted files, unpushed commits, a lock. A worktree holding another worktree is still " +
			"refused, and so is a ticket whose agent is live.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ticket := args[0]
			out := &errWriter{w: cmd.OutOrStdout()}
			if force {
				if err := a.confirmDiscard(cmd, out, ticket); err != nil {
					return err
				}
			}
			var results []api.CleanedWorktree
			path := strings.Replace(api.PathWorktreesClean, "{ticket}", url.PathEscape(ticket), 1)
			if err := a.do(cmd, http.MethodPost, path, &api.CleanWorktrees{Force: force}, &results); err != nil {
				return err
			}
			var failed int
			for _, r := range results {
				switch {
				case r.Error != "":
					failed++
					out.printf("couldn't clean up %s: %s\n", r.Path, r.Error)
				case r.Removed:
					out.printf("removed %s\n", r.Path)
				default:
					why := make([]string, 0, len(r.Reasons))
					for _, reason := range r.Reasons {
						why = append(why, reason.Kind+": "+reason.Detail)
					}
					out.printf("kept %s, which holds work: %s. Once you've looked, `slop-bot worktrees clean %s --force` discards it\n",
						r.Path, strings.Join(why, "; "), ticket)
				}
			}
			if out.err != nil {
				return out.err
			}
			if failed > 0 {
				return fmt.Errorf("%d of ticket %s's worktrees couldn't be cleaned up", failed, ticket)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "discard the worktrees and branches whatever they hold, once you confirm")
	return c
}

// errNotDiscarded is a forced clean the user didn't confirm.
var errNotDiscarded = errors.New("not confirmed; nothing was discarded")

// confirmDiscard shows what a forced clean of ticket loses and has the user
// confirm it: nothing a remote doesn't have comes back.
func (a *app) confirmDiscard(cmd *cobra.Command, out *errWriter, ticket string) error {
	var wts []api.Worktree
	if err := a.do(cmd, http.MethodGet, api.PathWorktrees, nil, &wts); err != nil {
		return err
	}
	var mine []*api.Worktree
	for i := range wts {
		if wts[i].Ticket == ticket {
			mine = append(mine, &wts[i])
		}
	}
	if len(mine) == 0 {
		return fmt.Errorf("no worktrees are recorded for ticket %s", ticket)
	}
	out.printf("--force discards ticket %s's worktrees, and its slop-bot/t%s/ branches, whatever they hold:\n", ticket, ticket)
	for _, w := range mine {
		out.printf("  %s\n", w.Path)
		for _, r := range w.Reasons {
			out.printf("    %s: %s\n", r.Kind, r.Detail)
		}
	}
	out.printf("type 'discard' to discard them: ")
	if out.err != nil {
		return out.err
	}
	if !confirmed(cmd.Context(), a.deps.Stdin, "discard") {
		return errNotDiscarded
	}
	return nil
}
