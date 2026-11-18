// Package oauthtest is test wiring for the agent and OAuth services, which
// are built from each other.
package oauthtest

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// DeferredGrants forwards to an *oauth.Service assigned after the agent
// service is built, as the agent service needs OAuth and OAuth needs it.
type DeferredGrants struct {
	OAuth *oauth.Service
}

func (d *DeferredGrants) Authenticate(ctx context.Context, accessToken string) (*agent.Access, error) {
	return d.OAuth.Authenticate(ctx, accessToken)
}

func (d *DeferredGrants) RevokeAgentGrants(ctx context.Context, tenantID, agentID uuid.UUID) error {
	return d.OAuth.RevokeAgentGrants(ctx, tenantID, agentID)
}

func (d *DeferredGrants) IssueSlotCredential(ctx context.Context, tenantID, supervisorID, slotID uuid.UUID) (*agent.SlotCredential, error) {
	return d.OAuth.IssueSlotCredential(ctx, tenantID, supervisorID, slotID)
}

func (d *DeferredGrants) RevokeAgentCredential(ctx context.Context, tenantID, agentID uuid.UUID, refreshToken string) error {
	return d.OAuth.RevokeAgentCredential(ctx, tenantID, agentID, refreshToken)
}

func (d *DeferredGrants) SlotApprover(ctx context.Context, tenantID, supervisorID uuid.UUID) (*uuid.UUID, error) {
	return d.OAuth.SlotApprover(ctx, tenantID, supervisorID)
}
