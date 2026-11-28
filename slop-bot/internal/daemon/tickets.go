package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/agentspec"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var ticketErrors = []control.ErrorMapping{
	{Err: tickets.ErrNotFound, Status: http.StatusNotFound},
	{Err: tickets.ErrNotPermitted, Status: http.StatusForbidden},
	{Err: tickets.ErrCycle, Status: http.StatusConflict},
	{Err: tickets.ErrBlocked, Status: http.StatusConflict},
	{Err: tickets.ErrInvalidTransition, Status: http.StatusConflict},
	{Err: tickets.ErrInvalid, Status: http.StatusBadRequest},
	{Err: agentspec.ErrInvalid, Status: http.StatusBadRequest},
	{Err: errBadRequest, Status: http.StatusBadRequest},
	{Err: errAgentLive, Status: http.StatusConflict},
	{Err: errBudget, Status: http.StatusTooManyRequests},
}

var errBadRequest = errors.New("bad request")

// ticketRoutes is the ticket API: a thin layer over the TicketingSystem,
// which owns every rule, plus agent-spec validation, which needs this
// machine's config.
type ticketRoutes struct {
	sys  tickets.TicketingSystem
	cfg  *config.Config
	user tickets.UserID
	// autonomy reports whether agent-filed tickets are worked without a
	// person (`slop-bot autonomy`).
	autonomy func(context.Context) bool
	// hasLiveAgent reports whether the ticket has an agent that hasn't ended.
	hasLiveAgent func(context.Context, ident.TicketID) bool
	// overBudget tells the user, once per filling-up, that agents hit the
	// budget.
	budgetMu   sync.Mutex
	budgetFull bool
	tellUser   func(context.Context, string)
}

// errAgentLive refuses a spec change the running agent would never see.
var errAgentLive = errors.New("the ticket's agent is running and keeps the spec it started with: " +
	"change the harness, model and effort once it has ended")

// errBudget refuses an agent's ticket past the autonomy budget.
var errBudget = errors.New("the budget of outstanding agent-filed tickets is used up: " +
	"file again once some are done, or ask the user")

func (tr *ticketRoutes) register(s *control.Server) {
	s.HandleFunc("POST "+api.PathTickets, tr.create)
	s.HandleFunc("GET "+api.PathTickets, tr.list)
	s.HandleFunc("GET "+api.PathTicket, tr.get)
	s.HandleFunc("PATCH "+api.PathTicket, tr.update)
	s.HandleFunc("POST "+api.PathTicketAssign, tr.assign)
	s.HandleFunc("POST "+api.PathTicketComments, tr.comment)
	s.HandleFunc("POST "+api.PathTicketBlockers, tr.addBlocker)
	s.HandleFunc("DELETE "+api.PathTicketBlocker, tr.removeBlocker)
}

func fail(w http.ResponseWriter, err error) {
	control.WriteError(w, control.StatusFor(err, ticketErrors), err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := control.DecodeJSON(r, v); err != nil {
		fail(w, errors.Join(errBadRequest, err))
		return false
	}
	return true
}

// view renders t, flagging a spec this machine can't run.
func (tr *ticketRoutes) view(t *tickets.Ticket) api.Ticket {
	out := apiTicket(t)
	if err := agentspec.Validate(agentspec.Resolve(t.Agent, tr.cfg), tr.cfg); err != nil {
		out.SpecError = err.Error()
	}
	return out
}

// checkSpec refuses a spec this machine can't run, at filing or edit time.
func (tr *ticketRoutes) checkSpec(spec tickets.AgentSpec) error {
	return agentspec.Validate(agentspec.Resolve(spec, tr.cfg), tr.cfg)
}

func (tr *ticketRoutes) create(w http.ResponseWriter, r *http.Request) {
	var in api.NewTicket
	if !decode(w, r, &in) {
		return
	}
	nt := &tickets.NewTicket{
		Title: in.Title, Body: in.Body, Status: tickets.Status(in.Status), Agent: specFrom(in.Agent),
		AutoMerge: in.AutoMerge, Urgent: in.Urgent, UrgentReason: in.UrgentReason, Repos: in.Repos,
	}
	for _, b := range in.BlockedOn {
		nt.BlockedOn = append(nt.BlockedOn, ident.TicketID(b))
	}
	if err := tr.checkSpec(nt.Agent); err != nil {
		fail(w, err)
		return
	}
	by := control.PrincipalFrom(r.Context())
	if by.IsUser() {
		if in.Assign {
			nt.Assignee = tr.user
		}
	} else if err := tr.agentFiling(r.Context(), nt); err != nil {
		fail(w, err)
		return
	}
	t, err := tr.sys.Create(r.Context(), by, nt)
	if err != nil {
		fail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusCreated, tr.view(t))
}

// agentFiling applies the autonomous loop's rules to a ticket an agent
// files: it counts against a budget that scales with the agent slots, and
// it is assigned (and so worked) only when autonomy is on; otherwise it
// waits, unassigned, in the review stream for the user.
func (tr *ticketRoutes) agentFiling(ctx context.Context, nt *tickets.NewTicket) error {
	n, err := tr.sys.CountOutstandingAgentCreated(ctx)
	if err != nil {
		return err
	}
	budget := tr.cfg.AutonomyBudget()
	tr.budgetMu.Lock()
	full := n >= budget
	tellNow := full && !tr.budgetFull
	tr.budgetFull = full
	tr.budgetMu.Unlock()
	if tellNow && tr.tellUser != nil {
		tr.tellUser(ctx, fmt.Sprintf("agents have filed %d outstanding tickets, the budget (%d per agent slot): further filings are refused until some finish",
			n, tr.cfg.Autonomy.TicketsPerAgent))
	}
	if full {
		return errBudget
	}
	nt.Assignee = ""
	if tr.autonomy != nil && tr.autonomy(ctx) {
		nt.Assignee = tr.user
	}
	return nil
}

func (tr *ticketRoutes) list(w http.ResponseWriter, r *http.Request) {
	q := &tickets.Query{}
	for _, s := range r.URL.Query()["status"] {
		for _, one := range strings.Split(s, ",") {
			q.Statuses = append(q.Statuses, tickets.Status(one))
		}
	}
	if a, ok := r.URL.Query()["assignee"]; ok {
		id := tickets.UserID(a[0])
		if id == "me" {
			id = tr.user
		}
		q.Assignee = &id
	}
	ts, err := tr.sys.List(r.Context(), q)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]api.Ticket, 0, len(ts))
	for _, t := range ts {
		out = append(out, tr.view(t))
	}
	control.WriteJSON(w, http.StatusOK, out)
}

func (tr *ticketRoutes) get(w http.ResponseWriter, r *http.Request) {
	id := ident.TicketID(r.PathValue("id"))
	t, err := tr.sys.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	bs, err := tr.sys.Blockers(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	out := tr.view(t)
	out.Blockers = &api.Blockers{BlockedBy: apiEdges(bs.BlockedBy), Blocks: apiEdges(bs.Blocks)}
	control.WriteJSON(w, http.StatusOK, out)
}

func (tr *ticketRoutes) update(w http.ResponseWriter, r *http.Request) {
	var in api.TicketChange
	if !decode(w, r, &in) {
		return
	}
	ch := &tickets.TicketChange{Title: in.Title, Body: in.Body, AutoMerge: in.AutoMerge, Repos: in.Repos}
	if in.Status != nil {
		st := tickets.Status(*in.Status)
		ch.Status = &st
	}
	id := ident.TicketID(r.PathValue("id"))
	if in.Agent != nil {
		spec, err := tr.mergedSpec(r.Context(), id, in.Agent)
		if err != nil {
			fail(w, err)
			return
		}
		ch.Agent = spec
	}
	if in.Urgent != nil {
		ch.Urgent = &tickets.UrgentChange{Urgent: *in.Urgent, Reason: in.UrgentReason}
	}
	t, err := tr.sys.Update(r.Context(), control.PrincipalFrom(r.Context()), id, ch)
	if err != nil {
		fail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, tr.view(t))
}

// mergedSpec applies the given fields onto the ticket's current spec: the
// store replaces a spec outright, so a partial edit must be completed here.
func (tr *ticketRoutes) mergedSpec(ctx context.Context, id ident.TicketID, in *api.AgentSpecChange) (*tickets.AgentSpec, error) {
	cur, err := tr.sys.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// A live agent resumes from the spec saved with it, so an edit would be
	// accepted and then ignored.
	if tr.hasLiveAgent != nil && tr.hasLiveAgent(ctx, id) {
		return nil, errAgentLive
	}
	spec := cur.Agent
	if in.Harness != nil {
		spec.Harness = *in.Harness
	}
	if in.Model != nil {
		spec.Model = *in.Model
	}
	if in.Effort != nil {
		spec.Effort = *in.Effort
	}
	if err := tr.checkSpec(spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (tr *ticketRoutes) assign(w http.ResponseWriter, r *http.Request) {
	var in api.Assign
	if !decode(w, r, &in) {
		return
	}
	who := tickets.UserID(in.Assignee)
	if who == "me" {
		who = tr.user
	}
	t, err := tr.sys.Assign(r.Context(), control.PrincipalFrom(r.Context()), ident.TicketID(r.PathValue("id")), who)
	if err != nil {
		fail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, tr.view(t))
}

func (tr *ticketRoutes) comment(w http.ResponseWriter, r *http.Request) {
	var in api.NewComment
	if !decode(w, r, &in) {
		return
	}
	c, err := tr.sys.Comment(r.Context(), control.PrincipalFrom(r.Context()), ident.TicketID(r.PathValue("id")), in.Body)
	if err != nil {
		fail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusCreated, api.Comment{ID: c.ID, Author: apiPrincipal(c.Author), Body: c.Body, CreatedAt: c.CreatedAt})
}

func (tr *ticketRoutes) addBlocker(w http.ResponseWriter, r *http.Request) {
	var in api.NewBlocker
	if !decode(w, r, &in) {
		return
	}
	tr.blockerChange(w, r, func(ctx context.Context, by ident.Principal, id ident.TicketID) (*tickets.BlockerChange, error) {
		return tr.sys.AddBlocker(ctx, by, id, ident.TicketID(in.On))
	})
}

func (tr *ticketRoutes) removeBlocker(w http.ResponseWriter, r *http.Request) {
	tr.blockerChange(w, r, func(ctx context.Context, by ident.Principal, id ident.TicketID) (*tickets.BlockerChange, error) {
		return tr.sys.RemoveBlocker(ctx, by, id, ident.TicketID(r.PathValue("blocker")))
	})
}

func (tr *ticketRoutes) blockerChange(w http.ResponseWriter, r *http.Request,
	do func(context.Context, ident.Principal, ident.TicketID) (*tickets.BlockerChange, error)) {
	ch, err := do(r.Context(), control.PrincipalFrom(r.Context()), ident.TicketID(r.PathValue("id")))
	if err != nil {
		fail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, api.BlockerChange{Ticket: tr.view(ch.Ticket), Changed: ch.Changed})
}
