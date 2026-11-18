package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

const (
	perPage = 100
	// maxPages bounds the checks read for one commit. More than this is an
	// error, never a partial list: a failure on a page not read would let
	// the rest look green.
	maxPages = 10
)

// ErrTooManyChecks is a commit with more checks than Checks reads.
var ErrTooManyChecks = fmt.Errorf("github: the commit has more than %d checks", perPage*maxPages)

// ChangesOptions are what NewChanges needs.
type ChangesOptions struct {
	// Client is GitHub's API, built from the server's own configuration.
	Client *githubapp.Client
	// Credentials mints a token per operation. This is the only place a
	// token is asked for.
	Credentials codehost.Credentials
	// UpdatePoll is how UpdateBranch waits for the host to apply an update it
	// accepted: Attempts reads of the head, Interval apart. Zero values mean
	// 15 reads, a second apart.
	UpdatePoll PollOptions
}

// PollOptions bounds a wait on the host.
type PollOptions struct {
	Attempts int
	Interval time.Duration
}

// Changes is codehost.Changes on GitHub: pull requests, with their checks
// as check runs and commit statuses together.
type Changes struct {
	client *githubapp.Client
	creds  codehost.Credentials
	poll   PollOptions
}

var _ codehost.Changes = (*Changes)(nil)

func NewChanges(opts *ChangesOptions) (*Changes, error) {
	if opts.Client == nil || opts.Credentials == nil {
		return nil, errors.New("github: Client and Credentials are required")
	}
	poll := opts.UpdatePoll
	if poll.Attempts <= 0 {
		poll.Attempts = 15
	}
	if poll.Interval <= 0 {
		poll.Interval = time.Second
	}
	return &Changes{client: opts.Client, creds: opts.Credentials, poll: poll}, nil
}

type pullResponse struct {
	Number int64  `json:"number"`
	NodeID string `json:"node_id"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	// MergeCommitSHA is the merge commit of a merged change.
	MergeCommitSHA string `json:"merge_commit_sha"`
	MergedBy       struct {
		Login string `json:"login"`
	} `json:"merged_by"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c *Changes) OpenChange(ctx context.Context, tenant uuid.UUID, req *codehost.OpenRequest) (codehost.ChangeRef, error) {
	if req.Head == "" || req.Base == "" || strings.TrimSpace(req.Title) == "" {
		return codehost.ChangeRef{}, fmt.Errorf("%w: a change needs a head, a base and a title", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, req.Repo, codehost.OpenChange)
	if err != nil {
		return codehost.ChangeRef{}, err
	}
	body := map[string]any{"title": req.Title, "head": req.Head, "base": req.Base, "body": req.Body, "draft": req.Draft}
	var pr pullResponse
	if err := c.client.Do(ctx, http.MethodPost, pullsPath(req.Repo), token.Reveal(), body, http.StatusCreated, &pr); err != nil {
		return codehost.ChangeRef{}, mapError(err)
	}
	if pr.Number <= 0 {
		return codehost.ChangeRef{}, githubapp.ErrBadResponse
	}
	return codehost.ChangeRef{Host: hostName, Owner: strings.ToLower(req.Repo.Owner), Repo: strings.ToLower(req.Repo.Name), Number: pr.Number}, nil
}

const markReadyMutation = `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`

func (c *Changes) MarkReady(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	if err := checkHost(ref); err != nil {
		return err
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.MarkReady)
	if err != nil {
		return err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return err
	}
	if !pr.Draft {
		return nil
	}
	// REST cannot take a pull request out of draft; GraphQL can.
	if err := c.client.GraphQL(ctx, token.Reveal(), markReadyMutation, map[string]any{"id": pr.NodeID}, nil); err != nil {
		return mapError(err)
	}
	return nil
}

const convertToDraftMutation = `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`

var _ codehost.DraftMarker = (*Changes)(nil)

func (c *Changes) MarkDraft(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	if err := checkHost(ref); err != nil {
		return err
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.MarkDraft)
	if err != nil {
		return err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return err
	}
	// A closed or merged pull request cannot be converted, and has nothing
	// to be told apart from.
	if pr.Draft || pr.State == "closed" {
		return nil
	}
	if err := c.client.GraphQL(ctx, token.Reveal(), convertToDraftMutation, map[string]any{"id": pr.NodeID}, nil); err != nil {
		return mapError(err)
	}
	return nil
}

func (c *Changes) CloseChange(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	if err := checkHost(ref); err != nil {
		return err
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.CloseChange)
	if err != nil {
		return err
	}
	// Read first: closing a merged pull request is a 422 that reads like any
	// other refusal, and a closed one needs no write.
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return err
	}
	if pr.Merged {
		return codehost.ErrAlreadyMerged
	}
	if pr.State == "closed" {
		return nil
	}
	var closed pullResponse
	if err := c.client.Do(ctx, http.MethodPatch, pullPath(ref), token.Reveal(), map[string]any{"state": "closed"}, http.StatusOK, &closed); err != nil {
		// GitHub refuses to close a pull request that merged since the read
		// as a 422; look again so that is not reported as a plain refusal.
		err = mapError(err)
		if errors.Is(err, codehost.ErrRejected) {
			if again, getErr := c.pull(ctx, token, ref); getErr == nil && again.Merged {
				return codehost.ErrAlreadyMerged
			}
		}
		return err
	}
	// A merge between the read and the write is not a close.
	if closed.Merged {
		return codehost.ErrAlreadyMerged
	}
	return nil
}

func (c *Changes) RetargetChange(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, base string) error {
	if base == "" {
		return fmt.Errorf("%w: a change needs a base", codehost.ErrRejected)
	}
	return c.updatePull(ctx, tenant, ref, codehost.RetargetChange, "base", base, func(pr *pullResponse) string { return pr.Base.Ref })
}

func (c *Changes) RetitleChange(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, title string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%w: a change needs a title", codehost.ErrRejected)
	}
	return c.updatePull(ctx, tenant, ref, codehost.RetitleChange, "title", title, func(pr *pullResponse) string { return pr.Title })
}

// updatePull sets one field of a pull request unless it already holds want.
// The read first makes a repeat a no-op instead of a write.
func (c *Changes) updatePull(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, op codehost.Operation, field, want string, current func(*pullResponse) string) error {
	if err := checkHost(ref); err != nil {
		return err
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), op)
	if err != nil {
		return err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return err
	}
	if current(pr) == want {
		return nil
	}
	if err := c.client.Do(ctx, http.MethodPatch, pullPath(ref), token.Reveal(), map[string]any{field: want}, http.StatusOK, &pullResponse{}); err != nil {
		return mapError(err)
	}
	return nil
}

func (c *Changes) DeleteBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch string) error {
	if branch == "" {
		return fmt.Errorf("%w: a branch needs a name", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.DeleteBranch)
	if err != nil {
		return err
	}
	err = c.client.Do(ctx, http.MethodDelete, fmt.Sprintf("%s/git/refs/heads/%s", repoPath(repo), escapeRef(branch)), token.Reveal(), nil, http.StatusNoContent, nil)
	// GitHub answers a missing ref with a 422 "Reference does not exist". A
	// 404 is a missing or inaccessible repository, which is not a deleted
	// branch, so it stays an error.
	var apiErr *githubapp.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(apiErr.Message), "does not exist") {
		return nil
	}
	if err != nil {
		return mapError(err)
	}
	return nil
}

func (c *Changes) Checks(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) (*codehost.Checks, error) {
	if err := checkHost(ref); err != nil {
		return nil, err
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.ReadChecks)
	if err != nil {
		return nil, err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return nil, err
	}
	// Both kinds are read for this SHA, which Merge is then given: a push
	// after this read moves the head and the merge is refused.
	sha := pr.Head.SHA
	if sha == "" {
		return nil, githubapp.ErrBadResponse
	}
	runs, err := c.checkRuns(ctx, token, ref, sha)
	if err != nil {
		return nil, err
	}
	statuses, err := c.statuses(ctx, token, ref, sha)
	if err != nil {
		return nil, err
	}
	checks := codehost.NewChecks(sha, pr.Base.Ref, append(runs, statuses...))
	checks.Title = pr.Title
	return checks, nil
}

func (c *Changes) BranchChecks(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch string) (*codehost.Checks, error) {
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.ReadChecks)
	if err != nil {
		return nil, err
	}
	head, err := c.BranchHead(ctx, tenant, repo, branch)
	if err != nil {
		return nil, err
	}
	ref := codehost.ChangeRef{Host: hostName, Owner: strings.ToLower(repo.Owner), Repo: strings.ToLower(repo.Name)}
	runs, err := c.checkRuns(ctx, token, ref, head.SHA)
	if err != nil {
		return nil, err
	}
	statuses, err := c.statuses(ctx, token, ref, head.SHA)
	if err != nil {
		return nil, err
	}
	return codehost.NewChecks(head.SHA, "", append(runs, statuses...)), nil
}

func (c *Changes) ChangeState(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) (*codehost.ChangeState, error) {
	if err := checkHost(ref); err != nil {
		return nil, err
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.ReadChecks)
	if err != nil {
		return nil, err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return nil, err
	}
	if pr.Base.Ref == "" || pr.Head.SHA == "" {
		return nil, githubapp.ErrBadResponse
	}
	return &codehost.ChangeState{Merged: pr.Merged, Base: pr.Base.Ref, HeadSHA: pr.Head.SHA,
		MergeSHA: pr.MergeCommitSHA, MergedBy: pr.MergedBy.Login}, nil
}

// BranchContains compares sha...branch: the branch is "ahead" of or
// "identical" to a commit in its history, "diverged" or "behind" otherwise.
func (c *Changes) BranchContains(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) (bool, error) {
	if branch == "" || !isSHA(sha) {
		return false, fmt.Errorf("%w: a branch comparison needs a branch and a commit SHA", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.CompareBase)
	if err != nil {
		return false, err
	}
	var cmp struct {
		Status string `json:"status"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s?per_page=1", repoPath(repo), url.PathEscape(sha), escapeRef(branch))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &cmp); err != nil {
		return false, mapError(err)
	}
	switch cmp.Status {
	case "ahead", "identical":
		return true, nil
	case "behind", "diverged":
		return false, nil
	}
	return false, githubapp.ErrBadResponse
}

func (c *Changes) CompareBase(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, headSHA string) (*codehost.BaseComparison, error) {
	if err := checkHost(ref); err != nil {
		return nil, err
	}
	if headSHA == "" {
		return nil, fmt.Errorf("%w: a comparison needs the head SHA", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.CompareBase)
	if err != nil {
		return nil, err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return nil, err
	}
	if pr.Base.Ref == "" {
		return nil, githubapp.ErrBadResponse
	}
	// behind_by counts the base's commits the head does not have. A head
	// that is not a descendant of the base at all still reports how far
	// behind it is, so 0 is the only answer that means "contains the tip".
	var cmp struct {
		BehindBy *int `json:"behind_by"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s?per_page=1", repoPath(ref.InRepo()), escapeRef(pr.Base.Ref), url.PathEscape(headSHA))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &cmp); err != nil {
		return nil, mapError(err)
	}
	if cmp.BehindBy == nil {
		return nil, githubapp.ErrBadResponse
	}
	return &codehost.BaseComparison{Base: pr.Base.Ref, ContainsTip: *cmp.BehindBy == 0}, nil
}

const (
	diffPerPage = 100
	// maxDiffPages bounds the files read: GitHub lists at most 300 files of a
	// comparison, so a full last page may be a diff that was cut short, and
	// is refused rather than compared.
	maxDiffPages = 3
)

type compareFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Patch            string `json:"patch"`
}

type comparePage struct {
	BaseCommit      struct{ SHA string } `json:"base_commit"`
	MergeBaseCommit struct{ SHA string } `json:"merge_base_commit"`
	Files           []compareFile        `json:"files"`
}

func (c *Changes) Diff(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, headSHA string) (*codehost.Diff, error) {
	if err := checkHost(ref); err != nil {
		return nil, err
	}
	if headSHA == "" {
		return nil, fmt.Errorf("%w: a diff needs the head SHA", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.CompareBase)
	if err != nil {
		return nil, err
	}
	pr, err := c.pull(ctx, token, ref)
	if err != nil {
		return nil, err
	}
	if pr.Base.Ref == "" {
		return nil, githubapp.ErrBadResponse
	}
	out, err := c.compare(ctx, token.Reveal(), ref.InRepo(), escapeRef(pr.Base.Ref), headSHA)
	if err != nil {
		return nil, err
	}
	out.Base = pr.Base.Ref
	return out, nil
}

func (c *Changes) CompareCommits(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, fromSHA, toSHA string) (*codehost.Diff, error) {
	if err := checkHost(ref); err != nil {
		return nil, err
	}
	if !isSHA(fromSHA) || !isSHA(toSHA) {
		return nil, fmt.Errorf("%w: a comparison of commits needs two commit SHAs", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, ref.InRepo(), codehost.CompareBase)
	if err != nil {
		return nil, err
	}
	return c.compare(ctx, token.Reveal(), ref.InRepo(), fromSHA, toSHA)
}

// compare reads GitHub's three-dot comparison base...head page by page. The
// first page may name the base by branch; the base commit it reports is then
// what later pages are read against, so a push to the base between pages
// cannot make the pages describe different diffs.
func (c *Changes) compare(ctx context.Context, token string, repo codehost.Repo, base, head string) (*codehost.Diff, error) {
	out := &codehost.Diff{}
	for page := 1; page <= maxDiffPages; page++ {
		var p comparePage
		path := fmt.Sprintf("%s/compare/%s...%s?per_page=%d&page=%d", repoPath(repo), base, url.PathEscape(head), diffPerPage, page)
		if err := c.client.Do(ctx, http.MethodGet, path, token, nil, http.StatusOK, &p); err != nil {
			return nil, mapError(err)
		}
		if p.BaseCommit.SHA == "" || p.MergeBaseCommit.SHA == "" {
			return nil, githubapp.ErrBadResponse
		}
		if page == 1 {
			out.BaseSHA, out.MergeBaseSHA = p.BaseCommit.SHA, p.MergeBaseCommit.SHA
			base = url.PathEscape(p.BaseCommit.SHA)
		} else if p.BaseCommit.SHA != out.BaseSHA || p.MergeBaseCommit.SHA != out.MergeBaseSHA {
			return nil, codehost.ErrDiffChanged
		}
		for _, f := range p.Files {
			out.Files = append(out.Files, codehost.FileDiff{Path: f.Filename, Status: diffStatus(f.Status), Patch: f.Patch, PreviousPath: f.PreviousFilename})
		}
		// GitHub's own documentation has the files of a paged comparison
		// all on its first page, up to its cap, rather than spread across
		// pages: a cap reached on any one page may be a list cut short.
		if len(out.Files) >= diffPerPage*maxDiffPages {
			break
		}
		if len(p.Files) < diffPerPage {
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: the comparison has %d files or more, which GitHub may have cut short", codehost.ErrDiffUnsupported, diffPerPage*maxDiffPages)
}

// isSHA is a full hexadecimal commit name, so nothing else -- a branch, a
// range, a path -- can be put into a comparison's URL in its place.
func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// diffStatus: only what a patch describes in full maps to a comparable
// status; GitHub's renamed, copied and changed (mode only) are told apart, and
// unchanged, and any status it adds later, are other.
func diffStatus(s string) codehost.DiffStatus {
	switch s {
	case "added":
		return codehost.DiffAdded
	case "modified":
		return codehost.DiffModified
	case "removed":
		return codehost.DiffRemoved
	case "renamed":
		return codehost.DiffRenamed
	case "copied":
		return codehost.DiffCopied
	case "changed":
		return codehost.DiffModeChanged
	}
	return codehost.DiffOther
}

// escapeRef escapes a branch name for a path, keeping its slashes.
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (c *Changes) DefaultBranchHead(ctx context.Context, tenant uuid.UUID, repo codehost.Repo) (*codehost.BranchHead, error) {
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.ReadBranch)
	if err != nil {
		return nil, err
	}
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.client.Do(ctx, http.MethodGet, repoPath(repo), token.Reveal(), nil, http.StatusOK, &info); err != nil {
		return nil, mapError(err)
	}
	if info.DefaultBranch == "" {
		return nil, githubapp.ErrBadResponse
	}
	// Named by the branch just read, so the answer is that branch's head at
	// the time of this second read; a push in between only makes it newer.
	var commit struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("%s/commits/%s", repoPath(repo), escapeRef(info.DefaultBranch))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &commit); err != nil {
		return nil, mapError(err)
	}
	if commit.SHA == "" {
		return nil, githubapp.ErrBadResponse
	}
	return &codehost.BranchHead{Branch: info.DefaultBranch, SHA: commit.SHA}, nil
}

func (c *Changes) BranchHead(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch string) (*codehost.BranchHead, error) {
	if branch == "" {
		return nil, fmt.Errorf("%w: a branch needs a name", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.ReadBranch)
	if err != nil {
		return nil, err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("%s/commits/%s", repoPath(repo), escapeRef(branch))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &commit); err != nil {
		// The commits endpoint takes the branch as a ref, and answers an
		// unknown one with a 422 "No commit found for SHA", not a 404.
		var apiErr *githubapp.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity && strings.Contains(apiErr.Message, "No commit found") {
			return nil, fmt.Errorf("%w: %s", codehost.ErrChangeNotFound, apiErr.Message)
		}
		return nil, mapError(err)
	}
	if commit.SHA == "" {
		return nil, githubapp.ErrBadResponse
	}
	return &codehost.BranchHead{Branch: branch, SHA: commit.SHA}, nil
}

// TreeOf reads the commit object itself, not a branch, so the answer is for
// exactly the commit asked about whatever moves afterwards.
func (c *Changes) TreeOf(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, sha string) (string, error) {
	if !isSHA(sha) {
		return "", fmt.Errorf("%w: %q is not a full commit hash", codehost.ErrRejected, sha)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.ReadBranch)
	if err != nil {
		return "", err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.client.Do(ctx, http.MethodGet, repoPath(repo)+"/git/commits/"+sha, token.Reveal(), nil, http.StatusOK, &commit); err != nil {
		return "", mapError(err)
	}
	if !isSHA(commit.Tree.SHA) {
		return "", githubapp.ErrBadResponse
	}
	return commit.Tree.SHA, nil
}

func (c *Changes) CreateBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) error {
	have, err := c.createRef(ctx, tenant, repo, "heads", branch, sha)
	if err != nil {
		return err
	}
	if have != sha {
		return &codehost.BranchConflictError{Branch: branch, Have: have, Want: sha}
	}
	return nil
}

// EnsureBranch is CreateBranch that accepts a branch wherever it is: an
// attempt's branch is re-claimed with an earlier agent's work on it, and
// moving it would destroy that work.
func (c *Changes) EnsureBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) error {
	_, err := c.createRef(ctx, tenant, repo, "heads", branch, sha)
	return err
}

// createRef creates the ref (a branch under "heads", a tag under "tags") at
// sha unless it exists, and returns the commit it is at: sha for one it made,
// or wherever an existing one is.
func (c *Changes) createRef(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, namespace, branch, sha string) (string, error) {
	if branch == "" || sha == "" {
		return "", fmt.Errorf("%w: a ref needs a name and a commit", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.CreateBranch)
	if err != nil {
		return "", err
	}
	body := map[string]any{"ref": "refs/" + namespace + "/" + branch, "sha": sha}
	var created struct{}
	err = c.client.Do(ctx, http.MethodPost, repoPath(repo)+"/git/refs", token.Reveal(), body, http.StatusCreated, &created)
	if err == nil {
		return sha, nil
	}
	// 422 is "Reference already exists" among other things: read the branch
	// to tell a retry of this create from a branch somebody put elsewhere.
	var apiErr *githubapp.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		return "", mapError(err)
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	path := fmt.Sprintf("%s/git/ref/%s/%s", repoPath(repo), namespace, escapeRef(branch))
	if getErr := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &ref); getErr != nil {
		// A branch that is not there is the refusal's own reason (a bad sha,
		// say); any other failure of the read is what the caller must see.
		var getAPI *githubapp.APIError
		if errors.As(getErr, &getAPI) && getAPI.Status == http.StatusNotFound {
			return "", mapError(err)
		}
		return "", mapError(getErr)
	}
	if ref.Object.SHA == "" {
		return "", githubapp.ErrBadResponse
	}
	return ref.Object.SHA, nil
}

func (c *Changes) CreateTag(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, name, sha string) error {
	have, err := c.createRef(ctx, tenant, repo, "tags", name, sha)
	if err != nil {
		return err
	}
	if have != sha {
		return &codehost.TagConflictError{Tag: name, Have: have, Want: sha}
	}
	return nil
}

// maxTagDepth bounds the chain of annotated tags ResolveRef follows, so a
// host answering with a cycle cannot hold the call.
const maxTagDepth = 5

// ResolveRef reads a hash-looking ref as a commit and any other as a tag, and
// finally asks whether the name is a branch, to refuse it with a reason
// rather than a bare "not found".
func (c *Changes) ResolveRef(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, ref string) (*codehost.ResolvedRef, error) {
	if ref == "" || strings.ContainsAny(ref, " \t\n?*[\\^~:") {
		return nil, fmt.Errorf("%w: %q is not a ref name", codehost.ErrRejected, ref)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.ReadBranch)
	if err != nil {
		return nil, err
	}
	get := func(path string, out any) (found bool, err error) {
		err = c.client.Do(ctx, http.MethodGet, repoPath(repo)+path, token.Reveal(), nil, http.StatusOK, out)
		if err == nil {
			return true, nil
		}
		var apiErr *githubapp.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound ||
			// An unknown hash is a 422 here; an ambiguous short one is a 422 too, with another message.
			apiErr.Status == http.StatusUnprocessableEntity && strings.Contains(apiErr.Message, "No commit found")) {
			return false, nil
		}
		return false, mapError(err)
	}

	// A hash-looking ref is a commit before it is anything else, so a tag or
	// branch somebody named like a hash cannot stand in for the commit the
	// filer meant. A branch so named is refused rather than resolved.
	if hexRef.MatchString(ref) {
		var head struct{}
		if found, err := get("/git/ref/heads/"+escapeRef(ref), &head); err != nil {
			return nil, err
		} else if found {
			return nil, fmt.Errorf("%w: %s", codehost.ErrRefIsBranch, ref)
		}
		var commit struct {
			SHA string `json:"sha"`
		}
		if found, err := get("/commits/"+url.PathEscape(ref), &commit); err != nil {
			return nil, err
		} else if found {
			if !isSHA(commit.SHA) {
				return nil, githubapp.ErrBadResponse
			}
			return &codehost.ResolvedRef{SHA: commit.SHA, Kind: codehost.RefCommit}, nil
		}
	}

	var tag struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if found, err := get("/git/ref/tags/"+escapeRef(ref), &tag); err != nil {
		return nil, err
	} else if found {
		obj := tag.Object
		for depth := 0; obj.Type == "tag"; depth++ {
			if depth >= maxTagDepth {
				return nil, fmt.Errorf("%w: tag %s is nested too deeply", codehost.ErrRejected, ref)
			}
			var next struct {
				Object struct {
					Type string `json:"type"`
					SHA  string `json:"sha"`
				} `json:"object"`
			}
			if found, err := get("/git/tags/"+url.PathEscape(obj.SHA), &next); err != nil {
				return nil, err
			} else if !found {
				return nil, fmt.Errorf("%w: tag %s", codehost.ErrChangeNotFound, ref)
			}
			obj = next.Object
		}
		if obj.Type != "commit" || obj.SHA == "" {
			return nil, fmt.Errorf("%w: tag %s does not name a commit", codehost.ErrRejected, ref)
		}
		return &codehost.ResolvedRef{SHA: obj.SHA, Kind: codehost.RefTag}, nil
	}

	var head struct{}
	if found, err := get("/git/ref/heads/"+escapeRef(ref), &head); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("%w: %s", codehost.ErrRefIsBranch, ref)
	}
	return nil, fmt.Errorf("%w: no tag or commit %q", codehost.ErrChangeNotFound, ref)
}

// hexRef is a full or abbreviated commit hash.
var hexRef = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// TakesInBranchPast: the commits of branch that head contains are the history
// of the merge base of the two, so head took in something past since exactly
// when that merge base is not since or an ancestor of it. Two comparisons, and
// the second is skipped for the common answer, a head branched from since.
func (c *Changes) TakesInBranchPast(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, since, head string) (bool, error) {
	if branch == "" || head == "" || !isSHA(since) {
		return false, fmt.Errorf("%w: a comparison against a branch needs the branch, a commit SHA and a head", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.CompareBase)
	if err != nil {
		return false, err
	}
	var cmp struct {
		MergeBaseCommit struct{ SHA string } `json:"merge_base_commit"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s?per_page=1", repoPath(repo), escapeRef(branch), url.PathEscape(head))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &cmp); err != nil {
		return false, mapError(err)
	}
	mergeBase := cmp.MergeBaseCommit.SHA
	if mergeBase == "" {
		return false, githubapp.ErrBadResponse
	}
	if mergeBase == since {
		return false, nil
	}
	// since...mergeBase: "behind" or "identical" says mergeBase is in since's
	// history. Anything else (ahead, diverged) is a main commit past since.
	var anc struct {
		Status string `json:"status"`
	}
	path = fmt.Sprintf("%s/compare/%s...%s?per_page=1", repoPath(repo), url.PathEscape(since), url.PathEscape(mergeBase))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &anc); err != nil {
		return false, mapError(err)
	}
	switch anc.Status {
	case "behind", "identical":
		return false, nil
	case "ahead", "diverged":
		return true, nil
	}
	return false, githubapp.ErrBadResponse
}

// CommitsAhead reads ahead_by of the comparison since...branch.
func (c *Changes) CommitsAhead(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, since, branch string) (int, error) {
	if branch == "" || !isSHA(since) {
		return 0, fmt.Errorf("%w: a comparison against a commit needs a commit SHA and a branch", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.CompareBase)
	if err != nil {
		return 0, err
	}
	var cmp struct {
		AheadBy *int `json:"ahead_by"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s?per_page=1", repoPath(repo), url.PathEscape(since), escapeRef(branch))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &cmp); err != nil {
		return 0, mapError(err)
	}
	if cmp.AheadBy == nil {
		return 0, githubapp.ErrBadResponse
	}
	return *cmp.AheadBy, nil
}

// CompareBranches reads ahead_by and behind_by of the comparison base...branch.
func (c *Changes) CompareBranches(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, base, branch string) (*codehost.BranchComparison, error) {
	if base == "" || branch == "" {
		return nil, fmt.Errorf("%w: a comparison of branches needs two branch names", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.CompareBase)
	if err != nil {
		return nil, err
	}
	var cmp struct {
		AheadBy  *int `json:"ahead_by"`
		BehindBy *int `json:"behind_by"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s?per_page=1", repoPath(repo), escapeRef(base), escapeRef(branch))
	if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &cmp); err != nil {
		return nil, mapError(err)
	}
	if cmp.AheadBy == nil || cmp.BehindBy == nil {
		return nil, githubapp.ErrBadResponse
	}
	return &codehost.BranchComparison{Ahead: *cmp.AheadBy, Behind: *cmp.BehindBy}, nil
}

// MergeBranch is GitHub's merges endpoint: 201 with the merge commit, 204
// when there was nothing to merge, 409 on a conflict.
func (c *Changes) MergeBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, into, from string) (*codehost.BranchMerge, error) {
	if into == "" || from == "" {
		return nil, fmt.Errorf("%w: a branch merge needs two branch names", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, repo, codehost.MergeBranch)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"base": into, "head": from, "commit_message": fmt.Sprintf("Merge branch '%s' into %s", from, into)}
	var resp struct {
		SHA string `json:"sha"`
	}
	err = c.client.Do(ctx, http.MethodPost, repoPath(repo)+"/merges", token.Reveal(), body, http.StatusCreated, &resp)
	var apiErr *githubapp.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusNoContent:
			head, err := c.BranchHead(ctx, tenant, repo, into)
			if err != nil {
				return nil, err
			}
			return &codehost.BranchMerge{SHA: head.SHA, UpToDate: true}, nil
		case http.StatusConflict:
			return nil, fmt.Errorf("%w: %s", codehost.ErrMergeConflict, apiErr.Message)
		}
	}
	if err != nil {
		return nil, mapError(err)
	}
	if resp.SHA == "" {
		return nil, githubapp.ErrBadResponse
	}
	return &codehost.BranchMerge{SHA: resp.SHA}, nil
}

// UpdateBranch is GitHub's update-branch endpoint. It answers 202 before the
// merge commit exists, so the new head is read back from the pull request,
// bounded by the poll options. Any head other than the one asked for counts
// as the update landing, so a push by someone else in that window would be
// reported as the merge commit; callers hold the integration lock, which is
// what keeps pushes to the branch out. A 422 carries three different refusals, told
// apart by GitHub's message: the only signal it gives.
func (c *Changes) UpdateBranch(ctx context.Context, tenant uuid.UUID, req *codehost.UpdateBranchRequest) (*codehost.BranchUpdate, error) {
	if err := checkHost(req.Change); err != nil {
		return nil, err
	}
	if req.HeadSHA == "" {
		return nil, fmt.Errorf("%w: a branch update needs the head SHA it is for", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, req.Change.InRepo(), codehost.UpdateChangeBranch)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"expected_head_sha": req.HeadSHA}
	err = c.client.Do(ctx, http.MethodPut, pullPath(req.Change)+"/update-branch", token.Reveal(), body, http.StatusAccepted, nil)
	var apiErr *githubapp.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
		msg := strings.ToLower(apiErr.Message)
		switch {
		case strings.Contains(msg, "expected head sha"):
			return nil, fmt.Errorf("%w: %s", codehost.ErrHeadMoved, apiErr.Message)
		case strings.Contains(msg, "merge conflict"):
			return nil, fmt.Errorf("%w: %s", codehost.ErrMergeConflict, apiErr.Message)
		case strings.Contains(msg, "no new commits"):
			return &codehost.BranchUpdate{SHA: req.HeadSHA, UpToDate: true}, nil
		}
	}
	if err != nil {
		return nil, mapError(err)
	}
	for i := 0; i < c.poll.Attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.poll.Interval):
			}
		}
		pr, err := c.pull(ctx, token, req.Change)
		if err != nil {
			return nil, err
		}
		if pr.Head.SHA != "" && pr.Head.SHA != req.HeadSHA {
			return &codehost.BranchUpdate{SHA: pr.Head.SHA}, nil
		}
	}
	return nil, codehost.ErrUpdatePending
}

func (c *Changes) Merge(ctx context.Context, tenant uuid.UUID, req *codehost.MergeRequest) (*codehost.Merged, error) {
	if err := checkHost(req.Change); err != nil {
		return nil, err
	}
	// An empty sha would be a merge of whatever the head is, which is what
	// the sha is there to prevent.
	if req.HeadSHA == "" {
		return nil, fmt.Errorf("%w: a merge needs the head SHA it is for", codehost.ErrRejected)
	}
	token, err := c.creds.TokenFor(ctx, tenant, req.Change.InRepo(), codehost.MergeChange)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"merge_method": "merge", "sha": req.HeadSHA, "commit_title": req.Subject}
	if req.Body != "" {
		body["commit_message"] = req.Body
	}
	var resp struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	path := pullPath(req.Change) + "/merge"
	if err := c.client.Do(ctx, http.MethodPut, path, token.Reveal(), body, http.StatusOK, &resp); err != nil {
		var apiErr *githubapp.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Status {
			case http.StatusConflict:
				return nil, fmt.Errorf("%w: %s", codehost.ErrHeadMoved, apiErr.Message)
			case http.StatusMethodNotAllowed:
				// GitHub answers 405 rather than 409 for a stale sha when the
				// pull request's mergeability is still being recomputed after
				// a push, so a 405 is told apart by reading the head back.
				if pr, perr := c.pull(ctx, token, req.Change); perr == nil && pr.Head.SHA != "" && pr.Head.SHA != req.HeadSHA {
					return nil, fmt.Errorf("%w: %s", codehost.ErrHeadMoved, apiErr.Message)
				}
				return nil, fmt.Errorf("%w: %s", codehost.ErrNotMergeable, apiErr.Message)
			}
		}
		return nil, mapError(err)
	}
	if !resp.Merged {
		return nil, fmt.Errorf("%w: GitHub did not merge it", codehost.ErrNotMergeable)
	}
	return &codehost.Merged{SHA: resp.SHA}, nil
}

func (c *Changes) pull(ctx context.Context, token codehost.Token, ref codehost.ChangeRef) (*pullResponse, error) {
	var pr pullResponse
	if err := c.client.Do(ctx, http.MethodGet, pullPath(ref), token.Reveal(), nil, http.StatusOK, &pr); err != nil {
		return nil, mapError(err)
	}
	return &pr, nil
}

// entry is one check as a listing gives it. ID tells a check seen twice,
// which is what a listing that shifted under the reader looks like.
type entry struct {
	ID    int64
	Check codehost.Check
}

// ErrChecksChanged is checks that changed while they were being read: the
// listing shifted between pages, so the pages do not add up to one list.
// Nothing partial is returned; read again.
var ErrChecksChanged = errors.New("github: the checks changed while they were being read")

// collect reads every page of a check listing, newest first and live. A
// check that starts, or a status that is set, while the pages are read
// shifts them, so a page can repeat what the last one had and miss
// something new -- a failure not seen would read as green. Anything that
// does not add up (a repeated check, a total that moved, a page that is
// short of the total) is ErrChecksChanged rather than a guess. A check that
// only appears after the last page is read is not caught here, and cannot
// be: it is what reading the checks again, as late as possible before the
// merge, is for.
func collect(fetch func(page int) (total int, entries []entry, err error)) ([]codehost.Check, error) {
	seen := map[int64]bool{}
	var out []codehost.Check
	firstTotal := 0
	for page := 1; page <= maxPages; page++ {
		total, entries, err := fetch(page)
		if err != nil {
			return nil, err
		}
		if page == 1 {
			firstTotal = total
		} else if total != firstTotal {
			return nil, ErrChecksChanged
		}
		for _, e := range entries {
			if seen[e.ID] {
				return nil, ErrChecksChanged
			}
			seen[e.ID] = true
			out = append(out, e.Check)
		}
		if len(out) == total {
			return out, nil
		}
		if len(out) > total || len(entries) == 0 {
			return nil, ErrChecksChanged
		}
	}
	return nil, ErrTooManyChecks
}

func (c *Changes) checkRuns(ctx context.Context, token codehost.Token, ref codehost.ChangeRef, sha string) ([]codehost.Check, error) {
	return collect(func(page int) (int, []entry, error) {
		var p struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		path := fmt.Sprintf("%s/commits/%s/check-runs?filter=latest&per_page=%d&page=%d", repoPath(ref.InRepo()), url.PathEscape(sha), perPage, page)
		if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &p); err != nil {
			return 0, nil, mapError(err)
		}
		entries := make([]entry, len(p.CheckRuns))
		for i, r := range p.CheckRuns {
			entries[i] = entry{r.ID, codehost.Check{Name: r.Name, State: checkRunState(r.Status, r.Conclusion)}}
		}
		return p.TotalCount, entries, nil
	})
}

func (c *Changes) statuses(ctx context.Context, token codehost.Token, ref codehost.ChangeRef, sha string) ([]codehost.Check, error) {
	return collect(func(page int) (int, []entry, error) {
		var p struct {
			TotalCount int `json:"total_count"`
			Statuses   []struct {
				ID        int64  `json:"id"`
				Context   string `json:"context"`
				State     string `json:"state"`
				TargetURL string `json:"target_url"`
			} `json:"statuses"`
		}
		path := fmt.Sprintf("%s/commits/%s/status?per_page=%d&page=%d", repoPath(ref.InRepo()), url.PathEscape(sha), perPage, page)
		if err := c.client.Do(ctx, http.MethodGet, path, token.Reveal(), nil, http.StatusOK, &p); err != nil {
			return 0, nil, mapError(err)
		}
		entries := make([]entry, len(p.Statuses))
		for i, s := range p.Statuses {
			entries[i] = entry{s.ID, codehost.Check{Name: s.Context, State: statusState(s.State), TargetURL: s.TargetURL}}
		}
		return p.TotalCount, entries, nil
	})
}

// checkRunState: anything not yet completed is pending; of the conclusions,
// only success passes, neutral and skipped do not block, and every other one
// -- including one GitHub adds later -- fails, so an unknown can never pass.
func checkRunState(status, conclusion string) codehost.CheckState {
	if status != "completed" {
		return codehost.CheckPending
	}
	switch conclusion {
	case "success":
		return codehost.CheckSuccess
	case "neutral", "skipped":
		return codehost.CheckNeutral
	}
	return codehost.CheckFailure
}

// statusState: GitHub's commit status states are pending, success, failure
// and error. An unknown one is pending, which does not pass either.
func statusState(state string) codehost.CheckState {
	switch state {
	case "success":
		return codehost.CheckSuccess
	case "failure", "error":
		return codehost.CheckFailure
	}
	return codehost.CheckPending
}

func checkHost(ref codehost.ChangeRef) error {
	if ref.Host != hostName {
		return fmt.Errorf("github: %q is not a %s change", ref.Host, hostName)
	}
	return nil
}

func repoPath(r codehost.Repo) string {
	return "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name)
}

func pullsPath(r codehost.Repo) string { return repoPath(r) + "/pulls" }

func pullPath(ref codehost.ChangeRef) string {
	return pullsPath(ref.InRepo()) + "/" + strconv.FormatInt(ref.Number, 10)
}

// mapError turns the client's errors into codehost's, keeping GitHub's own
// text, which never holds a credential.
func mapError(err error) error {
	var apiErr *githubapp.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", codehost.ErrChangeNotFound, apiErr.Message)
		case http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %s", codehost.ErrRejected, apiErr.Message)
		}
	}
	var gqlErr *githubapp.GraphQLError
	if errors.As(err, &gqlErr) {
		return fmt.Errorf("%w: %s", codehost.ErrRejected, gqlErr.Message)
	}
	if errors.Is(err, githubapp.ErrUnreachable) {
		return fmt.Errorf("%w: %w", codehost.ErrUnreachable, err)
	}
	return err
}
