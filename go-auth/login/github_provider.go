package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// GitHubEndpoints are GitHub's URLs, overridable so tests can stand in a
// fake GitHub.
type GitHubEndpoints struct {
	OAuth oauth2.Endpoint
	API   string
}

var DefaultGitHubEndpoints = GitHubEndpoints{OAuth: endpoints.GitHub, API: "https://api.github.com"}

// GitHubAuthProvider logs in with a GitHub OAuth app. The only scope
// requested is none at all: the default grants read access to the public
// profile, which is every field used here -- so no email, and
// Identity.Email stays empty.
type GitHubAuthProvider struct {
	oauth  *oauth2.Config
	api    string
	client *http.Client
}

func NewGitHubAuthProvider(clientID, clientSecret, callbackURL string, ep GitHubEndpoints) *GitHubAuthProvider {
	return &GitHubAuthProvider{
		oauth: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     ep.OAuth,
			RedirectURL:  callbackURL,
		},
		api:    ep.API,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// GitHubProviderName is GitHub's identity namespace.
const GitHubProviderName = "github"

func (g *GitHubAuthProvider) Name() string { return GitHubProviderName }

func (g *GitHubAuthProvider) DisplayName() string { return "GitHub" }

// AuthCodeURL sends a PKCE challenge too: GitHub supports it for OAuth apps,
// and a provider that ignored it would lose nothing.
func (g *GitHubAuthProvider) AuthCodeURL(a *LoginAttempt) (string, error) {
	return g.oauth.AuthCodeURL(a.State,
		oauth2.SetAuthURLParam("allow_signup", "false"),
		oauth2.S256ChallengeOption(a.Verifier),
	), nil
}

func (g *GitHubAuthProvider) Exchange(ctx context.Context, code string, a *LoginAttempt) (*Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.client)
	tok, err := g.oauth.Exchange(ctx, code, oauth2.VerifierOption(a.Verifier))
	if err != nil {
		return nil, fmt.Errorf("auth: github: exchange code: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api+"/user", nil)
	if err != nil {
		return nil, fmt.Errorf("auth: github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	tok.SetAuthHeader(req)

	resp, err := g.oauth.Client(ctx, tok).Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: github: fetch user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("auth: github: fetch user: HTTP %d: %s", resp.StatusCode, body)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return nil, fmt.Errorf("auth: github: decode user: %w", err)
	}
	if user.ID == 0 || user.Login == "" {
		return nil, errors.New("auth: github: user response has no id or login")
	}
	display := user.Name
	if display == "" {
		display = user.Login
	}
	return &Identity{
		Provider:    g.Name(),
		Subject:     strconv.FormatInt(user.ID, 10),
		Login:       user.Login,
		DisplayName: display,
	}, nil
}
