package daemon

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var agentErrors = append([]control.ErrorMapping{
	{Err: agents.ErrUnknownTarget, Status: http.StatusNotFound},
	{Err: agents.ErrNoLiveAgent, Status: http.StatusNotFound},
	{Err: agents.ErrLoopGuard, Status: http.StatusTooManyRequests},
	{Err: agents.ErrUserOnly, Status: http.StatusForbidden},
	{Err: agents.ErrNotYourTicket, Status: http.StatusForbidden},
	{Err: agents.ErrHasAutoMerge, Status: http.StatusConflict},
	{Err: agents.ErrTicketEnded, Status: http.StatusConflict},
	{Err: agents.ErrNotInDiscusson, Status: http.StatusConflict},
	{Err: agents.ErrCantFinish, Status: http.StatusConflict},
}, ticketErrors...)

// agentRoutes is the agents' verbs, messaging, status and the event log.
type agentRoutes struct{ d *Daemon }

func (ar *agentRoutes) register(s *control.Server) {
	s.HandleFunc("POST "+api.PathTell, ar.tell)
	s.HandleFunc("GET "+api.PathInbox, ar.inbox)
	s.HandleFunc("POST "+api.PathAsk, ar.ask)
	s.HandleFunc("GET "+api.PathAsks, ar.asks)
	s.HandleFunc("POST "+api.PathDiscuss, ar.discuss)
	s.HandleFunc("GET "+api.PathDiscussions, ar.discussions)
	s.HandleFunc("POST "+api.PathResolve, ar.resolve)
	s.HandleFunc("POST "+api.PathReadyToMerge, ar.readyToMerge)
	s.HandleFunc("POST "+api.PathPreTool, ar.preTool)
	s.HandleFunc("POST "+api.PathDone, ar.done)
	s.HandleFunc("POST "+api.PathNote, ar.note)
	s.HandleFunc("GET "+api.PathStatus, ar.status)
	s.HandleFunc("GET "+api.PathEvents, ar.events)
	s.HandleFunc("GET "+api.PathAutonomy, ar.getAutonomy)
	s.HandleFunc("POST "+api.PathAutonomy, ar.setAutonomy)
}

func agentFail(w http.ResponseWriter, err error) {
	control.WriteError(w, control.StatusFor(err, agentErrors), err)
}

func ok(w http.ResponseWriter) { control.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

func (ar *agentRoutes) tell(w http.ResponseWriter, r *http.Request) {
	var in api.Tell
	if !decode(w, r, &in) {
		return
	}
	by := control.PrincipalFrom(r.Context())
	if in.All && in.Interrupt {
		fail(w, errors.Join(errBadRequest, errors.New("--interrupt can't be combined with --all: a broadcast never interrupts")))
		return
	}
	if in.Merge || (by.IsUser() && !in.All && ar.d.agents.IsMergeGoAhead(r.Context(), in.To, in.Text)) {
		text, err := ar.authoriseMerge(r, &in)
		if err != nil {
			agentFail(w, err)
			return
		}
		in.Text = text
	}
	if in.All {
		n, err := ar.d.agents.TellAll(r.Context(), by, in.Text)
		if err != nil {
			agentFail(w, err)
			return
		}
		control.WriteJSON(w, http.StatusOK, api.Told{Recipients: n})
		return
	}
	if _, err := ar.d.agents.Tell(r.Context(), by, in.To, in.Text, in.Interrupt); err != nil {
		agentFail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, api.Told{Recipients: 1})
}

func apiMessages(msgs []state.Message) []api.Message {
	out := make([]api.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, api.Message{ID: m.ID, From: string(m.From), To: string(m.To), Kind: string(m.Kind),
			Body: m.Body, Status: string(m.Status), CreatedAt: m.CreatedAt})
	}
	return out
}

// inbox is the caller's unread messages (an agent's: those not yet pushed
// to it), marked read as they're returned;
// with ?log, the history instead (user only).
func (ar *agentRoutes) inbox(w http.ResponseWriter, r *http.Request) {
	by := control.PrincipalFrom(r.Context())
	me := state.PartyOf(by)
	msgs := ar.d.state.Messages
	if _, isLog := r.URL.Query()["log"]; isLog {
		if !by.IsUser() {
			agentFail(w, agents.ErrUserOnly)
			return
		}
		q := &state.LogQuery{}
		if t := r.URL.Query().Get("target"); t != "" {
			p := state.Party(t)
			q.Party = p
		}
		log, err := msgs.Log(r.Context(), q)
		if err != nil {
			agentFail(w, err)
			return
		}
		control.WriteJSON(w, http.StatusOK, apiMessages(log))
		return
	}
	// An agent has had its delivered messages pushed into its conversation,
	// all but those a pointer sent it here for; replaying the rest would
	// repeat old instructions.
	list := msgs.Unread
	if !by.IsUser() {
		list = func(ctx context.Context, _ state.Party) ([]state.Message, error) {
			return msgs.AgentInbox(ctx, by.Agent)
		}
	}
	unread, err := list(r.Context(), me)
	if err != nil {
		agentFail(w, err)
		return
	}
	ids := make([]int64, 0, len(unread))
	for _, m := range unread {
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		if _, err := msgs.MarkRead(r.Context(), me, ids); err != nil {
			agentFail(w, err)
			return
		}
	}
	control.WriteJSON(w, http.StatusOK, apiMessages(unread))
}

func (ar *agentRoutes) ask(w http.ResponseWriter, r *http.Request) {
	var in api.Ask
	if !decode(w, r, &in) {
		return
	}
	a, err := ar.d.agents.Ask(r.Context(), control.PrincipalFrom(r.Context()), in.Question, in.Options, in.Recommend)
	if err != nil {
		agentFail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, api.Ask{ID: a.ID, Agent: string(a.From), Question: a.Body, Options: a.Options, Recommend: a.Recommendation, CreatedAt: a.CreatedAt})
}

// asks are every agent's open questions: the user's to answer, and not
// one agent's to read another's.
func (ar *agentRoutes) asks(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r, "asks") {
		return
	}
	open, err := ar.d.state.Messages.OpenAsks(r.Context())
	if err != nil {
		agentFail(w, err)
		return
	}
	out := make([]api.Ask, 0, len(open))
	for _, a := range open {
		out = append(out, api.Ask{ID: a.ID, Agent: string(a.From), Question: a.Body, Options: a.Options, Recommend: a.Recommendation, CreatedAt: a.CreatedAt})
	}
	control.WriteJSON(w, http.StatusOK, out)
}

func (ar *agentRoutes) discuss(w http.ResponseWriter, r *http.Request) {
	var in api.Discuss
	if !decode(w, r, &in) {
		return
	}
	if err := ar.d.agents.Discuss(r.Context(), control.PrincipalFrom(r.Context()), in.Decision, in.Options, in.Recommend); err != nil {
		agentFail(w, err)
		return
	}
	ok(w)
}

// discussions is the review stream: tickets in discussion.
func (ar *agentRoutes) discussions(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r, "discussions") {
		return
	}
	ts, err := ar.d.tickets.List(r.Context(), &tickets.Query{Statuses: []tickets.Status{tickets.StatusDiscussion}})
	if err != nil {
		agentFail(w, err)
		return
	}
	out := make([]api.Ticket, 0, len(ts))
	for _, t := range ts {
		full, err := ar.d.tickets.Get(r.Context(), t.ID)
		if err != nil {
			agentFail(w, err)
			return
		}
		out = append(out, ar.d.ticketRoutes.view(full))
	}
	control.WriteJSON(w, http.StatusOK, out)
}

func (ar *agentRoutes) resolve(w http.ResponseWriter, r *http.Request) {
	var in api.Resolve
	if !decode(w, r, &in) {
		return
	}
	if err := ar.d.agents.ResolveDiscussion(r.Context(), control.PrincipalFrom(r.Context()), ident.TicketID(r.PathValue("ticket")), in.Resolution); err != nil {
		agentFail(w, err)
		return
	}
	ok(w)
}

func (ar *agentRoutes) readyToMerge(w http.ResponseWriter, r *http.Request) {
	var in api.ReadyToMerge
	if !decode(w, r, &in) {
		return
	}
	if err := ar.d.agents.ReadyToMerge(r.Context(), control.PrincipalFrom(r.Context()), in.PR); err != nil {
		agentFail(w, err)
		return
	}
	ok(w)
}

func (ar *agentRoutes) done(w http.ResponseWriter, r *http.Request) {
	var in api.Done
	if !decode(w, r, &in) {
		return
	}
	if err := ar.d.agents.Done(r.Context(), control.PrincipalFrom(r.Context()), in.Summary); err != nil {
		agentFail(w, err)
		return
	}
	ok(w)
}

func (ar *agentRoutes) note(w http.ResponseWriter, r *http.Request) {
	var in api.Note
	if !decode(w, r, &in) {
		return
	}
	if err := ar.d.agents.SetNote(r.Context(), control.PrincipalFrom(r.Context()), in.Note); err != nil {
		agentFail(w, err)
		return
	}
	ok(w)
}

func (ar *agentRoutes) status(w http.ResponseWriter, r *http.Request) {
	live, err := ar.d.state.Agents.ListLive(r.Context())
	if err != nil {
		agentFail(w, err)
		return
	}
	out := api.Status{Agents: []api.AgentStatus{}, Autonomy: ar.d.autonomy(r.Context()), Budget: ar.d.opts.Config.AutonomyBudget(), Limits: []api.HarnessLimit{}}
	for _, l := range ar.d.agents.Limits() {
		out.Limits = append(out.Limits, api.HarnessLimit{Harness: l.Harness, Window: l.Window, UsedPercent: l.UsedPercent, ResetsAt: l.ResetsAt,
			ReportedAt: l.ReportedAt, Threshold: l.Threshold, Held: l.Held})
	}
	if out.Outstanding, err = ar.d.tickets.CountOutstandingAgentCreated(r.Context()); err != nil {
		agentFail(w, err)
		return
	}
	for _, u := range ar.d.agents.PoolUsage() {
		out.Pools = append(out.Pools, api.PoolUsage{Pool: string(u.Pool), Active: u.Active, Max: u.Max})
	}
	for _, a := range live {
		var title, ticketStatus string
		var blockers []string
		if t, err := ar.d.tickets.Get(r.Context(), a.Ticket); err == nil {
			title, ticketStatus = t.Title, string(t.Status)
			for _, b := range t.BlockedBy {
				if b.Status != tickets.StatusDone {
					blockers = append(blockers, string(b.ID))
				}
			}
		}
		out.Agents = append(out.Agents, api.AgentStatus{
			ID: string(a.ID), Ticket: string(a.Ticket), Title: title, TicketStatus: ticketStatus, Blockers: blockers, State: string(a.State), Since: a.StateSince,
			Harness: a.Harness, Model: a.Model, Effort: a.Effort, Pool: string(a.Pool), Note: a.ProgressNote, Nudges: a.Nudges,
			ParkedAt: a.ParkedAt,
		})
	}
	control.WriteJSON(w, http.StatusOK, out)
}

// events streams the event log as newline-delimited JSON from ?after=<seq>;
// with ?follow it keeps the stream open for new events.
func (ar *agentRoutes) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") == "now" {
		var err error
		if after, err = ar.d.state.Events.Latest(r.Context()); err != nil {
			agentFail(w, err)
			return
		}
	}
	_, follow := r.URL.Query()["follow"]
	read := ar.d.state.Events.ReadAfter
	if follow {
		read = ar.d.state.Events.Follow
	}
	by := control.PrincipalFrom(r.Context())
	out := newStream(w)
	// Send the headers now: a follower that has this response knows its
	// starting point is fixed, even if no event comes for a while.
	out.flush()
	for {
		evs, err := read(r.Context(), after, 500)
		if err != nil {
			return
		}
		for i := range evs {
			after = evs[i].Seq
			if e, ok := visibleTo(by, &evs[i]); ok && out.event(e) != nil {
				return
			}
		}
		out.flush()
		if !follow && len(evs) < 500 {
			return
		}
	}
}

// agentVisible are the events an agent sees of other agents': that they
// happened, without their payloads, which carry what the other agent asked,
// was told or said. An agent follows the log for its own inbox (`inbox
// --follow`), and has no need to read another's conversation (`watch` and
// `transcript` are the user's). Anything not listed, an event type added
// later included, it doesn't see at all.
var agentVisible = map[string]bool{
	agents.EventSpawned: true, agents.EventResumed: true, agents.EventState: true, agents.EventFinished: true,
	agents.EventReleased: true, agents.EventParked: true, agents.EventNudged: true,
	agents.EventQueued: true, agents.EventDelivered: true, agents.EventUserTold: true, "agent.asked": true,
	agents.EventAttached: true, agents.EventHandedBack: true,
}

// visibleTo is e as by may see it, and whether it may see it at all: the
// user sees everything, and an agent its own events whole.
func visibleTo(by ident.Principal, e *state.Event) (*state.Event, bool) {
	if by.IsUser() || (e.Agent != "" && e.Agent == by.Agent) {
		return e, true
	}
	if !agentVisible[string(e.Type)] {
		return nil, false
	}
	redacted := *e
	redacted.Payload = nil
	return &redacted, true
}

// autonomy is whether agent-filed tickets are worked without a person: the
// runtime switch, or the config's default until it's been flipped.
func (d *Daemon) autonomy(ctx context.Context) bool {
	on, set, err := d.state.Settings.Autonomy(ctx)
	if err != nil || !set {
		return d.opts.Config.Autonomy.Enabled
	}
	return on
}

func (ar *agentRoutes) getAutonomy(w http.ResponseWriter, r *http.Request) {
	control.WriteJSON(w, http.StatusOK, api.Autonomy{Enabled: ar.d.autonomy(r.Context())})
}

func (ar *agentRoutes) setAutonomy(w http.ResponseWriter, r *http.Request) {
	if !control.PrincipalFrom(r.Context()).IsUser() {
		agentFail(w, agents.ErrUserOnly)
		return
	}
	var in api.Autonomy
	if !decode(w, r, &in) {
		return
	}
	if err := ar.d.state.Settings.SetAutonomy(r.Context(), in.Enabled); err != nil {
		agentFail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, in)
}
