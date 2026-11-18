package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

// longPollWriteSlack is the time a poll's response gets to be written once
// the poll itself has run out.
const longPollWriteSlack = 10 * time.Second

// States is Service as the handler uses it.
type States interface {
	State(ctx context.Context, tenant, agentID uuid.UUID) (*State, error)
}

// Poller is the wakeup.Waiter as the handler uses it.
type Poller interface {
	WorkerPoll(ctx context.Context, agentID uuid.UUID, req *wakeup.WorkerPollRequest) (*wakeup.WorkerPoll, error)
	SupervisorPoll(ctx context.Context, supervisorID uuid.UUID, req *wakeup.WorkerPollRequest) (*wakeup.WorkerPoll, error)
	WorkerPollTimeout() time.Duration
}

// SlotReporter stores what a supervisor says about one of its slots.
type SlotReporter interface {
	ReportSlot(ctx context.Context, agentID uuid.UUID, r *supervisorwatch.SlotReport) error
}

// LayoutSaver stores the workspace layout an agent reports.
type LayoutSaver interface {
	SaveLayout(ctx context.Context, agentID uuid.UUID, layout *agent.Layout) error
}

type Handler struct {
	states  States
	poller  Poller
	slots   SlotReporter
	layouts LayoutSaver
	logger  *slog.Logger
}

func NewHandler(states States, poller Poller, slots SlotReporter, layouts LayoutSaver, logHandler slog.Handler) *Handler {
	return &Handler{states: states, poller: poller, slots: slots, layouts: layouts, logger: slog.New(logHandler)}
}

// RegisterRoutes mounts the supervisor's routes. Every one acts as the
// authenticated agent, and as that agent only.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "getWorkerState",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodGet,
		Path:        "/api/worker/state",
		Summary:     "Where the caller stands: its lease, its ticket and attempt, its place at the integration lock and its change's checks",
		Description: "A read: it extends no lease, takes no revocation notice and joins no queue. After a lease has ended, " +
			"ticket and attempt are still those of the latest lease and ended says how it ended, which is how a supervisor learns " +
			"what a run that has exited left behind. checks is null with no lease or no change open; its state is unknown " +
			"when the code host could not be asked. review_request is the caller's open request for a review, which its supervisor runs.",
	}, h.GetState)
	huma.Register(api, huma.Operation{
		OperationID: "pollWorker",
		// A supervisor polls once for all of its slots; it is the one thing
		// besides its own routes that it is let do.
		Metadata: authn.RequireAgentOrSupervisor(),
		Method:   http.MethodPost,
		Path:     "/api/worker/poll",
		Summary:  "Hold open until the caller has something to do, or the poll runs out",
		Description: "Events: claimable (work to claim, a ticket owed to the caller by resume affinity included), disabled (ticket " +
			"processing is off), messages (a person wrote to a caller that holds a ticket), revoked, finished or lease_lost (the lease " +
			"named by lease_id ended), offered (the integration lock is free and the caller is first in its queue) and restart (version " +
			"is not the one the server expects: finish up and exit). No events means the poll ran out, or the server is shutting down: " +
			"poll again. Each poll is recorded as the caller's supervisor being alive. A supervisor agent polls for all of its slots " +
			"at once: slot_leases names the lease each is working, and every event carries the slot_id it is about.",
		Middlewares: huma.Middlewares{h.allowLongPoll},
	}, h.Poll)
	huma.Register(api, huma.Operation{
		OperationID:   "reportWorkerSlot",
		Metadata:      authn.RequireAgent(),
		Method:        http.MethodPost,
		Path:          "/api/worker/slot-state",
		Summary:       "Say what the caller's supervisor slot is doing",
		DefaultStatus: http.StatusNoContent,
		Description: "Replaces the caller's last report. phase_age_seconds is how long the slot has been in the phase, by its own " +
			"clock: the server keeps when the phase began, and moves it only when the phase or the ticket changes. error_class is a " +
			"short lower-case token for the error the slot keeps hitting, never an error message; error_streak is how many steps in " +
			"a row ended in it.",
	}, h.ReportSlot)
}

// allowLongPoll lifts the server-wide write timeout for a poll, which is held
// open on purpose. The read deadline is left alone.
func (h *Handler) allowLongPoll(ctx huma.Context, next func(huma.Context)) {
	if _, w := humago.Unwrap(ctx); w != nil {
		// Only a writer that does not support deadlines fails here, and the
		// poll then still works, with the server's shorter write deadline.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.poller.WorkerPollTimeout() + longPollWriteSlack))
	}
	next(ctx)
}

func callerAgent(ctx context.Context) (authn.Identity, error) {
	found, err := authn.FromContext(ctx)
	if err != nil {
		// A wiring bug, not an auth failure: the middleware has rejected an
		// unauthenticated caller before the handler runs.
		return nil, huma.Error500InternalServerError("agent route is missing its auth middleware")
	}
	return found, nil
}

// -- State --

type WorkerStateRequest struct{}

type WorkerLeaseBody struct {
	ID        int64     `json:"id"`
	TicketID  int64     `json:"ticket_id"`
	Attempt   int       `json:"attempt" doc:"the attempt the lease is on"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WorkerTicketBody struct {
	ID     int64         `json:"id"`
	Status ticket.Status `json:"status" doc:"the ticket's status now, not as the lease left it"`
	Paths  []string      `json:"paths" doc:"the ticket's path scopes now; a supervisor starts the run in the repo they name"`
}

type WorkerAttemptBody struct {
	Number           int          `json:"number"`
	Status           string       `json:"status"`
	Promoted         bool         `json:"promoted"`
	Harness          harness.Name `json:"harness,omitempty"`
	HarnessSessionID string       `json:"harness_session_id,omitempty" doc:"that run's session, to resume it with"`
}

type WorkerEndedBody struct {
	Kind   string `json:"kind" enum:"revoked,finished,lapsed" doc:"revoked: a person took the ticket back; finished: the holder gave it up; lapsed: the lease ran out"`
	Status string `json:"status,omitempty" doc:"where a person moved the ticket, for revoked"`
	Exit   string `json:"exit,omitempty" enum:"done,review,discussion,preparing,released" doc:"how the holder left, for finished"`
}

type WorkerLockBody struct {
	Holds    bool `json:"holds" doc:"whether the caller holds the integration lock"`
	Position int  `json:"position" doc:"the caller's place in the lock queue, counting from 1; 0 when not queued"`
	Offered  bool `json:"offered" doc:"the lock is free and the caller is first in line for it"`
}

type WorkerChecksBody struct {
	URL     string `json:"url,omitempty"`
	HeadSHA string `json:"head_sha,omitempty" doc:"the commit the checks are for"`
	State   string `json:"state" enum:"success,failure,pending,none,unknown" doc:"as change_checks says; none is not a pass; unknown means the code host could not be asked"`
	Passed  bool   `json:"passed"`
	Error   string `json:"error,omitempty"`
}

type WorkerReviewRequestBody struct {
	HeadSHA     string    `json:"head_sha" doc:"the full commit sha to review"`
	Intent      string    `json:"intent" doc:"what the change is meant to do (its PR description), for the reviewers"`
	RequestedAt time.Time `json:"requested_at"`
}

type WorkerStateResponseBody struct {
	Lease         *WorkerLeaseBody         `json:"lease" doc:"the lease the caller holds; null when none"`
	Ticket        *WorkerTicketBody        `json:"ticket" doc:"the ticket of the caller's latest lease, held or ended; null if it never held one"`
	Attempt       *WorkerAttemptBody       `json:"attempt" doc:"the attempt of that lease"`
	Ended         *WorkerEndedBody         `json:"ended" doc:"how that lease ended; null while held"`
	Lock          WorkerLockBody           `json:"lock"`
	Checks        *WorkerChecksBody        `json:"checks" doc:"the checks on the held attempt's change; null with no lease or no change"`
	ReviewRequest *WorkerReviewRequestBody `json:"review_request" doc:"the caller's open request (review_request) for a slop-review of the held attempt; null when none is open or no lease is held. It closes once the caller's supervisor has posted its slop-review pass of that attempt and head since the request, a failed run included. Posting the run is what closes it"`
}

type WorkerStateResponse struct {
	Body WorkerStateResponseBody
}

func (h *Handler) GetState(ctx context.Context, _ *WorkerStateRequest) (*WorkerStateResponse, error) {
	caller, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	state, err := h.states.State(ctx, caller.TenantID(), caller.AgentID())
	if err != nil {
		h.logger.Error("worker state", "agent_id", caller.AgentID(), "err", err)
		return nil, huma.Error500InternalServerError("could not read the worker's state")
	}
	return &WorkerStateResponse{Body: stateBody(state)}, nil
}

func stateBody(s *State) WorkerStateResponseBody {
	var out WorkerStateResponseBody
	if s.Lease != nil {
		out.Lease = &WorkerLeaseBody{ID: s.Lease.ID, TicketID: s.Lease.TicketID, Attempt: s.Lease.Attempt, ExpiresAt: s.Lease.ExpiresAt}
	}
	if s.Ticket != nil {
		out.Ticket = &WorkerTicketBody{ID: s.Ticket.ID, Status: s.Ticket.Status, Paths: s.Ticket.Paths}
		if out.Ticket.Paths == nil {
			out.Ticket.Paths = []string{}
		}
	}
	if s.Attempt != nil {
		out.Attempt = &WorkerAttemptBody{Number: s.Attempt.Number, Status: s.Attempt.Status, Promoted: s.Attempt.Promoted}
		if s.Attempt.Session != nil {
			out.Attempt.Harness, out.Attempt.HarnessSessionID = harness.Name(s.Attempt.Session.Harness), s.Attempt.Session.SessionID
		}
	}
	if s.Ended != nil {
		out.Ended = &WorkerEndedBody{Kind: string(s.Ended.Kind), Status: string(s.Ended.Status), Exit: string(s.Ended.Exit)}
	}
	out.Lock = WorkerLockBody{Holds: s.Lock.Holds, Position: s.Lock.Position, Offered: s.Lock.Offered}
	if s.Checks != nil {
		out.Checks = &WorkerChecksBody{URL: s.Checks.URL, HeadSHA: s.Checks.HeadSHA, State: s.Checks.State, Passed: s.Checks.Passed, Error: s.Checks.Error}
	}
	if r := s.ReviewRequest; r != nil {
		out.ReviewRequest = &WorkerReviewRequestBody{HeadSHA: r.HeadSHA, Intent: r.Intent, RequestedAt: r.RequestedAt}
	}
	return out
}

// -- Poll --

type WorkerPollBody struct {
	Version     string `json:"version,omitempty" doc:"the supervisor's own version; the poll answers restart if it is not the one the server expects"`
	Protocol    int    `json:"protocol,omitempty" minimum:"0" doc:"the supervisor protocol version it speaks; the poll answers protocol_unsupported if it is older than the server supports. Absent: the X-Supervisor-Version header is read instead, and with neither the supervisor is not refused"`
	LeaseID     int64  `json:"lease_id,omitempty" minimum:"0" doc:"the lease being worked, to hear when it ends"`
	WaitSeconds int    `json:"wait_seconds,omitempty" minimum:"0" doc:"how long to hold the poll open with nothing to report; capped by the server"`
	// SlotLeases is for a supervisor agent's poll, which has no lease of its own.
	SlotLeases map[string]int64 `json:"slot_leases,omitempty" doc:"for a supervisor's poll: the lease each slot (by agent id) is working, to hear when it ends; lease_id is for a worker's own"`
}

type WorkerPollRequest struct {
	SupervisorVersion string `header:"X-Supervisor-Version" doc:"the supervisor protocol version, an integer; sent on every supervisor request. A value that is not a whole number is ignored"`
	Body              WorkerPollBody
}

type WorkerEventBody struct {
	Kind     string `json:"kind" enum:"claimable,disabled,messages,revoked,finished,lease_lost,offered,restart,protocol_unsupported"`
	SlotID   string `json:"slot_id,omitempty" doc:"for a supervisor's poll, the slot the event is about"`
	TicketID int64  `json:"ticket_id,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty" doc:"for revoked, finished and lease_lost, the lease that ended: the one named in the request, which may not be the one the caller holds now"`
	Messages int    `json:"messages,omitempty" doc:"how many are pending, for messages"`
	Message  string `json:"message,omitempty" doc:"for protocol_unsupported, what the operator should do"`
	Ended    string `json:"ended,omitempty" doc:"for revoked, the status a person moved the ticket to; for finished, how the holder left"`
}

type WorkerPollResponseBody struct {
	Events []WorkerEventBody `json:"events"`
}

type WorkerPollResponse struct {
	Body WorkerPollResponseBody
}

func (h *Handler) Poll(ctx context.Context, in *WorkerPollRequest) (*WorkerPollResponse, error) {
	caller, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	req := &wakeup.WorkerPollRequest{
		Version:  in.Body.Version,
		Protocol: protocolOf(in.Body.Protocol, in.SupervisorVersion),
		LeaseID:  in.Body.LeaseID,
		Wait:     time.Duration(in.Body.WaitSeconds) * time.Second,
	}
	poll := h.poller.WorkerPoll
	if authn.IsSupervisor(caller) {
		poll = h.poller.SupervisorPoll
		if req.SlotLeases, err = slotLeases(in.Body.SlotLeases); err != nil {
			return nil, err
		}
	} else if len(in.Body.SlotLeases) > 0 {
		return nil, huma.Error422UnprocessableEntity("slot_leases is for a supervisor's poll")
	}
	polled, err := poll(ctx, caller.AgentID(), req)
	if err != nil {
		if ctx.Err() != nil {
			// The supervisor hung up; there is nobody to answer.
			return nil, huma.Error503ServiceUnavailable("poll cancelled")
		}
		h.logger.Error("worker poll", "agent_id", caller.AgentID(), "err", err)
		return nil, huma.Error500InternalServerError("could not poll")
	}
	out := &WorkerPollResponse{Body: WorkerPollResponseBody{Events: make([]WorkerEventBody, 0, len(polled.Events))}}
	for _, e := range polled.Events {
		event := WorkerEventBody{Kind: string(e.Reason), TicketID: e.TicketID, LeaseID: e.LeaseID, Messages: e.Messages, Ended: e.Ended, Message: e.Message}
		if e.SlotID != uuid.Nil {
			event.SlotID = e.SlotID.String()
		}
		out.Body.Events = append(out.Body.Events, event)
	}
	return out, nil
}

// protocolOf is the protocol the poll body names, or else the header's. A
// header that is not a whole number is ignored: refusing a poll over it would
// silence a supervisor whose real fault is only a malformed header.
func protocolOf(body int, header string) int {
	if body > 0 {
		return body
	}
	n, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func slotLeases(raw map[string]int64) (map[uuid.UUID]int64, error) {
	out := make(map[uuid.UUID]int64, len(raw))
	for slot, lease := range raw {
		id, err := uuid.Parse(slot)
		if err != nil || lease < 0 {
			return nil, huma.Error422UnprocessableEntity("slot_leases maps an agent id to a lease id")
		}
		out[id] = lease
	}
	return out, nil
}

// -- Slot state --

type WorkerSlotStateBody struct {
	Phase           supervisorwatch.Phase `json:"phase"`
	PhaseAgeSeconds float64               `json:"phase_age_seconds" minimum:"0" maximum:"31622400" doc:"how long the slot has been in the phase"`
	TicketID        *int64                `json:"ticket_id,omitempty" minimum:"1" doc:"the ticket the slot has, if any"`
	ErrorClass      string                `json:"error_class,omitempty" maxLength:"64" pattern:"^[a-z0-9_]*$" doc:"a short token for the error the slot keeps hitting; never a message"`
	ErrorStreak     int                   `json:"error_streak,omitempty" minimum:"0" maximum:"1000000"`
	PauseReason     string                `json:"pause_reason,omitempty" doc:"why a paused slot is paused; free text, cut to 512 characters and ignored unless the phase is paused"`
	Reason          string                `json:"reason,omitempty" maxLength:"64" doc:"why the slot is not running a ticket: no_claimable, fleet_paused, slot_paused, lock_queued, cooldown, backoff_after_error, waiting_ci, reviewing, updating, starting or other. A code the server does not know is stored as other"`
	ReasonDetail    string                `json:"reason_detail,omitempty" doc:"a short note beside the reason; free text, cut to 160 characters"`
	BundleHash      string                `json:"bundle_hash,omitempty" maxLength:"64" doc:"the hash of the managed asset bundle the slot's workspace last installed, read from .slop-haus/bundle-hash; hex, and dropped when it is not"`
	Idle            bool                  `json:"idle,omitempty" doc:"the slot has no live session, so it may be restarted or have its workspace synced; absent means not idle"`
	Layout          *WorkerLayout         `json:"layout,omitempty" doc:"what the slot's workspace looks like on disk, as the hooks of an interactive worker report it"`
}

// WorkerLayout is agent.Layout on the wire. It is sanitised again by
// agent.ParseLayout: a supervisor is self-asserting, like a hook script.
type WorkerLayout struct {
	Root  WorkerLayoutEntry   `json:"root"`
	Repos []WorkerLayoutEntry `json:"repos"`
}

type WorkerLayoutEntry struct {
	Path   string `json:"path" maxLength:"255"`
	Git    bool   `json:"git"`
	Origin string `json:"origin,omitempty" maxLength:"500"`
}

type WorkerSlotStateRequest struct {
	Body WorkerSlotStateBody
}

func (h *Handler) ReportSlot(ctx context.Context, in *WorkerSlotStateRequest) (*struct{}, error) {
	caller, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	b := &in.Body
	err = h.slots.ReportSlot(ctx, caller.AgentID(), &supervisorwatch.SlotReport{
		Phase:        b.Phase,
		PhaseAge:     time.Duration(b.PhaseAgeSeconds * float64(time.Second)),
		TicketID:     b.TicketID,
		ErrorClass:   b.ErrorClass,
		ErrorStreak:  b.ErrorStreak,
		PauseReason:  b.PauseReason,
		Reason:       b.Reason,
		ReasonDetail: b.ReasonDetail,
		BundleHash:   b.BundleHash,
		Idle:         b.Idle,
	})
	if errors.Is(err, supervisorwatch.ErrInvalidReport) {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	if err != nil {
		h.logger.Error("report slot state", "agent_id", caller.AgentID(), "err", err)
		return nil, huma.Error500InternalServerError("could not record the slot state")
	}
	h.recordLayout(ctx, caller.AgentID(), b.Layout)
	return nil, nil
}

// recordLayout is bookkeeping beside the slot state: a failure is logged and
// the report still succeeds, as for the hooks that report the same thing.
func (h *Handler) recordLayout(ctx context.Context, agentID uuid.UUID, in *WorkerLayout) {
	if in == nil || h.layouts == nil {
		return
	}
	raw, err := json.Marshal(map[string]any{"layout": in})
	if err != nil {
		return
	}
	layout := agent.ParseLayout(raw)
	if layout == nil {
		return
	}
	if err := h.layouts.SaveLayout(ctx, agentID, layout); err != nil {
		h.logger.Warn("record workspace layout", "agent_id", agentID, "err", err)
	}
}
