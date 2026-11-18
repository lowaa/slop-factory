package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

const (
	secretToken = "ghs_secret_token_value"
	headSHA     = "1111111111111111111111111111111111111111"
)

var changeRef = codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12}

type request struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

// fakeAPI stands in for GitHub: it answers each route with what the test
// configured and records what it was asked. Whether a request was right is
// the test's to assert from the record.
type fakeAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []request
	// routes maps "METHOD path" to the answer; a func gets the request's
	// page query for the paged routes.
	routes map[string]func(r *http.Request) (int, any)
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{routes: map[string]func(*http.Request) (int, any){}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.requests = append(f.requests, request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
	route := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	status, answer := http.StatusNotFound, any(map[string]string{"message": "no route"})
	if route != nil {
		status, answer = route(r)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(answer)
}

func (f *fakeAPI) on(method, path string, status int, answer any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = func(*http.Request) (int, any) { return status, answer }
}

func (f *fakeAPI) calls(method, path string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []request
	for _, r := range f.requests {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeAPI) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type fakeToken struct{ value string }

func (t fakeToken) Reveal() string { return t.value }

// fakeCredentials mints the same token for any request and records what each
// was minted for.
type fakeCredentials struct {
	mu    sync.Mutex
	err   error
	asked []asked
}

type asked struct {
	Tenant uuid.UUID
	Repo   codehost.Repo
	Op     codehost.Operation
}

func (c *fakeCredentials) TokenFor(_ context.Context, tenant uuid.UUID, repo codehost.Repo, op codehost.Operation) (codehost.Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, asked{tenant, repo, op})
	if c.err != nil {
		return nil, c.err
	}
	return fakeToken{secretToken}, nil
}

func (c *fakeCredentials) ops() []codehost.Operation {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []codehost.Operation
	for _, a := range c.asked {
		out = append(out, a.Op)
	}
	return out
}

type fixture struct {
	api    *fakeAPI
	creds  *fakeCredentials
	ch     *github.Changes
	tenant uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithPoll(t, github.PollOptions{})
}

func newFixtureWithPoll(t *testing.T, poll github.PollOptions) *fixture {
	t.Helper()
	api := newFakeAPI(t)
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: api.srv.URL})
	require.NoError(t, err)
	creds := &fakeCredentials{}
	ch, err := github.NewChanges(&github.ChangesOptions{Client: client, Credentials: creds, UpdatePoll: poll})
	require.NoError(t, err)
	return &fixture{api: api, creds: creds, ch: ch, tenant: uuid.New()}
}

const pullPath = "/repos/acme/widgets/pulls/12"

func (f *fixture) pullIs(draft bool, sha string) {
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "node_id": "PR_node", "draft": draft, "head": map[string]string{"sha": sha}, "base": map[string]string{"ref": "main"}})
}

func TestOpenChange_DraftAndReady(t *testing.T) {
	for _, draft := range []bool{true, false} {
		t.Run(fmt.Sprintf("draft=%v", draft), func(t *testing.T) {
			f := newFixture(t)
			f.api.on("POST", "/repos/Acme/Widgets/pulls", 201, map[string]any{"number": 12})

			ref, err := f.ch.OpenChange(context.Background(), f.tenant, &codehost.OpenRequest{
				Repo: codehost.Repo{Owner: "Acme", Name: "Widgets"}, Head: "ticket-1-x", Base: "main", Title: "T", Body: "B", Draft: draft})

			require.NoError(t, err)
			assert.Equal(t, changeRef, ref)
			calls := f.api.calls("POST", "/repos/Acme/Widgets/pulls")
			require.Len(t, calls, 1)
			assert.Equal(t, map[string]any{"title": "T", "head": "ticket-1-x", "base": "main", "body": "B", "draft": draft}, calls[0].Body)
			assert.Equal(t, "Bearer "+secretToken, calls[0].Auth)
			assert.Equal(t, []codehost.Operation{codehost.OpenChange}, f.creds.ops())
		})
	}
}

func TestOpenChange_RefusedRequestsNeverReachGitHub(t *testing.T) {
	repo := codehost.Repo{Owner: "a", Name: "b"}
	for name, req := range map[string]*codehost.OpenRequest{
		"no head":       {Repo: repo, Base: "main", Title: "t"},
		"no base":       {Repo: repo, Head: "h", Title: "t"},
		"no title":      {Repo: repo, Head: "h", Base: "main"},
		"a blank title": {Repo: repo, Head: "h", Base: "main", Title: " \t\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.ch.OpenChange(context.Background(), f.tenant, req)
			assert.ErrorIs(t, err, codehost.ErrRejected)
			assert.Zero(t, f.api.total())
			assert.Empty(t, f.creds.ops(), "no token minted for a request that is not made")
		})
	}
}

// GitHub's 422 says only "Validation Failed" at the top; what was refused is
// in its errors, either as a message or as the field and a code.
func TestOpenChange_GitHubRefusingIsRejectedWithItsReason(t *testing.T) {
	cases := map[string]struct {
		errors []any
		want   string
	}{
		"message": {
			errors: []any{map[string]string{"resource": "PullRequest", "code": "custom", "message": "A pull request already exists for acme:h."}},
			want:   "Validation Failed (A pull request already exists for acme:h.)",
		},
		"field and code": {
			errors: []any{map[string]string{"resource": "PullRequest", "field": "head", "code": "invalid"}},
			want:   "Validation Failed (PullRequest.head: invalid)",
		},
		"several": {
			errors: []any{map[string]string{"resource": "PullRequest", "field": "head", "code": "invalid"}, map[string]string{"resource": "PullRequest", "field": "base", "code": "invalid"}},
			want:   "Validation Failed (PullRequest.head: invalid; PullRequest.base: invalid)",
		},
		"strings": {
			errors: []any{"Repository was archived so is read-only."},
			want:   "Validation Failed (Repository was archived so is read-only.)",
		},
		"one unreadable among readable": {
			errors: []any{42, map[string]string{"resource": "PullRequest", "field": "head", "code": "invalid"}},
			want:   "Validation Failed (PullRequest.head: invalid)",
		},
		"none": {want: "Validation Failed"},
		"cut at the bound": {
			errors: []any{strings.Repeat("x", 500)},
			want:   ("Validation Failed (" + strings.Repeat("x", 500))[:400],
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.api.on("POST", "/repos/acme/widgets/pulls", 422, map[string]any{"message": "Validation Failed", "errors": tc.errors})
			_, err := f.ch.OpenChange(context.Background(), f.tenant, &codehost.OpenRequest{
				Repo: codehost.Repo{Owner: "acme", Name: "widgets"}, Head: "h", Base: "main", Title: "t"})
			require.ErrorIs(t, err, codehost.ErrRejected)
			assert.True(t, strings.HasSuffix(err.Error(), ": "+tc.want), err.Error())
			assert.NotContains(t, err.Error(), secretToken)
		})
	}
}

func TestMarkReady_TakesADraftOutOfDraftThroughGraphQL(t *testing.T) {
	f := newFixture(t)
	f.pullIs(true, headSHA)
	f.api.on("POST", "/graphql", 200, map[string]any{"data": map[string]any{}})

	require.NoError(t, f.ch.MarkReady(context.Background(), f.tenant, changeRef))

	calls := f.api.calls("POST", "/graphql")
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"id": "PR_node"}, calls[0].Body["variables"])
	assert.Contains(t, calls[0].Body["query"], "markPullRequestReadyForReview")
	assert.Equal(t, []codehost.Operation{codehost.MarkReady}, f.creds.ops())
}

func TestMarkDraft_ConvertsAReadyChangeThroughGraphQL(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, headSHA)
	f.api.on("POST", "/graphql", 200, map[string]any{"data": map[string]any{}})

	require.NoError(t, f.ch.MarkDraft(context.Background(), f.tenant, changeRef))

	calls := f.api.calls("POST", "/graphql")
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"id": "PR_node"}, calls[0].Body["variables"])
	assert.Contains(t, calls[0].Body["query"], "convertPullRequestToDraft")
	assert.Equal(t, []codehost.Operation{codehost.MarkDraft}, f.creds.ops())
}

func TestMarkDraft_ADraftIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	f.pullIs(true, headSHA)
	require.NoError(t, f.ch.MarkDraft(context.Background(), f.tenant, changeRef))
	assert.Empty(t, f.api.calls("POST", "/graphql"))
}

func TestMarkReady_AReadyChangeIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, headSHA)
	require.NoError(t, f.ch.MarkReady(context.Background(), f.tenant, changeRef))
	assert.Empty(t, f.api.calls("POST", "/graphql"))
}

func TestMarkReady_AGraphQLErrorIsAFailure(t *testing.T) {
	f := newFixture(t)
	f.pullIs(true, headSHA)
	f.api.on("POST", "/graphql", 200, map[string]any{"errors": []map[string]string{{"message": "Resource not accessible"}}})
	err := f.ch.MarkReady(context.Background(), f.tenant, changeRef)
	require.ErrorIs(t, err, codehost.ErrRejected)
	assert.Contains(t, err.Error(), "Resource not accessible")
}

func TestMarkReady_OnEnterpriseGraphQLIsBesideTheV3Root(t *testing.T) {
	api := newFakeAPI(t)
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: api.srv.URL + "/api/v3"})
	require.NoError(t, err)
	ch, err := github.NewChanges(&github.ChangesOptions{Client: client, Credentials: &fakeCredentials{}})
	require.NoError(t, err)
	api.on("GET", "/api/v3"+pullPath, 200, map[string]any{"node_id": "n", "draft": true, "head": map[string]string{"sha": headSHA}})
	api.on("POST", "/api/graphql", 200, map[string]any{"data": map[string]any{}})

	require.NoError(t, ch.MarkReady(context.Background(), uuid.New(), changeRef))
	assert.Len(t, api.calls("POST", "/api/graphql"), 1)
}

type run struct{ name, status, conclusion string }

func (f *fixture) checksAre(runs []run, statuses map[string]string) {
	f.pullIs(false, headSHA)
	rs := []map[string]any{}
	for i, r := range runs {
		rs = append(rs, map[string]any{"id": i + 1, "name": r.name, "status": r.status, "conclusion": r.conclusion})
	}
	f.api.on("GET", "/repos/acme/widgets/commits/"+headSHA+"/check-runs", 200, map[string]any{"total_count": len(rs), "check_runs": rs})
	ss := []map[string]any{}
	i := 0
	for name, state := range statuses {
		i++
		ss = append(ss, map[string]any{"id": i, "context": name, "state": state})
	}
	f.api.on("GET", "/repos/acme/widgets/commits/"+headSHA+"/status", 200, map[string]any{"total_count": len(ss), "statuses": ss})
}

func TestChecks_CombineCheckRunsAndCommitStatuses(t *testing.T) {
	done := func(name, conclusion string) run { return run{name, "completed", conclusion} }
	for _, tc := range []struct {
		name     string
		runs     []run
		statuses map[string]string
		want     codehost.CheckState
	}{
		{"only statuses, as Woodpecker reports", nil, map[string]string{"ci/woodpecker/pr/test": "success"}, codehost.CheckSuccess},
		{"only check runs", []run{done("build", "success")}, nil, codehost.CheckSuccess},
		{"neither", nil, nil, codehost.CheckNone},
		{"a status pending beside a run succeeded", []run{done("build", "success")}, map[string]string{"ci": "pending"}, codehost.CheckPending},
		{"a status failing beside a run succeeded", []run{done("build", "success")}, map[string]string{"ci": "failure"}, codehost.CheckFailure},
		{"a status error", nil, map[string]string{"ci": "error"}, codehost.CheckFailure},
		{"a run failing beside a status succeeded", []run{done("build", "failure")}, map[string]string{"ci": "success"}, codehost.CheckFailure},
		{"a run still running", []run{{"build", "in_progress", ""}}, map[string]string{"ci": "success"}, codehost.CheckPending},
		{"a run cancelled", []run{done("build", "cancelled")}, nil, codehost.CheckFailure},
		{"a conclusion GitHub adds later", []run{done("build", "something_new")}, map[string]string{"ci": "success"}, codehost.CheckFailure},
		{"skipped runs vouch for nothing", []run{done("opt", "skipped")}, nil, codehost.CheckNone},
		{"an unknown status state does not pass", nil, map[string]string{"ci": "mystery"}, codehost.CheckPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.checksAre(tc.runs, tc.statuses)

			got, err := f.ch.Checks(context.Background(), f.tenant, changeRef)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.State)
			assert.Equal(t, tc.want == codehost.CheckSuccess, got.Passed())
			assert.Equal(t, headSHA, got.HeadSHA)
			assert.Len(t, got.Checks, len(tc.runs)+len(tc.statuses))
			assert.Equal(t, []codehost.Operation{codehost.ReadChecks}, f.creds.ops(), "one read-only token for the whole read")
		})
	}
}

// pagedRoute answers a listing of total entries 100 to a page, from page
// fn(page), and records the queries it was asked.
func (f *fixture) pagedRoute(path, key string, total int, entry func(page, i int) map[string]any) {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	f.api.routes["GET "+path] = func(r *http.Request) (int, any) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		n := total - (page-1)*100
		if n > 100 {
			n = 100
		}
		if n < 0 {
			n = 0
		}
		items := []map[string]any{}
		for i := 0; i < n; i++ {
			items = append(items, entry(page, i))
		}
		return 200, map[string]any{"total_count": total, key: items}
	}
}

const (
	runsPath     = "/repos/acme/widgets/commits/" + headSHA + "/check-runs"
	statusesPath = "/repos/acme/widgets/commits/" + headSHA + "/status"
)

func runEntry(conclusionOfLast string) func(page, i int) map[string]any {
	return func(page, i int) map[string]any {
		conclusion := "success"
		if page == 2 {
			conclusion = conclusionOfLast
		}
		return map[string]any{"id": page*1000 + i, "name": fmt.Sprintf("r%d-%d", page, i), "status": "completed", "conclusion": conclusion}
	}
}

func statusEntry(stateOfLast string) func(page, i int) map[string]any {
	return func(page, i int) map[string]any {
		state := "success"
		if page == 2 {
			state = stateOfLast
		}
		return map[string]any{"id": page*1000 + i, "context": fmt.Sprintf("s%d-%d", page, i), "state": state}
	}
}

func TestChecks_ReadsEveryPageOfBothListings(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, headSHA)
	f.pagedRoute(runsPath, "check_runs", 101, runEntry("success"))
	f.pagedRoute(statusesPath, "statuses", 101, statusEntry("failure"))

	got, err := f.ch.Checks(context.Background(), f.tenant, changeRef)

	require.NoError(t, err)
	assert.Len(t, got.Checks, 202)
	assert.Equal(t, codehost.CheckFailure, got.State, "the failing status on the second page is seen")
	assert.Equal(t, "filter=latest&per_page=100&page=1", f.api.calls("GET", runsPath)[0].Query)
	assert.Equal(t, "filter=latest&per_page=100&page=2", f.api.calls("GET", runsPath)[1].Query)
	assert.Equal(t, "per_page=100&page=2", f.api.calls("GET", statusesPath)[1].Query)
}

func TestChecks_ASecondPageRunFailureIsSeen(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, headSHA)
	f.pagedRoute(runsPath, "check_runs", 101, runEntry("failure"))
	f.pagedRoute(statusesPath, "statuses", 0, statusEntry("success"))

	got, err := f.ch.Checks(context.Background(), f.tenant, changeRef)

	require.NoError(t, err)
	assert.Equal(t, codehost.CheckFailure, got.State)
}

func TestChecks_AnEndlessListIsAnErrorNotAPartialAnswer(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, headSHA)
	f.pagedRoute(runsPath, "check_runs", 100000, func(page, i int) map[string]any {
		return map[string]any{"id": page*1000 + i, "name": "r", "status": "completed", "conclusion": "success"}
	})
	f.pagedRoute(statusesPath, "statuses", 0, statusEntry("success"))

	_, err := f.ch.Checks(context.Background(), f.tenant, changeRef)
	assert.ErrorIs(t, err, github.ErrTooManyChecks)
}

// A check starting while the pages are read shifts a newest-first listing:
// the next page repeats an entry and a new one is never seen. That must not
// read as a complete list.
func TestChecks_AListingThatShiftedWhileReadIsAnErrorNotAPass(t *testing.T) {
	for name, route := range map[string]func(f *fixture){
		"a repeated check": func(f *fixture) {
			f.pagedRoute(runsPath, "check_runs", 101, func(page, i int) map[string]any {
				return map[string]any{"id": i, "name": "r", "status": "completed", "conclusion": "success"}
			})
		},
		"a total that moved": func(f *fixture) {
			f.api.mu.Lock()
			defer f.api.mu.Unlock()
			f.api.routes["GET "+runsPath] = func(r *http.Request) (int, any) {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				total, n := 101, 100
				if page == 2 {
					total, n = 102, 2
				}
				items := []map[string]any{}
				for i := 0; i < n; i++ {
					items = append(items, map[string]any{"id": page*1000 + i, "name": "r", "status": "completed", "conclusion": "success"})
				}
				return 200, map[string]any{"total_count": total, "check_runs": items}
			}
		},
		"a page that ends short of the total": func(f *fixture) {
			f.api.on("GET", runsPath, 200, map[string]any{"total_count": 5, "check_runs": []any{}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.pullIs(false, headSHA)
			f.pagedRoute(statusesPath, "statuses", 0, statusEntry("success"))
			route(f)

			got, err := f.ch.Checks(context.Background(), f.tenant, changeRef)

			assert.ErrorIs(t, err, github.ErrChecksChanged)
			assert.Nil(t, got)
		})
	}
}

func TestChecks_AChangeGitHubDoesNotKnow(t *testing.T) {
	f := newFixture(t)
	_, err := f.ch.Checks(context.Background(), f.tenant, changeRef)
	assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
}

func (f *fixture) pullState(state string, merged bool) {
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "state": state, "merged": merged, "head": map[string]string{"sha": headSHA}, "base": map[string]string{"ref": "main"}})
}

func TestCloseChange_ClosesAnOpenChange(t *testing.T) {
	f := newFixture(t)
	f.pullState("open", false)
	f.api.on("PATCH", pullPath, 200, map[string]any{"state": "closed"})

	require.NoError(t, f.ch.CloseChange(context.Background(), f.tenant, changeRef))

	calls := f.api.calls("PATCH", pullPath)
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"state": "closed"}, calls[0].Body)
	assert.Equal(t, []codehost.Operation{codehost.CloseChange}, f.creds.ops())
}

func TestCloseChange_AClosedChangeIsSuccessWithoutAWrite(t *testing.T) {
	f := newFixture(t)
	f.pullState("closed", false)
	require.NoError(t, f.ch.CloseChange(context.Background(), f.tenant, changeRef))
	assert.Empty(t, f.api.calls("PATCH", pullPath))
}

func TestCloseChange_AMergedChangeIsNotClosed(t *testing.T) {
	f := newFixture(t)
	f.pullState("closed", true)
	err := f.ch.CloseChange(context.Background(), f.tenant, changeRef)
	require.ErrorIs(t, err, codehost.ErrAlreadyMerged)
	assert.Empty(t, f.api.calls("PATCH", pullPath))
}

func TestCloseChange_AMergeBetweenTheReadAndTheWriteIsNotAClose(t *testing.T) {
	pull := func(merged bool) map[string]any {
		return map[string]any{"number": 12, "state": "open", "merged": merged, "head": map[string]string{"sha": headSHA}, "base": map[string]string{"ref": "main"}}
	}
	t.Run("the write is refused", func(t *testing.T) {
		f := newFixture(t)
		reads := 0
		f.api.onFunc("GET", pullPath, func(*http.Request) (int, any) {
			reads++
			return 200, pull(reads > 1)
		})
		f.api.on("PATCH", pullPath, 422, map[string]string{"message": "Pull request is already merged"})
		assert.ErrorIs(t, f.ch.CloseChange(context.Background(), f.tenant, changeRef), codehost.ErrAlreadyMerged)
	})
	t.Run("the write answers merged", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", pullPath, 200, pull(false))
		f.api.on("PATCH", pullPath, 200, map[string]any{"state": "closed", "merged": true})
		assert.ErrorIs(t, f.ch.CloseChange(context.Background(), f.tenant, changeRef), codehost.ErrAlreadyMerged)
	})
}

func TestCloseChange_Refusals(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", pullPath, 404, map[string]string{"message": "Not Found"})
	assert.ErrorIs(t, f.ch.CloseChange(context.Background(), f.tenant, changeRef), codehost.ErrChangeNotFound)

	f = newFixture(t)
	f.pullState("open", false)
	f.api.on("PATCH", pullPath, 422, map[string]string{"message": "no"})
	assert.ErrorIs(t, f.ch.CloseChange(context.Background(), f.tenant, changeRef), codehost.ErrRejected)
}

func TestMerge_MergesWithAMergeCommitOnTheSHA(t *testing.T) {
	f := newFixture(t)
	f.api.on("PUT", pullPath+"/merge", 200, map[string]any{"sha": "mergesha", "merged": true})

	got, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "Merge x: y"})

	require.NoError(t, err)
	assert.Equal(t, "mergesha", got.SHA)
	calls := f.api.calls("PUT", pullPath+"/merge")
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"merge_method": "merge", "sha": headSHA, "commit_title": "Merge x: y"}, calls[0].Body)
	assert.Equal(t, []codehost.Operation{codehost.MergeChange}, f.creds.ops())
}

func TestMerge_CarriesTheCommitBody(t *testing.T) {
	f := newFixture(t)
	f.api.on("PUT", pullPath+"/merge", 200, map[string]any{"sha": "mergesha", "merged": true})

	_, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s", Body: "Approved by: Ada"})

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"merge_method": "merge", "sha": headSHA, "commit_title": "s", "commit_message": "Approved by: Ada"},
		f.api.calls("PUT", pullPath+"/merge")[0].Body)
}

func TestMerge_RefusedOnAMovedHead(t *testing.T) {
	f := newFixture(t)
	f.api.on("PUT", pullPath+"/merge", 409, map[string]string{"message": "Head branch was modified. Review and try the merge again."})

	got, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s"})

	assert.Nil(t, got)
	require.ErrorIs(t, err, codehost.ErrHeadMoved)
	assert.NotErrorIs(t, err, codehost.ErrNotMergeable)
}

func TestMerge_OtherRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{405, codehost.ErrNotMergeable},
		{404, codehost.ErrChangeNotFound},
		{422, codehost.ErrRejected},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			f := newFixture(t)
			f.api.on("PUT", pullPath+"/merge", tc.status, map[string]string{"message": "no"})
			_, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s"})
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestMerge_NotMergedIsNotMerged(t *testing.T) {
	f := newFixture(t)
	f.api.on("PUT", pullPath+"/merge", 200, map[string]any{"merged": false})
	_, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s"})
	assert.ErrorIs(t, err, codehost.ErrNotMergeable)
}

func TestMerge_WithoutASHAIsRefusedBeforeAnyRequest(t *testing.T) {
	f := newFixture(t)
	_, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, Subject: "s"})
	assert.ErrorIs(t, err, codehost.ErrRejected)
	assert.Zero(t, f.api.total())
	assert.Empty(t, f.creds.ops())
}

func TestTokensAreMintedPerOperationForTheChangesRepo(t *testing.T) {
	f := newFixture(t)
	f.pullIs(true, headSHA)
	f.api.on("POST", "/graphql", 200, map[string]any{"data": map[string]any{}})
	f.api.on("PUT", pullPath+"/merge", 200, map[string]any{"sha": "m", "merged": true})
	f.checksAre(nil, nil)
	ctx := context.Background()

	require.NoError(t, f.ch.MarkReady(ctx, f.tenant, changeRef))
	_, err := f.ch.Checks(ctx, f.tenant, changeRef)
	require.NoError(t, err)
	_, err = f.ch.Merge(ctx, f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s"})
	require.NoError(t, err)

	want := codehost.Repo{Owner: "acme", Name: "widgets"}
	assert.Equal(t, []asked{
		{f.tenant, want, codehost.MarkReady},
		{f.tenant, want, codehost.ReadChecks},
		{f.tenant, want, codehost.MergeChange},
	}, f.creds.asked)
}

func TestCredentialErrorsPassThroughAndNothingIsSent(t *testing.T) {
	f := newFixture(t)
	f.creds.err = codehost.ErrNotConnected
	ctx := context.Background()

	_, err := f.ch.Checks(ctx, f.tenant, changeRef)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	err = f.ch.MarkReady(ctx, f.tenant, changeRef)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	_, err = f.ch.Merge(ctx, f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA})
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	_, err = f.ch.OpenChange(ctx, f.tenant, &codehost.OpenRequest{Repo: codehost.Repo{Owner: "a", Name: "b"}, Head: "h", Base: "m", Title: "t"})
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	assert.Zero(t, f.api.total())
}

func TestAChangeOfAnotherHostIsNotThisHosts(t *testing.T) {
	f := newFixture(t)
	other := changeRef
	other.Host = "gitlab.com"
	_, err := f.ch.Checks(context.Background(), f.tenant, other)
	require.Error(t, err)
	assert.Zero(t, f.creds.ops(), "no token for a change that is not GitHub's")
}

func TestAnUnreachableGitHubIsUnreachable(t *testing.T) {
	f := newFixture(t)
	f.api.srv.Close()
	_, err := f.ch.Checks(context.Background(), f.tenant, changeRef)
	assert.ErrorIs(t, err, codehost.ErrUnreachable)
	assert.False(t, errors.Is(err, codehost.ErrChangeNotFound))
	assert.NotContains(t, err.Error(), secretToken)
	assert.False(t, strings.Contains(fmt.Sprint(err), "Bearer"))
}

func TestChecks_ReportTheBaseTheChangeTargetsNow(t *testing.T) {
	f := newFixture(t)
	f.checksAre(nil, map[string]string{"ci": "success"})
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "head": map[string]string{"sha": headSHA}, "base": map[string]string{"ref": "release/1"}, "title": "Add widgets"})

	checks, err := f.ch.Checks(context.Background(), f.tenant, changeRef)

	require.NoError(t, err)
	assert.Equal(t, "release/1", checks.Base, "the host's answer, not what the change was opened with")
	assert.Equal(t, "Add widgets", checks.Title)
}

func TestCompareBase(t *testing.T) {
	tests := map[string]struct {
		answer any
		status int
		want   bool
		errIs  error
	}{
		"not behind":      {answer: map[string]any{"behind_by": 0, "ahead_by": 2}, status: 200, want: true},
		"behind":          {answer: map[string]any{"behind_by": 3}, status: 200, want: false},
		"no behind_by":    {answer: map[string]any{"status": "ahead"}, status: 200, errIs: githubapp.ErrBadResponse},
		"unknown commits": {answer: map[string]any{"message": "Not Found"}, status: 404, errIs: codehost.ErrChangeNotFound},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.pullIs(false, headSHA)
			f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, tc.status, tc.answer)

			got, err := f.ch.CompareBase(context.Background(), f.tenant, changeRef, headSHA)

			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				assert.Nil(t, got, "an error never reads as contained")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, &codehost.BaseComparison{Base: "main", ContainsTip: tc.want}, got)
			assert.Equal(t, []asked{{f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, codehost.CompareBase}}, f.creds.asked)
		})
	}
}

func TestCompareBase_UsesTheBaseTheChangeTargetsNowAndNeedsAHead(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "head": map[string]string{"sha": headSHA}, "base": map[string]string{"ref": "release/1"}})
	f.api.on("GET", "/repos/acme/widgets/compare/release/1..."+headSHA, 200, map[string]any{"behind_by": 0})

	got, err := f.ch.CompareBase(context.Background(), f.tenant, changeRef, headSHA)
	require.NoError(t, err)
	assert.Equal(t, &codehost.BaseComparison{Base: "release/1", ContainsTip: true}, got)

	before := f.api.total()
	_, err = f.ch.CompareBase(context.Background(), f.tenant, changeRef, "")
	require.ErrorIs(t, err, codehost.ErrRejected)
	assert.Equal(t, before, f.api.total())
}

func TestCompareBase_ComparesTheHeadItWasGivenAndNeedsABase(t *testing.T) {
	const other = "2222222222222222222222222222222222222222"
	f := newFixture(t)
	f.pullIs(false, headSHA)
	f.api.on("GET", "/repos/acme/widgets/compare/main..."+other, 200, map[string]any{"behind_by": 4})

	got, err := f.ch.CompareBase(context.Background(), f.tenant, changeRef, other)

	require.NoError(t, err)
	assert.False(t, got.ContainsTip)
	assert.Len(t, f.api.calls("GET", "/repos/acme/widgets/compare/main..."+other), 1, "the head asked about is the one compared, not the pull's own")

	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "head": map[string]string{"sha": headSHA}})
	before := len(f.api.calls("GET", "/repos/acme/widgets/compare/main..."+other))
	_, err = f.ch.CompareBase(context.Background(), f.tenant, changeRef, other)
	require.ErrorIs(t, err, githubapp.ErrBadResponse)
	assert.Equal(t, before, len(f.api.calls("GET", "/repos/acme/widgets/compare/main..."+other)), "no comparison without a base")
}

func (f *fakeAPI) onFunc(method, path string, route func(r *http.Request) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = route
}

const (
	tipSHA       = "3333333333333333333333333333333333333333"
	mergeBaseSHA = "4444444444444444444444444444444444444444"
)

func comparePage(base, mergeBase string, files ...map[string]any) map[string]any {
	return map[string]any{"base_commit": map[string]string{"sha": base}, "merge_base_commit": map[string]string{"sha": mergeBase}, "files": files}
}

func file(name, status, patch string) map[string]any {
	return map[string]any{"filename": name, "status": status, "patch": patch}
}

func renamed(name, previous string) map[string]any {
	return map[string]any{"filename": name, "previous_filename": previous, "status": "renamed", "patch": ""}
}

func manyFiles(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = file(fmt.Sprintf("f%03d.go", i), "modified", "@@ -1 +1 @@\n-a\n+b")
	}
	return out
}

// prHead is where the pull request's head is, which a diff of another head
// must not follow.
const prHead = "5555555555555555555555555555555555555555"

func TestDiff_ReadsFilesAgainstTheBaseTip(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, prHead)
	f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 200, comparePage(tipSHA, tipSHA,
		file("a.go", "modified", "@@ -1 +1 @@\n-a\n+b"), file("b.go", "added", "@@ -0,0 +1 @@\n+x"),
		file("c.go", "removed", "@@ -1 +0,0 @@\n-x"), renamed("d.go", "old/d.go"), map[string]any{"filename": "c2.go", "previous_filename": "c.go", "status": "copied"}, file("e.bin", "modified", ""), file("m.sh", "changed", "")))

	got, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)

	require.NoError(t, err)
	assert.Equal(t, &codehost.Diff{Base: "main", BaseSHA: tipSHA, MergeBaseSHA: tipSHA, Files: []codehost.FileDiff{
		{Path: "a.go", Status: codehost.DiffModified, Patch: "@@ -1 +1 @@\n-a\n+b"},
		{Path: "b.go", Status: codehost.DiffAdded, Patch: "@@ -0,0 +1 @@\n+x"},
		{Path: "c.go", Status: codehost.DiffRemoved, Patch: "@@ -1 +0,0 @@\n-x"},
		{Path: "d.go", Status: codehost.DiffRenamed, PreviousPath: "old/d.go"},
		{Path: "c2.go", Status: codehost.DiffCopied, PreviousPath: "c.go"},
		{Path: "e.bin", Status: codehost.DiffModified},
		{Path: "m.sh", Status: codehost.DiffModeChanged},
	}}, got)
	assert.True(t, got.ContainsTip())
	first := f.api.calls("GET", "/repos/acme/widgets/compare/main..."+headSHA)
	require.Len(t, first, 1, "the head asked about is compared, not the pull's own")
	assert.Equal(t, "per_page=100&page=1", first[0].Query)
	assert.Equal(t, []asked{{f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, codehost.CompareBase}}, f.creds.asked)
}

func TestDiff_ABaseThatMovedBehindTheHeadIsReported(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, prHead)
	f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 200, comparePage(tipSHA, mergeBaseSHA, file("a.go", "modified", "@@ -1 +1 @@\n-a\n+b")))

	got, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)

	require.NoError(t, err)
	assert.False(t, got.ContainsTip())
}

func TestDiff_PagesAreReadAgainstTheTipTheFirstOneNamed(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, prHead)
	f.api.onFunc("GET", "/repos/acme/widgets/compare/main..."+headSHA, func(r *http.Request) (int, any) {
		return 200, comparePage(tipSHA, tipSHA, manyFiles(100)...)
	})
	f.api.onFunc("GET", "/repos/acme/widgets/compare/"+tipSHA+"..."+headSHA, func(r *http.Request) (int, any) {
		return 200, comparePage(tipSHA, tipSHA, file("last.go", "modified", "@@ -1 +1 @@\n-a\n+b"))
	})

	got, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)

	require.NoError(t, err)
	assert.Len(t, got.Files, 101)
	assert.Equal(t, "per_page=100&page=1", f.api.calls("GET", "/repos/acme/widgets/compare/main..."+headSHA)[0].Query)
	second := f.api.calls("GET", "/repos/acme/widgets/compare/"+tipSHA+"..."+headSHA)
	require.Len(t, second, 1)
	assert.Equal(t, "per_page=100&page=2", second[0].Query)
}

func TestDiff_ABaseThatMovedBetweenPagesIsRefused(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, prHead)
	f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 200, comparePage(tipSHA, tipSHA, manyFiles(100)...))
	f.api.on("GET", "/repos/acme/widgets/compare/"+tipSHA+"..."+headSHA, 200, comparePage(mergeBaseSHA, mergeBaseSHA, file("last.go", "modified", "@@ -1 +1 @@\n-a\n+b")))

	got, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)

	require.ErrorIs(t, err, codehost.ErrDiffChanged)
	assert.Nil(t, got)
}

// GitHub lists at most 300 files: a full last page may have been cut.
func TestDiff_ACutShortListingIsUnsupported(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, prHead)
	full := func(*http.Request) (int, any) { return 200, comparePage(tipSHA, tipSHA, manyFiles(100)...) }
	f.api.onFunc("GET", "/repos/acme/widgets/compare/main..."+headSHA, full)
	f.api.onFunc("GET", "/repos/acme/widgets/compare/"+tipSHA+"..."+headSHA, full)

	got, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)

	require.ErrorIs(t, err, codehost.ErrDiffUnsupported)
	assert.Nil(t, got)
}

func TestDiff_ErrorsAndMissingAnswersFailClosed(t *testing.T) {
	tests := map[string]struct {
		status int
		answer any
		errIs  error
	}{
		"unknown commit": {404, map[string]any{"message": "Not Found"}, codehost.ErrChangeNotFound},
		"no base commit": {200, map[string]any{"merge_base_commit": map[string]string{"sha": tipSHA}, "files": []any{}}, githubapp.ErrBadResponse},
		"no merge base":  {200, map[string]any{"base_commit": map[string]string{"sha": tipSHA}, "files": []any{}}, githubapp.ErrBadResponse},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.pullIs(false, prHead)
			f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, tc.status, tc.answer)

			got, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)

			require.ErrorIs(t, err, tc.errIs)
			assert.Nil(t, got)
		})
	}
}

func TestDiff_NeedsAHeadAndAGitHubChange(t *testing.T) {
	f := newFixture(t)
	before := f.api.total()
	_, err := f.ch.Diff(context.Background(), f.tenant, changeRef, "")
	require.ErrorIs(t, err, codehost.ErrRejected)
	other := changeRef
	other.Host = "gitlab.example"
	_, err = f.ch.Diff(context.Background(), f.tenant, other, headSHA)
	require.Error(t, err)
	assert.Equal(t, before, f.api.total())
}

// The base's own history between two commits, read as Diff reads a change:
// paged against the commit the first page named, with a compare token.
func TestCompareCommits_ReadsWhatTheLaterCommitChanged(t *testing.T) {
	f := newFixture(t)
	path := "/repos/acme/widgets/compare/" + mergeBaseSHA + "..." + tipSHA
	f.api.on("GET", path, 200, comparePage(mergeBaseSHA, mergeBaseSHA, file("deploy/k.yaml", "modified", "@@ -1 +1 @@\n-a\n+b")))

	got, err := f.ch.CompareCommits(context.Background(), f.tenant, changeRef, mergeBaseSHA, tipSHA)

	require.NoError(t, err)
	assert.Equal(t, &codehost.Diff{BaseSHA: mergeBaseSHA, MergeBaseSHA: mergeBaseSHA, Files: []codehost.FileDiff{
		{Path: "deploy/k.yaml", Status: codehost.DiffModified, Patch: "@@ -1 +1 @@\n-a\n+b"},
	}}, got)
	calls := f.api.calls("GET", path)
	require.Len(t, calls, 1)
	assert.Equal(t, "per_page=100&page=1", calls[0].Query)
	assert.Empty(t, f.api.calls("GET", "/repos/acme/widgets/pulls/12"), "commits need no pull request read")
	assert.Equal(t, []asked{{f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, codehost.CompareBase}}, f.creds.asked)
}

// From a commit that is not an ancestor of the other, the comparison is
// still three-dot: GitHub's merge base is reported, not taken to be fromSHA.
func TestCompareCommits_ReportsTheMergeBaseGitHubFound(t *testing.T) {
	f := newFixture(t)
	path := "/repos/acme/widgets/compare/" + headSHA + "..." + tipSHA
	f.api.on("GET", path, 200, comparePage(headSHA, mergeBaseSHA, file("x.go", "modified", "@@ -1 +1 @@\n-a\n+b")))

	got, err := f.ch.CompareCommits(context.Background(), f.tenant, changeRef, headSHA, tipSHA)

	require.NoError(t, err)
	assert.Equal(t, headSHA, got.BaseSHA)
	assert.Equal(t, mergeBaseSHA, got.MergeBaseSHA)
	assert.Len(t, f.api.calls("GET", path), 1, "a three-dot comparison of the two commits as given")
}

func TestCompareCommits_ACutShortListingIsUnsupported(t *testing.T) {
	f := newFixture(t)
	f.api.onFunc("GET", "/repos/acme/widgets/compare/"+mergeBaseSHA+"..."+tipSHA, func(*http.Request) (int, any) {
		return 200, comparePage(mergeBaseSHA, mergeBaseSHA, manyFiles(100)...)
	})

	got, err := f.ch.CompareCommits(context.Background(), f.tenant, changeRef, mergeBaseSHA, tipSHA)

	require.ErrorIs(t, err, codehost.ErrDiffUnsupported)
	assert.Nil(t, got)
	assert.Len(t, f.api.calls("GET", "/repos/acme/widgets/compare/"+mergeBaseSHA+"..."+tipSHA), 3)
}

// Only full commit names reach the URL: a branch, a range or a path in their
// place could compare something else than the two commits asked about.
// GitHub may list every file of a comparison on its first page: a first page
// at the 300-file cap is as cut short as three full pages, for a change's
// diff and for the base's own history alike.
func TestCompare_AFirstPageAtTheCapIsUnsupported(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, prHead)
	atCap := comparePage(tipSHA, mergeBaseSHA, manyFiles(300)...)
	f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 200, atCap)
	f.api.on("GET", "/repos/acme/widgets/compare/"+tipSHA+"..."+headSHA, 200, comparePage(tipSHA, mergeBaseSHA))
	f.api.on("GET", "/repos/acme/widgets/compare/"+mergeBaseSHA+"..."+tipSHA, 200, comparePage(mergeBaseSHA, mergeBaseSHA, manyFiles(300)...))

	diff, err := f.ch.Diff(context.Background(), f.tenant, changeRef, headSHA)
	require.ErrorIs(t, err, codehost.ErrDiffUnsupported)
	assert.Nil(t, diff)

	drift, err := f.ch.CompareCommits(context.Background(), f.tenant, changeRef, mergeBaseSHA, tipSHA)
	require.ErrorIs(t, err, codehost.ErrDiffUnsupported)
	assert.Nil(t, drift)
}

func TestCompareCommits_NeedsTwoCommitsAndAGitHubChange(t *testing.T) {
	f := newFixture(t)
	for name, pair := range map[string][2]string{
		"empty":      {"", tipSHA},
		"a branch":   {"main", tipSHA},
		"a range":    {mergeBaseSHA, mergeBaseSHA[:39] + "."},
		"upper case": {strings.ToUpper("abcdef" + mergeBaseSHA[6:]), tipSHA},
		"short":      {mergeBaseSHA[:12], tipSHA},
		"path in it": {"../../" + mergeBaseSHA[6:], tipSHA},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.ch.CompareCommits(context.Background(), f.tenant, changeRef, pair[0], pair[1])
			require.ErrorIs(t, err, codehost.ErrRejected)
		})
	}
	other := changeRef
	other.Host = "gitlab.example"
	_, err := f.ch.CompareCommits(context.Background(), f.tenant, other, mergeBaseSHA, tipSHA)
	require.Error(t, err)
	assert.Zero(t, f.api.total())
}

func TestDefaultBranchHead_ReadsTheDefaultBranchsHeadWithABranchReadToken(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", "/repos/acme/widgets", 200, map[string]any{"default_branch": "release/1"})
	f.api.on("GET", "/repos/acme/widgets/commits/release/1", 200, map[string]any{"sha": headSHA})
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	got, err := f.ch.DefaultBranchHead(context.Background(), f.tenant, repo)

	require.NoError(t, err)
	assert.Equal(t, &codehost.BranchHead{Branch: "release/1", SHA: headSHA}, got)
	assert.Equal(t, []asked{{f.tenant, repo, codehost.ReadBranch}}, f.creds.asked)
}

func TestDefaultBranchHead_FailuresAreNeverAHead(t *testing.T) {
	tests := map[string]struct {
		repo, commit     int
		repoAns, commitA any
		errIs            error
	}{
		"unknown repo":       {repo: 404, repoAns: map[string]any{"message": "Not Found"}, errIs: codehost.ErrChangeNotFound},
		"no default branch":  {repo: 200, repoAns: map[string]any{}, errIs: githubapp.ErrBadResponse},
		"empty repository":   {repo: 200, repoAns: map[string]any{"default_branch": "main"}, commit: 409, commitA: map[string]any{"message": "Git Repository is empty."}},
		"commit without sha": {repo: 200, repoAns: map[string]any{"default_branch": "main"}, commit: 200, commitA: map[string]any{}, errIs: githubapp.ErrBadResponse},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.api.on("GET", "/repos/acme/widgets", tc.repo, tc.repoAns)
			if tc.commit != 0 {
				f.api.on("GET", "/repos/acme/widgets/commits/main", tc.commit, tc.commitA)
			}

			got, err := f.ch.DefaultBranchHead(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"})

			require.Error(t, err)
			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
			}
			assert.Nil(t, got)
		})
	}
}

func TestCreateBranch_CreatesTheBranchAtTheCommitWithABranchWriteToken(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 201, map[string]any{"ref": "refs/heads/slop-haus/base/ticket-4"})
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	err := f.ch.CreateBranch(context.Background(), f.tenant, repo, "slop-haus/base/ticket-4", headSHA)

	require.NoError(t, err)
	calls := f.api.calls("POST", "/repos/acme/widgets/git/refs")
	require.Len(t, calls, 1)
	assert.Equal(t, "refs/heads/slop-haus/base/ticket-4", calls[0].Body["ref"])
	assert.Equal(t, headSHA, calls[0].Body["sha"])
	assert.Equal(t, []asked{{f.tenant, repo, codehost.CreateBranch}}, f.creds.asked)
}

func TestCreateBranch_ABranchAlreadyAtTheCommitIsSuccess(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/heads/slop-haus/base/ticket-4", 200, map[string]any{"object": map[string]any{"sha": headSHA}})

	err := f.ch.CreateBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "slop-haus/base/ticket-4", headSHA)

	require.NoError(t, err)
}

func TestCreateBranch_ABranchAtAnotherCommitIsAConflictNamingBoth(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/heads/slop-haus/base/ticket-4", 200, map[string]any{"object": map[string]any{"sha": "cccc"}})

	err := f.ch.CreateBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "slop-haus/base/ticket-4", headSHA)

	require.ErrorIs(t, err, codehost.ErrBranchConflict)
	var conflict *codehost.BranchConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "slop-haus/base/ticket-4", conflict.Branch)
	assert.Equal(t, "cccc", conflict.Have)
	assert.Equal(t, headSHA, conflict.Want)
	assert.Contains(t, err.Error(), "cccc")
	assert.Contains(t, err.Error(), headSHA)
}

func TestCreateBranch_OtherRefusalsAreNotAnExistingBranch(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Object does not exist"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/heads/b", 404, map[string]any{"message": "Not Found"})

	err := f.ch.CreateBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "b", headSHA)

	require.ErrorIs(t, err, codehost.ErrRejected)
	assert.NotErrorIs(t, err, codehost.ErrBranchConflict)
}

func TestCreateBranch_AFailedReadOfTheExistingBranchIsReportedAsThatFailure(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/heads/b", 500, map[string]any{"message": "boom"})

	err := f.ch.CreateBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "b", headSHA)

	require.Error(t, err)
	var apiErr *githubapp.APIError
	require.ErrorAs(t, err, &apiErr, "the read's own failure is what comes back")
	assert.Equal(t, 500, apiErr.Status)
	assert.NotErrorIs(t, err, codehost.ErrRejected, "the host failing is not the branch being refused")
	assert.NotErrorIs(t, err, codehost.ErrBranchConflict)
}

func TestCreateBranch_NeedsABranchAndACommitAndAsksForNoCredentialWithout(t *testing.T) {
	for name, args := range map[string][2]string{"no branch": {"", headSHA}, "no commit": {"b", ""}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)

			err := f.ch.CreateBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, args[0], args[1])

			require.ErrorIs(t, err, codehost.ErrRejected)
			assert.Empty(t, f.creds.asked)
			assert.Zero(t, f.api.total())
		})
	}
}

func TestTakesInBranchPast(t *testing.T) {
	const since = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const older = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const newer = "cccccccccccccccccccccccccccccccccccccccc"
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	for name, tc := range map[string]struct {
		mergeBase, status string
		want              bool
	}{
		"head built on since":                          {mergeBase: since, want: false},
		"head branched before since (ancestor)":        {mergeBase: older, status: "behind", want: false},
		"merge base is since by another route":         {mergeBase: older, status: "identical", want: false},
		"head took in main past since":                 {mergeBase: newer, status: "ahead", want: true},
		"merge base on another line of main's history": {mergeBase: newer, status: "diverged", want: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 200, map[string]any{"merge_base_commit": map[string]any{"sha": tc.mergeBase}})
			if tc.mergeBase != since {
				f.api.on("GET", "/repos/acme/widgets/compare/"+since+"..."+tc.mergeBase, 200, map[string]any{"status": tc.status})
			}

			got, err := f.ch.TakesInBranchPast(context.Background(), f.tenant, repo, "main", since, headSHA)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("an unanswered or unknown comparison is an error, never a pass", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 500, map[string]any{"message": "boom"})
		_, err := f.ch.TakesInBranchPast(context.Background(), f.tenant, repo, "main", since, headSHA)
		require.Error(t, err)

		f = newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/main..."+headSHA, 200, map[string]any{"merge_base_commit": map[string]any{"sha": newer}})
		f.api.on("GET", "/repos/acme/widgets/compare/"+since+"..."+newer, 200, map[string]any{"status": "weird"})
		_, err = f.ch.TakesInBranchPast(context.Background(), f.tenant, repo, "main", since, headSHA)
		require.ErrorIs(t, err, githubapp.ErrBadResponse)
	})

	t.Run("a since that is not a commit is refused before any request", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.ch.TakesInBranchPast(context.Background(), f.tenant, repo, "main", "main", headSHA)
		require.ErrorIs(t, err, codehost.ErrRejected)
		assert.Empty(t, f.creds.asked)
	})
}

func TestEnsureBranch_CreatesAMissingBranchAtTheCommit(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 201, map[string]any{"ref": "refs/heads/ticket-4-a2"})
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	err := f.ch.EnsureBranch(context.Background(), f.tenant, repo, "ticket-4-a2", headSHA)

	require.NoError(t, err)
	calls := f.api.calls("POST", "/repos/acme/widgets/git/refs")
	require.Len(t, calls, 1)
	assert.Equal(t, "refs/heads/ticket-4-a2", calls[0].Body["ref"])
	assert.Equal(t, headSHA, calls[0].Body["sha"])
	assert.Equal(t, []asked{{f.tenant, repo, codehost.CreateBranch}}, f.creds.asked)
}

func TestEnsureBranch_ABranchAtAnotherCommitIsSuccessAndIsNotMoved(t *testing.T) {
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/heads/ticket-4-a2", 200, map[string]any{"object": map[string]any{"sha": "cccc"}})

	err := f.ch.EnsureBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "ticket-4-a2", headSHA)

	require.NoError(t, err)
	assert.Len(t, f.api.calls("POST", "/repos/acme/widgets/git/refs"), 1)
	assert.Len(t, f.api.calls("GET", "/repos/acme/widgets/git/ref/heads/ticket-4-a2"), 1, "the existing branch is read, not assumed")
	assert.Equal(t, 2, f.api.total(), "the create and the read are the only calls: nothing updates the ref")
}

func TestEnsureBranch_RefusalsThatAreNotAnExistingBranchStayFailures(t *testing.T) {
	t.Run("bad commit", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Object does not exist"})
		f.api.on("GET", "/repos/acme/widgets/git/ref/heads/b", 404, map[string]any{"message": "Not Found"})

		err := f.ch.EnsureBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "b", headSHA)

		require.ErrorIs(t, err, codehost.ErrRejected)
	})
	t.Run("the read fails", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
		f.api.on("GET", "/repos/acme/widgets/git/ref/heads/b", 500, map[string]any{"message": "boom"})

		err := f.ch.EnsureBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "b", headSHA)

		var apiErr *githubapp.APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 500, apiErr.Status)
	})
	for name, args := range map[string][2]string{"no branch": {"", headSHA}, "no commit": {"b", ""}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)

			err := f.ch.EnsureBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, args[0], args[1])

			require.ErrorIs(t, err, codehost.ErrRejected)
			assert.Empty(t, f.creds.asked)
			assert.Zero(t, f.api.total())
		})
	}
}

func (f *fixture) pullTitled(base, title string) {
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "state": "open", "title": title, "head": map[string]string{"sha": headSHA}, "base": map[string]string{"ref": base}})
}

func TestRetargetChange_PatchesTheBase(t *testing.T) {
	f := newFixture(t)
	f.pullTitled("old", "t")
	f.api.on("PATCH", pullPath, 200, map[string]any{"number": 12})

	require.NoError(t, f.ch.RetargetChange(context.Background(), f.tenant, changeRef, "main"))

	calls := f.api.calls("PATCH", pullPath)
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"base": "main"}, calls[0].Body)
	assert.Equal(t, []codehost.Operation{codehost.RetargetChange}, f.creds.ops())
}

func TestRetargetChange_AlreadyOnTheBaseWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.pullTitled("main", "t")
	require.NoError(t, f.ch.RetargetChange(context.Background(), f.tenant, changeRef, "main"))
	assert.Empty(t, f.api.calls("PATCH", pullPath))
}

func TestRetargetChange_Refusals(t *testing.T) {
	f := newFixture(t)
	assert.ErrorIs(t, f.ch.RetargetChange(context.Background(), f.tenant, changeRef, "main"), codehost.ErrChangeNotFound)

	f = newFixture(t)
	f.pullTitled("old", "t")
	f.api.on("PATCH", pullPath, 422, map[string]string{"message": "no"})
	assert.ErrorIs(t, f.ch.RetargetChange(context.Background(), f.tenant, changeRef, "main"), codehost.ErrRejected)

	assert.ErrorIs(t, f.ch.RetargetChange(context.Background(), f.tenant, changeRef, ""), codehost.ErrRejected)
}

func TestRetitleChange_PatchesTheTitle(t *testing.T) {
	f := newFixture(t)
	f.pullTitled("main", "old")
	f.api.on("PATCH", pullPath, 200, map[string]any{"number": 12})

	require.NoError(t, f.ch.RetitleChange(context.Background(), f.tenant, changeRef, "new"))

	calls := f.api.calls("PATCH", pullPath)
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"title": "new"}, calls[0].Body)
	assert.Equal(t, []codehost.Operation{codehost.RetitleChange}, f.creds.ops())
}

func TestRetitleChange_AlreadyTitledWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.pullTitled("main", "same")
	require.NoError(t, f.ch.RetitleChange(context.Background(), f.tenant, changeRef, "same"))
	assert.Empty(t, f.api.calls("PATCH", pullPath))
}

func TestRetitleChange_Refusals(t *testing.T) {
	f := newFixture(t)
	assert.ErrorIs(t, f.ch.RetitleChange(context.Background(), f.tenant, changeRef, "x"), codehost.ErrChangeNotFound)
	assert.ErrorIs(t, f.ch.RetitleChange(context.Background(), f.tenant, changeRef, "  "), codehost.ErrRejected)

	f = newFixture(t)
	f.pullTitled("main", "old")
	f.api.on("PATCH", pullPath, 422, map[string]string{"message": "no"})
	assert.ErrorIs(t, f.ch.RetitleChange(context.Background(), f.tenant, changeRef, "new"), codehost.ErrRejected)
}

func TestDeleteBranch_DeletesTheRefWithABranchWriteToken(t *testing.T) {
	f := newFixture(t)
	const path = "/repos/acme/widgets/git/refs/heads/slop-haus/base/ticket-4"
	f.api.on("DELETE", path, 204, nil)
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	require.NoError(t, f.ch.DeleteBranch(context.Background(), f.tenant, repo, "slop-haus/base/ticket-4"))

	assert.Len(t, f.api.calls("DELETE", path), 1)
	assert.Equal(t, []asked{{f.tenant, repo, codehost.DeleteBranch}}, f.creds.asked)
}

func TestDeleteBranch_AMissingBranchIsSuccess(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	const path = "/repos/acme/widgets/git/refs/heads/gone"
	f := newFixture(t)
	f.api.on("DELETE", path, 422, map[string]string{"message": "Reference does not exist"})
	require.NoError(t, f.ch.DeleteBranch(context.Background(), f.tenant, repo, "gone"))
	assert.Len(t, f.api.calls("DELETE", path), 1)
}

func TestDeleteBranch_AMissingRepositoryIsNotADeletedBranch(t *testing.T) {
	f := newFixture(t)
	f.api.on("DELETE", "/repos/acme/widgets/git/refs/heads/gone", 404, map[string]string{"message": "Not Found"})
	err := f.ch.DeleteBranch(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "gone")
	assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
}

func TestDeleteBranch_OtherRefusalsAreErrors(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	f := newFixture(t)
	f.api.on("DELETE", "/repos/acme/widgets/git/refs/heads/main", 422, map[string]string{"message": "Cannot delete the default branch"})
	assert.ErrorIs(t, f.ch.DeleteBranch(context.Background(), f.tenant, repo, "main"), codehost.ErrRejected)
	assert.ErrorIs(t, f.ch.DeleteBranch(context.Background(), f.tenant, repo, ""), codehost.ErrRejected)
}

func TestCommitsAhead(t *testing.T) {
	const since = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	t.Run("reads ahead_by of the branch against the commit", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/"+since+"...ticket-1-a1", 200, map[string]any{"ahead_by": 3})
		got, err := f.ch.CommitsAhead(context.Background(), f.tenant, repo, since, "ticket-1-a1")
		require.NoError(t, err)
		assert.Equal(t, 3, got)
	})

	t.Run("a branch still at the commit has none", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/"+since+"...ticket-1-a1", 200, map[string]any{"ahead_by": 0})
		got, err := f.ch.CommitsAhead(context.Background(), f.tenant, repo, since, "ticket-1-a1")
		require.NoError(t, err)
		assert.Zero(t, got)
	})

	t.Run("an unanswered comparison is an error, never zero", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/"+since+"...ticket-1-a1", 500, map[string]any{"message": "boom"})
		_, err := f.ch.CommitsAhead(context.Background(), f.tenant, repo, since, "ticket-1-a1")
		require.Error(t, err)

		f = newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/"+since+"...ticket-1-a1", 200, map[string]any{})
		_, err = f.ch.CommitsAhead(context.Background(), f.tenant, repo, since, "ticket-1-a1")
		require.ErrorIs(t, err, githubapp.ErrBadResponse)
	})
}

func TestCompareBranches(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	t.Run("reads ahead_by and behind_by of base...branch", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/main...team/a", 200, map[string]any{"ahead_by": 2, "behind_by": 5})
		got, err := f.ch.CompareBranches(context.Background(), f.tenant, repo, "main", "team/a")
		require.NoError(t, err)
		assert.Equal(t, &codehost.BranchComparison{Ahead: 2, Behind: 5}, got)
		assert.Equal(t, []asked{{f.tenant, repo, codehost.CompareBase}}, f.creds.asked)
	})

	t.Run("a missing branch is not found", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/main...gone", 404, map[string]any{"message": "Not Found"})
		_, err := f.ch.CompareBranches(context.Background(), f.tenant, repo, "main", "gone")
		assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
	})

	t.Run("an answer without counts is an error, never in step", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("GET", "/repos/acme/widgets/compare/main...team-a", 200, map[string]any{"ahead_by": 0})
		_, err := f.ch.CompareBranches(context.Background(), f.tenant, repo, "main", "team-a")
		assert.ErrorIs(t, err, githubapp.ErrBadResponse)
	})
}

func TestMergeBranch(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	const path = "/repos/acme/widgets/merges"

	t.Run("a merge commit", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("POST", path, 201, map[string]any{"sha": "abc"})
		got, err := f.ch.MergeBranch(context.Background(), f.tenant, repo, "team-a", "main")
		require.NoError(t, err)
		assert.Equal(t, &codehost.BranchMerge{SHA: "abc"}, got)
		calls := f.api.calls("POST", path)
		require.Len(t, calls, 1)
		assert.Equal(t, "team-a", calls[0].Body["base"])
		assert.Equal(t, "main", calls[0].Body["head"])
		assert.Equal(t, []asked{{f.tenant, repo, codehost.MergeBranch}}, f.creds.asked)
	})

	t.Run("nothing to merge reports the branch's head", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("POST", path, 204, nil)
		f.api.on("GET", "/repos/acme/widgets/commits/team-a", 200, map[string]any{"sha": "def"})
		got, err := f.ch.MergeBranch(context.Background(), f.tenant, repo, "team-a", "main")
		require.NoError(t, err)
		assert.Equal(t, &codehost.BranchMerge{SHA: "def", UpToDate: true}, got)
	})

	t.Run("a conflict is ErrMergeConflict", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("POST", path, 409, map[string]any{"message": "Merge conflict"})
		_, err := f.ch.MergeBranch(context.Background(), f.tenant, repo, "team-a", "main")
		assert.ErrorIs(t, err, codehost.ErrMergeConflict)
	})

	t.Run("a missing branch is not found", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("POST", path, 404, map[string]any{"message": "Base does not exist"})
		_, err := f.ch.MergeBranch(context.Background(), f.tenant, repo, "gone", "main")
		assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
	})
}

func TestUpdateBranch(t *testing.T) {
	const path = pullPath + "/update-branch"
	fast := github.PollOptions{Attempts: 3, Interval: time.Millisecond}
	update := func(f *fixture) (*codehost.BranchUpdate, error) {
		return f.ch.UpdateBranch(context.Background(), f.tenant, &codehost.UpdateBranchRequest{Change: changeRef, HeadSHA: "old"})
	}

	t.Run("accepted: the new head is read back, pinned to the head it was for", func(t *testing.T) {
		f := newFixtureWithPoll(t, fast)
		f.api.on("PUT", path, 202, map[string]any{"message": "Updating pull request branch."})
		f.pullIs(false, "new")
		got, err := update(f)
		require.NoError(t, err)
		assert.Equal(t, &codehost.BranchUpdate{SHA: "new"}, got)
		calls := f.api.calls("PUT", path)
		require.Len(t, calls, 1)
		assert.Equal(t, map[string]any{"expected_head_sha": "old"}, calls[0].Body)
		assert.Equal(t, []codehost.Operation{codehost.UpdateChangeBranch}, f.creds.ops())
	})

	t.Run("a head that arrives after the acceptance is waited for", func(t *testing.T) {
		f := newFixtureWithPoll(t, fast)
		f.api.on("PUT", path, 202, map[string]any{})
		var reads atomic.Int32
		f.api.routes["GET "+pullPath] = func(*http.Request) (int, any) {
			sha := "old"
			if reads.Add(1) >= 3 {
				sha = "new"
			}
			return 200, map[string]any{"number": 12, "head": map[string]string{"sha": sha}}
		}
		got, err := update(f)
		require.NoError(t, err)
		assert.Equal(t, "new", got.SHA)
		assert.EqualValues(t, 3, reads.Load())
	})

	t.Run("a head that never moves is pending, not success", func(t *testing.T) {
		f := newFixtureWithPoll(t, fast)
		f.api.on("PUT", path, 202, map[string]any{})
		f.pullIs(false, "old")
		_, err := update(f)
		assert.ErrorIs(t, err, codehost.ErrUpdatePending)
		assert.Len(t, f.api.calls("GET", pullPath), 3)
	})

	t.Run("422s are told apart by message", func(t *testing.T) {
		for msg, want := range map[string]error{
			"merge conflict between base and head":             codehost.ErrMergeConflict,
			"Expected head sha didn't match current head ref.": codehost.ErrHeadMoved,
			"something else": codehost.ErrRejected,
		} {
			f := newFixture(t)
			f.api.on("PUT", path, 422, map[string]any{"message": msg})
			_, err := update(f)
			assert.ErrorIs(t, err, want, msg)
		}
	})

	t.Run("no new commits on the base is up to date", func(t *testing.T) {
		f := newFixture(t)
		f.api.on("PUT", path, 422, map[string]any{"message": "There are no new commits on the base branch."})
		got, err := update(f)
		require.NoError(t, err)
		assert.Equal(t, &codehost.BranchUpdate{SHA: "old", UpToDate: true}, got)
	})

	t.Run("an empty head SHA is refused without a call", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.ch.UpdateBranch(context.Background(), f.tenant, &codehost.UpdateBranchRequest{Change: changeRef})
		assert.ErrorIs(t, err, codehost.ErrRejected)
		assert.Zero(t, f.api.total())
	})
}

func TestCreateTag_CreatesATagRefAndKnowsAnExistingOne(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	f := newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 201, map[string]any{})

	require.NoError(t, f.ch.CreateTag(context.Background(), f.tenant, repo, "slop-haus/base/4", headSHA))

	calls := f.api.calls("POST", "/repos/acme/widgets/git/refs")
	require.Len(t, calls, 1)
	assert.Equal(t, "refs/tags/slop-haus/base/4", calls[0].Body["ref"])

	f = newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/tags/slop-haus/base/4", 200, map[string]any{"object": map[string]any{"sha": headSHA}})
	require.NoError(t, f.ch.CreateTag(context.Background(), f.tenant, repo, "slop-haus/base/4", headSHA), "the same commit is success")

	f = newFixture(t)
	f.api.on("POST", "/repos/acme/widgets/git/refs", 422, map[string]any{"message": "Reference already exists"})
	f.api.on("GET", "/repos/acme/widgets/git/ref/tags/slop-haus/base/4", 200, map[string]any{"object": map[string]any{"sha": "cccc"}})
	err := f.ch.CreateTag(context.Background(), f.tenant, repo, "slop-haus/base/4", headSHA)
	var conflict *codehost.TagConflictError
	require.ErrorAs(t, err, &conflict)
	require.ErrorIs(t, err, codehost.ErrTagConflict)
	assert.Equal(t, "cccc", conflict.Have)
}

func TestResolveRef(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	const full = "dddddddddddddddddddddddddddddddddddddddd"
	const tagObj = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	type route struct {
		path   string
		status int
		body   map[string]any
	}
	obj := func(typ, sha string) map[string]any {
		return map[string]any{"object": map[string]any{"type": typ, "sha": sha}}
	}
	for name, tc := range map[string]struct {
		ref    string
		routes []route
		want   *codehost.ResolvedRef
		err    error
	}{
		"lightweight tag": {ref: "v1", routes: []route{{"/git/ref/tags/v1", 200, obj("commit", full)}},
			want: &codehost.ResolvedRef{SHA: full, Kind: codehost.RefTag}},
		"annotated tag is followed to its commit": {ref: "v2", routes: []route{
			{"/git/ref/tags/v2", 200, obj("tag", tagObj)}, {"/git/tags/" + tagObj, 200, obj("commit", full)}},
			want: &codehost.ResolvedRef{SHA: full, Kind: codehost.RefTag}},
		"short hash": {ref: "dddddddd", routes: []route{{"/commits/dddddddd", 200, map[string]any{"sha": full}}},
			want: &codehost.ResolvedRef{SHA: full, Kind: codehost.RefCommit}},
		"unknown full hash": {ref: full, routes: []route{{"/commits/" + full, 422, map[string]any{"message": "No commit found for SHA: " + full}}},
			err: codehost.ErrChangeNotFound},
		"ambiguous short hash": {ref: "dddddddd", routes: []route{{"/commits/dddddddd", 422, map[string]any{"message": "Ambiguous"}}},
			err: codehost.ErrRejected},
		"a hash-named branch is a branch, not a commit": {ref: "dddddddd", routes: []route{
			{"/git/ref/heads/dddddddd", 200, obj("commit", full)}, {"/commits/dddddddd", 200, map[string]any{"sha": full}}},
			err: codehost.ErrRefIsBranch},
		"a hash-named tag does not shadow the commit": {ref: "dddddddd", routes: []route{
			{"/git/ref/tags/dddddddd", 200, obj("commit", tagObj)}, {"/commits/dddddddd", 200, map[string]any{"sha": full}}},
			want: &codehost.ResolvedRef{SHA: full, Kind: codehost.RefCommit}},
		"branch": {ref: "main", routes: []route{{"/git/ref/heads/main", 200, obj("commit", full)}},
			err: codehost.ErrRefIsBranch},
		"nothing": {ref: "ghost", err: codehost.ErrChangeNotFound},
		"tag naming a tree": {ref: "t", routes: []route{{"/git/ref/tags/t", 200, obj("tree", full)}},
			err: codehost.ErrRejected},
		"not a ref name": {ref: "a b", err: codehost.ErrRejected},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			for _, r := range tc.routes {
				f.api.on("GET", "/repos/acme/widgets"+r.path, r.status, r.body)
			}

			got, err := f.ch.ResolveRef(context.Background(), f.tenant, repo, tc.ref)

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, []asked{{f.tenant, repo, codehost.ReadBranch}}, f.creds.asked)
		})
	}
}

func TestTreeOf_ReadsTheCommitsTreeWithABranchReadToken(t *testing.T) {
	f := newFixture(t)
	tree := strings.Repeat("7", 40)
	f.api.on("GET", "/repos/acme/widgets/git/commits/"+headSHA, 200, map[string]any{"sha": headSHA, "tree": map[string]any{"sha": tree}})
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	got, err := f.ch.TreeOf(context.Background(), f.tenant, repo, headSHA)

	require.NoError(t, err)
	assert.Equal(t, tree, got)
	assert.Equal(t, []asked{{f.tenant, repo, codehost.ReadBranch}}, f.creds.asked)
}

func TestTreeOf_FailuresAreNeverATree(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	for name, tc := range map[string]struct {
		sha    string
		status int
		body   any
		err    error
	}{
		"unknown commit":    {sha: headSHA, status: 404, body: map[string]any{"message": "Not Found"}, err: codehost.ErrChangeNotFound},
		"no tree":           {sha: headSHA, status: 200, body: map[string]any{"sha": headSHA}, err: githubapp.ErrBadResponse},
		"malformed tree":    {sha: headSHA, status: 200, body: map[string]any{"tree": map[string]any{"sha": strings.Repeat("z", 40)}}, err: githubapp.ErrBadResponse},
		"not a full commit": {sha: "main", err: codehost.ErrRejected},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if tc.status != 0 {
				f.api.on("GET", "/repos/acme/widgets/git/commits/"+tc.sha, tc.status, tc.body)
			}

			got, err := f.ch.TreeOf(context.Background(), f.tenant, repo, tc.sha)

			require.ErrorIs(t, err, tc.err)
			assert.Empty(t, got)
		})
	}
}

func TestTreeOf_WithoutACredentialNothingIsAsked(t *testing.T) {
	f := newFixture(t)
	f.creds.err = codehost.ErrNotConnected

	got, err := f.ch.TreeOf(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, headSHA)

	require.ErrorIs(t, err, codehost.ErrNotConnected)
	assert.Empty(t, got)
	assert.Zero(t, f.api.total())
}

func TestBranchChecks_ReadTheChecksOfTheBranchsHeadWithoutAChange(t *testing.T) {
	f := newFixture(t)
	f.checksAre([]run{{"build", "completed", "failure"}}, map[string]string{"ci": "success"})
	f.api.on("GET", "/repos/acme/widgets/commits/batch/7", 200, map[string]any{"sha": headSHA})
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}

	got, err := f.ch.BranchChecks(context.Background(), f.tenant, repo, "batch/7")

	require.NoError(t, err)
	assert.Equal(t, headSHA, got.HeadSHA)
	assert.Equal(t, codehost.CheckFailure, got.State)
	assert.Len(t, got.Checks, 2)
	assert.Empty(t, got.Base)
}

func TestBranchChecks_AMissingBranchIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", "/repos/acme/widgets/commits/gone", 404, map[string]any{"message": "Not Found"})

	got, err := f.ch.BranchChecks(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "gone")

	require.ErrorIs(t, err, codehost.ErrChangeNotFound)
	assert.Nil(t, got)
}

func TestBranchHead_AnUnknownBranchIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", "/repos/acme/widgets/commits/gone", 422, map[string]string{"message": "No commit found for SHA: gone"})

	got, err := f.ch.BranchHead(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "gone")

	assert.Nil(t, got)
	assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
}

func TestMerge_AStaleSHAReportedAsNotMergeableIsAMovedHead(t *testing.T) {
	f := newFixture(t)
	f.api.on("PUT", pullPath+"/merge", 405, map[string]string{"message": "Pull Request is not mergeable"})
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "head": map[string]string{"sha": "2222222222222222222222222222222222222222"}, "base": map[string]string{"ref": "main"}})

	_, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s"})

	require.ErrorIs(t, err, codehost.ErrHeadMoved)
}

func TestMerge_NotMergeableAtTheSameHeadStaysNotMergeable(t *testing.T) {
	f := newFixture(t)
	f.api.on("PUT", pullPath+"/merge", 405, map[string]string{"message": "Pull Request is not mergeable"})
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "head": map[string]string{"sha": headSHA}, "base": map[string]string{"ref": "main"}})

	_, err := f.ch.Merge(context.Background(), f.tenant, &codehost.MergeRequest{Change: changeRef, HeadSHA: headSHA, Subject: "s"})

	require.ErrorIs(t, err, codehost.ErrNotMergeable)
	assert.NotErrorIs(t, err, codehost.ErrHeadMoved)
}

func TestBranchHead_OtherRefusalsStayRejected(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", "/repos/acme/widgets/commits/odd", 422, map[string]string{"message": "Validation Failed"})

	_, err := f.ch.BranchHead(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "odd")

	require.ErrorIs(t, err, codehost.ErrRejected)
	assert.NotErrorIs(t, err, codehost.ErrChangeNotFound)
}
