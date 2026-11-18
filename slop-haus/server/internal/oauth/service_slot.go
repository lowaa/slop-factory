package oauth

import (
	"context"
	"errors"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"

	"github.com/google/uuid"
)

// SlotCredential is a fresh credential for one of a supervisor's slots.
type SlotCredential = agent.SlotCredential

// IssueSlotCredential issues a grant for slotID on the workspace client, with
// the token pair the device grant returns. Whether slotID is a live slot of
// supervisorID is the caller's to have established (the agent package owns
// that); this checks what OAuth owns: the supervisor still holds a full-access
// grant, and says who approved it.
func (s *Service) IssueSlotCredential(ctx context.Context, tenantID, supervisorID, slotID uuid.UUID) (*SlotCredential, error) {
	if s.slotLimit == nil {
		return nil, ErrSlotCredentialsOff
	}
	if !s.slotLimit.Allow(supervisorID.String()) {
		return nil, ErrTooManySlotCredentials
	}
	tokens, pair, err := s.newTokenPair()
	if err != nil {
		return nil, err
	}
	g := &Grant{ID: uuid.New(), TenantID: tenantID, AgentID: slotID, ClientID: WorkspaceClientID, Resource: s.Resource(), Scope: ScopeAgent}
	approver, err := s.store.IssueSlotGrant(ctx, supervisorID, g, pair)
	if err != nil {
		return nil, err
	}
	return &SlotCredential{Tokens: tokens, GrantID: g.ID, ApprovedBy: approver}, nil
}

// SlotApprover is who approved the supervisor's newest live full-access grant,
// the person its slots' credentials are recorded as approved by (the same
// grant IssueSlotCredential reads); nil when that grant names nobody or there
// is none.
func (s *Service) SlotApprover(ctx context.Context, tenantID, supervisorID uuid.UUID) (*uuid.UUID, error) {
	approvers, err := s.FullAccessApprovers(ctx, tenantID, supervisorID)
	if err != nil || len(approvers) == 0 {
		return nil, err
	}
	return approvers[0], nil
}

// RevokeAgentCredential ends the grant refreshToken was issued under, if it is
// a live grant acting as agentID in tenantID. Anything else, an unknown token or
// another agent's, is ErrNotFound, so the answer does not say which exist.
func (s *Service) RevokeAgentCredential(ctx context.Context, tenantID, agentID uuid.UUID, refreshToken string) error {
	g, err := s.agentGrantOfRefresh(ctx, tenantID, agentID, refreshToken)
	if err != nil {
		return err
	}
	if err := s.store.RevokeGrant(ctx, tenantID, g.ID); err != nil {
		return err
	}
	return s.noteGrantsMaybeEnded(ctx, tenantID, agentID)
}

func (s *Service) agentGrantOfRefresh(ctx context.Context, tenantID, agentID uuid.UUID, refreshToken string) (*Grant, error) {
	g, err := s.store.LiveGrantOfRefresh(ctx, hashSecret(refreshToken))
	if errors.Is(err, errInvalidGrant) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if g.TenantID != tenantID || g.AgentID != agentID {
		return nil, ErrNotFound
	}
	return g, nil
}
