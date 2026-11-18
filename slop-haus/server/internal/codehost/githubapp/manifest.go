package githubapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// accountRE is a GitHub user or organisation login. It is checked before the
// account goes into a path, which is also why it may not hold a slash or a dot.
var accountRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// slugRE is an App's slug as GitHub names it, checked before it goes into the
// install page's path: GitHub's answer is not trusted to be a path segment.
var slugRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,62})$`)

// codeRE is the shape of a manifest code (a hex string today), checked before
// it goes into the conversion's path.
var codeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type hookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// appManifest is the body of GitHub's create-from-manifest page.
type appManifest struct {
	Name               string         `json:"name"`
	URL                string         `json:"url"`
	HookAttributes     hookAttributes `json:"hook_attributes"`
	RedirectURL        string         `json:"redirect_url"`
	SetupURL           string         `json:"setup_url"`
	Public             bool           `json:"public"`
	DefaultPermissions Permissions    `json:"default_permissions"`
	DefaultEvents      []string       `json:"default_events"`
}

// Where the board's pages that GitHub redirects back to live.
const (
	createdPagePath   = "/workspace/github-app/created"
	installedPagePath = "/workspace/github-app/installed"
)

var errNoBoardURL = errors.New("githubapp: the server does not know the board's public URL")

// AppWebhookEvents are the events the App's webhook must be subscribed to:
// the ones that move a ticket from its change (a review, a merge).
var AppWebhookEvents = []string{"pull_request", "pull_request_review"}

// buildManifest is the App the board asks GitHub to create. With a hookURL
// (this environment's App receive URL) the App delivers AppWebhookEvents
// there; the secret GitHub signs with is set after the App is installed, by
// the webhook configurer. With none the App takes no deliveries. Not public, so it can only be
// installed on the account that owns it. Every URL GitHub is told to send the
// browser back to is the board's own, built here.
func buildManifest(board *url.URL, hookURL string) (*appManifest, error) {
	if board == nil {
		return nil, errNoBoardURL
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("githubapp: random name: %w", err)
	}
	base := strings.TrimRight(board.String(), "/")
	hook, events := hookAttributes{URL: base, Active: false}, []string{}
	if hookURL != "" {
		hook, events = hookAttributes{URL: hookURL, Active: true}, slices.Clone(AppWebhookEvents)
	}
	return &appManifest{
		// App names are unique across GitHub, so a fixed one would be taken
		// by the second tenant.
		Name:               "slop-haus-" + hex.EncodeToString(suffix),
		URL:                base,
		HookAttributes:     hook,
		RedirectURL:        base + createdPagePath,
		SetupURL:           base + installedPagePath,
		Public:             false,
		DefaultPermissions: appPermissions(),
		DefaultEvents:      events,
	}, nil
}

func (m *appManifest) json() (string, error) {
	b, err := json.Marshal(m)
	return string(b), err
}

// createAppURL is GitHub's create-from-manifest page for the account (the
// signed-in user's own for an empty one), carrying the state it returns.
func createAppURL(web *url.URL, account, state string) (string, error) {
	if err := checkAccount(account); err != nil {
		return "", err
	}
	path := "/settings/apps/new"
	if account != "" {
		path = "/organizations/" + account + "/settings/apps/new"
	}
	return withState(web, path, state), nil
}

// checkAccount is separate so a bad name is refused before a state is issued.
func checkAccount(account string) error {
	if account != "" && !accountRE.MatchString(account) {
		return fmt.Errorf("%w: that is not a GitHub account name", ErrInvalidInput)
	}
	return nil
}

// installAppURL is the App's install page, which returns to the manifest's
// setup_url with the state.
func installAppURL(web *url.URL, slug, state string) (string, error) {
	if !slugRE.MatchString(slug) {
		return "", fmt.Errorf("%w: GitHub's App name is not usable", ErrInvalidInput)
	}
	return withState(web, "/apps/"+slug+"/installations/new", state), nil
}

func withState(web *url.URL, path, state string) string {
	u := *web
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = url.Values{"state": {state}}.Encode()
	return u.String()
}
