package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// errNoAgent is a transcript or watch target that names no agent, and no
// ticket that has had one.
var errNoAgent = errors.New("no agent with that id, and no ticket that has had one")

var observeErrors = append([]control.ErrorMapping{
	{Err: errNoAgent, Status: http.StatusNotFound},
	{Err: transcript.ErrNotFound, Status: http.StatusNotFound},
}, agentErrors...)

func observeFail(w http.ResponseWriter, err error) {
	control.WriteError(w, control.StatusFor(err, observeErrors), err)
}

// settleAfter is when a followed transcript is read once more after the
// last event: a harness may append its file a moment after it reports the
// event, and without a later event nothing would read the tail.
const settleAfter = time.Second

// observeRoutes are the user's views of what an agent is doing: its
// transcript, and its live conversation events. Agents may not use them:
// transcripts hold whatever the agent saw, secrets included.
type observeRoutes struct{ d *Daemon }

func (o *observeRoutes) register(s *control.Server) {
	s.HandleFunc("GET "+api.PathTranscript, o.transcript)
	s.HandleFunc("GET "+api.PathWatch, o.watch)
}

// target is the agent an id names: an agent's own id, else the newest agent
// of the ticket with that id, ended or not.
func (o *observeRoutes) target(ctx context.Context, r *http.Request) (*state.Agent, error) {
	if !control.PrincipalFrom(ctx).IsUser() {
		return nil, fmt.Errorf("%w: transcripts and watch are the user's", agents.ErrUserOnly)
	}
	id := r.PathValue("target")
	if a, err := o.d.state.Agents.Get(ctx, ident.AgentID(id)); err == nil {
		return a, nil
	}
	if a, err := o.d.state.Agents.LatestForTicket(ctx, ident.TicketID(id)); err == nil {
		return a, nil
	}
	return nil, fmt.Errorf("%w: %q", errNoAgent, id)
}

// from is the event seq to follow from, and the target as it was then. The
// seq is taken first: an agent that ends after it is read has its ending
// events after the seq, so a follower sees them, where the other order
// could read a live agent and miss its end.
func (o *observeRoutes) from(ctx context.Context, r *http.Request) (int64, *state.Agent, error) {
	seq, err := o.d.state.Events.Latest(ctx)
	if err != nil {
		return 0, nil, err
	}
	a, err := o.target(ctx, r)
	return seq, a, err
}

// ended reports whether a's conversation can still grow.
func ended(a *state.Agent) bool { return a.EndedAt != nil || a.State.Ends() }

// stream writes NDJSON, flushing each batch so a follower sees it at once.
type stream struct {
	enc     *json.Encoder
	flusher http.Flusher
}

func newStream(w http.ResponseWriter) *stream {
	w.Header().Set("Content-Type", "application/x-ndjson")
	f, _ := w.(http.Flusher)
	return &stream{enc: json.NewEncoder(w), flusher: f}
}

func (s *stream) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *stream) event(e *state.Event) error {
	return s.enc.Encode(api.Event{Seq: e.Seq, At: e.At, Type: string(e.Type), Agent: string(e.Agent), Ticket: string(e.Ticket), Payload: e.Payload})
}

type transcriptQuery struct {
	tail   int
	since  time.Time
	full   bool
	follow bool
}

func parseTranscriptQuery(r *http.Request) (*transcriptQuery, error) {
	q := r.URL.Query()
	tq := &transcriptQuery{}
	_, tq.full = q["full"]
	_, tq.follow = q["follow"]
	var err error
	if v := q.Get("tail"); v != "" {
		if tq.tail, err = strconv.Atoi(v); err != nil || tq.tail < 0 {
			return nil, fmt.Errorf("tail: %q isn't a count", v)
		}
	}
	if v := q.Get("since"); v != "" {
		if tq.since, err = time.Parse(time.RFC3339Nano, v); err != nil {
			return nil, fmt.Errorf("since: %q isn't an RFC 3339 time", v)
		}
	}
	return tq, nil
}

// view applies the query's filters to entries; tail only to the first
// batch, as `tail -f` does.
func (tq *transcriptQuery) view(entries []transcript.Entry, first bool) []transcript.Entry {
	entries = transcript.Since(entries, tq.since)
	if first {
		entries = transcript.Tail(entries, tq.tail)
	}
	if !tq.full {
		entries = transcript.Truncate(entries, transcript.DefaultTruncate)
	}
	return entries
}

// transcript streams an agent's normalised transcript. With ?follow it keeps
// streaming as it grows, re-reading on the agent's conversation events
// rather than polling, until the agent ends.
func (o *observeRoutes) transcript(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tq, err := parseTranscriptQuery(r)
	if err != nil {
		control.WriteError(w, http.StatusBadRequest, err)
		return
	}
	seq, a, err := o.from(ctx, r)
	if err != nil {
		observeFail(w, err)
		return
	}
	entries, err := o.d.transcripts.Read(ctx, a)
	// A live agent may not have written anything yet.
	if errors.Is(err, transcript.ErrNotFound) && !ended(a) {
		entries, err = nil, nil
	}
	if err != nil {
		observeFail(w, err)
		return
	}
	out := newStream(w)
	if err := o.emit(out, tq.view(entries, true)); err != nil || !tq.follow || ended(a) {
		return
	}
	f := &follower{o: o, agent: a, seq: seq, sent: len(entries), q: tq, out: out}
	f.run(ctx)
}

func (o *observeRoutes) emit(s *stream, entries []transcript.Entry) error {
	for i := range entries {
		if err := s.enc.Encode(&entries[i]); err != nil {
			return err
		}
	}
	s.flush()
	return nil
}

// follower tails one agent's transcript for a follow request.
type follower struct {
	o     *observeRoutes
	agent *state.Agent
	seq   int64
	// sent counts the transcript's entries already streamed.
	sent int
	q    *transcriptQuery
	out  *stream
}

func (f *follower) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	evs := make(chan []state.Event)
	go f.feed(ctx, evs)
	var settle <-chan time.Time
	for {
		select {
		case batch, ok := <-evs:
			if !ok {
				return
			}
			changed, done := f.relevant(batch)
			if done {
				_ = f.reread(ctx)
				return
			}
			if !changed {
				continue
			}
			settle = f.o.d.opts.Clock.After(settleAfter)
		case <-settle:
			settle = nil
		}
		if f.reread(ctx) != nil {
			return
		}
	}
}

// feed passes the event log on from seq, and closes evs once it can't.
func (f *follower) feed(ctx context.Context, evs chan<- []state.Event) {
	defer close(evs)
	seq := f.seq
	for {
		batch, err := f.o.d.state.Events.Follow(ctx, seq, 0)
		if err != nil {
			return
		}
		seq = batch[len(batch)-1].Seq
		select {
		case evs <- batch:
		case <-ctx.Done():
			return
		}
	}
}

// relevant reports whether batch may have grown the transcript, and
// whether the agent has ended.
func (f *follower) relevant(batch []state.Event) (changed, done bool) {
	for i := range batch {
		e := &batch[i]
		if e.Agent != f.agent.ID {
			continue
		}
		if endsAgent(e) {
			return true, true
		}
		changed = changed || strings.HasPrefix(string(e.Type), agents.EventHarness)
	}
	return changed, false
}

func endsIn(payload json.RawMessage) bool {
	var p struct {
		To string `json:"to"`
	}
	return json.Unmarshal(payload, &p) == nil && state.AgentState(p.To).Ends()
}

// reread streams what the transcript has gained since the last read.
func (f *follower) reread(ctx context.Context) error {
	entries, err := f.o.d.transcripts.Read(ctx, f.agent)
	if errors.Is(err, transcript.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) <= f.sent {
		return nil
	}
	fresh := entries[f.sent:]
	f.sent = len(entries)
	return f.o.emit(f.out, f.q.view(fresh, false))
}

// watchable are the events `watch` shows besides the conversation's own
// (harness.*): what the agent's states and messages do to it.
var watchable = map[string]bool{
	agents.EventState: true, agents.EventFinished: true, agents.EventDelivered: true,
	agents.EventNudged: true, agents.EventResumed: true, agents.EventParked: true, "agent.asked": true, agents.EventReleased: true,
}

// watch streams an agent's conversation events, live, from now, until it
// ends. The first line says whom it watches.
func (o *observeRoutes) watch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	seq, a, err := o.from(ctx, r)
	if err != nil {
		observeFail(w, err)
		return
	}
	out := newStream(w)
	payload, _ := json.Marshal(api.WatchTarget{State: string(a.State), Ended: ended(a)})
	if out.enc.Encode(api.Event{At: o.d.opts.Clock.Now(), Type: api.WatchStarted, Agent: string(a.ID), Ticket: string(a.Ticket), Payload: payload}) != nil {
		return
	}
	out.flush()
	if ended(a) {
		return
	}
	for {
		evs, err := o.d.state.Events.Follow(ctx, seq, 0)
		if err != nil {
			return
		}
		for i := range evs {
			e := &evs[i]
			seq = e.Seq
			if e.Agent != a.ID || !watched(e) {
				continue
			}
			if out.event(e) != nil {
				return
			}
			if endsAgent(e) {
				out.flush()
				return
			}
		}
		out.flush()
	}
}

func watched(e *state.Event) bool {
	return strings.HasPrefix(string(e.Type), agents.EventHarness) || watchable[string(e.Type)]
}

// endsAgent reports whether e is its agent ending. A release slop-bot
// makes from the record (endRecord) logs agent.released, with no
// agent.state.
func endsAgent(e *state.Event) bool {
	return string(e.Type) == agents.EventFinished || string(e.Type) == agents.EventReleased ||
		(string(e.Type) == agents.EventState && endsIn(e.Payload))
}
