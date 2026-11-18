package supervisorwatch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Phase is where a supervisor slot's state machine is.
type Phase string

const PhasePaused Phase = "paused"
const PhaseIdle Phase = "idle"
const PhaseRunning Phase = "running"

// SlotPhases are the phases the supervisor's slot can be in, spelt as
// slop_haus_supervisor/fleet/phase.py's Phase spells them. A test reads that
// file and fails when the two differ, so a phase added there cannot be
// silently refused here.
var SlotPhases = []Phase{PhaseIdle, PhaseRunning, "wait_lock", "wait_checks", "wait_review", "backoff", "wait_reset", PhasePaused}

// Schema is the phase's OpenAPI schema: the enum comes from SlotPhases, so
// the wire contract and the validation cannot drift apart.
func (Phase) Schema(_ huma.Registry) *huma.Schema {
	enum := make([]any, len(SlotPhases))
	for i, p := range SlotPhases {
		enum[i] = string(p)
	}
	return &huma.Schema{Type: "string", Enum: enum}
}

// IdleReason is why a slot that is not running a ticket is not: a bounded
// code, so an operator can tell an idle slot from a stuck one without logs.
type IdleReason string

const (
	ReasonNoClaimable IdleReason = "no_claimable"
	ReasonFleetPaused IdleReason = "fleet_paused"
	ReasonSlotPaused  IdleReason = "slot_paused"
	ReasonLockQueued  IdleReason = "lock_queued"
	ReasonCooldown    IdleReason = "cooldown"
	ReasonBackoff     IdleReason = "backoff_after_error"
	ReasonWaitingCI   IdleReason = "waiting_ci"
	ReasonReviewing   IdleReason = "reviewing"
	ReasonUpdating    IdleReason = "updating"
	ReasonStarting    IdleReason = "starting"
	ReasonOther       IdleReason = "other"
	// ReasonUnknown is what a slot shows when its supervisor predates the
	// field. It is never stored: the empty string is, and means the same.
	ReasonUnknown IdleReason = "unknown"
)

// IdleReasons are the codes a supervisor may report, spelt as
// slop_haus_supervisor/fleet/phase.py's Reason spells them. A test reads that
// file and fails when the two differ.
var IdleReasons = []IdleReason{
	ReasonNoClaimable, ReasonFleetPaused, ReasonSlotPaused, ReasonLockQueued, ReasonCooldown,
	ReasonBackoff, ReasonWaitingCI, ReasonReviewing, ReasonUpdating, ReasonStarting, ReasonOther,
}

// ReasonOf maps a stored reason to what is shown: "" (never reported) is
// unknown, and a code a newer supervisor made up is other, so a report is never
// refused for a code this server has not learnt.
func ReasonOf(stored string) IdleReason {
	switch r := IdleReason(stored); {
	case r == "":
		return ReasonUnknown
	case slices.Contains(IdleReasons, r):
		return r
	default:
		return ReasonOther
	}
}

const (
	// MaxReasonDetailLen bounds the detail beside a reason, in characters.
	MaxReasonDetailLen = 160
	// MaxErrorClassLen bounds error_class. It is a token the supervisor picks
	// from a small set, never an error message: see errorClassPattern.
	MaxErrorClassLen = 64
	// MaxPhaseAge bounds the age a slot may claim for its phase.
	MaxPhaseAge = 366 * 24 * time.Hour
	// MaxErrorStreak bounds the streak count a slot may report.
	MaxErrorStreak = 1_000_000
	// MaxBundleHashLen bounds bundle_hash, a hex digest.
	MaxBundleHashLen = 64
	// MaxPauseReasonLen bounds pause_reason in characters. A longer reason is
	// cut rather than refused: the supervisor's own bound is shorter, and a
	// report that fails would hide the pause it is there to announce.
	MaxPauseReasonLen = 512
)

var errorClassPattern = regexp.MustCompile(`^[a-z0-9_]*$`)

var bundleHashPattern = regexp.MustCompile(`^[0-9a-f]*$`)

// ErrInvalidReport is a slot report the server refuses: an unknown phase or a
// value out of bounds.
var ErrInvalidReport = errors.New("invalid slot report")

// SlotReport is what a supervisor says about one of its slots.
type SlotReport struct {
	Phase Phase
	// PhaseAge is how long the slot has been in Phase, as the supervisor
	// measured it: an age rather than a timestamp, so its clock does not matter.
	PhaseAge   time.Duration
	TicketID   *int64
	ErrorClass string
	// ErrorStreak is how many steps in a row ended in ErrorClass.
	ErrorStreak int
	// PauseReason is free text from the slot, set only while it is paused.
	// ReportSlot cuts it to MaxPauseReasonLen.
	PauseReason string
	// Reason is an IdleReason, or empty from a supervisor that predates it.
	Reason string
	// ReasonDetail is a short free-text note beside Reason; ReportSlot cuts it
	// to MaxReasonDetailLen.
	ReasonDetail string
	// BundleHash is the managed asset bundle hash the slot's workspace
	// installed; empty from a supervisor that predates it or a workspace
	// with no bundle-hash file. ReportSlot drops one that is not a hash.
	BundleHash string
	// Idle: the slot has no live session, so it may be restarted or have its
	// workspace synced. False from a supervisor that predates it.
	Idle bool
}

func (r *SlotReport) validate() error {
	switch {
	case !slices.Contains(SlotPhases, r.Phase):
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidReport, r.Phase)
	case r.PhaseAge < 0 || r.PhaseAge > MaxPhaseAge:
		return fmt.Errorf("%w: phase age out of range", ErrInvalidReport)
	case r.TicketID != nil && *r.TicketID < 1:
		return fmt.Errorf("%w: ticket id out of range", ErrInvalidReport)
	case len(r.ErrorClass) > MaxErrorClassLen || !errorClassPattern.MatchString(r.ErrorClass):
		return fmt.Errorf("%w: error class must be a short lower-case token", ErrInvalidReport)
	case r.ErrorStreak < 0 || r.ErrorStreak > MaxErrorStreak:
		return fmt.Errorf("%w: error streak out of range", ErrInvalidReport)
	}
	return nil
}

// StallReason is why a slot counts as stalled.
type StallReason string

const (
	// StallIdleWithWork: idle past the bound while it could take a ticket.
	StallIdleWithWork StallReason = "idle_with_work"
	// StallNoHeartbeat: running, and its lease has had no heartbeat for the bound.
	StallNoHeartbeat StallReason = "no_heartbeat"
	// StallRepeatingError: the same error class for more steps than the bound.
	StallRepeatingError StallReason = "repeating_error"
	// StallHeldWithoutRun: idle past the bound while its agent holds a live
	// lease or the slot names a ticket, so nothing is running the claim.
	StallHeldWithoutRun StallReason = "held_without_run"
)

// Bounds are how long, or how many steps, a slot may go without progress
// before it is stalled.
type Bounds struct {
	// IdleWithWork is how long a slot may sit idle while it could claim a ticket.
	IdleWithWork time.Duration
	// NoHeartbeat is how long a running slot's lease may go without a heartbeat.
	NoHeartbeat time.Duration
	// HeldWithoutRun is how long a slot may report idle while it holds a claim.
	// The supervisor's own retries report backoff, not idle, so idle with a
	// claim is only momentary.
	HeldWithoutRun time.Duration
	// ErrorSteps is how many steps in a row may end in the same error class.
	ErrorSteps int
	// PausedNotice is how long a slot may stay paused before the board says so.
	PausedNotice time.Duration
}

// DefaultBounds: a lease with no heartbeat for its whole TTL is one the
// expiry sweep is about to take, so a running slot that quiet is wedged.
func DefaultBounds() Bounds {
	return Bounds{IdleWithWork: 10 * time.Minute, NoHeartbeat: ticket.DefaultLeaseTTL, HeldWithoutRun: 5 * time.Minute, ErrorSteps: 5, PausedNotice: 10 * time.Minute}
}

// SlotRow is a slot's latest report with what the stall rule reads beside it.
type SlotRow struct {
	AgentID   uuid.UUID
	AgentName string
	// SupervisorID is the supervisor the slot's agent belongs to.
	SupervisorID   *uuid.UUID
	Phase          Phase
	PhaseEnteredAt time.Time
	TicketID       *int64
	ErrorClass     string
	ErrorStreak    int
	PauseReason    string
	// Reason is the stored code; "" when the supervisor never sent one.
	Reason       string
	ReasonDetail string
	// BundleHash is what the slot's workspace reported; "" when it never has.
	BundleHash string
	// Idle is what the slot last reported; false when it never has.
	Idle       bool
	ReportedAt time.Time
	// LeaseHeartbeatAt is when the agent's live lease last had a heartbeat;
	// nil when it holds none.
	LeaseHeartbeatAt *time.Time
}

// Slot is a SlotRow judged.
type Slot struct {
	SlotRow
	// Offline: the slot's own report is older than OfflineAfter, which is the
	// supervisor being gone, not the slot stalled.
	Offline bool
	// Stall is why the slot is stalled; empty when it is not.
	Stall StallReason
	// PausedLong: the slot has been paused for longer than Bounds.PausedNotice.
	// A paused slot is never stalled, so this is how it is made loud.
	PausedLong bool
}

func (s *Slot) Stalled() bool { return s.Stall != "" }

// Paused: the slot's supervisor is there and it is paused.
func (s *Slot) Paused() bool { return !s.Offline && s.Phase == PhasePaused }

// Claimability is the per-agent probe the claim path uses: whether the agent
// could take a ticket, roles, variant, scope blocks and affinity included.
type Claimability interface {
	Peek(ctx context.Context, agentID uuid.UUID, only *int64) (*ticket.ClaimResult, error)
}

// ReportSlot records an agent's slot report.
func (s *Service) ReportSlot(ctx context.Context, agentID uuid.UUID, r *SlotReport) error {
	if err := r.validate(); err != nil {
		return err
	}
	report := *r
	report.PauseReason = truncateRunes(r.PauseReason, MaxPauseReasonLen)
	if report.Phase != PhasePaused {
		report.PauseReason = ""
	}
	if report.Reason != "" {
		report.Reason = string(ReasonOf(report.Reason))
	}
	report.ReasonDetail = truncateRunes(r.ReasonDetail, MaxReasonDetailLen)
	// Dropped, not refused: a report that fails would hide the phase it carries.
	if len(r.BundleHash) > MaxBundleHashLen || !bundleHashPattern.MatchString(r.BundleHash) {
		report.BundleHash = ""
	}
	return s.store.ReportSlot(ctx, agentID, &report)
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// Slots judges the context tenant's slots. A disabled or archived agent has
// none: its last report would otherwise stall for ever.
func (s *Service) Slots(ctx context.Context) ([]*Slot, error) {
	rows, err := s.store.Slots(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var enabled *bool
	slots := make([]*Slot, 0, len(rows))
	for _, row := range rows {
		slot := &Slot{SlotRow: *row, Offline: now.Sub(row.ReportedAt) > OfflineAfter}
		slots = append(slots, slot)
		if slot.Paused() {
			slot.PausedLong = now.Sub(row.PhaseEnteredAt) > s.bounds.PausedNotice
		}
		if slot.Offline || row.Phase == PhasePaused {
			continue
		}
		slot.Stall, err = s.stallOf(ctx, row, now, &enabled)
		if err != nil {
			return nil, err
		}
	}
	return slots, nil
}

// Reported is the context tenant's reported slots with their offline-ness
// and nothing else judged: it skips the stall probes Slots makes, which a
// reader of names and phases does not need.
func (s *Service) Reported(ctx context.Context) ([]*Slot, error) {
	rows, err := s.store.Slots(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	slots := make([]*Slot, len(rows))
	for i, row := range rows {
		slots[i] = &Slot{SlotRow: *row, Offline: now.Sub(row.ReportedAt) > OfflineAfter}
	}
	return slots, nil
}

func (s *Service) stallOf(ctx context.Context, row *SlotRow, now time.Time, enabled **bool) (StallReason, error) {
	switch {
	case row.Phase == PhaseRunning && now.Sub(s.heartbeatOf(row)) > s.bounds.NoHeartbeat:
		return StallNoHeartbeat, nil
	case row.ErrorClass != "" && row.ErrorStreak > s.bounds.ErrorSteps:
		return StallRepeatingError, nil
	case row.Phase == PhaseIdle && (row.LeaseHeartbeatAt != nil || row.TicketID != nil) &&
		now.Sub(row.PhaseEnteredAt) > s.bounds.HeldWithoutRun:
		// Before idle_with_work: an agent holding a claim can claim nothing,
		// so that probe would come back empty and hide this stall.
		return StallHeldWithoutRun, nil
	case row.Phase == PhaseIdle && now.Sub(row.PhaseEnteredAt) > s.bounds.IdleWithWork:
		// Checked last: it costs a probe. Idle is only a stall when the agent
		// could take something; while processing is off, idle is deliberate.
		// The bound is on how long the slot has been idle, not on how long the
		// work has been claimable: the server does not know the latter, and a
		// slot that polls takes claimable work within seconds, so the two
		// differ only for a slot that is not polling, which is the stall.
		if *enabled == nil {
			on, err := s.policy.TicketProcessingEnabled(ctx)
			if err != nil {
				return "", err
			}
			*enabled = &on
		}
		if !**enabled {
			return "", nil
		}
		result, err := s.claims.Peek(ctx, row.AgentID, nil)
		if err != nil {
			return "", err
		}
		if result.Ticket != nil {
			return StallIdleWithWork, nil
		}
	}
	return "", nil
}

// heartbeatOf is when a running slot last showed life: its lease's heartbeat,
// or, with no live lease, when it entered the phase.
func (s *Service) heartbeatOf(row *SlotRow) time.Time {
	if row.LeaseHeartbeatAt != nil {
		return *row.LeaseHeartbeatAt
	}
	return row.PhaseEnteredAt
}

// StalledSlots counts the stalled slots of every tenant.
func (s *Service) StalledSlots(ctx context.Context) (int, error) {
	total := 0
	err := tenantstate.Each(ctx, s.store, func(ctx context.Context, _ uuid.UUID) error {
		slots, err := s.Slots(ctx)
		if err != nil {
			return err
		}
		for _, slot := range slots {
			if slot.Stalled() {
				total++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// PausedSlots counts the paused slots of every tenant that has work waiting.
// How long they have been paused is the alert's `for`, so the age is not
// judged here; a pause with nothing claimable costs nothing and is not counted.
func (s *Service) PausedSlots(ctx context.Context) (int, error) {
	total := 0
	err := tenantstate.Each(ctx, s.store, func(ctx context.Context, _ uuid.UUID) error {
		slots, err := s.Slots(ctx)
		if err != nil {
			return err
		}
		paused := 0
		for _, slot := range slots {
			if slot.Paused() {
				paused++
			}
		}
		if paused == 0 {
			return nil
		}
		f, err := s.facts(ctx)
		if err != nil {
			return err
		}
		if f.Claimable > 0 {
			total += paused
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// SlotsByReason counts the online slots of every tenant by the reason they
// show, for the metric. A running slot has no reason to give and is not
// counted. Every code is present, zero or not, so the series set is bounded.
func (s *Service) SlotsByReason(ctx context.Context) (map[IdleReason]int, error) {
	counts := map[IdleReason]int{ReasonUnknown: 0}
	for _, r := range IdleReasons {
		counts[r] = 0
	}
	err := tenantstate.Each(ctx, s.store, func(ctx context.Context, _ uuid.UUID) error {
		rows, err := s.store.Slots(ctx)
		if err != nil {
			return err
		}
		now := s.now()
		for _, row := range rows {
			if row.Phase == PhaseRunning || now.Sub(row.ReportedAt) > OfflineAfter {
				continue
			}
			counts[ReasonOf(row.Reason)]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}
