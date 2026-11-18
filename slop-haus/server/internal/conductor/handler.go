package conductor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Conversations is the part of Service the handlers use. An interface so the
// handlers' answers to refusals can be tested with a fake that returns a
// chosen error; what the refusals are is tested against the real Service.
type Conversations interface {
	Start(ctx context.Context, owned *agent.OwnedConductor, req *StartRequest) (*Conversation, error)
	Send(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, req *SendRequest) (*Event, error)
	Disconnect(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) (*Conversation, error)
	Interrupt(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, turn int64) (*Conversation, error)
	Clear(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, interrupt bool) (*Conversation, error)
	Compact(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, interrupt bool) (*Conversation, error)
	Delete(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) error
	Rename(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, title *string) (*Conversation, error)
	Get(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) (*Conversation, error)
	Describe(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) (*Detail, error)
	HostInfo(ctx context.Context, owned *agent.OwnedConductor) (HostStatus, error)
	List(ctx context.Context, owned *agent.OwnedConductor, limit int) ([]Conversation, error)
	Events(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, after int64, limit int) ([]Event, error)
	EventsByTurns(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, before int64, turns int) (*TranscriptPage, error)
	AuthenticateHost(ctx context.Context) (*Host, error)
	Poll(ctx context.Context, host *Host, req *PollRequest) (*PollResult, error)
	Report(ctx context.Context, host *Host, id uuid.UUID, req *ReportRequest) (int64, error)
	Release(ctx context.Context, host *Host, hostID uuid.UUID) error
	ReportLayout(ctx context.Context, host *Host, layout *agent.Layout) error
	MaxWait() time.Duration
	Closed() <-chan struct{}
}

// Owners proves a person owns a conductor: agent.ConductorService.
type Owners interface {
	AuthorizeOwner(ctx context.Context, who *authn.User, id uuid.UUID) (*agent.OwnedConductor, error)
}

// Handler is the HTTP wrapper over Service: the owner's routes (a signed-in
// person) and the host's (a conductor's own credential).
type Handler struct {
	svc    Conversations
	owners Owners
	stream *streamer
	logger *slog.Logger
}

func NewHandler(svc Conversations, owners Owners, opts *StreamOptions, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, owners: owners, stream: newStreamer(opts), logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can use a conductor"},
		{Err: agent.ErrNotOwner, Kind: httperr.Forbidden, Message: "only the remote control's owner may do that"},
		{Err: ErrNotConductor, Kind: httperr.Forbidden, Message: "only the remote control's owner may do that"},
		{Err: agent.ErrNotFound, Kind: httperr.NotFound, Message: "not found"},
		{Err: ErrNotFound, Kind: httperr.NotFound, Message: "not found"},
		{Err: agent.ErrIsDisabled, Kind: httperr.Conflict, Message: "the remote control is disabled"},
		{Err: ErrBusy, Kind: httperr.Conflict, Message: "another session was opened at the same moment: try again"},
		{Err: ErrHostBusy, Kind: httperr.Conflict, Message: "another host process is serving this conductor"},
		{Err: ErrWorkPending, Kind: httperr.Conflict, Message: "the remote control's host has not yet acknowledged the session's work: try again shortly"},
		{Err: ErrGap, Kind: httperr.Conflict, Message: "a report skips events"},
		{Err: ErrQueueFull, Kind: httperr.TooMany, Message: "too much is waiting for the remote control's host: is it running?"},
		{Err: ErrTranscriptFull, Kind: httperr.TooLarge, Message: "the conversation has reached its size limit: start another"},
		{Err: ErrHostLacksInterrupt, Kind: httperr.Conflict, Message: "This Remote Control host is on an older version; restart it to enable Stop"},
		{Err: ErrTurnRunning, Kind: httperr.Conflict, Message: "a reply is running: stop it first, or ask to interrupt it"},
		{Err: ErrNoProcess, Kind: httperr.Conflict, Message: "the conversation is disconnected: send a message to reconnect it first"},
		{Err: ErrCannotCompact, Kind: httperr.Conflict, Message: "this conversation cannot be compacted"},
		{Err: ErrNotReplying, Kind: httperr.Conflict, Message: "that reply is not running"},
		{Err: ErrInvalid, Kind: httperr.Invalid, Message: "invalid request"},
	},
	// These carry the specifics of this refusal, which the rules' fixed text
	// cannot.
	Custom: func(err error) error {
		var gap *GapError
		if errors.As(err, &gap) {
			return huma.Error409Conflict(fmt.Sprintf("a report skips events: the server has up to host_seq %d, send from the next", gap.Acked))
		}
		var r *refusal
		if errors.As(err, &r) && errors.Is(err, ErrInvalid) {
			return huma.Error422UnprocessableEntity(r.detail)
		}
		return nil
	},
	Unexpected: "unexpected conductor conversation error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// authorize is the ownership check every owner route starts with: the
// caller must own the conductor the path names.
func (h *Handler) authorize(ctx context.Context, conductorID string) (*agent.OwnedConductor, error) {
	who := authn.PersonFromContext(ctx)
	id, err := uuid.Parse(conductorID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("conductor id must be a valid uuid")
	}
	owned, err := h.owners.AuthorizeOwner(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return owned, nil
}

func parseID(raw, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, huma.Error422UnprocessableEntity(what + " must be a valid uuid")
	}
	return id, nil
}

func parseClientID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := parseID(raw, "client_id")
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// -- Wire types --

type ConversationBody struct {
	ID          string  `json:"id" doc:"Also the harness's session id"`
	ConductorID string  `json:"conductor_id"`
	State       string  `json:"state" enum:"starting,ready,failed,stopped" doc:"starting and ready have a harness process on the host; failed and stopped have none, and the next message reconnects"`
	Failure     *string `json:"failure"`
	FirstLine   *string `json:"first_line" nullable:"true" doc:"The first line of the first message: the default name. Null before there is one"`
	Title       *string `json:"title" nullable:"true" doc:"The name its owner gave it, which takes the place of first_line. Null when not set"`
	// LastSeq is the newest transcript event: stream or read after it.
	LastSeq       int64     `json:"last_seq"`
	Truncated     bool      `json:"truncated" doc:"assistant text past the size limit was not kept"`
	ContextTokens int64     `json:"context_tokens" minimum:"0" doc:"the input side of the newest turn's usage (cache read + cache write + input) as the host reported it; 0 is unknown"`
	CanCompact    bool      `json:"can_compact" doc:"whether the host's harness can compact this session"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func conversationBody(c *Conversation) ConversationBody {
	return ConversationBody{
		ID: c.ID.String(), ConductorID: c.ConductorID.String(), State: string(c.State), Failure: c.Failure, FirstLine: c.FirstLine, Title: c.Title, LastSeq: c.LastSeq, Truncated: c.Truncated, ContextTokens: c.ContextTokens, CanCompact: c.CanCompact, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type RepoCommitBody struct {
	SHA     string `json:"sha" pattern:"^[0-9a-f]{7,64}$"`
	Subject string `json:"subject" maxLength:"200"`
}

type PushBody struct {
	Ref  string `json:"ref" minLength:"1" maxLength:"255" doc:"the remote branch the host names it by, \"origin/x\""`
	From string `json:"from" maxLength:"64" doc:"empty when the push created the branch"`
	To   string `json:"to" pattern:"^[0-9a-f]{7,64}$"`
}

// RepoStateBody is where the working copy stands after a turn. The host reports it, so it is what the host says, shown as such.
type RepoStateBody struct {
	Branch       string           `json:"branch" maxLength:"255" doc:"empty when HEAD is detached"`
	Head         string           `json:"head" pattern:"^[0-9a-f]{7,64}$"`
	Dirty        bool             `json:"dirty" doc:"there are uncommitted changes"`
	DirtyCount   int              `json:"dirty_count" minimum:"0" maximum:"10000000"`
	Upstream     string           `json:"upstream" maxLength:"255"`
	Ahead        int              `json:"ahead" minimum:"0" maximum:"10000000" doc:"against the upstream; without one, the commits no remote branch holds"`
	Behind       int              `json:"behind" minimum:"0" maximum:"10000000"`
	Commits      []RepoCommitBody `json:"commits" maxItems:"20" doc:"the turn's commits, newest first"`
	Pushes       []PushBody       `json:"pushes" maxItems:"20" doc:"the turn's pushes"`
	ChangedPaths []string         `json:"changed_paths" maxItems:"200" doc:"paths the conversation has changed, at most 200 of changed_total"`
	ChangedTotal int              `json:"changed_total" minimum:"0" maximum:"10000000"`
}

func repoStateBody(r *RepoState) *RepoStateBody {
	if r == nil {
		return nil
	}
	b := &RepoStateBody{
		Branch: r.Branch, Head: r.Head, Dirty: r.Dirty, DirtyCount: r.DirtyCount, Upstream: r.Upstream,
		Ahead: r.Ahead, Behind: r.Behind, ChangedPaths: append([]string{}, r.ChangedPaths...), ChangedTotal: r.ChangedTotal,
		Commits: make([]RepoCommitBody, len(r.Commits)), Pushes: make([]PushBody, len(r.Pushes)),
	}
	for i, c := range r.Commits {
		b.Commits[i] = RepoCommitBody{SHA: c.SHA, Subject: c.Subject}
	}
	for i, p := range r.Pushes {
		b.Pushes[i] = PushBody{Ref: p.Ref, From: p.From, To: p.To}
	}
	return b
}

func (b *RepoStateBody) state() *RepoState {
	r := &RepoState{
		Branch: b.Branch, Head: b.Head, Dirty: b.Dirty, DirtyCount: b.DirtyCount, Upstream: b.Upstream,
		Ahead: b.Ahead, Behind: b.Behind, ChangedPaths: b.ChangedPaths, ChangedTotal: b.ChangedTotal,
	}
	for _, c := range b.Commits {
		r.Commits = append(r.Commits, RepoCommit{SHA: c.SHA, Subject: c.Subject})
	}
	for _, p := range b.Pushes {
		r.Pushes = append(r.Pushes, Push{Ref: p.Ref, From: p.From, To: p.To})
	}
	return r
}

// HeldScopeOverlapBody is a scope an agent holds that a path the conversation
// changed lies in.
type HeldScopeOverlapBody struct {
	TicketID int64  `json:"ticket_id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Path     string `json:"path" doc:"the path the conversation changed"`
	Held     string `json:"held" doc:"the scope it lies in"`
	Resolve  string `json:"resolve,omitempty" enum:"migration,regenerate,merge" doc:"set when it lies inside a shared path of the workspace: how a clash there is settled when main is merged"`
}

// ConversationDetailBody is one conversation as the chat shows it.
type ConversationDetailBody struct {
	ConversationBody
	HostOnline       bool                   `json:"host_online" doc:"whether the remote control's host has polled lately"`
	HostCapabilities []string               `json:"host_capabilities,omitempty" doc:"what the host that polled last said it can do, e.g. interrupt; empty for a host on older code, which the board offers none of the newer actions for"`
	HostVersion      string                 `json:"host_version,omitempty" doc:"the version the host that polled last reported; empty for a host on older code"`
	Repo             *RepoStateBody         `json:"repo" doc:"the working copy's state after the newest turn, as the host reported it"`
	ScopeOverlaps    []HeldScopeOverlapBody `json:"scope_overlaps" doc:"scopes agents hold that the paths a live conversation has changed lie in: a notice, nothing is stopped"`
}

func heldScopeOverlapBodies(overlaps []ticket.HeldScopeOverlap) []HeldScopeOverlapBody {
	out := make([]HeldScopeOverlapBody, len(overlaps))
	for i, o := range overlaps {
		out[i] = HeldScopeOverlapBody{TicketID: o.TicketID, Title: o.Title, Status: string(o.Status), Path: o.Path, Held: o.Held, Resolve: o.Resolve}
	}
	return out
}

type ConversationDetailResponse struct {
	Body ConversationDetailBody
}

type EventBody struct {
	Seq  int64  `json:"seq"`
	Kind string `json:"kind" enum:"user,assistant,turn_end,ready,failed,stopped,truncated,repo_state,interrupt_requested,interrupted,activity,unsupported,context_cleared,context_compacted"`
	Turn *int64 `json:"turn"`
	// Block says which model message an assistant event belongs to; null on
	// events from before it was reported, and on other kinds.
	Block *string `json:"block"`
	// Body is the text of a user or assistant event, the reason of a failed or
	// stopped, the repo state (a RepoStateBody as JSON) of a repo_state, the tool
	// call (an Activity as JSON) of an activity.
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func eventBody(e *Event) EventBody {
	return EventBody{Seq: e.Seq, Kind: string(e.Kind), Turn: e.Turn, Block: e.Block, Body: e.Body, CreatedAt: e.CreatedAt}
}

// -- The owner's routes --

type conductorPath struct {
	ID string `path:"id" doc:"The conductor"`
}

type StartConversationRequest struct {
	ID   string `path:"id" doc:"The conductor"`
	Body struct {
		Message  string `json:"message,omitempty" maxLength:"32768" doc:"The opening message"`
		ClientID string `json:"client_id,omitempty" format:"uuid" doc:"The browser's id for this start: repeated after a lost reply it returns the conversation the first made"`
	}
}

type ConversationResponse struct {
	Body ConversationBody
}

// StartConversation begins a conversation on the caller's remote control.
func (h *Handler) StartConversation(ctx context.Context, input *StartConversationRequest) (*ConversationResponse, error) {
	owned, err := h.authorize(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	clientID, err := parseClientID(input.Body.ClientID)
	if err != nil {
		return nil, err
	}
	req := &StartRequest{ClientID: clientID}
	if input.Body.Message != "" {
		req.Message = &input.Body.Message
	}
	c, err := h.svc.Start(ctx, owned, req)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationResponse{Body: conversationBody(c)}, nil
}

type ListConversationsRequest struct {
	ID    string `path:"id" doc:"The conductor"`
	Limit int    `query:"limit" minimum:"0" maximum:"100" doc:"at most this many, newest activity first; 0 for the default"`
}

type ListConversationsResponse struct {
	Body struct {
		Conversations []ConversationBody `json:"conversations"`
	}
}

func (h *Handler) ListConversations(ctx context.Context, input *ListConversationsRequest) (*ListConversationsResponse, error) {
	owned, err := h.authorize(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, owned, input.Limit)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListConversationsResponse{}
	resp.Body.Conversations = make([]ConversationBody, len(list))
	for i := range list {
		resp.Body.Conversations[i] = conversationBody(&list[i])
	}
	return resp, nil
}

// authorizeConversation proves ownership of the conductor and names the
// conversation: the first steps of every route on one conversation.
func (h *Handler) authorizeConversation(ctx context.Context, conductorID, conversationID string) (*agent.OwnedConductor, uuid.UUID, error) {
	owned, err := h.authorize(ctx, conductorID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := parseID(conversationID, "conversation id")
	if err != nil {
		return nil, uuid.Nil, err
	}
	return owned, id, nil
}

type ConversationRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
}

func (h *Handler) GetConversation(ctx context.Context, input *ConversationRequest) (*ConversationDetailResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Describe(ctx, owned, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationDetailResponse{Body: ConversationDetailBody{
		ConversationBody: conversationBody(d.Conversation), HostOnline: d.HostOnline, HostCapabilities: d.HostCapabilities, HostVersion: d.HostVersion, Repo: repoStateBody(d.Conversation.Repo), ScopeOverlaps: heldScopeOverlapBodies(d.ScopeOverlaps),
	}}, nil
}

type RenameConversationRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
	Body           struct {
		Title *string `json:"title" required:"true" nullable:"true" doc:"At most 100 characters, no control characters; null or blank clears it"`
	}
}

// RenameConversation sets or clears the owner's name for a conversation.
func (h *Handler) RenameConversation(ctx context.Context, input *RenameConversationRequest) (*ConversationResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Rename(ctx, owned, id, input.Body.Title)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationResponse{Body: conversationBody(c)}, nil
}

func (h *Handler) DisconnectConversation(ctx context.Context, input *ConversationRequest) (*ConversationResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Disconnect(ctx, owned, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationResponse{Body: conversationBody(c)}, nil
}

type InterruptConversationRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
	Body           struct {
		Turn int64 `json:"turn" minimum:"1" doc:"The turn to stop, as the transcript numbers it; a repeat for the same turn changes nothing"`
	}
}

// InterruptConversation stops the turn in flight and keeps the process.
func (h *Handler) InterruptConversation(ctx context.Context, input *InterruptConversationRequest) (*ConversationResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Interrupt(ctx, owned, id, input.Body.Turn)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationResponse{Body: conversationBody(c)}, nil
}

type ContextActionRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
	Body           struct {
		Interrupt bool `json:"interrupt,omitempty" doc:"stop a running reply first, then act; without it a running reply is refused (409)"`
	}
}

// ClearConversation asks the host to clear the conversation's context.
func (h *Handler) ClearConversation(ctx context.Context, input *ContextActionRequest) (*ConversationResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Clear(ctx, owned, id, input.Body.Interrupt)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationResponse{Body: conversationBody(c)}, nil
}

// CompactConversation asks the host to compact the conversation's context.
func (h *Handler) CompactConversation(ctx context.Context, input *ContextActionRequest) (*ConversationResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Compact(ctx, owned, id, input.Body.Interrupt)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConversationResponse{Body: conversationBody(c)}, nil
}

// DeleteConversation ends the conversation if it is live and deletes it.
func (h *Handler) DeleteConversation(ctx context.Context, input *ConversationRequest) (*struct{}, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, owned, id); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}

type SendMessageRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
	Body           struct {
		Text          string   `json:"text" maxLength:"32768" doc:"Required unless attachments are given"`
		AttachmentIDs []string `json:"attachment_ids,omitempty" maxItems:"8" doc:"Files the owner staged in this conversation, still unfiled: upload them with POST /api/conversations/{conversation_id}/attachments first"`
		ClientID      string   `json:"client_id,omitempty" format:"uuid" doc:"The browser's id for this send: sent again after a lost reply it is stored once"`
	}
}

type EventResponse struct {
	Body EventBody
}

func (h *Handler) SendMessage(ctx context.Context, input *SendMessageRequest) (*EventResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	clientID, err := parseClientID(input.Body.ClientID)
	if err != nil {
		return nil, err
	}
	attachments := make([]uuid.UUID, len(input.Body.AttachmentIDs))
	for i, raw := range input.Body.AttachmentIDs {
		if attachments[i], err = parseID(raw, "attachment_ids"); err != nil {
			return nil, err
		}
	}
	e, err := h.svc.Send(ctx, owned, id, &SendRequest{Text: input.Body.Text, ClientID: clientID, AttachmentIDs: attachments})
	if err != nil {
		return nil, h.mapError(err)
	}
	return &EventResponse{Body: eventBody(e)}, nil
}

type ListEventsRequest struct {
	ID             string `path:"id" doc:"The conductor"`
	ConversationID string `path:"conversation_id"`
	After          int64  `query:"after" minimum:"0" doc:"events after this seq; 0 for all"`
	Limit          int64  `query:"limit" minimum:"0" maximum:"500" doc:"at most this many; 0 for the maximum"`
	Before         int64  `query:"before" minimum:"0" doc:"read backwards by turns: events before this seq; 0 or absent for after the newest, which needs turns to select a backward read. Refused with after"`
	Turns          int64  `query:"turns" minimum:"0" maximum:"50" doc:"with before: how many of the most recent turns; 0 for one. A turn is a user event and what follows it, never split"`
}

type ListEventsResponse struct {
	Body struct {
		Events     []EventBody `json:"events"`
		HasMore    *bool       `json:"has_more,omitempty" doc:"backward reads: older events exist"`
		NextBefore *int64      `json:"next_before,omitempty" doc:"backward reads: the before to ask with next"`
		Prompts    []string    `json:"prompts,omitempty" doc:"the first backward read (no before): the owner's last 10 distinct non-blank messages, newest last, for the composer's history"`
	}
}

// ListEvents is the conversation's canonical transcript, for a page that
// loads it before streaming from its last seq.
func (h *Handler) ListEvents(ctx context.Context, input *ListEventsRequest) (*ListEventsResponse, error) {
	owned, id, err := h.authorizeConversation(ctx, input.ID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	if input.Before > 0 || input.Turns > 0 {
		return h.listEventsByTurns(ctx, input, owned, id)
	}
	events, err := h.svc.Events(ctx, owned, id, input.After, int(input.Limit))
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListEventsResponse{}
	resp.Body.Events = make([]EventBody, len(events))
	for i := range events {
		resp.Body.Events[i] = eventBody(&events[i])
	}
	return resp, nil
}

func (h *Handler) listEventsByTurns(ctx context.Context, input *ListEventsRequest, owned *agent.OwnedConductor, id uuid.UUID) (*ListEventsResponse, error) {
	if input.After != 0 || input.Limit != 0 {
		return nil, huma.Error422UnprocessableEntity("before and turns cannot be combined with after or limit")
	}
	page, err := h.svc.EventsByTurns(ctx, owned, id, input.Before, int(input.Turns))
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListEventsResponse{}
	resp.Body.Events = make([]EventBody, len(page.Events))
	for i := range page.Events {
		resp.Body.Events[i] = eventBody(&page.Events[i])
	}
	resp.Body.HasMore, resp.Body.NextBefore, resp.Body.Prompts = &page.HasMore, &page.NextBefore, page.Prompts
	return resp, nil
}
