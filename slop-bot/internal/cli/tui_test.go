package cli

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/statusview"
)

func TestTheTUIFeedSendsASnapshotPerDaemonEvent(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	ctx, cancel := context.WithCancel(e.ctx())
	t.Cleanup(cancel)

	snaps := make(chan *api.Status, 256)
	feed := &statusFeed{client: control.NewClient(e.h.runtime+"/slop-bot/slop-bot.sock", "")}
	done := make(chan error, 1)
	go func() {
		done <- feed.Run(ctx, func(m tea.Msg) { snaps <- m.(statusview.SnapshotMsg).Status })
	}()
	// The first snapshot is the state before any agent exists.
	select {
	case first := <-snaps:
		require.Empty(t, first.Agents)
	case <-ctx.Done():
		require.FailNow(t, "no first snapshot", ctx.Err().Error())
	}

	e.h.ticket(t, "ticket", "create", "--title", "Add export", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	e.do(s, fake.CmdTurnStart)
	e.agent(s, "ask", "CSV or XLSX?")
	e.do(s, fake.CmdTurnEnd)

	var got *api.AgentStatus
	var pools []api.PoolUsage
	for got == nil {
		select {
		case st := <-snaps:
			for i := range st.Agents {
				if st.Agents[i].State == "waiting_user" {
					got, pools = &st.Agents[i], st.Pools
				}
			}
		case <-ctx.Done():
			require.FailNow(t, "no snapshot showed the agent waiting", ctx.Err().Error())
		}
	}
	assert.Equal(t, "Add export", got.Title)
	assert.NotEmpty(t, got.TicketStatus)
	assert.NotEmpty(t, got.Pool)
	assert.Equal(t, []api.PoolUsage{{Pool: "normal", Max: 4}, {Pool: "urgent", Max: 1}}, pools, "waiting agents hold no slot")

	cancel()
	require.NoError(t, <-done)
}
