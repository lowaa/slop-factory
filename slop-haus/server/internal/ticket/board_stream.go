package ticket

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ssestream"
)

// BoardRevisions reads the tenant's board revision: a counter the database
// moves whenever anything the board shows changes (see bump_board_revision).
type BoardRevisions interface {
	BoardRevision(ctx context.Context) (int64, error)
	// BoardDelta is what changed in the revisions after since, up to upTo.
	BoardDelta(ctx context.Context, since, upTo int64) (*BoardDelta, error)
}

// BoardStreamOptions tunes the board's change stream. Zero fields take the
// defaults below.
type BoardStreamOptions struct {
	// PollInterval is how often the revision is read. Polling rather than
	// LISTEN/NOTIFY: a second's lag costs nobody anything, a connection
	// pinned per open board does.
	PollInterval time.Duration
	// KeepAlive is the longest the stream goes without writing. huma's sse
	// gives each write sse.WriteTimeout to land and leaves that deadline in
	// place after it, so a stream quiet for longer than that breaks on its
	// next write; a comment in between keeps it open, and lets a dead
	// client's connection be noticed rather than held.
	KeepAlive time.Duration
	// MaxLifetime bounds a stream by the access token that opened it: it
	// ends once the token it authenticated with has expired, so a session
	// signed out or revoked elsewhere stops getting events then. The
	// browser reconnects by itself and is authenticated afresh.
	MaxLifetime time.Duration
	// PerUser and Total cap the streams open at once, per signed-in user
	// and over the whole process: each one polls the database every
	// PollInterval for as long as it is open.
	PerUser int
	Total   int
	// BusyRetry is what a stream refused by a cap tells the browser to wait
	// before trying again.
	BusyRetry time.Duration
	// Costs reads the worker cost of the attempt cards an event carries; nil
	// leaves it empty.
	Costs AttemptCosts
	// Clock times the poll, the keepalive and the lifetime; nil is the wall
	// clock.
	Clock clock.Clock
}

const (
	defaultBoardPollInterval = time.Second
	defaultBoardMaxLifetime  = 5 * time.Minute
	defaultBoardPerUser      = 8
	defaultBoardTotal        = 512
	defaultBoardBusyRetry    = 30 * time.Second
)

func (o *BoardStreamOptions) withDefaults() BoardStreamOptions {
	out := BoardStreamOptions{}
	if o != nil {
		out = *o
	}
	d := ssestream.Options{
		PollInterval: out.PollInterval,
		KeepAlive:    out.KeepAlive,
		MaxLifetime:  out.MaxLifetime,
		PerUser:      out.PerUser,
		Total:        out.Total,
		BusyRetry:    out.BusyRetry,
		Clock:        out.Clock,
	}.WithDefaults(ssestream.Options{
		PollInterval: defaultBoardPollInterval,
		MaxLifetime:  defaultBoardMaxLifetime,
		PerUser:      defaultBoardPerUser,
		Total:        defaultBoardTotal,
		BusyRetry:    defaultBoardBusyRetry,
	})
	out.PollInterval, out.KeepAlive, out.MaxLifetime = d.PollInterval, d.KeepAlive, d.MaxLifetime
	out.PerUser, out.Total, out.BusyRetry, out.Clock = d.PerUser, d.Total, d.BusyRetry, d.Clock
	return out
}

func (o *BoardStreamOptions) sse() ssestream.Options {
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

// BoardStream serves GET /api/tickets/stream: an event each time the
// tenant's board changes: the new revision, and the tickets that changed
// since the last event, or a reload marker when the stream cannot name them.
// A client applies the tickets against its own filter; one that predates
// the tickets reads only the revision and refetches.
type BoardStream struct {
	revisions BoardRevisions
	opts      BoardStreamOptions
	stream    *ssestream.Stream
	logger    *slog.Logger
}

func NewBoardStream(revisions BoardRevisions, opts *BoardStreamOptions, logHandler slog.Handler) *BoardStream {
	o := opts.withDefaults()
	return &BoardStream{
		revisions: revisions,
		opts:      o,
		stream:    ssestream.New(o.sse(), "board"),
		logger:    slog.New(logHandler),
	}
}

// BoardStreamRequest's Since is the revision the client already has, and
// LastEventID the same thing as the browser's own reconnect sends it (each
// event's id is its revision). Plain int64s because huma rejects
// pointer query parameters; zero is "none".
type BoardStreamRequest struct {
	Since       int64 `query:"since" minimum:"0" doc:"board revision already seen; omit to be sent the current one"`
	LastEventID int64 `header:"Last-Event-ID" minimum:"0" doc:"set by the browser when it reconnects by itself"`
}

// BoardChangeBody is the one event the stream sends.
type BoardChangeBody struct {
	Revision int64        `json:"revision" doc:"the tenant's board revision; it moves with every change"`
	Tickets  []TicketBody `json:"tickets,omitempty" doc:"the tickets that changed since the last event, as the list returns them without bodies"`
	Cards    []TicketBody `json:"cards,omitempty" doc:"the attempt cards, in the In progress, Discussion and Review columns, of those of the changed tickets that have several attempts, in full: replace every card of each changed ticket with the ticket's cards here, or with its one ticket card (from tickets, in the column of its status) when none is; a ticket in a work column always has a live attempt there, so a ticket with no cards here is not in one unless it has a single attempt"`
	Reload   bool         `json:"reload,omitempty" doc:"the change cannot be described (a first event, a deletion, a bulk change, a gap): refetch the board"`
}

// RegisterBoardStream mounts the board's change stream. RequireUser like
// the list it tells the board to refetch: the user's session scopes the
// request to their tenant, and the revision is read through that tenant's
// RLS like every other read.
func RegisterBoardStream(api huma.API, s *BoardStream) {
	sse.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "streamBoardChanges",
		Method:      http.MethodGet,
		Path:        "/api/tickets/stream",
		Summary:     "Stream an event each time the board's tickets change",
	}, map[string]any{
		"message": BoardChangeBody{},
	}, s.Stream)
}

// Stream sends the revision whenever it differs from the last one sent:
// on connect when the client has none or an older one, then on each change.
//
// It ends on ctx.Done() -- the client going away, the server's shutdown
// (internal/server cancels event streams as it begins), or the stream's
// lifetime running out -- and on a failed write.
func (s *BoardStream) Stream(ctx context.Context, input *BoardStreamRequest, send sse.Sender) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		s.logger.Error("board stream: no user on a RequireUser route", "err", err)
		return
	}
	ctx, release, ok := s.stream.Open(ctx, user, send)
	if !ok {
		return
	}
	defer release()

	sent := max(input.Since, input.LastEventID)
	known := sent > 0
	s.stream.Loop(ctx, send, nil, func(ctx context.Context) (wrote, stop bool) {
		revision, err := s.revisions.BoardRevision(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				s.logger.Error("board stream: read revision", "err", err)
			}
		case !known || revision != sent:
			body, err := s.change(ctx, known, sent, revision)
			if err != nil {
				if ctx.Err() == nil {
					s.logger.Error("board stream: read changes", "err", err)
				}
				break
			}
			if err := send(sse.Message{ID: int(revision), Data: body}); err != nil {
				return false, true
			}
			sent, known = revision, true
			return true, false
		}
		return false, false
	})
}

// change is the event for the move from sent to revision. Without a cursor
// the client has nothing to apply changes to, so it is told to reload; so is
// a client whose revision went backwards (a restored database).
func (s *BoardStream) change(ctx context.Context, known bool, sent, revision int64) (BoardChangeBody, error) {
	body := BoardChangeBody{Revision: revision}
	if !known || revision < sent {
		body.Reload = true
		return body, nil
	}
	delta, err := s.revisions.BoardDelta(ctx, sent, revision)
	if err != nil {
		return body, err
	}
	body.Reload = delta.Reload
	if len(delta.Tickets) > 0 {
		body.Tickets = ticketBodies(delta.Tickets)
	}
	if len(delta.Cards) > 0 {
		body.Cards = ticketBodies(delta.Cards)
		if err := fillCosts(ctx, s.opts.Costs, body.Cards); err != nil {
			return body, err
		}
	}
	return body, nil
}
