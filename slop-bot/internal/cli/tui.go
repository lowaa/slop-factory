package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/statusview"
)

func (a *app) tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Show every agent in a live, read-only table",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			p := tea.NewProgram(statusview.New(&statusview.Options{Clock: clock.Real{}}),
				tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			feedErr := make(chan error, 1)
			go func() {
				err := (&statusFeed{client: c}).Run(ctx, p.Send)
				if err != nil {
					p.Quit()
				}
				feedErr <- err
			}()
			_, runErr := p.Run()
			cancel()
			if err := <-feedErr; runErr == nil {
				return err
			}
			return runErr
		},
	}
}

// statusFeed turns the daemon's event log into status snapshots. It never
// polls: each event re-reads GET /v1/status, which the TUI then shows. The
// time-in-state column only moves on those snapshots, so it can read stale
// until the next event.
type statusFeed struct {
	client *control.Client
}

// Run sends a snapshot now and another per event until ctx ends. It opens
// the stream before the first snapshot, so no change falls between them.
func (f *statusFeed) Run(ctx context.Context, send func(tea.Msg)) error {
	body, err := f.client.Stream(ctx, api.PathEvents+"?follow&after=now")
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if err := f.snapshot(ctx, send); err != nil {
		return err
	}
	err = eachEvent(body, func(*api.Event) error { return f.snapshot(ctx, send) })
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("the daemon closed the event stream")
}

// eachEvent calls fn for every event on an event stream until the stream
// ends; a nil error means it ended cleanly.
func eachEvent(body io.Reader, fn func(*api.Event) error) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var ev api.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if err := fn(&ev); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (f *statusFeed) snapshot(ctx context.Context, send func(tea.Msg)) error {
	var st api.Status
	if err := f.client.Do(ctx, http.MethodGet, api.PathStatus, nil, &st); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	send(statusview.SnapshotMsg{Status: &st})
	return nil
}
