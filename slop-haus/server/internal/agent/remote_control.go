package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// RemoteControlHost is the conductor behind a `remote_control` entry of the
// supervisor's own desired state that it is to run (launch supervisor), made
// on the first call. A conductor, like a hand-registered host, because the
// host lease, the Remote Control tab and session ownership all go by its
// owner: the person who approved the supervisor, whose machine runs it. It
// records the supervisor, which is what retires it when the entry is removed.
// The entry is the only authority: a name that is not one reads as not found,
// as does a name held by anything but a conductor this supervisor made for
// that person, so the supervisor cannot be pointed at an unrelated agent or at
// someone else's session. A conductor's credential is its owner's to hand
// out, and this keeps that true: the credential is recorded as approved by
// the owner.
func (p *ProvisionService) RemoteControlHost(ctx context.Context, supervisorID uuid.UUID, name string) (*Agent, error) {
	supervisor, err := p.agents.Supervisor(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	owner, err := p.hostOwner(ctx, supervisor)
	if err != nil {
		return nil, err
	}
	// An empty document while the tenant does not provision from the board, so switching it off stops enrolment too.
	state, _, err := p.SupervisorView(ctx, supervisor)
	if err != nil {
		return nil, err
	}
	if !hasSupervisorRunHost(state.Agents, name) {
		return nil, ErrNotFound
	}
	host, err := p.agents.store.GetByName(ctx, name)
	if errors.Is(err, ErrNotFound) {
		host, err = p.agents.store.CreateConductor(ctx, uuid.New(), name, owner, &supervisor.ID)
		if errors.Is(err, ErrNameTaken) {
			// Another call made it between the lookup and the insert.
			host, err = p.agents.store.GetByName(ctx, name)
		} else if err == nil {
			p.logger.InfoContext(ctx, "audit", "action", "supervisor_remote_control_host_created", "tenant", host.Tenant,
				"supervisor", supervisor.ID, "agent", host.ID, "approved_by", p.agents.approverOf(ctx, supervisor))
		}
	}
	switch {
	case err != nil:
		return nil, err
	case !runsHost(supervisor, host) || !host.OwnedBy(owner):
		return nil, ErrNotFound
	case host.Archived():
		// Archived agents keep their names; ErrArchived would read as the supervisor's own.
		return nil, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	return host, nil
}

// hostOwner is who owns the conductors the supervisor runs: the person who
// approved it.
func (p *ProvisionService) hostOwner(ctx context.Context, supervisor *Agent) (uuid.UUID, error) {
	if p.agents.slotCredentials == nil {
		return uuid.Nil, ErrSlotCredentialsOff
	}
	approver, err := p.agents.slotCredentials.SlotApprover(ctx, supervisor.Tenant, supervisor.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if approver == nil {
		return uuid.Nil, ErrGrantNoApprover
	}
	return *approver, nil
}

// runsHost is whether host is a conductor the supervisor made for one of its entries.
func runsHost(supervisor, host *Agent) bool {
	return host.IsConductor() && host.RunBySupervisorID != nil && *host.RunBySupervisorID == supervisor.ID
}

func hasSupervisorRunHost(entries []DesiredAgent, name string) bool {
	for i := range entries {
		e := &entries[i]
		if e.Kind == ProvisionRemoteControl && e.Name == name && e.Launch == LaunchSupervisor && e.State != StateRemoved {
			return true
		}
	}
	return false
}

// IssueRemoteControlCredential gives the supervisor a fresh credential for a
// host it is to run, recorded as approved by the person who approved the
// supervisor, so the host acts with a full-access grant like a console's.
// A disabled host is ErrIsDisabled: enabling it is a person's decision.
func (p *ProvisionService) IssueRemoteControlCredential(ctx context.Context, supervisorID uuid.UUID, name string) (*SlotCredential, error) {
	if p.agents.slotCredentials == nil {
		return nil, ErrSlotCredentialsOff
	}
	host, err := p.RemoteControlHost(ctx, supervisorID, name)
	if err != nil {
		return nil, err
	}
	if host.Disabled() {
		return nil, ErrIsDisabled
	}
	issued, err := p.agents.slotCredentials.IssueSlotCredential(ctx, host.Tenant, supervisorID, host.ID)
	if errors.Is(err, ErrAgentUnavailable) {
		return nil, ErrIsDisabled
	}
	if err != nil {
		return nil, err
	}
	// Issuing reads the supervisor's approver again, in a transaction of its
	// own: one who signed the supervisor in since the check above would hold
	// the owner's session. The grant is ended unused and the answer is the
	// one any other owner's is.
	if issued.ApprovedBy != *host.OwnerUserID {
		if err := p.agents.slotCredentials.RevokeAgentCredential(ctx, host.Tenant, host.ID, issued.Tokens.RefreshToken); err != nil {
			return nil, fmt.Errorf("agent: end a host credential approved by someone else: %w", err)
		}
		return nil, ErrNotFound
	}
	p.logger.InfoContext(ctx, "audit", "action", "supervisor_remote_control_credential_issued", "tenant", host.Tenant,
		"supervisor", supervisorID, "agent", host.ID, "approved_by", issued.ApprovedBy, "grant", issued.GrantID)
	return issued, nil
}

// RevokeRemoteControlCredential ends the grant refreshToken belongs to, when
// it is the named host's; anything else is ErrGrantNotFound.
func (p *ProvisionService) RevokeRemoteControlCredential(ctx context.Context, supervisorID uuid.UUID, name, refreshToken string) error {
	if p.agents.slotCredentials == nil {
		return ErrSlotCredentialsOff
	}
	host, err := p.RemoteControlHost(ctx, supervisorID, name)
	if err != nil {
		return err
	}
	if err := p.agents.slotCredentials.RevokeAgentCredential(ctx, host.Tenant, host.ID, refreshToken); err != nil {
		return err
	}
	p.logger.InfoContext(ctx, "audit", "action", "supervisor_remote_control_credential_revoked", "tenant", host.Tenant,
		"supervisor", supervisorID, "agent", host.ID)
	return nil
}
