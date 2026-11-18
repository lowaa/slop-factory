package conductor

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ssestream"
)

// StreamOptions tunes the conversation stream. Zero fields take the defaults.
type StreamOptions struct {
	// PollInterval is how often the conversation's newest event number is
	// read. Polling, as the board's stream does: any replica can serve the
	// stream of an event another wrote, and no connection is pinned per open
	// stream.
	PollInterval time.Duration
	// KeepAlive is the longest the stream goes without writing: see
	// ssestream.Options.
	KeepAlive time.Duration
	// MaxLifetime bounds a stream by the access token that opened it, so a
	// session signed out elsewhere stops getting events then; the browser
	// reconnects by itself, with Last-Event-ID, and is authenticated afresh.
	MaxLifetime time.Duration
	// RecheckEvery is how many polls pass between checks that the person still
	// owns the conductor and it is still enabled (each poll already reads
	// only this conductor's own conversation). A conductor disabled or an
	// owner removed ends the stream within that many polls.
	RecheckEvery int
	// PerUser and Total cap the streams open at once, per signed-in user and
	// in this process: each polls the database for as long as it is open.
	PerUser int
	Total   int
	// BusyRetry is what a stream refused by a cap tells the browser to wait.
	BusyRetry time.Duration
	// Clock times the poll, the keepalive and the lifetime; nil is the wall
	// clock.
	Clock clock.Clock
}

const (
	defaultStreamPoll         = time.Second
	defaultStreamMaxLifetime  = 5 * time.Minute
	defaultStreamRecheckEvery = 10
	defaultStreamPerUser      = 4
	defaultStreamTotal        = 256
	defaultStreamBusyRetry    = 30 * time.Second
)

func (o *StreamOptions) withDefaults() StreamOptions {
	out := StreamOptions{}
	if o != nil {
		out = *o
	}
	d := out.sse().WithDefaults(ssestream.Options{
		PollInterval: defaultStreamPoll,
		MaxLifetime:  defaultStreamMaxLifetime,
		PerUser:      defaultStreamPerUser,
		Total:        defaultStreamTotal,
		BusyRetry:    defaultStreamBusyRetry,
	})
	out.PollInterval, out.KeepAlive, out.MaxLifetime = d.PollInterval, d.KeepAlive, d.MaxLifetime
	out.PerUser, out.Total, out.BusyRetry, out.Clock = d.PerUser, d.Total, d.BusyRetry, d.Clock
	if out.RecheckEvery <= 0 {
		out.RecheckEvery = defaultStreamRecheckEvery
	}
	return out
}

func (o *StreamOptions) sse() ssestream.Options {
	return ssestream.Options{
		PollInterval: o.PollInterval,
		KeepAlive:    o.KeepAlive,
		MaxLifetime:  o.MaxLifetime,
		PerUser:      o.PerUser,
		Total:        o.Total,
		BusyRetry:    o.BusyRetry,
		Clock:        o.Clock,
	}
}

type streamer struct {
	opts   StreamOptions
	stream *ssestream.Stream
}

func newStreamer(opts *StreamOptions) *streamer {
	o := opts.withDefaults()
	return &streamer{opts: o, stream: ssestream.New(o.sse(), "conversation")}
}

type streamAuthKey struct{}

// streamAuth is what the gate in front of a stream proved: the caller owns
// the conductor, and the conversation is its own.
type streamAuth struct {
	owned        *agent.OwnedConductor
	conversation uuid.UUID
}

// StreamConversationRequest's After is the last event the client has, and
// LastEventID the same as the browser's own reconnect sends it (each event's
// id is its seq). Zero is "from the start". Plain int64s because huma rejects
// pointer query parameters.
type StreamConversationRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
	After          int64  `query:"after" minimum:"0" doc:"the last event seq already seen; omit for the whole transcript"`
	LastEventID    int64  `header:"Last-Event-ID" minimum:"0" doc:"set by the browser when it reconnects by itself"`
}

// streamGate is the ownership check in front of the stream. An operation
// middleware rather than the handler's own check because the response has
// already started when sse calls the handler, so a refusal there could not be
// a 403.
func (h *Handler) streamGate(api huma.API) func(huma.Context, func(huma.Context)) {
	refuse := func(ctx huma.Context, err error) {
		var status huma.StatusError
		if errors.As(err, &status) {
			_ = huma.WriteErr(api, ctx, status.GetStatus(), status.Error())
			return
		}
		_ = huma.WriteErr(api, ctx, 500, "internal server error")
	}
	return func(ctx huma.Context, next func(huma.Context)) {
		owned, err := h.authorize(ctx.Context(), ctx.Param("id"))
		if err != nil {
			refuse(ctx, err)
			return
		}
		id, err := parseID(ctx.Param("conversation_id"), "conversation id")
		if err != nil {
			refuse(ctx, err)
			return
		}
		if _, err := h.svc.Get(ctx.Context(), owned, id); err != nil {
			refuse(ctx, h.mapError(err))
			return
		}
		next(huma.WithValue(ctx, streamAuthKey{}, &streamAuth{owned: owned, conversation: id}))
	}
}

// StreamConversation sends the conversation's transcript events as they are
// stored: first any after the client's last, then each new one, in order. A
// client that reconnects with the last id it saw misses nothing and sees
// nothing twice.
//
// It reads the same rows any replica writes, so an event reported through one
// api process reaches a stream held open on another.
//
// It ends on ctx.Done() -- the client going away, the server's shutdown, or
// the stream's lifetime running out -- on a failed write, and when the person
// no longer owns the conductor or it is disabled.
func (h *Handler) StreamConversation(ctx context.Context, input *StreamConversationRequest, send sse.Sender) {
	auth, _ := ctx.Value(streamAuthKey{}).(*streamAuth)
	who, err := authn.UserFromContext(ctx)
	if auth == nil || err != nil {
		h.logger.Error("conversation stream reached without its gate")
		return
	}
	s := h.stream
	ctx, release, ok := s.stream.Open(ctx, who, send)
	if !ok {
		return
	}
	defer release()

	sent := max(input.After, input.LastEventID, 0)
	polls := 0
	s.stream.Loop(ctx, send, h.svc.Closed(), func(ctx context.Context) (wrote, stop bool) {
		defer func() { polls++ }()
		// Before the events, so what is committed after access is gone is not sent:
		// a conductor disabled, or an owner removed, ends the stream.
		if polls%s.opts.RecheckEvery == s.opts.RecheckEvery-1 {
			if err := h.recheckAccess(ctx, who, auth, sent, send); err != nil {
				return false, true
			}
		}
		conv, err := h.svc.Get(ctx, auth.owned, auth.conversation)
		switch {
		case err != nil:
			// Whatever it was, the stream ends: the browser reconnects, and is told
			// no (404, 403, 409) if the conversation or its access is gone.
			if ctx.Err() == nil && !errors.Is(err, ErrNotFound) {
				h.logger.Error("conversation stream: read conversation", "err", err)
			}
			return false, true
		case conv.LastSeq > sent:
			next, err := h.sendEventsAfter(ctx, who, auth, sent, send)
			if err != nil {
				return false, true
			}
			if next > sent {
				sent = next
				return true, false
			}
		}
		return false, false
	})
}

// recheckAccess ends the stream (an error) when the person no longer owns the
// conductor or it is disabled.
func (h *Handler) recheckAccess(ctx context.Context, who *authn.User, auth *streamAuth, sent int64, send sse.Sender) error {
	err := h.stillAllowed(ctx, who, auth)
	// A conductor that was disabled, whose owner is still its owner, says
	// why in the transcript (the disable wrote a "stopped"), and that is
	// sent. Any other failure -- the person is no longer the owner, or no
	// longer a member -- sends nothing more: access is gone, and what was
	// committed since belongs to nobody they may read.
	if errors.Is(err, agent.ErrIsDisabled) {
		_, _ = h.sendEventsAfter(ctx, who, auth, sent, send)
	}
	return err
}

func (h *Handler) stillAllowed(ctx context.Context, who *authn.User, auth *streamAuth) error {
	_, err := h.owners.AuthorizeOwner(ctx, who, auth.owned.Agent().ID)
	return err
}

// sendEventsAfter sends every event after seq, a page at a time, and returns
// the last one sent. An error is a failed write: the stream is over.
//
// Access is checked again after each page is read and before it is written.
// The check at the top of a poll alone leaves a gap: an owner removed between
// that check and the read has the events committed with the removal (the
// disable's "stopped") sent to them. A page that holds one was read after the
// removal committed, so a check made after the read cannot pass.
func (h *Handler) sendEventsAfter(ctx context.Context, who *authn.User, auth *streamAuth, after int64, send sse.Sender) (int64, error) {
	sent := after
	for {
		events, err := h.svc.Events(ctx, auth.owned, auth.conversation, sent, MaxEventsPerRead)
		if err != nil {
			// The stream ends and the browser reconnects with what it has seen,
			// rather than hanging on a connection that no longer delivers.
			if ctx.Err() == nil {
				h.logger.Error("conversation stream: read events", "err", err)
			}
			return sent, err
		}
		if len(events) > 0 {
			// A disabled conductor's owner may still read why: that is the
			// case recheckAccess sends on purpose.
			if err := h.stillAllowed(ctx, who, auth); err != nil && !errors.Is(err, agent.ErrIsDisabled) {
				return sent, err
			}
		}
		for i := range events {
			if err := send(sse.Message{ID: int(events[i].Seq), Data: eventBody(&events[i])}); err != nil {
				return sent, err
			}
			sent = events[i].Seq
		}
		if len(events) < MaxEventsPerRead {
			return sent, nil
		}
	}
}
