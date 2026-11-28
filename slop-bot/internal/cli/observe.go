package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// The observation commands are the user's: they aren't annotated as agent
// commands, so the briefing doesn't offer them, and the daemon refuses an
// agent's token.

func (a *app) transcriptCmd() *cobra.Command {
	var tail int
	var since, format string
	var follow, full bool
	c := &cobra.Command{
		Use:   "transcript <agent|ticket>",
		Short: "Show an agent's conversation: turns, tool calls (long output cut unless --full), usage",
		Long: "Show an agent's conversation, from its harness's own transcript, or slop-bot's retained copy once " +
			"the harness has pruned it. A ticket id means the ticket's latest agent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "jsonl" {
				return fmt.Errorf("--format: %q isn't text or jsonl", format)
			}
			q := url.Values{}
			if tail > 0 {
				q.Set("tail", strconv.Itoa(tail))
			}
			if since != "" {
				t, err := parseSince(since, time.Now())
				if err != nil {
					return err
				}
				q.Set("since", t.Format(time.RFC3339Nano))
			}
			if follow {
				q.Set("follow", "")
			}
			if full {
				q.Set("full", "")
			}
			return a.streamLines(cmd, targetPath(api.PathTranscript, args[0])+"?"+q.Encode(), func(line []byte) error {
				if format == "jsonl" {
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n", line)
					return err
				}
				var e transcript.Entry
				if err := json.Unmarshal(line, &e); err != nil {
					return err
				}
				return transcript.Render(cmd.OutOrStdout(), &e)
			})
		},
	}
	c.Flags().IntVar(&tail, "tail", 0, "only the last N entries")
	c.Flags().StringVar(&since, "since", "", "only entries since a time (RFC 3339) or a duration ago (e.g. 30m)")
	c.Flags().BoolVar(&follow, "follow", false, "keep printing as the conversation goes on, until the agent ends")
	c.Flags().StringVar(&format, "format", "text", "text or jsonl")
	c.Flags().BoolVar(&full, "full", false, "don't cut long tool input and output")
	return c
}

// parseSince reads --since: a time, or a duration before now.
func parseSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("--since: %q is neither an RFC 3339 time nor a duration like 30m", s)
	}
	return now.Add(-d), nil
}

func (a *app) watchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch <agent|ticket>",
		Short: "Follow an agent live: its turns, tool calls, text and state changes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			return a.streamLines(cmd, targetPath(api.PathWatch, args[0]), func(line []byte) error {
				var ev api.Event
				if err := json.Unmarshal(line, &ev); err != nil {
					return err
				}
				_, err := io.WriteString(w, describe(&ev))
				return err
			})
		},
	}
}

func targetPath(pattern, target string) string {
	return strings.Replace(pattern, "{target}", url.PathEscape(target), 1)
}

// streamLines feeds each line of a streamed response to each, until the
// daemon ends it or the command is interrupted.
func (a *app) streamLines(cmd *cobra.Command, path string, each func([]byte) error) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	body, err := c.Stream(cmd.Context(), path)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		if err := each(sc.Bytes()); err != nil {
			return err
		}
	}
	if cmd.Context().Err() != nil {
		return nil
	}
	return sc.Err()
}

// describeLimit is a harness's plan allowance, for `status`.
func describeLimit(l *api.HarnessLimit) string {
	s := fmt.Sprintf("plan %s: %.0f%% used (new agents wait at %.0f%%)", l.Harness, l.UsedPercent, l.Threshold)
	if !l.ResetsAt.IsZero() {
		s += ", resets " + l.ResetsAt.Local().Format("Jan 2 15:04")
	}
	if l.Held {
		s += ": holding new agents"
	}
	return s + "\n"
}

// describe is one watched event as a line for a person.
func describe(ev *api.Event) string {
	var p struct {
		Tool        string    `json:"tool"`
		Outcome     string    `json:"outcome"`
		Text        string    `json:"text"`
		Truncated   int       `json:"truncated"`
		Input       int64     `json:"input_tokens"`
		Output      int64     `json:"output_tokens"`
		UsedPercent float64   `json:"used_percent"`
		ResetsAt    time.Time `json:"resets_at"`
		From        string    `json:"from"`
		To          string    `json:"to"`
		Reason      string    `json:"reason"`
		State       string    `json:"state"`
		Ended       bool      `json:"ended"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	var what string
	switch ev.Type {
	case api.WatchStarted:
		what = fmt.Sprintf("watching %s (ticket %s), %s", ev.Agent, ev.Ticket, p.State)
		if p.Ended {
			what += ": it has ended; `slop-bot transcript " + ev.Agent + "` shows what it did"
		}
	case "harness.turn_started":
		what = "turn started"
	case "harness.turn_ended":
		what = "turn ended: " + p.Outcome
		if p.Text != "" {
			what += ": " + p.Text
		}
	case "harness.tool_started":
		what = "tool " + p.Tool
	case "harness.tool_ended":
		what = "tool done " + p.Tool
	case "harness.text":
		what = "text: " + strings.ReplaceAll(strings.TrimRight(p.Text, "\n"), "\n", "\n    ")
		if p.Truncated > 0 {
			what += fmt.Sprintf(" … (%d more bytes)", p.Truncated)
		}
	case "harness.usage":
		what = fmt.Sprintf("usage: input %d, output %d", p.Input, p.Output)
	case "harness.limit":
		what = fmt.Sprintf("plan: %.0f%% used", p.UsedPercent)
		if !p.ResetsAt.IsZero() {
			what += ", resets " + p.ResetsAt.Local().Format("Jan 2 15:04")
		}
	case "agent.state":
		what = fmt.Sprintf("state %s → %s", p.From, p.To)
		if p.Reason != "" {
			what += " (" + p.Reason + ")"
		}
	default:
		what = strings.TrimPrefix(ev.Type, "agent.")
	}
	stamp := "--:--:--"
	if !ev.At.IsZero() {
		stamp = ev.At.Local().Format("15:04:05")
	}
	return fmt.Sprintf("[%s] %s\n", stamp, what)
}
