package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

// agentAnnotation marks a command agents may run. The briefing's generated
// CLI reference lists exactly these.
const agentAnnotation = "slop-bot/agent"

func agentCmd(c *cobra.Command) *cobra.Command {
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations[agentAnnotation] = "yes"
	return c
}

func (a *app) ticketCmd() *cobra.Command {
	c := &cobra.Command{Use: "ticket", Short: "File, read and change tickets"}
	c.AddCommand(
		a.ticketCreateCmd(), a.ticketListCmd(), a.ticketShowCmd(), a.ticketSetCmd(),
		a.ticketAssignCmd(), a.ticketCommentCmd(), a.ticketBlockCmd(true), a.ticketBlockCmd(false),
		a.ticketCancelCmd(),
	)
	return agentCmd(c)
}

type specFlags struct{ harness, model, effort string }

func (s *specFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&s.harness, "harness", "", "harness to run it (claude-code, codex, pi, agy); blank: default_harness")
	c.Flags().StringVar(&s.model, "model", "", "model; blank: harnesses.<name>.model from the config, else the harness's own default")
	c.Flags().StringVar(&s.effort, "effort", "", "effort or thinking level; blank: the harness's own default")
}

func (s *specFlags) spec() api.AgentSpec {
	return api.AgentSpec{Harness: s.harness, Model: s.model, Effort: s.effort}
}

func readBody(body, file string) (string, error) {
	if file == "" {
		return body, nil
	}
	if body != "" {
		return "", fmt.Errorf("give --body or --body-file, not both")
	}
	if file == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(file) // #nosec G304 -- the caller names the file to read
	return string(b), err
}

func (a *app) ticketCreateCmd() *cobra.Command {
	var in api.NewTicket
	var spec specFlags
	var bodyFile string
	var asJSON bool
	c := &cobra.Command{
		Use:   "create --title <title>",
		Short: "File a ticket",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if in.Body, err = readBody(in.Body, bodyFile); err != nil {
				return err
			}
			in.Agent = spec.spec()
			var t api.Ticket
			if err := a.do(cmd, http.MethodPost, api.PathTickets, &in, &t); err != nil {
				return err
			}
			return a.printTicket(cmd, &t, asJSON)
		},
	}
	f := c.Flags()
	f.StringVar(&in.Title, "title", "", "the ticket's title (required)")
	f.StringVar(&in.Body, "body", "", "the ticket's description")
	f.StringVar(&bodyFile, "body-file", "", "read the description from a file (- for stdin)")
	f.StringVar(&in.Status, "status", "", "starting status: preparing (default) or open")
	f.BoolVar(&in.AutoMerge, "auto-merge", false, "let the agent merge without a person (user only)")
	f.BoolVar(&in.Urgent, "urgent", false, "put it in the urgent pool (agents: only for something breaking the pipeline)")
	f.StringVar(&in.UrgentReason, "reason", "", "why it's urgent (required for agents)")
	f.StringSliceVar(&in.Repos, "repo", nil, "a repo it touches, from the repo lookup; repeat, primary first")
	f.StringSliceVar(&in.BlockedOn, "blocked-on", nil, "a ticket it waits on; repeat")
	f.BoolVar(&in.Assign, "assign", false, "assign it to slop-bot's user, so an agent picks it up")
	f.BoolVar(&asJSON, "json", false, "print JSON")
	spec.add(c)
	_ = c.MarkFlagRequired("title")
	return agentCmd(c)
}

func (a *app) ticketListCmd() *cobra.Command {
	var statuses []string
	var assignee string
	var mine, asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List tickets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for _, s := range statuses {
				q.Add("status", s)
			}
			switch {
			case mine:
				q.Set("assignee", "me")
			case cmd.Flags().Changed("assignee"):
				q.Set("assignee", assignee)
			}
			path := api.PathTickets
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var ts []api.Ticket
			if err := a.do(cmd, http.MethodGet, path, nil, &ts); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), ts)
			}
			return printTicketTable(cmd.OutOrStdout(), ts)
		},
	}
	c.Flags().StringSliceVar(&statuses, "status", nil, "only these statuses")
	c.Flags().StringVar(&assignee, "assignee", "", `only this assignee ("" for unassigned)`)
	c.Flags().BoolVar(&mine, "mine", false, "only tickets assigned to slop-bot's user")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return agentCmd(c)
}

func (a *app) ticketShowCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a ticket, its blockers and comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var t api.Ticket
			if err := a.do(cmd, http.MethodGet, ticketPath(api.PathTicket, args[0]), nil, &t); err != nil {
				return err
			}
			return a.printTicket(cmd, &t, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return agentCmd(c)
}

// setFlags are `ticket set`'s flags; only those given change the ticket.
type setFlags struct {
	title, body, bodyFile, status, reason string
	autoMerge, urgent                     bool
	repos                                 []string
	spec                                  specFlags
}

// change builds the edit from the flags the user gave.
func (s *setFlags) change(f *pflag.FlagSet) (*api.TicketChange, error) {
	ch := &api.TicketChange{}
	if f.Changed("title") {
		ch.Title = &s.title
	}
	if f.Changed("body") || f.Changed("body-file") {
		b, err := readBody(s.body, s.bodyFile)
		if err != nil {
			return nil, err
		}
		ch.Body = &b
	}
	if f.Changed("status") {
		ch.Status = &s.status
	}
	if f.Changed("harness") || f.Changed("model") || f.Changed("effort") {
		ch.Agent = &api.AgentSpecChange{}
		if f.Changed("harness") {
			ch.Agent.Harness = &s.spec.harness
		}
		if f.Changed("model") {
			ch.Agent.Model = &s.spec.model
		}
		if f.Changed("effort") {
			ch.Agent.Effort = &s.spec.effort
		}
	}
	if f.Changed("auto-merge") {
		ch.AutoMerge = &s.autoMerge
	}
	if f.Changed("urgent") {
		ch.Urgent, ch.UrgentReason = &s.urgent, s.reason
	}
	if f.Changed("repo") {
		ch.Repos = &s.repos
	}
	return ch, nil
}

func (a *app) ticketSetCmd() *cobra.Command {
	var s setFlags
	var asJSON bool
	c := &cobra.Command{
		Use:   "set <id>",
		Short: "Change a ticket's fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := s.change(cmd.Flags())
			if err != nil {
				return err
			}
			var t api.Ticket
			if err := a.do(cmd, http.MethodPatch, ticketPath(api.PathTicket, args[0]), ch, &t); err != nil {
				return err
			}
			return a.printTicket(cmd, &t, asJSON)
		},
	}
	f := c.Flags()
	f.StringVar(&s.title, "title", "", "new title")
	f.StringVar(&s.body, "body", "", "new description")
	f.StringVar(&s.bodyFile, "body-file", "", "read the new description from a file (- for stdin)")
	f.StringVar(&s.status, "status", "", "new status")
	f.BoolVar(&s.autoMerge, "auto-merge", false, "let the agent merge without a person (user only)")
	f.BoolVar(&s.urgent, "urgent", false, "set or clear urgent (--urgent=false)")
	f.StringVar(&s.reason, "reason", "", "why it's urgent (required for agents)")
	f.StringSliceVar(&s.repos, "repo", nil, "replace the repos it touches; repeat, primary first")
	f.BoolVar(&asJSON, "json", false, "print JSON")
	s.spec.add(c)
	return agentCmd(c)
}

func (a *app) ticketAssignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <id> [<user>|me]",
		Short: "Assign a ticket (default: to slop-bot's user); with an empty user, unassign it",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			who := "me"
			if len(args) == 2 {
				who = args[1]
			}
			var t api.Ticket
			if err := a.do(cmd, http.MethodPost, ticketPath(api.PathTicketAssign, args[0]), &api.Assign{Assignee: who}, &t); err != nil {
				return err
			}
			return a.printTicket(cmd, &t, false)
		},
	}
}

func (a *app) ticketCommentCmd() *cobra.Command {
	return agentCmd(&cobra.Command{
		Use:   "comment <id> <text>",
		Short: "Comment on a ticket: for product decisions, blockers and scope changes",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var c api.Comment
			if err := a.do(cmd, http.MethodPost, ticketPath(api.PathTicketComments, args[0]), &api.NewComment{Body: args[1]}, &c); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "commented on %s\n", args[0])
			return err
		},
	})
}

func (a *app) ticketBlockCmd(add bool) *cobra.Command {
	var on string
	use, short := "block <id> --on <blocker>", "Make a ticket wait on another"
	if !add {
		use, short = "unblock <id> --on <blocker>", "Lift a blocker"
	}
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var ch api.BlockerChange
			var err error
			if add {
				err = a.do(cmd, http.MethodPost, ticketPath(api.PathTicketBlockers, args[0]), &api.NewBlocker{On: on}, &ch)
			} else {
				path := strings.Replace(ticketPath(api.PathTicketBlocker, args[0]), "{blocker}", url.PathEscape(on), 1)
				err = a.do(cmd, http.MethodDelete, path, nil, &ch)
			}
			if err != nil {
				return err
			}
			verb := map[bool]string{true: "now waits on", false: "no longer waits on"}[add]
			if !ch.Changed {
				verb = map[bool]string{true: "already waited on", false: "wasn't waiting on"}[add]
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", args[0], verb, on)
			return err
		},
	}
	c.Flags().StringVar(&on, "on", "", "the blocking ticket (required)")
	_ = c.MarkFlagRequired("on")
	return agentCmd(c)
}

func (a *app) ticketCancelCmd() *cobra.Command {
	return agentCmd(&cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a ticket (agents: your own, unless it is in discussion, or one you filed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cancelled := "cancelled"
			var t api.Ticket
			if err := a.do(cmd, http.MethodPatch, ticketPath(api.PathTicket, args[0]), &api.TicketChange{Status: &cancelled}, &t); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s cancelled\n", t.ID)
			return err
		},
	})
}

func ticketPath(pattern, id string) string {
	return strings.Replace(pattern, "{id}", url.PathEscape(id), 1)
}

func (a *app) do(cmd *cobra.Command, method, path string, in, out any) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	return c.Do(cmd.Context(), method, path, in, out)
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func flags(t *api.Ticket) string {
	var fs []string
	if t.Urgent {
		fs = append(fs, "urgent")
	}
	if t.AutoMerge {
		fs = append(fs, "auto-merge")
	}
	if len(t.BlockedBy) > 0 {
		fs = append(fs, "blocked")
	}
	if t.SpecError != "" {
		fs = append(fs, "bad-spec")
	}
	return strings.Join(fs, ",")
}

func printTicketTable(w io.Writer, ts []api.Ticket) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tASSIGNEE\tFLAGS\tTITLE")
	for i := range ts {
		t := &ts[i]
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, dash(t.Assignee), dash(flags(t)), t.Title)
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func who(p api.Principal) string {
	if p.Kind == "agent" {
		return "agent " + p.Agent
	}
	return "user"
}

func (a *app) printTicket(cmd *cobra.Command, t *api.Ticket, asJSON bool) error {
	w := cmd.OutOrStdout()
	if asJSON {
		return printJSON(w, t)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", t.ID, t.Title)
	fmt.Fprintf(&b, "- status: %s\n- assignee: %s\n", t.Status, dash(t.Assignee))
	fmt.Fprintf(&b, "- agent: harness=%s model=%s effort=%s\n", dash(t.Agent.Harness), dash(t.Agent.Model), dash(t.Agent.Effort))
	fmt.Fprintf(&b, "- auto-merge: %v\n- urgent: %v", t.AutoMerge, t.Urgent)
	if t.UrgentReason != "" {
		fmt.Fprintf(&b, " (%s)", t.UrgentReason)
	}
	fmt.Fprintf(&b, "\n- filed by: %s", who(t.CreatedBy))
	if t.Parent != "" {
		fmt.Fprintf(&b, " (from ticket %s)", t.Parent)
	}
	b.WriteString("\n")
	if len(t.Repos) > 0 {
		fmt.Fprintf(&b, "- repos: %s\n", strings.Join(t.Repos, ", "))
	}
	if t.SpecError != "" {
		fmt.Fprintf(&b, "- **won't be scheduled:** %s\n", t.SpecError)
	}
	if t.Blockers != nil {
		for _, e := range t.Blockers.BlockedBy {
			fmt.Fprintf(&b, "- waits on %s (%s, %s), added by %s\n", e.Ticket.ID, e.Ticket.Status, e.Ticket.Title, who(e.AddedBy))
		}
		for _, e := range t.Blockers.Blocks {
			fmt.Fprintf(&b, "- blocks %s (%s, %s)\n", e.Ticket.ID, e.Ticket.Status, e.Ticket.Title)
		}
	}
	if t.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimRight(t.Body, "\n"))
	}
	for _, c := range t.Comments {
		fmt.Fprintf(&b, "\n## %s, %s\n\n%s\n", who(c.Author), c.CreatedAt.Format("2006-01-02 15:04"), c.Body)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
