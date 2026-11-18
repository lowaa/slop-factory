package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// Handler is the board's side of a tenant's GitHub App credential.
//
// Nothing here takes, returns or logs a GitHub API URL: where slop-haus
// talks to GitHub is the server's configuration (ClientOptions.BaseURL),
// not something a request or a tenant can set.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Custom: customProblem,
	Rules: []httperr.Rule{
		{Err: ErrDisabled, Kind: httperr.Conflict, Code: "codehost_credentials_disabled"},
		{Err: ErrNotOwner, Kind: httperr.Forbidden},
		{Err: ErrWrongApp, Kind: httperr.Conflict, Code: "github_app_wrong_installation"},
		{Err: ErrNotInstalled, Kind: httperr.Conflict, Code: "github_app_not_installed"},
		{Err: ErrAlreadyInstalled, Kind: httperr.Conflict, Code: "github_app_already_installed"},
		{Err: ErrWebhookUnavailable, Kind: httperr.Conflict, Code: "github_app_webhook_unavailable",
			Message: "this server does not receive GitHub App webhooks"},
		{Err: ErrInstallationShared, Kind: httperr.Conflict, Code: "github_app_webhook_shared", Message: ErrInstallationShared.Error()},
		{Err: errNoBoardURL, Kind: httperr.Conflict, Code: "github_app_manifest_unavailable",
			Message: "this server cannot create a GitHub App for you; connect one by hand"},
		{Err: ErrInvalidInput, Kind: httperr.Invalid},
		{Err: codehost.ErrNotConnected, Kind: httperr.NotFound, Message: "no GitHub App is connected"},
		{Err: secretbox.ErrCannotOpen, Kind: httperr.Conflict, Code: "codehost_credential_unreadable", Message: unreadableDetail},
		{Err: ErrStoredKeyUnreadable, Kind: httperr.Conflict, Code: "codehost_credential_unreadable", Message: unreadableDetail},
		{Err: ErrCredentialChanged, Kind: httperr.Conflict, Code: "codehost_credential_changed",
			Message: "the credential changed while verifying; verify again"},
		{Err: ErrTooManyRepos, Kind: httperr.Conflict, Code: "codehost_too_many_repositories",
			Message: "the installation can see more repositories than can be listed here; restrict the installation on GitHub to the repositories it needs"},
		{Err: ErrAmbiguousInstallation, Kind: httperr.Conflict, Code: "github_app_ambiguous_installation", Message: ErrAmbiguousInstallation.Error()},
		{Err: ErrBadResponse, Kind: httperr.BadGateway, Code: "github_bad_response", Message: "GitHub answered with something unexpected"},
		{Err: ErrUnreachable, Kind: httperr.BadGateway, Code: "github_unreachable", Message: "could not reach GitHub"},
		httperr.SignIn,
	},
	Unexpected: "unexpected codehost credential error",
}

const unreadableDetail = "the stored key cannot be decrypted with this server's keys; connect the GitHub App again"

// customProblem answers the errors that carry data of their own, or that
// another package answers.
func customProblem(err error) error {
	if p := user.ProblemFor(err); p != nil {
		return p
	}
	// httperr has no 400 kind, and this is the only 400 here.
	if errors.Is(err, ErrBadState) {
		return authn.NewProblem(http.StatusBadRequest, "github_app_setup_state_invalid", err.Error())
	}
	var foreign *ForeignWebhookError
	if errors.As(err, &foreign) {
		return authn.NewProblem(http.StatusConflict, "github_app_webhook_foreign",
			"the App's webhook URL on GitHub points at "+foreign.Current+", not at this environment ("+foreign.Expected+"). If no other environment uses this App, check again and take it over")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return authn.NewProblem(http.StatusBadGateway, "github_refused", apiErr.Error())
	}
	return nil
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// GitHubAppBody is what the board may know about a connection. The private
// key is not in it, and no operation returns it.
type GitHubAppBody struct {
	AppID          int64      `json:"app_id"`
	AppSlug        string     `json:"app_slug" doc:"the App's name on GitHub; empty for one connected by hand"`
	InstallationID int64      `json:"installation_id" doc:"0 while the App is not installed yet"`
	Pending        bool       `json:"pending" doc:"true while the App has no installation: not installed, or waiting for an org owner to approve it"`
	KeyFingerprint string     `json:"key_fingerprint" doc:"SHA-256 of the key's public half: which key is connected"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	VerifiedAt     *time.Time `json:"verified_at" doc:"when Verify last succeeded; null for a connection never verified, or replaced since"`
}

type GitHubAppStatusBody struct {
	Enabled    bool           `json:"enabled" doc:"false when the server has no CREDENTIAL_ENCRYPTION_KEY: it keeps no credentials"`
	CanEdit    bool           `json:"can_edit" doc:"whether the signed-in user is an owner, who alone may connect, verify or remove"`
	Credential *GitHubAppBody `json:"credential" doc:"null while no GitHub App is connected"`
}

func credentialBody(r *Record) *GitHubAppBody {
	if r == nil {
		return nil
	}
	return &GitHubAppBody{AppID: r.AppID, AppSlug: r.AppSlug, InstallationID: r.InstallationID, Pending: r.Pending(), KeyFingerprint: r.Fingerprint,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, VerifiedAt: r.VerifiedAt}
}

type GitHubAppStatusResponse struct{ Body GitHubAppStatusBody }

func (h *Handler) Get(ctx context.Context, _ *struct{}) (*GitHubAppStatusResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	st, err := h.svc.Status(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GitHubAppStatusResponse{Body: GitHubAppStatusBody{Enabled: st.Enabled, CanEdit: st.CanEdit, Credential: credentialBody(st.Credential)}}, nil
}

// GitHubAppConnectBody is the connect request's JSON. It documents the body (see
// RegisterRoutes) but is not what huma validates against.
type GitHubAppConnectBody struct {
	AppID          int64  `json:"app_id" doc:"the GitHub App's id"`
	InstallationID int64  `json:"installation_id" doc:"the id of the App's installation on the account that owns the repositories"`
	PrivateKey     string `json:"private_key" doc:"the PEM of a private key generated for the App. Write-only: no operation returns it"`
}

// GitHubAppConnectRequest takes the body raw. huma validates a typed body, and its
// validation errors quote the offending value -- or, for a body that does
// not parse, the whole body -- back in the response; this body holds a
// private key. So it is read here, and a body that is wrong is refused
// without saying anything about what it held.
type GitHubAppConnectRequest struct {
	RawBody []byte `contentType:"application/json"`
}

// maxConnectBody is generous for a PEM (a 4096-bit key is under 4 KiB).
const maxConnectBody = 2 * MaxPEMBytes

var errBadConnectBody = errors.New("the body must be a JSON object with app_id, installation_id and private_key, and nothing else")

func decodeConnectBody(raw []byte) (*GitHubAppConnectBody, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body GitHubAppConnectBody
	// The decoder's own errors name the offending field and type, never
	// the value, but nothing of them is passed on regardless.
	if err := dec.Decode(&body); err != nil || dec.More() {
		return nil, errBadConnectBody
	}
	return &body, nil
}

type GitHubAppConnectResponse struct{ Body GitHubAppBody }

func (h *Handler) Connect(ctx context.Context, in *GitHubAppConnectRequest) (*GitHubAppConnectResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	body, err := decodeConnectBody(in.RawBody)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	rec, err := h.svc.Connect(ctx, who, &Connection{
		AppID: body.AppID, InstallationID: body.InstallationID, PrivateKey: []byte(body.PrivateKey),
	})
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GitHubAppConnectResponse{Body: *credentialBody(rec)}, nil
}

type GitHubAppRepoCheckBody struct {
	Remote  string `json:"remote"`
	Visible bool   `json:"visible" doc:"whether the installation can see this repository"`
}

type GitHubAppVerifyResponse struct {
	Body struct {
		Account  string                   `json:"account" doc:"the user or organisation the App is installed on"`
		Repos    []string                 `json:"repos" doc:"every repository the installation can see"`
		Manifest []GitHubAppRepoCheckBody `json:"manifest" doc:"the workspace manifest's GitHub repositories against that list"`
	}
}

func (h *Handler) Verify(ctx context.Context, _ *struct{}) (*GitHubAppVerifyResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	v, err := h.svc.Verify(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GitHubAppVerifyResponse{}
	resp.Body.Account = v.Account
	// Arrays, never null: the schema says required arrays and the board reads their length.
	resp.Body.Repos = append(make([]string, 0, len(v.Repos)), v.Repos...)
	resp.Body.Manifest = make([]GitHubAppRepoCheckBody, 0, len(v.Manifest))
	for _, m := range v.Manifest {
		resp.Body.Manifest = append(resp.Body.Manifest, GitHubAppRepoCheckBody{Remote: m.Remote, Visible: m.Visible})
	}
	return resp, nil
}

func (h *Handler) Remove(ctx context.Context, _ *struct{}) (*struct{}, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	if err := h.svc.Remove(ctx, who); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}

type GitHubAppManifestRequest struct {
	Body struct {
		Account string `json:"account" doc:"the organisation that should own the App; empty for the signed-in GitHub user's own account" maxLength:"39"`
	}
}

type GitHubAppManifestResponse struct {
	Body struct {
		ActionURL string `json:"action_url" doc:"GitHub's create-an-App page, built by the server: the browser posts the manifest there as the form field manifest"`
		Manifest  string `json:"manifest" doc:"the App manifest, a JSON document"`
	}
}

func (h *Handler) BeginManifest(ctx context.Context, in *GitHubAppManifestRequest) (*GitHubAppManifestResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	start, err := h.svc.BeginManifest(ctx, who, strings.TrimSpace(in.Body.Account))
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GitHubAppManifestResponse{}
	resp.Body.ActionURL, resp.Body.Manifest = start.ActionURL, start.Manifest
	return resp, nil
}

type GitHubAppCreatedRequest struct {
	Body struct {
		Code  string `json:"code" doc:"the code GitHub redirected back with" maxLength:"128"`
		State string `json:"state" doc:"the state GitHub redirected back with" maxLength:"128"`
	}
}

type GitHubAppCreatedResponse struct {
	Body struct {
		Credential GitHubAppBody `json:"credential"`
		InstallURL string        `json:"install_url" doc:"GitHub's page to install the new App from, built by the server"`
	}
}

func (h *Handler) CompleteManifest(ctx context.Context, in *GitHubAppCreatedRequest) (*GitHubAppCreatedResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	rec, installURL, err := h.svc.CompleteManifest(ctx, who, in.Body.Code, in.Body.State)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GitHubAppCreatedResponse{}
	resp.Body.Credential, resp.Body.InstallURL = *credentialBody(rec), installURL
	return resp, nil
}

type GitHubAppInstallURLResponse struct {
	Body struct {
		InstallURL string `json:"install_url"`
	}
}

func (h *Handler) InstallURL(ctx context.Context, _ *struct{}) (*GitHubAppInstallURLResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	u, err := h.svc.InstallURL(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GitHubAppInstallURLResponse{}
	resp.Body.InstallURL = u
	return resp, nil
}

type GitHubAppInstalledRequest struct {
	Body struct {
		InstallationID int64  `json:"installation_id,omitempty" doc:"the installation GitHub redirected back with; absent when setup_action is request"`
		SetupAction    string `json:"setup_action,omitempty" doc:"what GitHub redirected back with: install, update or request" maxLength:"32"`
		State          string `json:"state" maxLength:"128"`
	}
}

type GitHubAppInstalledResponse struct {
	Body struct {
		Credential GitHubAppBody `json:"credential"`
		Requested  bool          `json:"requested" doc:"true when the install awaits an org owner's approval"`
	}
}

func (h *Handler) CompleteInstall(ctx context.Context, in *GitHubAppInstalledRequest) (*GitHubAppInstalledResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	res, err := h.svc.CompleteInstall(ctx, who, in.Body.InstallationID, in.Body.SetupAction, in.Body.State)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GitHubAppInstalledResponse{}
	resp.Body.Credential, resp.Body.Requested = *credentialBody(res.Record), res.Requested
	return resp, nil
}

type GitHubAppDeliveryBody struct {
	Event      string    `json:"event"`
	Action     string    `json:"action"`
	Outcome    string    `json:"outcome" enum:"processing,applied,ignored,error"`
	ReceivedAt time.Time `json:"received_at"`
}

func deliveryBody(d *AppDelivery) *GitHubAppDeliveryBody {
	if d == nil {
		return nil
	}
	return &GitHubAppDeliveryBody{Event: d.Event, Action: d.Action, Outcome: d.Outcome, ReceivedAt: d.ReceivedAt}
}

// GitHubAppWebhookBody is the App's webhook as the board shows it: whether
// it delivers, and what is left to do if not.
type GitHubAppWebhookBody struct {
	State         string                 `json:"state" enum:"unavailable,not_applied,shared_installation,url_differs,awaiting_events,receiving" doc:"unavailable: nothing to configure; not_applied: slop-haus has not set the URL and secret; shared_installation: another workspace on this server owns the webhook for this installation; url_differs: the URL set on GitHub is not this environment's; awaiting_events: configured but no pull request event has arrived, so the App may not be subscribed to them; receiving: a pull request event has arrived"`
	CanEdit       bool                   `json:"can_edit" doc:"whether the signed-in user is an owner, who alone may check or rotate"`
	ExpectedURL   string                 `json:"expected_url" doc:"where this environment receives the App's deliveries; empty when it does not"`
	ConfiguredURL string                 `json:"configured_url" doc:"the URL slop-haus last set on GitHub; empty if never"`
	AppliedAt     *time.Time             `json:"applied_at"`
	Events        []string               `json:"events" doc:"the events the App's webhook must be subscribed to"`
	LastDelivery  *GitHubAppDeliveryBody `json:"last_delivery" doc:"the newest delivery of any event, a ping included; null if none"`
	LastEvent     *GitHubAppDeliveryBody `json:"last_event" doc:"the newest delivery of one of events; null until one arrives"`
}

func webhookBody(st *WebhookStatus) GitHubAppWebhookBody {
	return GitHubAppWebhookBody{State: string(st.State), CanEdit: st.CanEdit, ExpectedURL: st.ExpectedURL, ConfiguredURL: st.ConfiguredURL,
		AppliedAt: st.AppliedAt, Events: append([]string{}, AppWebhookEvents...),
		LastDelivery: deliveryBody(st.LastDelivery), LastEvent: deliveryBody(st.LastEvent)}
}

type GitHubAppWebhookResponse struct{ Body GitHubAppWebhookBody }

func (h *Handler) WebhookStatus(ctx context.Context, _ *struct{}) (*GitHubAppWebhookResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	st, err := h.svc.WebhookStatus(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GitHubAppWebhookResponse{Body: webhookBody(st)}, nil
}

type GitHubAppWebhookCheckRequest struct {
	TakeOver bool `query:"take_over" doc:"replace a webhook URL on another host; the owner's word that no other environment uses this App"`
}

type GitHubAppWebhookCheckResponse struct {
	Body struct {
		Outcome string               `json:"outcome" enum:"applied,unchanged" doc:"applied: GitHub's configuration was set; unchanged: it already matched"`
		Webhook GitHubAppWebhookBody `json:"webhook"`
	}
}

func (h *Handler) CheckWebhook(ctx context.Context, in *GitHubAppWebhookCheckRequest) (*GitHubAppWebhookCheckResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	outcome, st, err := h.svc.CheckWebhook(ctx, who, in.TakeOver)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GitHubAppWebhookCheckResponse{}
	resp.Body.Outcome, resp.Body.Webhook = string(outcome), webhookBody(st)
	return resp, nil
}

func (h *Handler) RotateWebhook(ctx context.Context, _ *struct{}) (*GitHubAppWebhookResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	st, err := h.svc.RotateWebhook(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GitHubAppWebhookResponse{Body: webhookBody(st)}, nil
}
