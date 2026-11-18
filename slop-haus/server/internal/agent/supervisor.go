package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// CreateSlot makes a worker for a supervisor to run a ticket in. The kind is
// not a parameter: a supervisor can only ever create workers, never a console,
// conductor or another supervisor, so widening what it can mint takes a new
// method rather than a different argument.
//
// The supervisor passed in only names who is asking. Whether it is a live
// supervisor, and how many slots it has, are read under a lock on its row, so
// a stale *Agent cannot create past a disable or past the cap.
func (s *Service) CreateSlot(ctx context.Context, supervisor *Agent, name string) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrEmptyName
	}
	created, err := s.store.CreateSlot(ctx, uuid.New(), supervisor.ID, name)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_slot_created", "tenant", created.Tenant,
		"supervisor", supervisor.ID, "slot", created.ID, "approved_by", s.approverOf(ctx, supervisor))
	return created, nil
}

// AdoptSlot links a worker that has no supervisor, by name, as one of the
// supervisor's slots: the case of an agent that began as an interactive worker
// and was reused as a slot. Which names are the supervisor's to adopt is the
// caller's to decide (its own manifest); the supervisor and its cap are
// checked here, under a lock.
func (s *Service) AdoptSlot(ctx context.Context, supervisor *Agent, name string) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrEmptyName
	}
	adopted, err := s.store.AdoptSlot(ctx, supervisor.ID, name)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_slot_adopted", "tenant", adopted.Tenant,
		"supervisor", supervisor.ID, "slot", adopted.ID, "approved_by", s.approverOf(ctx, supervisor))
	return adopted, nil
}

// RetireSlot ends one of the supervisor's slots: it is disabled (which
// revokes its grants and voids its proposals), its leases are released, and it
// is archived, which frees its place under the cap. Retiring a slot that is
// already retired succeeds. A slot of another supervisor, or an agent that is
// not a slot at all, reads as not found.
func (s *Service) RetireSlot(ctx context.Context, supervisor *Agent, slotID uuid.UUID) (*Agent, error) {
	slot, err := s.store.Get(ctx, slotID)
	if err != nil {
		return nil, err
	}
	if slot.SupervisorID == nil || *slot.SupervisorID != supervisor.ID {
		return nil, ErrNotFound
	}
	if _, err := s.setDisabled(ctx, slot.ID, true); err != nil {
		return nil, err
	}
	retired, err := s.store.SetArchived(ctx, slot.ID, true)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_slot_retired", "tenant", retired.Tenant,
		"supervisor", supervisor.ID, "slot", retired.ID, "approved_by", s.approverOf(ctx, supervisor))
	return retired, nil
}

// approverOf is who approved the supervisor, for the audit lines naming it;
// nil when that cannot be said, which must not stop the action it describes.
func (s *Service) approverOf(ctx context.Context, supervisor *Agent) *uuid.UUID {
	if s.slotCredentials == nil {
		return nil
	}
	approver, err := s.slotCredentials.SlotApprover(ctx, supervisor.Tenant, supervisor.ID)
	if err != nil {
		s.logger.WarnContext(ctx, "agent: look up a supervisor's approver", "supervisor", supervisor.ID, "err", err)
		return nil
	}
	return approver
}

// SetSupervisorDisabled switches a supervisor off, which takes its slots with
// it, or back on, which does not bring them back. It is a method of its own
// because the worker-facing ones read a supervisor as missing; who may call it
// is the caller's to decide.
func (s *Service) SetSupervisorDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*Agent, error) {
	found, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found.IsSupervisor() {
		return nil, ErrNotFound
	}
	updated, err := s.setDisabled(ctx, id, disabled)
	if err != nil {
		return nil, err
	}
	var actor uuid.UUID
	if who := authn.PersonFromContext(ctx); who != nil {
		actor = who.ID
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_disabled_set", "user_id", actor, "tenant", updated.Tenant,
		"supervisor", id, "disabled", disabled)
	return updated, nil
}

// SupervisorStaleAfter is how long a supervisor may go unheard of before the
// board offers to end it. Ending revokes its grants, so a supervisor that is
// only offline for a while must not look ended-able.
const SupervisorStaleAfter = 24 * time.Hour

// SupervisorLastSeen is when the supervisor last showed a sign of life, and
// whether that is long enough ago to be stale at now. A supervisor that never
// reported counts from its creation.
func (s *Service) SupervisorLastSeen(ctx context.Context, id uuid.UUID, now time.Time) (seen time.Time, stale bool, err error) {
	seen, err = s.store.SupervisorLastSeen(ctx, id)
	if err != nil {
		return time.Time{}, false, err
	}
	return seen, now.Sub(seen) > SupervisorStaleAfter, nil
}

// releaseSlots hands back what slots held when they were disabled. The
// database function that disabled them cannot: the leases belong to the
// ticket package.
func (s *Service) releaseSlots(ctx context.Context, supervisorID uuid.UUID, slots []Agent, reason string) error {
	for i := range slots {
		slot := &slots[i]
		if slot.DisabledAt == nil {
			continue
		}
		if err := s.releaser.ReleaseLeasesFor(ctx, slot.ID, reason, *slot.DisabledAt); err != nil {
			return fmt.Errorf("agent: release slot leases: %w", err)
		}
		s.logger.InfoContext(ctx, "audit", "action", "supervisor_slot_disabled", "tenant", slot.Tenant,
			"supervisor", supervisorID, "slot", slot.ID, "reason", reason)
	}
	return nil
}

// disableSlotsOf is what a supervisor having no grant left does to its slots.
// Any other agent is nothing to do.
func (s *Service) disableSlotsOf(ctx context.Context, agentID uuid.UUID, reason string) error {
	found, err := s.store.Get(ctx, agentID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !found.IsSupervisor() {
		return nil
	}
	disabled, err := s.store.DisableSlots(ctx, agentID, reason)
	if err != nil {
		return fmt.Errorf("agent: disable slots: %w", err)
	}
	return s.releaseSlots(ctx, agentID, disabled, reason)
}

// ErrSlotCredentialsOff is asking for a slot credential from a service built
// without a way to issue them.
var ErrSlotCredentialsOff = errors.New("agent: slot credentials are not available")

// Supervisor is the live supervisor id names: an enabled, unarchived agent of
// that kind, read now rather than trusted from the request that carried it.
// It is the one gate the supervisor's routes go through, so a credential that
// belongs to any other kind of agent, or to a supervisor switched off since
// it authenticated, gets nothing.
func (s *Service) Supervisor(ctx context.Context, id uuid.UUID) (*Agent, error) {
	found, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case !found.IsSupervisor():
		return nil, ErrNotSupervisor
	case found.Archived():
		return nil, ErrArchived
	case found.Disabled():
		return nil, ErrIsDisabled
	}
	return found, nil
}

// SupervisorCaller is the live supervisor the caller is: a supervisor's own
// credential, not read-only, for a supervisor that is still enabled. Route
// markers refuse the other kinds first; this is the policy itself, so a
// transport without those markers is held to it too. Every refusal is one
// error, so a caller learns nothing about why.
func (s *Service) SupervisorCaller(ctx context.Context, who authn.Identity) (*Agent, error) {
	if who == nil || who.ReadOnly() || !authn.IsSupervisor(who) {
		return nil, ErrNotSupervisorCaller
	}
	supervisor, err := s.Supervisor(ctx, who.AgentID())
	switch {
	case err == nil:
		return supervisor, nil
	case errors.Is(err, ErrNotSupervisor), errors.Is(err, ErrIsDisabled), errors.Is(err, ErrArchived), errors.Is(err, ErrNotFound):
		return nil, ErrNotSupervisorCaller
	default:
		return nil, err
	}
}

// Slots is the supervisor's live slots, and its retired ones too when asked.
func (s *Service) Slots(ctx context.Context, supervisor *Agent, includeArchived bool) ([]Agent, error) {
	return s.store.ListSlots(ctx, supervisor.ID, includeArchived)
}

// SlotIDs is the supervisor's slots that can act, for its poll: live and not
// disabled. A retired or disabled slot has nothing to hear.
func (s *Service) SlotIDs(ctx context.Context, supervisorID uuid.UUID) ([]uuid.UUID, error) {
	slots, err := s.store.ListSlots(ctx, supervisorID, false)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(slots))
	for i := range slots {
		if !slots[i].Disabled() {
			ids = append(ids, slots[i].ID)
		}
	}
	return ids, nil
}

// ListSupervisors is the tenant's supervisors, for the people who manage
// them. Archived ones only when asked.
func (s *Service) ListSupervisors(ctx context.Context, includeArchived bool) ([]Agent, error) {
	return s.store.ListSupervisors(ctx, includeArchived)
}

// IssueSlotCredential gives the supervisor a fresh credential for one of its
// own slots, recorded as approved by the person who approved the supervisor.
//
// Only a live slot of this supervisor qualifies. Another supervisor's slot, a
// plain worker, the supervisor itself or any other agent reads as not found,
// so the answer does not say which agents exist; a retired slot is gone, and
// a disabled one is ErrIsDisabled (enabling it is a person's decision, and a
// credential for it would be refused anyway).
func (s *Service) IssueSlotCredential(ctx context.Context, supervisorID, slotID uuid.UUID) (*SlotCredential, error) {
	if s.slotCredentials == nil {
		return nil, ErrSlotCredentialsOff
	}
	supervisor, err := s.Supervisor(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	slot, err := s.store.Get(ctx, slotID)
	if err != nil {
		return nil, err
	}
	if slot.SupervisorID == nil || *slot.SupervisorID != supervisor.ID || slot.Archived() {
		return nil, ErrNotFound
	}
	if slot.Disabled() {
		return nil, ErrIsDisabled
	}
	// A slot disabled between the read above and the insert is refused by the
	// database, which serialises the insert with the disable: either the
	// disable commits first and the grant is never made, or it waits for the
	// grant and revokes it.
	issued, err := s.slotCredentials.IssueSlotCredential(ctx, supervisor.Tenant, supervisor.ID, slot.ID)
	if errors.Is(err, ErrAgentUnavailable) {
		return nil, ErrIsDisabled
	}
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_slot_credential_issued", "tenant", supervisor.Tenant,
		"supervisor", supervisor.ID, "slot", slot.ID, "approved_by", issued.ApprovedBy, "grant", issued.GrantID)
	return issued, nil
}

// Steward is the tenant's steward, made by the first call. Only a live
// supervisor reaches it (the one gate Supervisor is), and nothing else mints
// one: the database refuses a steward row that no supervisor minted.
func (s *Service) Steward(ctx context.Context, supervisorID uuid.UUID) (*Agent, error) {
	supervisor, err := s.Supervisor(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	steward, err := s.store.EnsureSteward(ctx, uuid.New(), supervisor.ID)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_steward_ensured", "tenant", steward.Tenant,
		"supervisor", supervisor.ID, "steward", steward.ID, "approved_by", s.approverOf(ctx, supervisor))
	return steward, nil
}

// IssueStewardCredential gives the supervisor a fresh credential for the
// tenant's steward, recorded as approved by the person who approved the
// supervisor, so the steward acts with a full-access grant like a console's.
// Minting the steward first means a supervisor needs one call to get going.
// A disabled steward is ErrIsDisabled: enabling it is a person's decision.
func (s *Service) IssueStewardCredential(ctx context.Context, supervisorID uuid.UUID) (*SlotCredential, error) {
	if s.slotCredentials == nil {
		return nil, ErrSlotCredentialsOff
	}
	steward, err := s.Steward(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	if steward.Disabled() {
		return nil, ErrIsDisabled
	}
	issued, err := s.slotCredentials.IssueSlotCredential(ctx, steward.Tenant, supervisorID, steward.ID)
	if errors.Is(err, ErrAgentUnavailable) {
		return nil, ErrIsDisabled
	}
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_steward_credential_issued", "tenant", steward.Tenant,
		"supervisor", supervisorID, "steward", steward.ID, "approved_by", issued.ApprovedBy, "grant", issued.GrantID)
	return issued, nil
}

// RevokeStewardCredential ends the steward grant that refreshToken belongs to,
// so a supervisor replacing the credential it holds leaves no live copy behind.
// Only a grant of the tenant's steward qualifies; anything else is ErrGrantNotFound.
func (s *Service) RevokeStewardCredential(ctx context.Context, supervisorID uuid.UUID, refreshToken string) error {
	if s.slotCredentials == nil {
		return ErrSlotCredentialsOff
	}
	steward, err := s.Steward(ctx, supervisorID)
	if err != nil {
		return err
	}
	if err := s.slotCredentials.RevokeAgentCredential(ctx, steward.Tenant, steward.ID, refreshToken); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_steward_credential_revoked", "tenant", steward.Tenant,
		"supervisor", supervisorID, "steward", steward.ID)
	return nil
}
