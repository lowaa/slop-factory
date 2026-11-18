package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// DefaultAPIBaseURL is GitHub's API.
const DefaultAPIBaseURL = "https://api.github.com"

// DefaultWebBaseURL is GitHub's website, where the browser is sent to create
// and install an App.
const DefaultWebBaseURL = "https://github.com"

const (
	apiVersion      = "2022-11-28"
	requestTimeout  = 15 * time.Second
	maxResponseSize = 1 << 20
	reposPerPage    = 100
	// maxRepoPages bounds Verify's listing: an installation on thousands
	// of repositories is not one slop-haus is pointed at.
	maxRepoPages = 10
	maxMessage   = 400
)

// ClientOptions configure a Client.
type ClientOptions struct {
	// BaseURL is the GitHub API's root: api.github.com, or a GitHub
	// Enterprise Server's /api/v3. It comes from the server's own
	// configuration and from nowhere else -- never from a tenant, a
	// request or the database. Every call to GitHub carries a JWT signed
	// by a tenant's App key, so whoever chooses this URL chooses who
	// receives one; a tenant-chosen URL would turn the server into a
	// signer for it.
	BaseURL string
	// HTTP is the transport; nil is a client with its own timeout. Its
	// redirects are refused whatever it is: a redirect would carry the
	// credential to wherever GitHub, or whoever answers as GitHub, points.
	HTTP *http.Client
}

// Client is GitHub's REST API as far as a credential needs it.
type Client struct {
	base *url.URL
	http *http.Client
}

// NewClient validates the base URL: absolute http(s), a host, no
// credentials, query or fragment.
func NewClient(opts *ClientOptions) (*Client, error) {
	raw := opts.BaseURL
	if raw == "" {
		raw = DefaultAPIBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("githubapp: API base URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	hc := &http.Client{Timeout: requestTimeout}
	if opts.HTTP != nil {
		copied := *opts.HTTP
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: u, http: hc}, nil
}

// ErrTooManyRepos is an installation with more repositories than Verify lists.
var ErrTooManyRepos = fmt.Errorf("githubapp: the installation can see more than %d repositories", maxRepoPages*reposPerPage)

// ErrBadResponse is GitHub answering with something that is not the JSON
// the API documents.
var ErrBadResponse = errors.New("githubapp: unreadable response from GitHub")

// ErrUnreachable is GitHub not answering at all.
var ErrUnreachable = errors.New("githubapp: could not reach GitHub")

// APIError is GitHub refusing a request. Message is GitHub's own text,
// bounded; it is about the request and never contains a credential.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github API answered %d: %s", e.Status, e.Message)
}

// tokenRequest is POST /app/installations/{id}/access_tokens. Repositories
// and Permissions are what make the token worth less to a thief: without
// them GitHub mints one for everything the installation can reach.
type tokenRequest struct {
	Repositories []string    `json:"repositories,omitempty"`
	Permissions  Permissions `json:"permissions"`
}

type tokenResponse struct {
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Permissions  Permissions
	Repositories []struct {
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

type installationToken struct {
	token Token
	// repos is the full names the token covers.
	repos       []string
	permissions Permissions
}

func (c *Client) installationToken(ctx context.Context, app appJWT, installationID int64, req *tokenRequest) (*installationToken, error) {
	var resp tokenResponse
	path := "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	if err := c.do(ctx, http.MethodPost, path, app.bearer(), req, http.StatusCreated, &resp); err != nil {
		return nil, err
	}
	if resp.Token == "" || resp.ExpiresAt.IsZero() {
		return nil, errors.New("githubapp: GitHub's token response had no token or expiry")
	}
	out := &installationToken{
		token:       Token{value: resp.Token, expiresAt: resp.ExpiresAt},
		permissions: resp.Permissions,
	}
	for _, r := range resp.Repositories {
		out.repos = append(out.repos, r.FullName)
	}
	return out, nil
}

// Installation is what GitHub says about an installation.
type Installation struct {
	// ID is the installation's id.
	ID int64
	// AppID is the App the installation belongs to.
	AppID int64
	// Account is the user or organisation that installed the App.
	Account string
}

type installationJSON struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

func (j *installationJSON) installation() *Installation {
	return &Installation{ID: j.ID, AppID: j.AppID, Account: j.Account.Login}
}

func (c *Client) installation(ctx context.Context, app appJWT, installationID int64) (*Installation, error) {
	var resp installationJSON
	path := "/app/installations/" + strconv.FormatInt(installationID, 10)
	if err := c.do(ctx, http.MethodGet, path, app.bearer(), nil, http.StatusOK, &resp); err != nil {
		return nil, err
	}
	return resp.installation(), nil
}

// appInstallations lists the installations of the App the JWT signs for.
func (c *Client) appInstallations(ctx context.Context, app appJWT) ([]*Installation, error) {
	var resp []installationJSON
	if err := c.do(ctx, http.MethodGet, "/app/installations?per_page=100", app.bearer(), nil, http.StatusOK, &resp); err != nil {
		return nil, err
	}
	out := make([]*Installation, 0, len(resp))
	for i := range resp {
		out = append(out, resp[i].installation())
	}
	return out, nil
}

// convertedApp is what GitHub hands over, once, for a manifest's code. Its
// PEM is the App's private key: it is held in a type that redacts itself
// which never prints, and is sealed straight away.
type convertedApp struct {
	secretbox.Redacted
	appID int64
	slug  string
	pem   []byte
}

// convertManifest exchanges the code from a manifest redirect for the new
// App. The code is the credential for this call, so there is no bearer; it
// is in the path, which no error here repeats. Only the fields used are
// decoded: the response also carries a client secret and a webhook secret,
// which are not kept.
func (c *Client) convertManifest(ctx context.Context, code string) (*convertedApp, error) {
	var resp struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		PEM  string `json:"pem"`
	}
	if err := c.do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", struct{}{}, http.StatusCreated, &resp); err != nil {
		return nil, err
	}
	if resp.ID <= 0 || resp.Slug == "" || resp.PEM == "" {
		return nil, ErrBadResponse
	}
	return &convertedApp{appID: resp.ID, slug: resp.Slug, pem: []byte(resp.PEM)}, nil
}

// installationRepos lists the full names of the repositories the token's
// installation can see.
func (c *Client) installationRepos(ctx context.Context, token *Token) ([]string, error) {
	var names []string
	// One page past the limit, so an installation with exactly the limit's
	// worth of repositories (a full last page) is told from one with more.
	for page := 1; page <= maxRepoPages+1; page++ {
		var resp struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=%d&page=%d", reposPerPage, page)
		if err := c.do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &resp); err != nil {
			return nil, err
		}
		if page > maxRepoPages {
			if len(resp.Repositories) == 0 {
				return names, nil
			}
			break
		}
		for _, r := range resp.Repositories {
			names = append(names, r.FullName)
		}
		if len(resp.Repositories) < reposPerPage {
			return names, nil
		}
	}
	// Not a partial answer: a manifest repo past the cut would read as
	// invisible.
	return nil, ErrTooManyRepos
}

// Do calls the REST API with bearer, JSON-encoding body if not nil, and
// decodes the answer into out. A status other than want is an *APIError;
// no error carries the bearer. It is for the code host's own operations
// (internal/codehost/github), which hold a token from TokenFor.
func (c *Client) Do(ctx context.Context, method, path, bearer string, body any, want int, out any) error {
	return c.do(ctx, method, path, bearer, body, want, out)
}

// GraphQL runs one query or mutation against GitHub's GraphQL endpoint,
// which sits beside the REST root: api.github.com/graphql, and
// {host}/api/graphql on Enterprise Server where REST is {host}/api/v3.
// GitHub answers 200 with an "errors" list for a failed operation; those
// come back as *GraphQLError. out may be nil.
func (c *Client) GraphQL(ctx context.Context, bearer, query string, variables map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	endpoint := *c.base
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/v3") + "/graphql"
	body := map[string]any{"query": query, "variables": variables}
	if err := c.send(ctx, http.MethodPost, endpoint.String(), bearer, body, http.StatusOK, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		msg := resp.Errors[0].Message
		if len(msg) > maxMessage {
			msg = msg[:maxMessage]
		}
		return &GraphQLError{Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return ErrBadResponse
	}
	return nil
}

// GraphQLError is GitHub refusing a GraphQL operation. Message is GitHub's
// own text, bounded.
type GraphQLError struct{ Message string }

func (e *GraphQLError) Error() string { return "github GraphQL error: " + e.Message }

func (c *Client) do(ctx context.Context, method, path, bearer string, body any, want int, out any) error {
	// The path is appended to the configured base, never parsed against a
	// caller's URL, so a request can only go to the configured host.
	return c.send(ctx, method, c.base.String()+path, bearer, body, want, out)
}

func (c *Client) send(ctx context.Context, method, target, bearer string, body any, want int, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("githubapp: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("githubapp: build request: %w", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "slop-haus")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error carries the URL and the transport's reason; neither
		// holds a credential, which is only ever in a header.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("%w: %s %s: %w", ErrUnreachable, method, c.base.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("githubapp: read response: %w", err)
	}
	if resp.StatusCode != want {
		return &APIError{Status: resp.StatusCode, Message: apiMessage(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// Not wrapped: on a token response the body is the token.
		return ErrBadResponse
	}
	return nil
}

// apiMessage is GitHub's message with the detail of its errors array: a 422
// says only "Validation Failed" at the top, and which field was refused, and
// why, is in the errors (e.g. "PullRequest.head: invalid", or "A pull
// request already exists for ...").
func apiMessage(raw []byte) string {
	var parsed struct {
		Message string `json:"message"`
		// Most endpoints give objects; some give plain strings.
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(raw, &parsed) != nil || parsed.Message == "" {
		return "no message"
	}
	details := make([]string, 0, len(parsed.Errors))
	for _, e := range parsed.Errors {
		if d := apiErrorDetail(e); d != "" {
			details = append(details, d)
		}
	}
	msg := parsed.Message
	if len(details) > 0 {
		msg += " (" + strings.Join(details, "; ") + ")"
	}
	if len(msg) > maxMessage {
		return msg[:maxMessage]
	}
	return msg
}

func apiErrorDetail(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var e struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	switch {
	case e.Message != "":
		return e.Message
	case e.Field != "":
		return strings.TrimPrefix(e.Resource+"."+e.Field, ".") + ": " + e.Code
	default:
		return e.Code
	}
}

// HookConfig is an App's webhook configuration as GitHub reports it. The
// secret is write-only on GitHub's side and never part of it.
type HookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

func (c *Client) appHookConfig(ctx context.Context, app appJWT) (*HookConfig, error) {
	var resp HookConfig
	if err := c.do(ctx, http.MethodGet, "/app/hook/config", app.bearer(), nil, http.StatusOK, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// setAppHookConfig sets the App's webhook URL and secret, as JSON with TLS
// verification on. GitHub takes insecure_ssl as a string.
func (c *Client) setAppHookConfig(ctx context.Context, app appJWT, hookURL string, secret *WebhookSecret) error {
	body := struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		Secret      string `json:"secret"`
		InsecureSSL string `json:"insecure_ssl"`
	}{URL: hookURL, ContentType: "json", Secret: secret.Reveal(), InsecureSSL: "0"}
	return c.do(ctx, http.MethodPatch, "/app/hook/config", app.bearer(), body, http.StatusOK, nil)
}

// HookDelivery is one attempt GitHub made to deliver an event to the App's
// webhook.
type HookDelivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
}

// appHookDeliveries lists the App's most recent webhook deliveries, newest
// first.
func (c *Client) appHookDeliveries(ctx context.Context, app appJWT, limit int) ([]HookDelivery, error) {
	var resp []HookDelivery
	path := fmt.Sprintf("/app/hook/deliveries?per_page=%d", limit)
	if err := c.do(ctx, http.MethodGet, path, app.bearer(), nil, http.StatusOK, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
