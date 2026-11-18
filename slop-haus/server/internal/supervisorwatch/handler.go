package supervisorwatch

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getSupervisorStatus",
		Method:      http.MethodGet,
		Path:        "/api/supervisor/status",
		Summary:     "Whether the supervisor that runs headless workers is polling",
		Description: "offline is true once a supervisor that has polled before has been quiet for five minutes. " +
			"A tenant that has never run one is not offline.",
	}, h.GetStatus)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireSupervisor(),
		OperationID: "getSupervisorView",
		Method:      http.MethodGet,
		Path:        "/api/supervisor/view",
		Summary:     "The board's view of the supervisors, for a supervisor's own credential",
		Description: "The same answer as /api/supervisor/status, served to a supervisor so `slop-haus status` can show what the board " +
			"sees: the last poll and each slot's phase, reason and ticket. Covers every slot of the tenant, not only the caller's.",
	}, h.GetStatus)
}

// SlotBody is one supervisor slot as its last report and the stall rule leave it.
type SlotBody struct {
	AgentID        uuid.UUID     `json:"agent_id" doc:"the slot's agent"`
	AgentName      string        `json:"agent_name" doc:"the slot's agent's name, as the supervisor logs it"`
	Phase          Phase         `json:"phase"`
	PhaseEnteredAt time.Time     `json:"phase_entered_at"`
	TicketID       *int64        `json:"ticket_id" doc:"the ticket the slot has; null when none"`
	ErrorClass     string        `json:"error_class" doc:"a short token naming the error the slot keeps hitting; empty when none"`
	ErrorStreak    int           `json:"error_streak"`
	ReportedAt     time.Time     `json:"reported_at"`
	Offline        bool          `json:"offline" doc:"the slot's report is older than the offline bound: the supervisor is gone, which is not a stall"`
	PauseReason    string        `json:"pause_reason" maxLength:"512" doc:"why the slot is paused, in the supervisor's words; free text, show it as text only. Empty unless the phase is paused"`
	Reason         IdleReason    `json:"reason" enum:"no_claimable,fleet_paused,slot_paused,lock_queued,cooldown,backoff_after_error,waiting_ci,reviewing,updating,starting,other,unknown" doc:"why the slot is not running a ticket, as its supervisor reports it; unknown from a supervisor that does not say. Meaningless while the phase is running"`
	ReasonDetail   string        `json:"reason_detail" maxLength:"160" doc:"a short note beside the reason, in the supervisor's words; free text, show it as text only"`
	BundleHash     string        `json:"bundle_hash" maxLength:"64" doc:"the managed asset bundle hash the slot's workspace last installed; empty when it never said"`
	Idle           bool          `json:"idle,omitempty" doc:"the slot has no live session, so it may be restarted or synced; absent means not idle, including from a supervisor that does not say"`
	BundleStatus   VersionStatus `json:"bundle_status" enum:"current,stale,unknown" doc:"stale: the workspace has not updated since the bundle changed; unknown: it never said"`
	PausedAt       *time.Time    `json:"paused_at" doc:"when the pause began; null unless the slot is paused"`
	PausedLong     bool          `json:"paused_long" doc:"the slot has been paused longer than the bound after which the board says so"`
	Stalled        bool          `json:"stalled"`
	StallReason    string        `json:"stall_reason" enum:"idle_with_work,no_heartbeat,repeating_error,held_without_run," doc:"why the slot is stalled; empty when it is not"`
}

// PolledSupervisorBody is a supervisor's last poll.
type PolledSupervisorBody struct {
	AgentID        uuid.UUID     `json:"agent_id"`
	AgentName      string        `json:"agent_name"`
	Version        string        `json:"version" doc:"the supervisor's own package version, as it last polled; empty when it sent none"`
	Protocol       int           `json:"protocol" minimum:"0" doc:"the protocol version it last polled with; 0 when it sent none"`
	ProtocolStatus VersionStatus `json:"protocol_status" enum:"current,stale,unknown" doc:"stale: older than the newest protocol the server speaks; unknown: it sent none"`
	LastPolledAt   time.Time     `json:"last_polled_at"`
}

type StatusBody struct {
	Claimable         int64                  `json:"claimable" doc:"Tickets waiting for an agent to take them."`
	LastPolledAt      *time.Time             `json:"last_polled_at" doc:"When any supervisor last polled; null if none ever has."`
	Offline           bool                   `json:"offline"`
	CurrentBundleHash string                 `json:"current_bundle_hash" doc:"the managed asset bundle hash the server serves now, which a slot's bundle_hash is compared with"`
	CurrentProtocol   int                    `json:"current_protocol" doc:"the newest supervisor protocol the server speaks"`
	Supervisors       []PolledSupervisorBody `json:"supervisors" doc:"each supervisor that has polled and is neither disabled nor archived"`
	Slots             []SlotBody             `json:"slots" doc:"each slot of an enabled agent that has reported, by agent id. A paused slot is never stalled."`
}

type GetStatusRequest struct{}

type GetStatusResponse struct{ Body StatusBody }

func (h *Handler) GetStatus(ctx context.Context, _ *GetStatusRequest) (*GetStatusResponse, error) {
	s, err := h.svc.Status(ctx)
	if err != nil {
		h.logger.Error("supervisor status", "err", err)
		return nil, huma.Error500InternalServerError("internal server error")
	}
	body := StatusBody{Claimable: s.Claimable, LastPolledAt: s.LastPolledAt, Offline: s.Offline, CurrentBundleHash: s.Current.Bundle, CurrentProtocol: s.Current.Protocol,
		Supervisors: make([]PolledSupervisorBody, 0, len(s.Supervisors)), Slots: make([]SlotBody, 0, len(s.Slots))}
	for _, sup := range s.Supervisors {
		body.Supervisors = append(body.Supervisors, PolledSupervisorBody{
			AgentID: sup.AgentID, AgentName: sup.AgentName, Version: sup.Version, Protocol: sup.Protocol,
			ProtocolStatus: sup.ProtocolStatus, LastPolledAt: sup.LastPolledAt,
		})
	}
	for _, slot := range s.Slots {
		var pausedAt *time.Time
		// Not for an offline slot: its supervisor is gone, so "paused" is a
		// stale claim and the age would keep growing.
		if slot.Paused() {
			pausedAt = &slot.PhaseEnteredAt
		}
		body.Slots = append(body.Slots, SlotBody{
			BundleHash: slot.BundleHash, Idle: slot.Idle, BundleStatus: BundleStatus(slot.BundleHash, s.Current.Bundle),
			PauseReason: slot.PauseReason, Reason: ReasonOf(slot.Reason), ReasonDetail: slot.ReasonDetail, PausedAt: pausedAt, PausedLong: slot.PausedLong,
			AgentID: slot.AgentID, AgentName: slot.AgentName, Phase: slot.Phase, PhaseEnteredAt: slot.PhaseEnteredAt, TicketID: slot.TicketID,
			ErrorClass: slot.ErrorClass, ErrorStreak: slot.ErrorStreak, ReportedAt: slot.ReportedAt,
			Offline: slot.Offline, Stalled: slot.Stalled(), StallReason: string(slot.Stall),
		})
	}
	return &GetStatusResponse{Body: body}, nil
}
