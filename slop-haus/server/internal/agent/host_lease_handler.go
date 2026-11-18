package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// -- Wire --

type HostLeaseRequest struct {
	Body struct {
		Holder string `json:"holder" minLength:"1" maxLength:"100" doc:"An id the host picks once per run. Only the holder that acquired a lease can renew or release it."`
	}
}

type HostLeaseResponse struct {
	Body struct {
		ExpiresAt time.Time `json:"expires_at" doc:"The lease lapses then unless renewed. Renew well before it; stop serving once it has passed."`
		TTLSecs   float64   `json:"ttl_seconds" doc:"How long each acquire or renewal lasts."`
	}
}

func (h *Handler) hostLeases() (*HostLeaseService, error) {
	if h.hostLease == nil {
		return nil, huma.Error503ServiceUnavailable("host leases are not available")
	}
	return h.hostLease, nil
}

func (h *Handler) hostLeaseCall(ctx context.Context, holder string, do func(*HostLeaseService, uuid.UUID) (*HostLease, error)) (*HostLeaseResponse, error) {
	who, err := authn.FromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("host lease served without authentication", err)
	}
	svc, err := h.hostLeases()
	if err != nil {
		return nil, err
	}
	lease, err := do(svc, who.AgentID())
	if err != nil {
		return nil, httperr.Map(h.logger, err, &hostLeaseTable)
	}
	out := &HostLeaseResponse{}
	out.Body.ExpiresAt, out.Body.TTLSecs = lease.ExpiresAt, svc.ttl.Seconds()
	return out, nil
}

func (h *Handler) AcquireHostLease(ctx context.Context, in *HostLeaseRequest) (*HostLeaseResponse, error) {
	return h.hostLeaseCall(ctx, in.Body.Holder, func(s *HostLeaseService, id uuid.UUID) (*HostLease, error) {
		return s.Acquire(ctx, id, in.Body.Holder)
	})
}

func (h *Handler) RenewHostLease(ctx context.Context, in *HostLeaseRequest) (*HostLeaseResponse, error) {
	return h.hostLeaseCall(ctx, in.Body.Holder, func(s *HostLeaseService, id uuid.UUID) (*HostLease, error) {
		return s.Renew(ctx, id, in.Body.Holder)
	})
}

func (h *Handler) ReleaseHostLease(ctx context.Context, in *HostLeaseRequest) (*struct{}, error) {
	who, err := authn.FromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("host lease served without authentication", err)
	}
	svc, err := h.hostLeases()
	if err != nil {
		return nil, err
	}
	if err := svc.Release(ctx, who.AgentID(), in.Body.Holder); err != nil {
		return nil, httperr.Map(h.logger, err, &hostLeaseTable)
	}
	return &struct{}{}, nil
}

func registerHostLeaseRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "acquireHostLease",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/agent/host_lease",
		Summary:     "Take the calling Remote Control agent's host lease",
		Description: "Only the agent's own credential, and only a Remote Control agent. 409 while another holder's lease is live; " +
			"acquiring again as the current holder renews it. The lease lapses if it is not renewed.",
	}, h.AcquireHostLease)
	huma.Register(api, huma.Operation{
		OperationID: "renewHostLease",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/agent/host_lease/renew",
		Summary:     "Renew the calling agent's host lease",
		Description: "Only the holder that acquired it. 409 when the lease has lapsed or another host holds it: the caller must stop serving.",
	}, h.RenewHostLease)
	huma.Register(api, huma.Operation{
		OperationID:   "releaseHostLease",
		Metadata:      authn.RequireAgent(),
		Method:        http.MethodPost,
		Path:          "/api/agent/host_lease/release",
		Summary:       "Give up the calling agent's host lease",
		Description:   "Ends the lease if the holder holds it; a lease held by another host, or none, is left as it is.",
		DefaultStatus: http.StatusNoContent,
	}, h.ReleaseHostLease)
}
