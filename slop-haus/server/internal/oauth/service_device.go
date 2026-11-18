package oauth

import (
	"context"
	"errors"
	"net/netip"
	"net/url"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/clientip"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// DeviceAuthorization is the device authorization response.
type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDevice begins a device authorization for a client allowed the
// device grant.
func (s *Service) StartDevice(ctx context.Context, clientAddr netip.Addr, clientID, resource string) (*DeviceAuthorization, error) {
	if !s.startLimit.Allow(clientip.RateKey(clientAddr)) {
		return nil, errRateLimited
	}
	client, err := s.store.GetClient(ctx, clientID)
	if err != nil {
		return nil, errInvalidClient
	}
	if !client.allows(GrantDeviceCode) {
		return nil, errUnauthorizedGrant
	}
	res, err := s.checkResource(resource)
	if err != nil {
		return nil, err
	}
	device, deviceHash, err := newSecret()
	if err != nil {
		return nil, err
	}
	userCode, err := newUserCode()
	if err != nil {
		return nil, err
	}
	d := &DeviceCode{ID: uuid.New(), UserCode: userCode, ClientID: client.ID, Resource: res, ExpiresAt: s.clock.Now().Add(s.cfg.DeviceTTL),
		StartedFrom: clientAddr}
	if err := s.store.CreateDeviceCode(ctx, d, deviceHash); err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.DeviceStarted, "device_code_id", d.ID, "client_id", client.ID, "started_from", clientAddr.String())
	return &DeviceAuthorization{
		DeviceCode:              device,
		UserCode:                userCode,
		VerificationURI:         s.cfg.Issuer + "/device",
		VerificationURIComplete: s.cfg.Issuer + "/device?code=" + url.QueryEscape(userCode),
		ExpiresIn:               int(s.cfg.DeviceTTL.Seconds()),
		Interval:                int(s.cfg.PollInterval.Seconds()),
	}, nil
}

// ErrTooManyLookups means a user tried too many user codes.
var ErrTooManyLookups = errors.New("oauth: too many device code attempts")

// LookupDevice describes a pending device authorization for the page the
// user enters its code on.
func (s *Service) LookupDevice(ctx context.Context, who *authn.User, userCode string) (*DeviceCode, error) {
	if !s.lookupLimit.Allow(who.ID.String()) {
		return nil, ErrTooManyLookups
	}
	return s.store.FindDeviceCode(ctx, NormalizeUserCode(userCode))
}

// DecideDevice records the user's decision on a device authorization.
func (s *Service) DecideDevice(ctx context.Context, who *authn.User, userCode string, d *Decision) error {
	code := NormalizeUserCode(userCode)
	// Through the rate-limited lookup either way: denying is as much a use
	// of a guessed code as approving, and unlimited it would let any
	// signed-in user knock out other people's pending sign-ins.
	pending, err := s.LookupDevice(ctx, who, code)
	if err != nil {
		return err
	}
	// started_from beside the approver's own client_ip is what tells a
	// phished approval (RFC 8628 section 5.4) from a member approving
	// their own workspace.
	startedFrom := pending.StartedFrom.String()
	if !d.Approve {
		if err := s.store.DenyDeviceCode(ctx, code); err != nil {
			return err
		}
		s.events.Record(ctx, audit.DeviceDenied, "user_id", who.ID, "tenant", who.TenantID, "device_code_id", pending.ID,
			"started_from", startedFrom)
		return nil
	}
	g, err := s.newGrant(ctx, who, pending.ClientID, pending.Resource, d)
	if err != nil {
		return err
	}
	if err := s.store.ApproveDeviceCode(ctx, code, g); err != nil {
		return err
	}
	s.events.Record(ctx, audit.DeviceApproved, "user_id", who.ID, "tenant", who.TenantID, "device_code_id", pending.ID,
		"started_from", startedFrom, "agent_id", g.AgentID, "grant_id", g.ID)
	return nil
}
