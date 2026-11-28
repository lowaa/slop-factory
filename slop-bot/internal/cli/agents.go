package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func (a *app) agentCmds() []*cobra.Command {
	return []*cobra.Command{
		a.tellCmd(), a.inboxCmd(), a.askCmd(), a.asksCmd(), a.discussCmd(), a.discussionsCmd(),
		a.readyToMergeCmd(), a.doneCmd(), a.statusCmd(), a.eventsCmd(), a.autonomyCmd(), a.transcriptCmd(), a.watchCmd(),
		a.attachCmd(), a.detachCmd(),
	}
}

func (a *app) printOK(cmd *cobra.Command, format string, args ...any) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...)
	return err
}

func (a *app) tellCmd() *cobra.Command {
	var all, interrupt, merge bool
	c := &cobra.Command{
		Use:   "tell <agent|ticket|user> <text>",
		Short: "Message an agent, every agent (--all), or the user",
		Long: "Message an agent (by id, or by its ticket's id), every working agent with --all, " +
			"or the user. A user's message to an agent waiting on an ask is its answer. " +
			"The user's --merge (or just \"merge\" to an agent in waiting_merge) authorises the agent to merge " +
			"its ticket's work, which a ticket without AutoMerge needs.",
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case all:
				return cobra.ExactArgs(1)(cmd, args)
			case merge:
				return cobra.RangeArgs(1, 2)(cmd, args)
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			in := api.Tell{All: all, Interrupt: interrupt, Merge: merge}
			if !all {
				in.To = args[0]
			}
			if len(args) > 1 || all {
				in.Text = args[len(args)-1]
			}
			var out api.Told
			if err := a.do(cmd, http.MethodPost, api.PathTell, &in, &out); err != nil {
				return err
			}
			return a.printOK(cmd, "told %d", out.Recipients)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "every working agent")
	c.Flags().BoolVar(&interrupt, "interrupt", false, "stop the agent's current turn first (user only)")
	c.Flags().BoolVar(&merge, "merge", false, "authorise the agent to merge its ticket's work; the text is optional (user only)")
	return agentCmd(c)
}

func (a *app) inboxCmd() *cobra.Command {
	var follow, log, asJSON bool
	var target string
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Read your messages (with --log, the history)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if log {
				q := url.Values{"log": {""}}
				if target != "" {
					q.Set("target", target)
				}
				return a.printInbox(cmd, api.PathInbox+"?"+q.Encode(), asJSON, true)
			}
			if !follow {
				return a.printInbox(cmd, api.PathInbox, asJSON, true)
			}
			return a.followInbox(cmd, asJSON)
		},
	}
	c.Flags().BoolVar(&follow, "follow", false, "keep reading as messages arrive")
	c.Flags().BoolVar(&log, "log", false, "the message history (user only)")
	c.Flags().StringVar(&target, "target", "", "with --log, only messages to or from this agent")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return agentCmd(c)
}

func (a *app) printInbox(cmd *cobra.Command, path string, asJSON, sayEmpty bool) error {
	var msgs []api.Message
	if err := a.do(cmd, http.MethodGet, path, nil, &msgs); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if asJSON {
		if len(msgs) == 0 && !sayEmpty {
			return nil
		}
		return printJSON(w, msgs)
	}
	if len(msgs) == 0 && sayEmpty {
		_, err := fmt.Fprintln(w, "no new messages")
		return err
	}
	for _, m := range msgs {
		body := m.Body
		if a.deps.Token != "" {
			// An agent weighs a message by its header, so a body mustn't be
			// able to print one (a TUI agent reads all its messages here).
			body = agents.Quote(body)
		}
		if _, err := fmt.Fprintf(w, "[%s] from %s (%s):\n%s\n\n", m.CreatedAt.Format("15:04"), sender(&m), m.Kind, body); err != nil {
			return err
		}
	}
	return nil
}

// sender names who a message is from: slop-bot's own system and nudge
// messages are recorded as from the user.
func sender(m *api.Message) string {
	if m.Kind == "system" || m.Kind == "nudge" {
		return "slop-bot"
	}
	return m.From
}

// followInbox prints new messages as they arrive. It waits on the daemon's
// event log, from now, and re-reads the inbox when a message is queued,
// rather than polling.
func (a *app) followInbox(cmd *cobra.Command, asJSON bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	body, err := c.Stream(cmd.Context(), api.PathEvents+"?follow&after=now")
	if err != nil {
		// Interrupted while connecting: a stop, as it is once streaming.
		if cmd.Context().Err() != nil {
			return nil
		}
		return err
	}
	defer func() { _ = body.Close() }()
	// "now" is fixed once the stream is open, so a message queued before it
	// is in this read and one queued after it arrives as an event.
	if err := a.printInbox(cmd, api.PathInbox, asJSON, false); err != nil {
		return err
	}
	err = eachEvent(body, func(ev *api.Event) error {
		if ev.Type == "message.queued" || ev.Type == "user.told" || ev.Type == "agent.asked" {
			return a.printInbox(cmd, api.PathInbox, asJSON, false)
		}
		return nil
	})
	if cmd.Context().Err() != nil {
		return nil
	}
	return err
}

func (a *app) askCmd() *cobra.Command {
	var in api.Ask
	c := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask the user a question; returns at once, the answer arrives as a message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Question = args[0]
			var out api.Ask
			if err := a.do(cmd, http.MethodPost, api.PathAsk, &in, &out); err != nil {
				return err
			}
			return a.printOK(cmd, "asked. Carry on with what doesn't depend on the answer, then end your turn: the answer arrives as a message.")
		},
	}
	c.Flags().StringArrayVar(&in.Options, "option", nil, "a choice; repeat")
	c.Flags().StringVar(&in.Recommend, "recommend", "", "the option you recommend")
	return agentCmd(c)
}

func (a *app) asksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "asks",
		Short: "List agents' open questions, oldest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var asks []api.Ask
			if err := a.do(cmd, http.MethodGet, api.PathAsks, nil, &asks); err != nil {
				return err
			}
			if len(asks) == 0 {
				return a.printOK(cmd, "no open questions")
			}
			var b strings.Builder
			for _, q := range asks {
				fmt.Fprintf(&b, "%s asks (%s ago): %s\n", q.Agent, time.Since(q.CreatedAt).Round(time.Minute), q.Question)
				for i, o := range q.Options {
					fmt.Fprintf(&b, "  %d. %s\n", i+1, o)
				}
				if q.Recommend != "" {
					fmt.Fprintf(&b, "  recommends: %s\n", q.Recommend)
				}
				fmt.Fprintf(&b, "  answer: slop-bot tell %s \"...\"\n\n", q.Agent)
			}
			_, err := io.WriteString(cmd.OutOrStdout(), b.String())
			return err
		},
	}
}

func (a *app) discussCmd() *cobra.Command {
	var in api.Discuss
	c := &cobra.Command{
		Use:   "discuss <decision>",
		Short: "Raise a risky, costly-to-undo decision for human review, then end your turn",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Decision = args[0]
			if err := a.do(cmd, http.MethodPost, api.PathDiscuss, &in, nil); err != nil {
				return err
			}
			return a.printOK(cmd, "raised for review. End your turn: the resolution arrives as a message.")
		},
	}
	c.Flags().StringArrayVar(&in.Options, "option", nil, "an option; repeat")
	c.Flags().StringVar(&in.Recommend, "recommend", "", "the option you recommend")
	return agentCmd(c)
}

func (a *app) discussionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "discussions",
		Short: "The review stream: tickets waiting on a risky decision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ts []api.Ticket
			if err := a.do(cmd, http.MethodGet, api.PathDiscussions, nil, &ts); err != nil {
				return err
			}
			if len(ts) == 0 {
				return a.printOK(cmd, "nothing waiting for review")
			}
			var b strings.Builder
			for i := range ts {
				t := &ts[i]
				fmt.Fprintf(&b, "# %s: %s\n", t.ID, t.Title)
				for j := len(t.Comments) - 1; j >= 0; j-- {
					if strings.HasPrefix(t.Comments[j].Body, "**Discussion") {
						fmt.Fprintf(&b, "%s\n", t.Comments[j].Body)
						break
					}
				}
				fmt.Fprintf(&b, "resolve: slop-bot discussions resolve %s \"...\"\n\n", t.ID)
			}
			_, err := io.WriteString(cmd.OutOrStdout(), b.String())
			return err
		},
	}
	c.AddCommand(&cobra.Command{
		Use:   "resolve <ticket> <resolution>",
		Short: "Resolve a discussion: the agent carries on with your decision",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := strings.Replace(api.PathResolve, "{ticket}", url.PathEscape(args[0]), 1)
			if err := a.do(cmd, http.MethodPost, path, &api.Resolve{Resolution: args[1]}, nil); err != nil {
				return err
			}
			return a.printOK(cmd, "resolved %s", args[0])
		},
	})
	return c
}

func (a *app) readyToMergeCmd() *cobra.Command {
	var pr string
	c := &cobra.Command{
		Use:   "ready-to-merge",
		Short: "Say the work is ready to merge (tickets without AutoMerge), then end your turn",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.do(cmd, http.MethodPost, api.PathReadyToMerge, &api.ReadyToMerge{PR: pr}, nil); err != nil {
				return err
			}
			return a.printOK(cmd, "noted. End your turn: you'll be told when to merge.")
		},
	}
	c.Flags().StringVar(&pr, "pr", "", "the PR's URL")
	return agentCmd(c)
}

func (a *app) doneCmd() *cobra.Command {
	var summary string
	c := &cobra.Command{
		Use:   "done --summary <text>",
		Short: "Say the ticket's goal is met; it finishes when your turn ends",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.do(cmd, http.MethodPost, api.PathDone, &api.Done{Summary: summary}, nil); err != nil {
				return err
			}
			return a.printOK(cmd, "noted: the ticket finishes when this turn ends.")
		},
	}
	c.Flags().StringVar(&summary, "summary", "", "what was done, for the ticket (required)")
	_ = c.MarkFlagRequired("summary")
	return agentCmd(c)
}

func (a *app) statusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show every agent: its ticket, state, and progress note",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var st api.Status
			if err := a.do(cmd, http.MethodGet, api.PathStatus, nil, &st); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			on := map[bool]string{true: "on", false: "off"}[st.Autonomy]
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "autonomy %s, agent-filed tickets %d of %d\n", on, st.Outstanding, st.Budget); err != nil {
				return err
			}
			for _, l := range st.Limits {
				if _, err := io.WriteString(cmd.OutOrStdout(), describeLimit(&l)); err != nil {
					return err
				}
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "AGENT\tTICKET\tSTATE\tFOR\tHARNESS\tNOTE")
			sort.Slice(st.Agents, func(i, j int) bool { return st.Agents[i].Ticket < st.Agents[j].Ticket })
			for _, ag := range st.Agents {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", ag.ID, ag.Ticket, ag.State,
					time.Since(ag.Since).Round(time.Second), ag.Harness, dash(ag.Note))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	c.AddCommand(agentCmd(&cobra.Command{
		Use:   "set <note>",
		Short: "Set your one-line progress note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.do(cmd, http.MethodPost, api.PathNote, &api.Note{Note: args[0]}, nil); err != nil {
				return err
			}
			return a.printOK(cmd, "noted")
		},
	}))
	return agentCmd(c)
}

func (a *app) eventsCmd() *cobra.Command {
	var follow bool
	var after int64
	c := &cobra.Command{
		Use:   "events",
		Short: "Print the daemon's event log, as JSON lines",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			path := fmt.Sprintf("%s?after=%d", api.PathEvents, after)
			if follow {
				path += "&follow"
			}
			body, err := c.Stream(cmd.Context(), path)
			if err != nil {
				return err
			}
			defer func() { _ = body.Close() }()
			_, err = io.Copy(cmd.OutOrStdout(), body)
			if cmd.Context().Err() != nil {
				return nil
			}
			return err
		},
	}
	c.Flags().BoolVar(&follow, "follow", false, "keep streaming new events")
	c.Flags().Int64Var(&after, "after", 0, "start after this event")
	return c
}

func (a *app) autonomyCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "autonomy [on|off|status]",
		Short:     "Whether agent-filed tickets are worked without a person",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off", "status"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var out api.Autonomy
			var err error
			switch {
			case len(args) == 0 || args[0] == "status":
				err = a.do(cmd, http.MethodGet, api.PathAutonomy, nil, &out)
			case args[0] == "on" || args[0] == "off":
				err = a.do(cmd, http.MethodPost, api.PathAutonomy, &api.Autonomy{Enabled: args[0] == "on"}, &out)
			default:
				return fmt.Errorf("autonomy: %q isn't on, off or status", args[0])
			}
			if err != nil {
				return err
			}
			return a.printOK(cmd, "autonomy %s", map[bool]string{true: "on", false: "off"}[out.Enabled])
		},
	}
}

// AgentReference lists the commands agents may run, generated from the
// command tree: the briefing can't name a command that doesn't exist, or
// leave one out.
func AgentReference(root *cobra.Command) string {
	var b strings.Builder
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Annotations[agentAnnotation] != "yes" || sub.Hidden {
				continue
			}
			if sub.Runnable() {
				usage := sub.CommandPath()
				if _, args, ok := strings.Cut(sub.Use, " "); ok {
					usage += " " + args
				}
				fmt.Fprintf(&b, "- `%s`: %s\n", usage, sub.Short)
			}
			walk(sub)
		}
	}
	walk(root)
	return b.String()
}
