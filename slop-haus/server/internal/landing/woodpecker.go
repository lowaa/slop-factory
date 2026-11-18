package landing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxWoodpeckerBody bounds what is read from any response.
const maxWoodpeckerBody = 8 << 20

// Pipelines are looked for among the newest of the repo: a head being waited
// on is among the latest pushes, so a bounded scan finds it without walking
// the repo's history.
const (
	pipelinePageSize = 50
	pipelinePages    = 4
)

// WoodpeckerConfig is what WoodpeckerPipelines is built from.
type WoodpeckerConfig struct {
	// URL is the Woodpecker server's address, without a trailing /api.
	URL string
	// Token is the bot user's API token. It is held here and sent only as the
	// Authorization header; no error or log line carries it.
	Token string
	// RepoID is the Woodpecker repo the CI runs for.
	RepoID int64
	// BaseBranch is the branch whose tip PassedOnBase asks about.
	BaseBranch string
	// Timeout bounds one API call.
	Timeout time.Duration
}

// WoodpeckerPipelines is Pipelines over the Woodpecker API. It only reads,
// except Retry and Restart.
type WoodpeckerPipelines struct {
	cfg  WoodpeckerConfig
	http *http.Client
}

func NewWoodpeckerPipelines(cfg *WoodpeckerConfig) (*WoodpeckerPipelines, error) {
	c := *cfg
	if c.URL == "" || c.Token == "" || c.RepoID <= 0 {
		return nil, errors.New("landing: woodpecker needs a URL, a token and a repo id")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.BaseBranch == "" {
		c.BaseBranch = "main"
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return &WoodpeckerPipelines{cfg: c, http: &http.Client{Timeout: c.Timeout}}, nil
}

type wpPipeline struct {
	Number    int64  `json:"number"`
	Status    string `json:"status"`
	Event     string `json:"event"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit"`
	Created   int64  `json:"created"`
	Finished  int64  `json:"finished"`
	Workflows []struct {
		PID   int64  `json:"pid"`
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"workflows"`
}

func (p *wpPipeline) pipeline() (*Pipeline, error) {
	state, err := pipelineState(p.Status)
	if err != nil {
		return nil, err
	}
	out := &Pipeline{ID: p.Number, State: state}
	if p.Created > 0 {
		out.Created = time.Unix(p.Created, 0)
	}
	if p.Finished > 0 {
		out.Finished = time.Unix(p.Finished, 0)
	}
	return out, nil
}

// pipelineState maps Woodpecker's statuses. A status this does not know is an
// error rather than a guess: a wrong "success" or "failure" would land or hand
// back a change on a state nobody read.
func pipelineState(status string) (PipelineState, error) {
	switch status {
	case "created", "pending", "blocked":
		return PipelinePending, nil
	case "running", "started":
		return PipelineRunning, nil
	case "success":
		return PipelineSuccess, nil
	case "failure", "error":
		return PipelineFailure, nil
	case "killed", "declined", "skipped":
		return PipelineCancelled, nil
	}
	return "", fmt.Errorf("landing: unknown woodpecker status %q", status)
}

// Newest is the highest-numbered pipeline on the head; nil when none.
func (w *WoodpeckerPipelines) Newest(ctx context.Context, headSHA string) (*Pipeline, error) {
	raw, err := w.newestRaw(ctx, func(p *wpPipeline) bool { return strings.EqualFold(p.Commit, headSHA) })
	if err != nil || raw == nil {
		return nil, err
	}
	return raw.pipeline()
}

func (w *WoodpeckerPipelines) RepoTimeout(ctx context.Context) (time.Duration, error) {
	var repo struct {
		Timeout int64 `json:"timeout"`
	}
	if err := w.do(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d", w.cfg.RepoID), &repo); err != nil {
		return 0, err
	}
	return time.Duration(repo.Timeout) * time.Minute, nil
}

// Retry runs the head's newest pipeline again; Woodpecker makes it a new
// pipeline on the same commit.
func (w *WoodpeckerPipelines) Retry(ctx context.Context, headSHA string) error {
	raw, err := w.newestRaw(ctx, func(p *wpPipeline) bool { return strings.EqualFold(p.Commit, headSHA) })
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("landing: no pipeline on %s to retry", headSHA)
	}
	// Only a pipeline that ended unsuccessfully is run again. A newer one
	// that is going or green is a retry that already happened (the watchdog's
	// restart, or this call before a crash lost its answer), and running it
	// again would add a second.
	if !retryable(raw.Status) {
		return nil
	}
	return w.do(ctx, http.MethodPost, fmt.Sprintf("/api/repos/%d/pipelines/%d", w.cfg.RepoID, raw.Number), nil)
}

// Restart cancels a pipeline that is lost and runs it again. Woodpecker only
// restarts a pipeline that has ended, and keeps reporting a lost one running,
// so it is cancelled first.
func (w *WoodpeckerPipelines) Restart(ctx context.Context, headSHA string, pipelineID int64) error {
	raw, err := w.newestRaw(ctx, func(p *wpPipeline) bool { return strings.EqualFold(p.Commit, headSHA) })
	if err != nil {
		return err
	}
	// A newer pipeline than the lost one is a restart that already happened
	// (the watchdog's): running another would add a second. One not found in
	// the bounded scan is not evidence of that, so the lost one is restarted.
	if raw != nil && raw.Number > pipelineID {
		return nil
	}
	base := fmt.Sprintf("/api/repos/%d/pipelines/%d", w.cfg.RepoID, pipelineID)
	if err := w.do(ctx, http.MethodPost, base+"/cancel", nil); err != nil {
		return err
	}
	return w.do(ctx, http.MethodPost, base, nil)
}

// PassedOnBase is whether every named check passed in the newest finished push
// or cron pipeline on the base branch that ran all of them. The slop-haus test
// workflows run on main only from the nightly cron, not on every push. Path filters leave a
// workflow out of many pushes, so the newest pipeline often lacks it; the
// look-back is bounded by the pages newestRaw reads.
func (w *WoodpeckerPipelines) PassedOnBase(ctx context.Context, checks []string) (bool, error) {
	if len(checks) == 0 {
		return false, errors.New("landing: no checks to look up")
	}
	var found *wpPipeline
	var detail wpPipeline
	var scanErr error
	err := w.eachPipeline(ctx, func(p *wpPipeline) bool {
		if (p.Event != "push" && p.Event != "cron") || p.Branch != w.cfg.BaseBranch || !finished(p.Status) {
			return true
		}
		d, err := w.detail(ctx, p.Number)
		if err != nil {
			scanErr = err
			return false
		}
		if hasWorkflows(d, checks) {
			found, detail = p, *d
			return false
		}
		return true
	})
	if err == nil {
		err = scanErr
	}
	if err != nil {
		return false, err
	}
	if found == nil {
		return false, fmt.Errorf("landing: no finished pipeline on %s in the last %d has all of %v", w.cfg.BaseBranch, pipelinePages*pipelinePageSize, checks)
	}
	return allSucceeded(&detail, checks), nil
}

func (w *WoodpeckerPipelines) detail(ctx context.Context, number int64) (*wpPipeline, error) {
	var d wpPipeline
	if err := w.do(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines/%d", w.cfg.RepoID, number), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// eachPipeline walks the repo's newest pipelines, newest first, until visit
// returns false or the pages run out.
func (w *WoodpeckerPipelines) eachPipeline(ctx context.Context, visit func(*wpPipeline) bool) error {
	for page := 1; page <= pipelinePages; page++ {
		q := url.Values{"page": {fmt.Sprint(page)}, "perPage": {fmt.Sprint(pipelinePageSize)}}
		var batch []wpPipeline
		if err := w.do(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines?%s", w.cfg.RepoID, q.Encode()), &batch); err != nil {
			return err
		}
		sort.SliceStable(batch, func(i, j int) bool { return batch[i].Number > batch[j].Number })
		for i := range batch {
			if !visit(&batch[i]) {
				return nil
			}
		}
		if len(batch) < pipelinePageSize {
			return nil
		}
	}
	return nil
}

// checkWorkflow is the workflow a check names: the host calls a check
// "ci/woodpecker/<event>/<workflow>".
func checkWorkflow(check string) string { return check[strings.LastIndex(check, "/")+1:] }

func hasWorkflows(p *wpPipeline, checks []string) bool {
	have := map[string]bool{}
	for _, wf := range p.Workflows {
		have[wf.Name] = true
	}
	for _, c := range checks {
		if !have[checkWorkflow(c)] {
			return false
		}
	}
	return true
}

func allSucceeded(p *wpPipeline, checks []string) bool {
	states := map[string]string{}
	for _, wf := range p.Workflows {
		states[wf.Name] = wf.State
	}
	for _, c := range checks {
		if states[checkWorkflow(c)] != "success" {
			return false
		}
	}
	return true
}

// PassedOnCommit is whether every named check passed in the newest finished
// pipeline on the commit.
func (w *WoodpeckerPipelines) PassedOnCommit(ctx context.Context, sha string, checks []string) (bool, error) {
	return w.passed(ctx, checks, func(p *wpPipeline) bool {
		return strings.EqualFold(p.Commit, sha) && finished(p.Status)
	})
}

func retryable(status string) bool {
	switch status {
	case "failure", "error", "killed":
		return true
	}
	return false
}

func finished(status string) bool {
	switch status {
	case "success", "failure", "error":
		return true
	}
	return false
}

// targetPath is the path of a check's target_url that Woodpecker sets:
// /repos/<repo>/pipeline/<number>/<workflow pid>.
var targetPath = regexp.MustCompile(`^/repos/(\d+)/pipeline/(\d+)/(\d+)$`)

// CheckVerdict asks Woodpecker what became of the workflow a check's
// target_url names, for the status the host never heard of. A URL that is not
// this server's and repo's, or a pipeline that is not on headSHA, is
// ErrUnknown: the host's status is then the only word there is.
func (w *WoodpeckerPipelines) CheckVerdict(ctx context.Context, headSHA, targetURL string) (CheckVerdict, error) {
	u, err := url.Parse(targetURL)
	base, baseErr := url.Parse(w.cfg.URL)
	if err != nil || baseErr != nil || !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return "", ErrUnknown
	}
	m := targetPath.FindStringSubmatch(strings.TrimPrefix(u.Path, strings.TrimRight(base.Path, "/")))
	if m == nil {
		return "", ErrUnknown
	}
	repo, _ := strconv.ParseInt(m[1], 10, 64)
	number, _ := strconv.ParseInt(m[2], 10, 64)
	pid, _ := strconv.ParseInt(m[3], 10, 64)
	if repo != w.cfg.RepoID {
		return "", ErrUnknown
	}
	var detail wpPipeline
	if err := w.do(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines/%d", w.cfg.RepoID, number), &detail); err != nil {
		return "", err
	}
	if !strings.EqualFold(detail.Commit, headSHA) {
		return "", ErrUnknown
	}
	for _, wf := range detail.Workflows {
		if wf.PID != pid {
			continue
		}
		switch wf.State {
		case "success":
			return CIPassed, nil
		case "failure", "error", "killed":
			return CIFailed, nil
		}
		return CIRunning, nil
	}
	return "", ErrUnknown
}

// passed says whether each named check's workflow succeeded. The host names a
// check "ci/woodpecker/<event>/<workflow>"; the workflow is its last segment.
// A check with no workflow in the pipeline is unknown, never a pass.
func (w *WoodpeckerPipelines) passed(ctx context.Context, checks []string, match func(*wpPipeline) bool) (bool, error) {
	if len(checks) == 0 {
		return false, errors.New("landing: no checks to look up")
	}
	raw, err := w.newestRaw(ctx, match)
	if err != nil {
		return false, err
	}
	if raw == nil {
		return false, errors.New("landing: no finished pipeline to read the checks from")
	}
	var detail wpPipeline
	if err := w.do(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines/%d", w.cfg.RepoID, raw.Number), &detail); err != nil {
		return false, err
	}
	states := map[string]string{}
	for _, wf := range detail.Workflows {
		states[wf.Name] = wf.State
	}
	for _, check := range checks {
		name := check[strings.LastIndex(check, "/")+1:]
		state, ok := states[name]
		if !ok {
			return false, fmt.Errorf("landing: check %q has no workflow in pipeline %d", check, raw.Number)
		}
		if state != "success" {
			return false, nil
		}
	}
	return true, nil
}

// newestRaw is the highest-numbered pipeline among the repo's newest that
// match, nil when none does.
func (w *WoodpeckerPipelines) newestRaw(ctx context.Context, match func(*wpPipeline) bool) (*wpPipeline, error) {
	var best *wpPipeline
	for page := 1; page <= pipelinePages; page++ {
		q := url.Values{"page": {fmt.Sprint(page)}, "perPage": {fmt.Sprint(pipelinePageSize)}}
		var batch []wpPipeline
		if err := w.do(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines?%s", w.cfg.RepoID, q.Encode()), &batch); err != nil {
			return nil, err
		}
		for i := range batch {
			if match(&batch[i]) && (best == nil || batch[i].Number > best.Number) {
				best = &batch[i]
			}
		}
		// Pipelines are listed newest first, so a match on a page is the
		// newest there is.
		if best != nil || len(batch) < pipelinePageSize {
			break
		}
	}
	return best, nil
}

func (w *WoodpeckerPipelines) do(ctx context.Context, method, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, method, w.cfg.URL+path, nil)
	if err != nil {
		return fmt.Errorf("landing: woodpecker request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	resp, err := w.http.Do(req)
	if err != nil {
		// url.Error carries the URL, which holds no secret; the token is only
		// in a header.
		return fmt.Errorf("landing: woodpecker %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWoodpeckerBody))
	if err != nil {
		return fmt.Errorf("landing: woodpecker %s %s: %w", method, req.URL.Path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("landing: woodpecker %s %s: %s", method, req.URL.Path, resp.Status)
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("landing: woodpecker %s %s: decoding response: %w", method, req.URL.Path, err)
	}
	return nil
}

var _ Pipelines = (*WoodpeckerPipelines)(nil)
