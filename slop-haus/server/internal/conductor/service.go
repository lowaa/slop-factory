package conductor

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

const (
	// DefaultPollInterval is how often a waiting poll looks at the queue, and
	// so how late a message reaches a host that is waiting: one read-only
	// query per waiting conductor per interval. LISTEN/NOTIFY would tighten
	// it, at a connection pinned per replica; a second's lag is not felt in a
	// chat.
	DefaultPollInterval = time.Second
	// DefaultMaxWait bounds how long one poll is held open. Far shorter than
	// the half hour an agent's wait for work is held: the host's request crosses the frontend's
	// nginx and whatever fronts it, and a deploy's shutdown answers a poll at
	// once, so there is nothing to gain from holding it longer.
	DefaultMaxWait = 30 * time.Second
	// DefaultHostTTL is how long a host may be silent before another process
	// is let take the conductor over. Several polls long: a poll that fails
	// or a deploy in between must not hand the conductor to a second machine.
	DefaultHostTTL = 3 * DefaultMaxWait
)

// LayoutSaver stores the workspace layout an agent reports:
// agent.LayoutStore.
type LayoutSaver interface {
	SaveLayout(ctx context.Context, agentID uuid.UUID, layout *agent.Layout) error
}

// Scopes says which scopes agents hold that some changed paths lie in:
// *ticket.Service.
type Scopes interface {
	HeldScopeOverlaps(ctx context.Context, changed []string) ([]ticket.HeldScopeOverlap, error)
}

type Options struct {
	Store Store
	// Layouts is optional: without it a host's workspace layout report is
	// accepted and dropped.
	Layouts LayoutSaver
	// Scopes is optional: without it a conversation shows no overlaps.
	Scopes Scopes
	// Zero fields take the defaults above.
	PollInterval time.Duration
	MaxWait      time.Duration
	HostTTL      time.Duration
	// CurrentHostVersion is the build id of the host package this server ships,
	// which a host's reported version is compared with; empty when it ships none.
	CurrentHostVersion string
	// Clock defaults to the real one; tests drive it by hand.
	Clock wakeup.Clock
}

// Service is what starting, using and serving a conversation mean.
//
// Its methods for a person take the *agent.OwnedConductor that
// ConductorService.AuthorizeOwner returns, so a caller cannot reach a
// conversation without the ownership check; its methods for the host take a
// *Host. TestEveryServiceMethodTakesAProof fails for a method that takes
// neither.
type Service struct {
	store   Store
	layouts LayoutSaver
	scopes  Scopes
	clock   wakeup.Clock
	poll    time.Duration
	maxWait time.Duration
	hostTTL time.Duration
	// hostBuild is the build id of the host package this server ships.
	hostBuild string
	logger    *slog.Logger

	closed    chan struct{}
	closeOnce sync.Once
}

func NewService(opts *Options, logHandler slog.Handler) *Service {
	s := &Service{
		store:     opts.Store,
		layouts:   opts.Layouts,
		scopes:    opts.Scopes,
		clock:     opts.Clock,
		poll:      opts.PollInterval,
		maxWait:   opts.MaxWait,
		hostTTL:   opts.HostTTL,
		hostBuild: opts.CurrentHostVersion,
		logger:    slog.New(logHandler),
		closed:    make(chan struct{}),
	}
	if s.clock == nil {
		s.clock = wakeup.RealClock{}
	}
	if s.poll <= 0 {
		s.poll = DefaultPollInterval
	}
	if s.maxWait <= 0 {
		s.maxWait = DefaultMaxWait
	}
	if s.hostTTL <= 0 {
		s.hostTTL = DefaultHostTTL
	}
	return s
}

// Close answers every poll in progress, and every later one, with "restart":
// http.Server.Shutdown waits for in-flight requests, and a poll left to run
// would hold a deploy up until the shutdown deadline cut it off.
func (s *Service) Close() {
	s.closeOnce.Do(func() { close(s.closed) })
}

// MaxWait is the longest a poll is held open, which the route lifts the
// server's write deadline for.
func (s *Service) MaxWait() time.Duration { return s.maxWait }

// Closed is closed once the service has been closed, for a stream to end on.
func (s *Service) Closed() <-chan struct{} { return s.closed }

// -- The owner's side --

// StartRequest is a conversation to start.
type StartRequest struct {
	// Message is the owner's opening words, optional.
	Message *string
	// ClientID is the browser's id for this start: one repeated after a lost
	// reply returns the conversation the first made (whatever else it says),
	// rather than starting another or being refused as busy. It is also the
	// opening message's id, as SendRequest's is a message's.
	ClientID *uuid.UUID
}

// Start begins a conversation on the conductor and queues its start for the
// host, which attaches to the working copy as it is. A live conversation of the
// conductor is ended: the host runs one process at a time.
func (s *Service) Start(ctx context.Context, owned *agent.OwnedConductor, req *StartRequest) (*Conversation, error) {
	if req.Message != nil {
		if err := validateMessage(*req.Message); err != nil {
			return nil, err
		}
	}
	if req.ClientID != nil {
		if c, err := s.store.StartedWith(ctx, owned.Agent().ID, *req.ClientID); err != nil || c != nil {
			return c, err
		}
	}
	c, err := s.store.StartConversation(ctx, &StartParams{
		ID:           uuid.New(),
		ConductorID:  owned.Agent().ID,
		OwnerUserID:  owned.Owner(),
		FirstMessage: req.Message,
		ClientID:     req.ClientID,
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "conversation_started", "user_id", owned.Owner(),
		"agent", owned.Agent().ID, "conversation", c.ID)
	return c, nil
}

// SendRequest is a message to the model.
type SendRequest struct {
	Text string
	// ClientID is the browser's id for this send: a send repeated after a
	// lost reply is stored and queued once.
	ClientID *uuid.UUID
	// AttachmentIDs are files the owner staged in this conversation, at most
	// MaxMessageAttachments. A message with some may have no text.
	AttachmentIDs []uuid.UUID
}

// Send adds the owner's message to the conversation and queues it. One with no
// process is reconnected first, resuming its harness session.
func (s *Service) Send(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, req *SendRequest) (*Event, error) {
	if len(req.AttachmentIDs) == 0 || req.Text != "" {
		if err := validateMessage(req.Text); err != nil {
			return nil, err
		}
	}
	if err := validateAttachmentIDs(req.AttachmentIDs); err != nil {
		return nil, err
	}
	return s.store.AppendUserMessage(ctx, owned.Agent().ID, id, req.Text, req.ClientID, req.AttachmentIDs)
}

// Detail is a conversation with what a person looking at it needs and the
// stored row does not hold: whether its host is there, and who else is working
// in the paths it changed.
type Detail struct {
	*Conversation
	// HostOnline is whether a host has polled within the host TTL, the window
	// in which no other host may take the conductor over.
	HostOnline bool
	// HostCapabilities and HostVersion are what that host said of itself.
	HostCapabilities []string
	HostVersion      string
	// ScopeOverlaps are the scopes agents hold that the paths a live
	// conversation has changed lie in: a notice, since nothing can stop the
	// owner's own session.
	ScopeOverlaps []ticket.HeldScopeOverlap
}

// Describe reads a conversation with its Detail.
func (s *Service) Describe(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) (*Detail, error) {
	c, err := s.store.GetConversation(ctx, owned.Agent().ID, id)
	if err != nil {
		return nil, err
	}
	status, err := s.store.HostStatus(ctx, owned.Agent().ID, s.hostTTL)
	if err != nil {
		return nil, err
	}
	d := &Detail{Conversation: c, HostOnline: status.Online, HostCapabilities: nonNil(status.Capabilities), HostVersion: status.Version}
	if c.State.Live() {
		d.ScopeOverlaps = s.scopeOverlaps(ctx, c)
	}
	return d, nil
}

func (s *Service) scopeOverlaps(ctx context.Context, c *Conversation) []ticket.HeldScopeOverlap {
	if s.scopes == nil || c.Repo == nil || len(c.Repo.ChangedPaths) == 0 {
		return nil
	}
	overlaps, err := s.scopes.HeldScopeOverlaps(ctx, c.Repo.ChangedPaths)
	if err != nil {
		// A notice that cannot be worked out must not stop the conversation showing.
		s.logger.WarnContext(ctx, "conversation scope overlaps unavailable", "conversation", c.ID, "err", err)
		return nil
	}
	return overlaps
}

// HostInfo is the status of the conductor's host, for the Remote Control
// page's list of machines.
func (s *Service) HostInfo(ctx context.Context, owned *agent.OwnedConductor) (HostStatus, error) {
	status, err := s.store.HostStatus(ctx, owned.Agent().ID, s.hostTTL)
	if err != nil {
		return HostStatus{}, err
	}
	status.CurrentVersion = s.hostBuild
	return status, nil
}

// Disconnect ends the conversation's harness process, a turn in progress
// included, and keeps the session: the next message reconnects it. One with no
// process is returned as it is.
func (s *Service) Disconnect(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) (*Conversation, error) {
	c, err := s.store.DisconnectConversation(ctx, owned.Agent().ID, id, "disconnected by its owner")
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "conversation_disconnected", "user_id", owned.Owner(),
		"agent", owned.Agent().ID, "conversation", id)
	return c, nil
}

// Clear asks the host to clear the conversation's context, keeping the
// conversation and its transcript. A turn running is ErrTurnRunning unless
// interrupt, which stops it first.
func (s *Service) Clear(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, interrupt bool) (*Conversation, error) {
	return s.contextAction(ctx, owned, id, WorkClear, interrupt, "conversation_cleared")
}

// Compact asks the host to compact the conversation's context. As Clear, and
// ErrCannotCompact when the host has not said the harness can.
func (s *Service) Compact(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, interrupt bool) (*Conversation, error) {
	return s.contextAction(ctx, owned, id, WorkCompact, interrupt, "conversation_compacted")
}

func (s *Service) contextAction(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, kind WorkKind, interrupt bool, audit string) (*Conversation, error) {
	c, err := s.store.QueueContextAction(ctx, owned.Agent().ID, id, kind, interrupt)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", audit, "user_id", owned.Owner(),
		"agent", owned.Agent().ID, "conversation", id, "interrupt", interrupt)
	return c, nil
}

// Interrupt asks the host to end the turn in flight, keeping the process and
// the session, unlike Disconnect. It is idempotent per turn. A turn that is
// not running is ErrNotReplying; one whose host has not reported the interrupt
// capability is ErrHostLacksInterrupt, since the host would ignore the work.
func (s *Service) Interrupt(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, turn int64) (*Conversation, error) {
	if turn < 1 {
		return nil, invalid("turn starts at 1")
	}
	c, err := s.store.InterruptTurn(ctx, owned.Agent().ID, id, turn)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "conversation_interrupted", "user_id", owned.Owner(),
		"agent", owned.Agent().ID, "conversation", id, "turn", turn)
	return c, nil
}

// Delete removes the conversation and its transcript. A live one is ended
// first; while the host has work for it it has not acknowledged (the stop
// just queued included) it is ErrWorkPending, and the owner deletes again.
func (s *Service) Delete(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) error {
	if err := s.store.DeleteConversation(ctx, owned.Agent().ID, id, "deleted by its owner"); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "audit", "action", "conversation_deleted", "user_id", owned.Owner(),
		"agent", owned.Agent().ID, "conversation", id)
	return nil
}

// Rename sets the owner's name for a conversation, nil or blank to clear it.
// Any state may be renamed. It is the owner's own label on their own session,
// so it is not audit-logged.
func (s *Service) Rename(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, title *string) (*Conversation, error) {
	clean, err := cleanTitle(title)
	if err != nil {
		return nil, err
	}
	return s.store.SetTitle(ctx, owned.Agent().ID, id, clean)
}

func (s *Service) Get(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID) (*Conversation, error) {
	return s.store.GetConversation(ctx, owned.Agent().ID, id)
}

// List is the conductor's conversations, most recently active first.
func (s *Service) List(ctx context.Context, owned *agent.OwnedConductor, limit int) ([]Conversation, error) {
	return s.store.ListConversations(ctx, owned.Agent().ID, clamp(limit, 1, 100, 50))
}

// Events is the conversation's transcript after seq, oldest first.
func (s *Service) Events(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, after int64, limit int) ([]Event, error) {
	return s.store.Events(ctx, owned.Agent().ID, id, max(after, 0), clamp(limit, 1, MaxEventsPerRead, MaxEventsPerRead))
}

// EventsByTurns is the transcript's most recent turns before seq (0 for the
// end), oldest first. A turn starts at a user event and runs to the next, and
// the events before the first user event belong to the first turn.
func (s *Service) EventsByTurns(ctx context.Context, owned *agent.OwnedConductor, id uuid.UUID, before int64, turns int) (*TranscriptPage, error) {
	if before < 0 {
		return nil, invalid("before must not be negative")
	}
	return s.store.EventsByTurns(ctx, owned.Agent().ID, id, before, clamp(turns, 1, MaxTurnsPerPage, 1))
}

func clamp(n, lo, hi, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return min(max(n, lo), hi)
}

func validateText(s string) error {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return invalid("text must be UTF-8 without NUL")
	}
	return nil
}

func validateMessage(text string) error {
	if strings.TrimSpace(text) == "" {
		return invalid("a message must not be empty")
	}
	if len(text) > MaxMessageBytes {
		return invalid("a message is at most %d bytes", MaxMessageBytes)
	}
	return validateText(text)
}

// -- The host's side --

// Host is proof that the caller's own credential is a conductor's. Only
// AuthenticateHost makes one; the conversations it can reach are the
// conductor's own, whatever the request names.
type Host struct {
	conductorID uuid.UUID
}

func (h *Host) ConductorID() uuid.UUID { return h.conductorID }

// AuthenticateHost checks that the request was made with a conductor's
// credential. The middleware has already refused a token that is unknown,
// revoked, read-only or an agent that is disabled.
func (s *Service) AuthenticateHost(ctx context.Context) (*Host, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	kind, ok := caller.(interface{ IsConductor() bool })
	if !ok || !kind.IsConductor() {
		return nil, ErrNotConductor
	}
	return &Host{conductorID: caller.AgentID()}, nil
}

// PollRequest is a host asking for work.
type PollRequest struct {
	// HostID names the host process, chosen by it at startup.
	HostID uuid.UUID
	// Ack is the highest item number the host has handled; they are not
	// delivered again.
	Ack int64
	// Wait is how long to hold the poll open when there is nothing, at most
	// the service's maximum.
	Wait time.Duration
	// Capabilities and Version are what the host says of itself, sent with
	// every poll; a host on older code sends neither.
	Capabilities []string
	Version      string
}

// PollReason is why a poll returned.
type PollReason string

const (
	PollWork    PollReason = "work"
	PollTimeout PollReason = "timeout"
	// PollRestart: the server is shutting down for a deploy; poll again.
	PollRestart PollReason = "restart"
)

type PollResult struct {
	Reason PollReason
	Items  []WorkItem
	// Acked is the highest number acknowledged so far.
	Acked int64
}

// Poll hands the host the conductor's unacknowledged work in order, waiting
// up to req.Wait for some.
//
// Delivery is at least once: items stay until a later poll acknowledges
// them, so a host that crashes between receiving and handling is given them
// again, and one whose reply was lost likewise. The host handles items in
// order and remembers the highest number it has handled; an item it has
// handled already is skipped by number.
//
// The wait polls Postgres rather than listening in this process, so a
// message sent through another replica reaches this poll.
func (s *Service) Poll(ctx context.Context, host *Host, req *PollRequest) (*PollResult, error) {
	if req.HostID == uuid.Nil {
		return nil, invalid("host_id is required")
	}
	if err := validateHostSelfReport(req); err != nil {
		return nil, err
	}
	acked, err := s.store.ClaimHost(ctx, host.conductorID, req.HostID, max(req.Ack, 0), s.hostTTL, HostReport{Capabilities: nonNil(req.Capabilities), Version: req.Version})
	if err != nil {
		return nil, err
	}
	result := &PollResult{Reason: PollTimeout, Acked: acked}
	outcome, err := wakeup.Poll(ctx, s.clock, s.poll, min(max(req.Wait, 0), s.maxWait), s.closed,
		func(ctx context.Context) (bool, error) {
			items, err := s.store.ReadWork(ctx, host.conductorID, req.HostID, MaxWorkPerPoll)
			if err != nil {
				return false, err
			}
			result.Items = items
			return len(items) > 0, nil
		})
	if err != nil {
		return nil, err
	}
	switch outcome {
	case wakeup.Found:
		result.Reason = PollWork
	case wakeup.Closed:
		// What a check found as the server closed is not handed over: the
		// reply may not reach the host, and the work is still queued.
		result.Reason, result.Items = PollRestart, nil
	}
	return result, nil
}

func validateHostSelfReport(req *PollRequest) error {
	if len(req.Capabilities) > MaxHostCapabilities || len(req.Version) > MaxHostVersionBytes {
		return invalid("at most %d capabilities, and a version of at most %d bytes", MaxHostCapabilities, MaxHostVersionBytes)
	}
	for _, c := range req.Capabilities {
		if c == "" || len(c) > MaxHostCapabilityBytes {
			return invalid("a capability is 1 to %d bytes", MaxHostCapabilityBytes)
		}
		if err := validateText(c); err != nil {
			return err
		}
	}
	return validateText(req.Version)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Release is a host that is exiting giving up the conductor, so its next
// process is not refused until the ttl passes. A host that was already taken
// over, or released, changes nothing: it is not an error to say goodbye twice.
func (s *Service) Release(ctx context.Context, host *Host, hostID uuid.UUID) error {
	if hostID == uuid.Nil {
		return invalid("host_id is required")
	}
	return s.store.ReleaseHost(ctx, host.conductorID, hostID)
}

// ReportLayout stores what the host's workspace looks like on disk, for the
// Workspace page to compare with the manifest. The layout is self-asserted,
// like a hook's.
func (s *Service) ReportLayout(ctx context.Context, host *Host, layout *agent.Layout) error {
	if s.layouts == nil {
		return nil
	}
	return s.layouts.SaveLayout(ctx, host.conductorID, layout)
}

// ReportRequest is what the host says happened in one conversation.
type ReportRequest struct {
	HostID uuid.UUID
	Events []HostEvent
}

// Report stores what the host reports, once and in order: see
// Store.ApplyHostEvents. It returns the highest number the server has, which
// a host that was not sure its last report arrived resumes after.
func (s *Service) Report(ctx context.Context, host *Host, id uuid.UUID, req *ReportRequest) (int64, error) {
	if req.HostID == uuid.Nil {
		return 0, invalid("host_id is required")
	}
	if len(req.Events) > MaxEventsPerBatch {
		return 0, invalid("at most %d events per report", MaxEventsPerBatch)
	}
	for i := range req.Events {
		if err := validateHostEvent(&req.Events[i]); err != nil {
			return 0, err
		}
	}
	return s.store.ApplyHostEvents(ctx, host.conductorID, req.HostID, id, req.Events)
}

func validateHostEvent(e *HostEvent) error {
	if e.HostSeq < 1 {
		return invalid("host_seq starts at 1")
	}
	if err := validateText(e.Body); err != nil {
		return err
	}
	switch e.Kind {
	case EventAssistant:
		if e.Body == "" || len(e.Body) > MaxDeltaBytes {
			return invalid("assistant text is 1 to %d bytes", MaxDeltaBytes)
		}
		if len(e.Block) > MaxBlockBytes {
			return invalid("a block id is at most %d bytes", MaxBlockBytes)
		}
	case EventActivity:
		if e.Activity == nil {
			return invalid("activity needs activity")
		}
		return e.Activity.Validate()
	case EventTurnEnd:
		if e.Body != "" {
			return invalid("a turn_end carries no text")
		}
		if e.Interrupted && (e.Turn == nil || *e.Turn < 1) {
			return invalid("an interrupted turn_end needs its turn, from 1")
		}
	case EventContext:
		if e.ContextTokens == nil && e.CanCompact == nil {
			return invalid("context needs context_tokens or can_compact")
		}
		if e.ContextTokens != nil && *e.ContextTokens < 0 {
			return invalid("context_tokens must not be negative")
		}
	case EventContextCleared, EventContextCompacted:
		if e.Body != "" {
			return invalid("a %s carries no text", e.Kind)
		}
	case EventUnsupported:
		if e.Body == "" || len(e.Body) > MaxHostCapabilityBytes {
			return invalid("an unsupported needs the work kind, of at most %d bytes", MaxHostCapabilityBytes)
		}
	case EventRepoState:
		if e.Repo == nil {
			return invalid("repo_state needs repo")
		}
		if e.WorkSeq < 1 {
			return invalid("repo_state needs work_seq, the start the session is on")
		}
		return e.Repo.Validate()
	case EventReady:
		if e.Body != "" {
			return invalid("a ready carries no text")
		}
		if e.WorkSeq < 1 {
			return invalid("ready needs work_seq, the start it answers")
		}
	case EventFailed:
		if e.Body == "" || len(e.Body) > MaxFailureBytes {
			return invalid("a failure needs a reason of at most %d bytes", MaxFailureBytes)
		}
		if e.WorkSeq < 1 {
			return invalid("failed needs work_seq, the start it answers")
		}
	default:
		return invalid("event kind %q", e.Kind)
	}
	if e.Interrupted && e.Kind != EventTurnEnd {
		return invalid("only a turn_end can be interrupted")
	}
	return nil
}
