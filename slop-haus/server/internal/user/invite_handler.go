package user

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type ProviderBody struct {
	Name        string `json:"name" doc:"the {provider} of the sign-in, invite, link and reauth routes"`
	DisplayName string `json:"display_name" doc:"what to call it on its button"`
}

type AuthConfigResponse struct {
	Body struct {
		// RegistrationOpen decides whether the sign-in page offers sign-up.
		RegistrationOpen bool `json:"registration_open"`
		// PasswordLogin decides whether it offers an email and password.
		PasswordLogin bool `json:"password_login"`
		// Providers are the login providers it offers a button for.
		Providers []ProviderBody `json:"providers"`
	}
}

func (h *Handler) AuthConfig(_ context.Context, _ *struct{}) (*AuthConfigResponse, error) {
	resp := &AuthConfigResponse{}
	resp.Body.RegistrationOpen = h.svc.RegistrationOpen()
	resp.Body.PasswordLogin = h.svc.PasswordLoginEnabled()
	resp.Body.Providers = []ProviderBody{}
	for _, ap := range h.svc.Providers().List() {
		resp.Body.Providers = append(resp.Body.Providers, ProviderBody{Name: ap.Name(), DisplayName: ap.DisplayName()})
	}
	return resp, nil
}

// AdminCheck answers 204 to an admin and nothing else gets past the
// middleware. The frontend's nginx asks it before serving the admin pages,
// so their code never reaches anyone else.
func (h *Handler) AdminCheck(_ context.Context, _ *struct{}) (*NoContentResponse, error) {
	return &NoContentResponse{}, nil
}

type InviteBody struct {
	ID        uuid.UUID  `json:"id"`
	Email     string     `json:"email"`
	Kind      string     `json:"kind" enum:"new_tenant,join_tenant"`
	TenantID  *uuid.UUID `json:"tenant_id"`
	InvitedBy uuid.UUID  `json:"invited_by"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func inviteToBody(inv *Invite) InviteBody {
	return InviteBody{ID: inv.ID, Email: inv.Email, Kind: string(inv.Kind), TenantID: inv.TenantID,
		InvitedBy: inv.InvitedBy, ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt}
}

type CreateInviteRequest struct {
	Body struct {
		Email string `json:"email" format:"email" maxLength:"254"`
	}
}

type InviteResponse struct {
	Body InviteBody
}

type InvitesResponse struct {
	Body struct {
		Invites []InviteBody `json:"invites"`
	}
}

type InviteIDRequest struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) invitesResponse(invites []Invite) *InvitesResponse {
	resp := &InvitesResponse{}
	resp.Body.Invites = make([]InviteBody, 0, len(invites))
	for i := range invites {
		resp.Body.Invites = append(resp.Body.Invites, inviteToBody(&invites[i]))
	}
	return resp
}

// currentUser is the signed-in caller; the middleware guarantees one on
// the routes that use it, so its absence is a wiring bug.
func (h *Handler) currentUser(ctx context.Context) (*authn.User, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return who, nil
}

func (h *Handler) CreateAdminInvite(ctx context.Context, in *CreateInviteRequest) (*InviteResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := h.svc.InviteToNewTenant(ctx, who, in.Body.Email)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &InviteResponse{Body: inviteToBody(inv)}, nil
}

func (h *Handler) ListAdminInvites(ctx context.Context, _ *struct{}) (*InvitesResponse, error) {
	invites, err := h.svc.ListAdminInvites(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.invitesResponse(invites), nil
}

func (h *Handler) RevokeAdminInvite(ctx context.Context, in *InviteIDRequest) (*NoContentResponse, error) {
	if err := h.svc.RevokeAdminInvite(ctx, in.ID); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

func (h *Handler) CreateInvite(ctx context.Context, in *CreateInviteRequest) (*InviteResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := h.svc.InviteToTenant(ctx, who, in.Body.Email)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &InviteResponse{Body: inviteToBody(inv)}, nil
}

func (h *Handler) ListInvites(ctx context.Context, _ *struct{}) (*InvitesResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	invites, err := h.svc.ListInvites(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.invitesResponse(invites), nil
}

func (h *Handler) RevokeInvite(ctx context.Context, in *InviteIDRequest) (*NoContentResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RevokeInvite(ctx, who, in.ID); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

type InvitePreviewResponse struct {
	Body struct {
		Email         string `json:"email"`
		Kind          string `json:"kind" enum:"new_tenant,join_tenant"`
		TenantName    string `json:"tenant_name"`
		InvitedBy     string `json:"invited_by"`
		AccountExists bool   `json:"account_exists"`
	}
}

func (h *Handler) PreviewInvite(ctx context.Context, in *TokenRequest) (*InvitePreviewResponse, error) {
	p, err := h.svc.PreviewInvite(ctx, in.Body.Token)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &InvitePreviewResponse{}
	resp.Body.Email = p.Email
	resp.Body.Kind = string(p.Kind)
	resp.Body.TenantName = p.TenantName
	resp.Body.InvitedBy = p.InvitedBy
	resp.Body.AccountExists = p.AccountExists
	return resp, nil
}

type AcceptInviteAsNewAccountRequest struct {
	Body struct {
		Token       string `json:"token" minLength:"1" maxLength:"256"`
		Password    string `json:"password" minLength:"10" maxLength:"72"`
		DisplayName string `json:"display_name" minLength:"1" maxLength:"100"`
		TenantName  string `json:"tenant_name,omitempty" maxLength:"200" doc:"what to call the tenant a new_tenant invite makes; defaults to the display name"`
	}
}

func (h *Handler) AcceptInviteAsNewAccount(ctx context.Context, in *AcceptInviteAsNewAccountRequest) (*LoginResponse, error) {
	session, err := h.svc.AcceptInviteAsNewAccount(ctx, in.Body.Token, in.Body.DisplayName, in.Body.Password, in.Body.TenantName)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.sessionResponse(session), nil
}

type AcceptInviteRequest struct {
	Body struct {
		Token      string `json:"token" minLength:"1" maxLength:"256"`
		TenantName string `json:"tenant_name,omitempty" maxLength:"200" doc:"what to call the tenant a new_tenant invite makes; defaults to the display name, and is refused if it matches another of the caller's tenants"`
	}
}

func (h *Handler) AcceptInvite(ctx context.Context, in *AcceptInviteRequest) (*LoginResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	session, err := h.svc.AcceptInvite(ctx, who, in.Body.Token, in.Body.TenantName)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.sessionResponse(session), nil
}

type TenantBody struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Current bool      `json:"current"`
	// IsOwner is whether the caller made the tenant, and so may rename it.
	IsOwner bool `json:"is_owner,omitempty" required:"false"`
	// Owners are the owners' display names, to tell tenants of one name apart.
	Owners []string `json:"owners,omitempty" required:"false"`
	// Colour is the owner's chosen palette key; null until they choose, and
	// the frontend then derives one from the id.
	Colour *string `json:"colour" nullable:"true" enum:"red,orange,amber,green,teal,blue,violet,pink"`
	// ArchivedAt is when the owner archived the tenant; only the archived
	// list has any.
	ArchivedAt *time.Time `json:"archived_at,omitempty" required:"false"`
}

func tenantBody(t *TenantSummary, current bool) TenantBody {
	owners := t.Owners
	if owners == nil {
		owners = []string{}
	}
	return TenantBody{ID: t.ID, Name: t.Name, Current: current, IsOwner: t.IsOwner, Owners: owners, Colour: t.Colour, ArchivedAt: t.ArchivedAt}
}

type TenantsResponse struct {
	Body struct {
		Tenants []TenantBody `json:"tenants"`
		// OwnerLimit is how many tenants the caller may own; 0 means
		// creating their own is switched off.
		OwnerLimit int `json:"owner_limit"`
	}
}

func (h *Handler) Tenants(ctx context.Context, _ *struct{}) (*TenantsResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	tenants, err := h.svc.Tenants(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &TenantsResponse{}
	resp.Body.OwnerLimit = h.svc.TenantOwnerLimit()
	resp.Body.Tenants = make([]TenantBody, 0, len(tenants))
	for i := range tenants {
		resp.Body.Tenants = append(resp.Body.Tenants, tenantBody(&tenants[i], tenants[i].ID == who.TenantID))
	}
	return resp, nil
}

type RenameTenantRequest struct {
	Body struct {
		Name string `json:"name" maxLength:"200" doc:"1-40 characters, no line breaks, different from the name of every other tenant the caller belongs to"`
	}
}

type TenantResponse struct {
	Body TenantBody
}

// RenameTenant renames the session's tenant. Only its owner may.
func (h *Handler) RenameTenant(ctx context.Context, in *RenameTenantRequest) (*TenantResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.RenameTenant(ctx, who, in.Body.Name)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TenantResponse{Body: tenantBody(t, true)}, nil
}

type SetTenantColourRequest struct {
	Body struct {
		Colour *string `json:"colour" nullable:"true" enum:"red,orange,amber,green,teal,blue,violet,pink" doc:"a palette key, or null for the default derived from the tenant's id"`
	}
}

// SetTenantColour sets the session's tenant's colour. Only its owner may.
func (h *Handler) SetTenantColour(ctx context.Context, in *SetTenantColourRequest) (*TenantResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.SetTenantColour(ctx, who, in.Body.Colour)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TenantResponse{Body: tenantBody(t, true)}, nil
}

type CreateTenantRequest struct {
	Body struct {
		Name string `json:"name" maxLength:"200" doc:"1-40 characters, no line breaks, different from the name of every other tenant the caller belongs to"`
	}
}

// CreateTenant makes a tenant owned by the caller and moves the session into it.
func (h *Handler) CreateTenant(ctx context.Context, in *CreateTenantRequest) (*LoginResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	session, err := h.svc.CreateTenant(ctx, who, in.Body.Name)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.sessionResponse(session), nil
}

type SwitchTenantRequest struct {
	Body struct {
		TenantID uuid.UUID `json:"tenant_id"`
	}
}

func (h *Handler) SwitchTenant(ctx context.Context, in *SwitchTenantRequest) (*LoginResponse, error) {
	who, err := h.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	session, err := h.svc.SwitchTenant(ctx, who, in.Body.TenantID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.sessionResponse(session), nil
}
