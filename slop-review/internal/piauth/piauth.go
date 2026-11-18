// Package piauth gets OAuth bearer tokens from pi's credential store by
// running `pi auth print-bearer-token`. pi owns the login and the refresh
// (under its own lock on ~/.pi/agent/auth.json), so slop-review never
// handles a refresh token and never writes credentials anywhere.
package piauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ErrNoToken is returned (wrapped, with pi's own explanation) when pi
// can't produce a token: not logged in, refresh failed, pi missing.
var ErrNoToken = errors.New("piauth: no bearer token")

// ErrSameToken means a refresh was asked for after the backend rejected a
// token, and pi handed back that same token -- pi only refreshes a token
// that is near expiry, so it has nothing better to give.
var ErrSameToken = errors.New("piauth: pi returned the token the backend just rejected")

// Options configures a TokenSource. Zero values take the defaults below.
type Options struct {
	// Command is the pi executable and any leading arguments; tests point
	// it at a fake. Defaults to "pi".
	Command []string
	// Provider is pi's provider id. Defaults to "openai", the provider
	// "Sign in with ChatGPT" logs in under.
	Provider string
	// MinExpiry is passed to pi as --min-expiry: the token returned stays
	// valid at least this long (pi refreshes it first if not). Defaults to
	// 20 minutes, comfortably under the ~1h lifetime of a ChatGPT token --
	// pi errors outright if a fresh token can't meet the minimum.
	MinExpiry time.Duration
	// Timeout bounds one pi invocation. Defaults to 30 seconds.
	Timeout time.Duration
	// Now is the clock; tests replace it.
	Now func() time.Time
}

// TokenSource caches the token pi printed and asks pi again once half of
// MinExpiry has passed, so the token in use always has at least
// MinExpiry/2 left -- enough for the longest turn. Safe for concurrent
// use: one pi invocation at a time, and concurrent callers share its
// result.
type TokenSource struct {
	command   []string
	provider  string
	minExpiry time.Duration
	timeout   time.Duration
	now       func() time.Time

	mu        sync.Mutex
	token     string
	fetchedAt time.Time
}

func New(opts *Options) *TokenSource {
	s := &TokenSource{
		command:   opts.Command,
		provider:  opts.Provider,
		minExpiry: opts.MinExpiry,
		timeout:   opts.Timeout,
		now:       opts.Now,
	}
	if len(s.command) == 0 {
		s.command = []string{"pi"}
	}
	if s.provider == "" {
		s.provider = "openai"
	}
	if s.minExpiry <= 0 {
		s.minExpiry = 20 * time.Minute
	}
	if s.timeout <= 0 {
		s.timeout = 30 * time.Second
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Token returns a bearer token with at least MinExpiry/2 of validity left.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Sub(s.fetchedAt) < s.minExpiry/2 {
		return s.token, nil
	}
	return s.fetchLocked(ctx)
}

// Refresh asks pi again after the backend rejected stale. If another
// caller already replaced stale, that newer token is returned without
// running pi. ErrSameToken means pi still holds stale.
func (s *TokenSource) Refresh(ctx context.Context, stale string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.token != stale {
		return s.token, nil
	}
	token, err := s.fetchLocked(ctx)
	if err != nil {
		return "", err
	}
	if token == stale {
		// Don't keep serving a token the backend has rejected: the next
		// caller asks pi again rather than failing on it for MinExpiry/2.
		s.token = ""
		return "", ErrSameToken
	}
	return token, nil
}

// Check confirms, before any review starts, that pi is installed and holds
// a ready Sign in with ChatGPT login for Provider -- so a missing login
// fails the run up front with instructions, not a turn into a review. An
// API key configured for the provider doesn't count: it isn't a
// subscription, and a pi too old to have Sign in with ChatGPT on the
// openai provider can only have that.
func (s *TokenSource) Check(ctx context.Context) error {
	stdout, stderr, runErr := s.run(ctx, "auth", "check", "--provider", s.provider, "--json")
	var status struct {
		Status   string `json:"status"`
		AuthType string `json:"authType"`
		Reason   string `json:"reason"`
	}
	// pi exits non-zero for not_ready/invalid but still prints the JSON,
	// which says more than the exit status does.
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &status); err != nil {
		if runErr != nil {
			if missing := s.missingPi(runErr); missing != nil {
				return missing
			}
			return fmt.Errorf("%w: `%s auth check --provider %s` failed (%v): %s", ErrNoToken, s.command[0], s.provider, runErr, s.scrub(string(stderr)))
		}
		return fmt.Errorf("%w: `%s auth check --provider %s` printed no status (pi 0.99.1 or newer is needed)", ErrNoToken, s.command[0], s.provider)
	}
	if status.Status != "ready" || runErr != nil {
		reason := status.Reason
		if reason == "" && runErr != nil {
			reason = runErr.Error()
		}
		if reason == "" {
			reason = "no reason given"
		}
		return fmt.Errorf("%w: pi reports provider %s as %s (%s)", ErrNoToken, s.provider, status.Status, reason)
	}
	if status.AuthType != "oauth" {
		return fmt.Errorf("%w: pi's provider %s is set up with %s, not Sign in with ChatGPT", ErrNoToken, s.provider, status.AuthType)
	}
	return nil
}

// missingPi explains a pi that couldn't be started, which otherwise reads
// as a lost login with an empty stderr. It returns nil for any other error.
func (s *TokenSource) missingPi(runErr error) error {
	if !errors.Is(runErr, exec.ErrNotFound) && !errors.Is(runErr, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("%w: pi is not installed or not on PATH (%q not found; a supervisor service doesn't read ~/.bashrc: add pi's directory to PATH in <root>/run.env)", ErrNoToken, s.command[0])
}

func (s *TokenSource) run(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.command[0], append(append([]string(nil), s.command[1:]...), args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

func (s *TokenSource) fetchLocked(ctx context.Context) (string, error) {
	stdout, stderr, runErr := s.run(ctx, "auth", "print-bearer-token", "--provider", s.provider, "--min-expiry", formatDuration(s.minExpiry))

	if runErr != nil {
		if missing := s.missingPi(runErr); missing != nil {
			return "", missing
		}
		// stdout is never shown: on failure it's either empty or
		// something that shouldn't be repeated in an error message.
		return "", fmt.Errorf("%w: `%s auth print-bearer-token --provider %s` failed (%v): %s",
			ErrNoToken, s.command[0], s.provider, runErr, s.scrub(string(stderr)))
	}

	token := strings.TrimSpace(string(stdout))
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("%w: `%s auth print-bearer-token --provider %s` printed something other than a single token", ErrNoToken, s.command[0], s.provider)
	}
	s.token, s.fetchedAt = token, s.now()
	return token, nil
}

// pi's --min-expiry takes a whole number of ms, s, m or h.
func formatDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", (d+time.Second-1)/time.Second)
}

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	// jwtShaped matches anything that looks like a JWT, so a token pi
	// might one day echo into an error still never reaches our output.
	jwtShaped = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]*)?`)
)

const maxStderr = 500

// scrub makes pi's stderr safe to put in an error message: colour codes
// stripped, the cached token and anything JWT-shaped redacted, and the
// length bounded.
func (s *TokenSource) scrub(stderr string) string {
	out := ansiEscape.ReplaceAllString(stderr, "")
	if s.token != "" {
		out = strings.ReplaceAll(out, s.token, "[redacted]")
	}
	out = jwtShaped.ReplaceAllString(out, "[redacted]")
	out = strings.TrimSpace(out)
	if len(out) > maxStderr {
		out = out[:maxStderr] + "..."
	}
	if out == "" {
		return "(no output)"
	}
	return out
}
