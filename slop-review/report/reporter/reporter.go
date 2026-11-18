// Package reporter posts finished slop-review runs to the report server
// (see report.IngestPath) and retries whatever a previous invocation
// couldn't deliver.
//
// Reporting failure must never fail a review: every method here that can fail to
// reach the server instead spools the run and reports the problem as a
// warning string, for the caller to fold into the review's own output.
package reporter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-review/report"
)

// requestTimeout bounds one post -- the review itself has already
// finished by the time this runs, so there's nothing else waiting on it
// except the caller's own exit.
const requestTimeout = 30 * time.Second

// maxSpoolRetries bounds how many previously-spooled runs one invocation
// retries, so a large backlog can't turn every later review into a long
// tail of HTTP calls; the rest wait for the next invocation.
const maxSpoolRetries = 20

// spoolDirPerm/spoolFilePerm keep spooled runs -- redacted diffs and
// findings, but still code and its problems -- readable only by the user
// running slop-review.
const (
	spoolDirPerm  = 0o700
	spoolFilePerm = 0o600
)

// httpDoer is the http.Client method Reporter actually uses, so a test can
// substitute a client with different transport/TLS behaviour without a
// production-reachable flag to do the same (see Option/withHTTPClient).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Reporter posts to one report server, identified by baseURL.
type Reporter struct {
	baseURL  string
	tokens   TokenSource
	spoolDir string
	client   httpDoer
	logger   *slog.Logger

	// seen holds every token tokens has returned, for redact: with a token
	// command a message may quote one from an earlier request.
	mu   sync.Mutex
	seen []string
}

// Option customises a Reporter beyond New's required arguments.
type Option func(*Reporter)

// WithTokenSource makes the Reporter ask src for the token before each
// request, instead of using New's fixed one.
func WithTokenSource(src TokenSource) Option {
	return func(r *Reporter) { r.tokens = src }
}

// withHTTPClient overrides the HTTP client a Reporter posts with.
// Unexported deliberately: the only reason to ever want this is a test
// standing up its own TLS certificate, and that must never be reachable
// as a --skip-verify flag in production -- see New's doc comment.
func withHTTPClient(c httpDoer) Option {
	return func(r *Reporter) { r.client = c }
}

// New builds a Reporter that posts to baseURL and spools to spoolDir on
// failure. baseURL must be https, unless its host is loopback (127.0.0.0/8,
// ::1, or literally "localhost") -- the same rule slop's own front-door
// setup applies, so an ingest token never travels in the clear. There is no
// option, anywhere in this package's public API, to skip TLS verification.
func New(baseURL, token, spoolDir string, logHandler slog.Handler, opts ...Option) (*Reporter, error) {
	if err := validateReportURL(baseURL); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(baseURL, "/")
	r := &Reporter{
		baseURL:  base,
		tokens:   StaticToken(token),
		spoolDir: serverSpoolDir(spoolDir, base),
		client: &http.Client{
			Timeout: requestTimeout,
			// The ingest endpoint never redirects, so one is either a
			// misconfiguration or something steering the request -- and a
			// redirect back to the same host (to http, say) keeps the
			// Authorization header. Refused, so the token only ever goes to
			// the URL that was checked above.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errRedirect
			},
		},
		logger: slog.New(logHandler),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

var errRedirect = errors.New("refusing to follow a redirect from the report URL")

// serverSpoolDir keeps each server's spooled runs apart, so a run is only
// ever retried against the server it was made for -- switching
// --report-to must not send the old server's runs to the new one, under the
// new one's token.
func serverSpoolDir(root, baseURL string) string {
	sum := sha256.Sum256([]byte(baseURL))
	return filepath.Join(root, hex.EncodeToString(sum[:8]))
}

func validateReportURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("reporter: invalid report URL %q: %w", redactRawURL(raw), err)
	}
	if u.Host == "" {
		return fmt.Errorf("reporter: report URL %q must have a host", redactUserinfo(u))
	}
	if u.User != nil {
		// Credentials belong to SLOP_REVIEW_REPORT_TOKEN alone -- one typed
		// or pasted into the URL would end up wherever the URL itself does
		// (config files, process listings, this very error) instead of
		// staying wherever secrets are supposed to live.
		return fmt.Errorf("reporter: report URL %q must not carry credentials -- set SLOP_REVIEW_REPORT_TOKEN instead", redactUserinfo(u))
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("reporter: report URL %q must have no query or fragment", redactUserinfo(u))
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("reporter: report URL %q must be https unless the host is loopback", redactUserinfo(u))
	default:
		return fmt.Errorf("reporter: report URL %q must be https", redactUserinfo(u))
	}
}

// redactUserinfo formats u the way an error message may: with any userinfo
// (a stray user:pass@, or a token someone pasted in as the username)
// stripped, so validateReportURL's own errors never do the leaking they
// exist to prevent.
func redactUserinfo(u *url.URL) string {
	if u.User == nil {
		return u.String()
	}
	stripped := *u
	stripped.User = nil
	return stripped.String()
}

// redactRawURL is redactUserinfo for a URL that failed to parse, so
// userinfo can't be pulled off it structurally: a "scheme://user:pass@..."
// prefix is stripped textually instead, on the same best-effort basis the
// rest of validateReportURL's messages apply to a URL that *did* parse.
func redactRawURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	if _, after, ok := strings.Cut(rest, "@"); ok {
		return scheme + "://" + after
	}
	return raw
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Result is what happened when a run was reported. Accepted is set only on
// success; Warning, when non-empty, is what the caller should fold into
// the review's own warnings -- Send always returns cleanly, whatever went
// wrong reaching the server.
type Result struct {
	Accepted *report.Accepted
	Warning  string
}

// permanentError marks a post the server refused in a way retrying can't
// fix -- a malformed or conflicting run (4xx other than 401 and 429) --
// where spooling would only post the same rejection again later.
type permanentError struct {
	msg string
}

func (e *permanentError) Error() string { return e.msg }

// Send posts run. On success it returns the server's Accepted. On any
// failure -- network error, 5xx, 429 -- it spools run to disk for a later
// invocation to retry and returns a warning describing what happened. A
// permanent rejection (4xx other than 429) is warned about but not
// spooled, since retrying it can't help.
func (r *Reporter) Send(ctx context.Context, run *report.Run) Result {
	accepted, err := r.post(ctx, run)
	if err == nil {
		return Result{Accepted: accepted}
	}

	var perm *permanentError
	if errors.As(err, &perm) {
		return Result{Warning: r.redact(fmt.Sprintf("run not reported: %v", err))}
	}

	path := r.spoolPath(run)
	if spoolErr := r.spool(run, path); spoolErr != nil {
		return Result{Warning: r.redact(fmt.Sprintf("run not reported: %v; failed to spool it too: %v", err, spoolErr))}
	}
	return Result{Warning: r.redact(fmt.Sprintf("run not reported: %v; spooled to %s", err, path))}
}

// RetrySpooled retries every run waiting in the spool directory, oldest
// first, up to maxSpoolRetries per invocation. A run that succeeds, or is
// permanently rejected, is removed from the spool (the rejection is
// logged, since nowhere else records it); one that fails transiently is
// left for the next invocation.
func (r *Reporter) RetrySpooled(ctx context.Context) {
	paths, err := r.spooledPaths()
	if err != nil {
		if !os.IsNotExist(err) {
			r.logger.Warn("could not list report spool dir", "dir", r.spoolDir, "err", err)
		}
		return
	}
	if len(paths) > maxSpoolRetries {
		paths = paths[:maxSpoolRetries]
	}
	for _, p := range paths {
		if !r.retrySpooledRun(ctx, p) {
			return
		}
	}
}

// spooledPaths lists every spooled run's path, oldest (by modification
// time, since spool filenames are random run ids, not sequential) first.
func (r *Reporter) spooledPaths() ([]string, error) {
	entries, err := os.ReadDir(r.spoolDir)
	if err != nil {
		return nil, err
	}
	type spoolFile struct {
		path    string
		modTime time.Time
	}
	var files []spoolFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, spoolFile{path: filepath.Join(r.spoolDir, e.Name()), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	return paths, nil
}

// retrySpooledRun reports whether the caller should go on to the next run: not
// after a failure to get a token, which every later run would share.
func (r *Reporter) retrySpooledRun(ctx context.Context, path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		r.logger.Warn("could not read spooled run", "path", path, "err", err)
		return true
	}
	var run report.Run
	if err := json.Unmarshal(body, &run); err != nil {
		r.logger.Warn("dropping unreadable spooled run", "path", path, "err", err)
		r.removeSpooled(path)
		return true
	}

	_, err = r.post(ctx, &run)
	if err == nil {
		r.logger.Info("reported spooled run", "run_id", run.ID)
		r.removeSpooled(path)
		return true
	}

	var perm *permanentError
	if errors.As(err, &perm) {
		r.logger.Warn("dropping spooled run: server permanently rejected it", "run_id", run.ID, "err", err)
		r.removeSpooled(path)
		return true
	}
	r.logger.Info("spooled run still can't be reported, keeping it", "run_id", run.ID, "err", r.redact(err.Error()))
	var tokenErr *tokenError
	return !errors.As(err, &tokenErr)
}

func (r *Reporter) removeSpooled(path string) {
	if err := os.Remove(path); err != nil {
		r.logger.Warn("could not remove spooled run", "path", path, "err", err)
	}
}

func (r *Reporter) spoolPath(run *report.Run) string {
	return filepath.Join(r.spoolDir, run.ID.String()+".json")
}

// spool writes run to path, atomically (write to a temp file, then
// rename) so a crash mid-write never leaves a half-written file that a
// later retry would post truncated -- and with spoolFilePerm from
// creation, since the temp file itself may hold a redacted diff and
// findings.
func (r *Reporter) spool(run *report.Run, path string) error {
	if err := os.MkdirAll(r.spoolDir, spoolDirPerm); err != nil {
		return fmt.Errorf("create spool dir: %w", err)
	}
	// MkdirAll leaves an existing directory's permissions alone, and the
	// spool holds diffs and findings.
	if err := os.Chmod(r.spoolDir, spoolDirPerm); err != nil {
		return fmt.Errorf("restrict spool dir: %w", err)
	}
	body, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode run: %w", err)
	}
	tmp, err := os.CreateTemp(r.spoolDir, "spool-*.tmp")
	if err != nil {
		return fmt.Errorf("create spool temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed below

	if err := tmp.Chmod(spoolFilePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod spool file: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write spool file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close spool file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename spool file: %w", err)
	}
	return nil
}

// post sends one request. A network error or a 5xx/429 response is
// returned as a plain error (transient: worth spooling and retrying); a
// 4xx other than 429 is a *permanentError (not worth retrying).
func (r *Reporter) post(ctx context.Context, run *report.Run) (*report.Accepted, error) {
	body, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("encode run: %w", err)
	}
	token, err := r.bearer(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+report.IngestPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var accepted report.Accepted
		if err := json.Unmarshal(respBody, &accepted); err != nil {
			return nil, fmt.Errorf("parse response (status %d): %w", resp.StatusCode, err)
		}
		// An acknowledgement of some other run is no acknowledgement of this
		// one; treated as a failure, so the run is kept and retried.
		if accepted.RunID != run.ID {
			return nil, fmt.Errorf("server acknowledged run %s, not %s", accepted.RunID, run.ID)
		}
		return &accepted, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, firstLine(respBody))
	// A 401 is a problem with the token, not the run: a revoked or mistyped
	// token. Kept for retry, so fixing the token sends every run made in the
	// meantime instead of losing them.
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("server refused the token (401): %s -- check SLOP_REVIEW_REPORT_TOKEN or SLOP_REVIEW_REPORT_TOKEN_COMMAND", firstLine(respBody))
	case resp.StatusCode >= 400:
		return nil, &permanentError{msg: fmt.Sprintf("server rejected the run (%d): %s", resp.StatusCode, firstLine(respBody))}
	default:
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, firstLine(respBody))
	}
}

// tokenError is a failure to obtain the token, before any request was
// made: the next spooled run would only fail the same way.
type tokenError struct{ err error }

func (e *tokenError) Error() string { return e.err.Error() }
func (e *tokenError) Unwrap() error { return e.err }

// bearer asks the token source for the token to send now, and remembers it
// so redact can scrub it.
func (r *Reporter) bearer(ctx context.Context) (string, error) {
	token, err := r.tokens.Token(ctx)
	if err != nil {
		return "", &tokenError{err: err}
	}
	if token == "" {
		return "", &tokenError{err: errors.New("the token is empty")}
	}
	r.mu.Lock()
	if !slices.Contains(r.seen, token) {
		r.seen = append(r.seen, token)
	}
	r.mu.Unlock()
	return token, nil
}

// redact removes every token sent from a message that may quote the
// server's response: a misbehaving server echoing the Authorization header
// back must not get the token printed into the review's output.
func (r *Reporter) redact(msg string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.seen {
		msg = strings.ReplaceAll(msg, t, "[redacted]")
	}
	return msg
}

func firstLine(body []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(body), []byte("\n"))
	return string(line)
}
