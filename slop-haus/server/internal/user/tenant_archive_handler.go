package user

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type ArchivedTenantsResponse struct {
	Body struct {
		Tenants []TenantBody `json:"tenants"`
		// Count is N for the page's "Show archived (N)".
		Count int `json:"count"`
	}
}

func (h *Handler) ArchivedTenants(ctx context.Context, _ *struct{}) (*ArchivedTenantsResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	tenants, err := h.svc.ArchivedTenants(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ArchivedTenantsResponse{}
	resp.Body.Tenants = make([]TenantBody, 0, len(tenants))
	for i := range tenants {
		resp.Body.Tenants = append(resp.Body.Tenants, tenantBody(&tenants[i], false))
	}
	resp.Body.Count = len(tenants)
	return resp, nil
}

type ArchiveTenantRequest struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		ConfirmName string `json:"confirm_name" maxLength:"200" doc:"the tenant's exact name, typed back"`
	}
}

// ArchiveTenantResponse carries session cookies only when the caller's session
// was in the tenant and has moved; the body is then the new session's profile,
// else the caller's own.
type ArchiveTenantResponse struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      ProfileBody
}

func (h *Handler) ArchiveTenant(ctx context.Context, in *ArchiveTenantRequest) (*ArchiveTenantResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	session, err := h.svc.ArchiveOwnedTenant(ctx, who, in.ID, in.Body.ConfirmName)
	if err != nil {
		return nil, h.mapError(err)
	}
	if session != nil {
		moved := h.sessionResponse(session)
		return &ArchiveTenantResponse{SetCookie: moved.SetCookie, Body: moved.Body}, nil
	}
	p, err := h.svc.Me(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ArchiveTenantResponse{Body: profileToBody(p, h.svc.CSRFToken(who))}, nil
}

type UnarchiveTenantRequest struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) UnarchiveTenant(ctx context.Context, in *UnarchiveTenantRequest) (*NoContentResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.UnarchiveOwnedTenant(ctx, who, in.ID); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}
