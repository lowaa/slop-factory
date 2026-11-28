package daemon

import (
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

func apiPrincipal(p ident.Principal) api.Principal {
	if p.IsUser() {
		return api.Principal{Kind: "user"}
	}
	return api.Principal{Kind: "agent", Agent: string(p.Agent), Ticket: string(p.Ticket)}
}

func apiRef(r tickets.TicketRef) api.TicketRef {
	return api.TicketRef{ID: string(r.ID), Title: r.Title, Status: string(r.Status)}
}

func apiSpec(s tickets.AgentSpec) api.AgentSpec {
	return api.AgentSpec{Harness: s.Harness, Model: s.Model, Effort: s.Effort}
}

func specFrom(s api.AgentSpec) tickets.AgentSpec {
	return tickets.AgentSpec{Harness: s.Harness, Model: s.Model, Effort: s.Effort}
}

func apiTicket(t *tickets.Ticket) api.Ticket {
	out := api.Ticket{
		ID: string(t.ID), Title: t.Title, Body: t.Body, Status: string(t.Status),
		Assignee: string(t.AssigneeID), Agent: apiSpec(t.Agent), AutoMerge: t.AutoMerge,
		Urgent: t.Urgent, UrgentReason: t.UrgentReason, CreatedBy: apiPrincipal(t.CreatedBy),
		Parent: string(t.Parent), Repos: t.Repos, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	for _, b := range t.BlockedBy {
		out.BlockedBy = append(out.BlockedBy, apiRef(b))
	}
	for _, c := range t.Comments {
		out.Comments = append(out.Comments, api.Comment{ID: c.ID, Author: apiPrincipal(c.Author), Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return out
}

func apiEdges(edges []tickets.BlockerEdge) []api.BlockerEdge {
	out := make([]api.BlockerEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, api.BlockerEdge{Ticket: apiRef(e.Ticket), AddedBy: apiPrincipal(e.AddedBy)})
	}
	return out
}
