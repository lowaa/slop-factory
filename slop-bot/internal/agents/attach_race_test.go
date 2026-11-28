package agents_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// An attach given up while its agent's own start is still settling (the
// spawn's closing sync, slow under load) must resume the agent once that
// start is done, not drop the resume because the ticket's start place is
// taken. The spawn is held in its sync, just before it moves the open
// ticket to in_progress, until the hand-back has found the place taken.
func TestAnAttachGivenUpWhileTheStartSettlesResumesTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	inSync, release := make(chan struct{}), make(chan struct{})
	moved := f.racing.arm(func() {
		close(inSync)
		<-release
	})
	busy := make(chan ident.TicketID, 1)
	var once sync.Once
	f.mgr.OnStartBusy(func(tk ident.TicketID) { once.Do(func() { busy <- tk }) })

	tk := f.file("t", func(nt *tickets.NewTicket) { nt.Status = tickets.StatusOpen })
	s := f.session()
	f.user(s)
	<-inSync // the spawn holds the ticket's start place from here
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateWorking))
	a := f.agentFor(tk.ID)

	ctx, cancel := context.WithCancel(f.ctx())
	got := make(chan error, 1)
	go func() {
		_, err := f.mgr.Attach(ctx, &agents.AttachRequest{Target: string(a.ID), Holder: holder})
		got <- err
	}()
	f.await(agents.EventAttaching, nil)
	cancel()
	require.ErrorIs(t, <-got, context.Canceled)
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	assert.Equal(t, tk.ID, <-busy, "the abandoned attach's resume met the spawn still settling")

	close(release)
	require.NoError(t, <-moved)
	resumed := f.session()
	assert.Equal(t, s.ID, resumed.ID, "resumed once the spawn settled")
	f.user(resumed)
	back, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, back.Lease)
	assert.Equal(t, state.ModeProgrammatic, back.Mode)
}
