package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// EventAskReminded is the user reminded of an ask still open
// agents.ask_reminder_after after it was asked.
const EventAskReminded = "ask.reminded"

// remindAsks reminds the user, once per ask, of each ask still open
// agents.ask_reminder_after after it was asked. waiting_user isn't stalled,
// so nothing else would bring an unanswered question back to them.
func (m *Manager) remindAsks(ctx context.Context) {
	now := m.opts.Clock.Now()
	asks, err := m.opts.State.Messages.AsksToRemind(ctx, now.Add(-m.opts.Config.Agents.AskReminderAfter.D()))
	if err != nil {
		m.opts.Logger.Warn("agents: listing asks to remind the user of", "err", err)
		return
	}
	for _, ask := range asks {
		agent := ident.AgentID(ask.From)
		var ticket ident.TicketID
		if a, err := m.opts.State.Agents.Get(ctx, agent); err == nil {
			ticket = a.Ticket
		}
		body := fmt.Sprintf("Reminder: agent %s (ticket %s) asked %s ago and is still waiting for an answer:\n%s\n"+
			"`slop-bot asks` lists the open questions; `slop-bot tell %s ...` answers it.",
			agent, ticket, now.Sub(ask.CreatedAt).Round(time.Minute), quote(ask.Body), agent)
		// The reminder and its mark are one write, so it goes once per ask.
		reminded, err := m.opts.State.Messages.Remind(ctx, ask.ID, &state.NewMessage{
			From: state.AgentParty(agent), To: state.UserParty, Kind: state.KindSystem, Body: body,
		})
		if err != nil {
			m.opts.Logger.Warn("agents: reminding the user of an ask", "ask", ask.ID, "err", err)
			continue
		}
		if !reminded {
			continue
		}
		m.event(ctx, EventAskReminded, agent, ticket, map[string]int64{"ask": ask.ID})
		// What `inbox --follow` wakes on.
		m.event(ctx, EventUserTold, agent, ticket, map[string]string{"body": body})
	}
}
