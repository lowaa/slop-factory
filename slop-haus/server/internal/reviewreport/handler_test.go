package reviewreport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// fakeStore returns what the test gives it and records what it was asked.
type fakeStore struct {
	inserted *reviewreport.Inserted
	err      error

	insertedBy *reviewreport.Poster
	insertedRn *report.Run
	labelledBy uuid.UUID
	labelled   uuid.UUID
	label      *reviewreport.Label
	runs       []reviewreport.AttemptRun
}

func (f *fakeStore) InsertRun(_ context.Context, poster *reviewreport.Poster, run *report.Run) (*reviewreport.Inserted, error) {
	f.insertedBy, f.insertedRn = poster, run
	return f.inserted, f.err
}

func (f *fakeStore) LabelFinding(_ context.Context, agentID, findingID uuid.UUID, l *reviewreport.Label) error {
	f.labelledBy, f.labelled, f.label = agentID, findingID, l
	return f.err
}

func (f *fakeStore) AttemptRuns(context.Context, ticket.AttemptRef, int) ([]reviewreport.AttemptRun, error) {
	return f.runs, f.err
}

func (f *fakeStore) RequestReview(context.Context, uuid.UUID, string, string) (*reviewreport.Request, error) {
	panic("not reached by the handler")
}

func (f *fakeStore) OpenRequest(context.Context, uuid.UUID, int64, int) (*reviewreport.Request, error) {
	panic("not reached by the handler")
}

func (f *fakeStore) HeldAttemptRuns(context.Context, uuid.UUID, string, int) (*reviewreport.HeadReview, error) {
	panic("not reached by the handler")
}

func (f *fakeStore) RunsForAttemptHead(context.Context, int64, int, string, reviewreport.PostedBy) ([]reviewreport.HeadRun, error) {
	panic("not reached by the handler")
}

// fakeHistory is the provider config setting's history, newest first.
type fakeHistory struct {
	changes []settings.Change

	name          string
	before, limit int
}

func (f *fakeHistory) History(_ context.Context, name string, before int64, limit int) ([]settings.Change, int64, error) {
	f.name, f.before, f.limit = name, int(before), limit
	return f.changes, 0, nil
}

type identity struct {
	agent, tenant uuid.UUID
	supervisor    bool
}

func (i *identity) AgentID() uuid.UUID  { return i.agent }
func (i *identity) AgentName() string   { return "builder" }
func (i *identity) TenantID() uuid.UUID { return i.tenant }
func (i *identity) ReadOnly() bool      { return false }
func (i *identity) IsSupervisor() bool  { return i.supervisor }

const (
	agentToken      = "agent-token"
	supervisorToken = "supervisor-token"
)

type wire struct {
	srv     *httptest.Server
	store   *fakeStore
	history *fakeHistory
	caller  *identity
	// sup is the supervisor's credential, in the caller's tenant.
	sup *identity
}

func newWire(t *testing.T, limit int) *wire {
	t.Helper()
	w := &wire{store: &fakeStore{inserted: &reviewreport.Inserted{Created: true}}, history: &fakeHistory{},
		caller: &identity{agent: uuid.New(), tenant: uuid.New()}}
	w.sup = &identity{agent: uuid.New(), tenant: w.caller.tenant, supervisor: true}
	svc, err := reviewreport.NewService(&reviewreport.Options{
		Store: w.store, Settings: w.history, BoardURL: "https://board.example",
		Limit: &ratelimit.Config{Name: "review_report", Limit: limit, Window: time.Hour, MaxKeys: 100},
	}, slog.DiscardHandler)
	require.NoError(t, err)
	mux := http.NewServeMux()
	api := humago.New(mux, apiserver.NewConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		switch token {
		case agentToken:
			return w.caller, nil
		case supervisorToken:
			return w.sup, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	reviewreport.RegisterRoutes(api, reviewreport.NewHandler(svc, slog.DiscardHandler))
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wire) post(t *testing.T, path string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.srv.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func (w *wire) reporter(t *testing.T, token string) *reporter.Reporter {
	t.Helper()
	rep, err := reporter.New(w.srv.URL, token, t.TempDir(), slog.DiscardHandler)
	require.NoError(t, err)
	return rep
}

func encode(t *testing.T, run *report.Run) []byte {
	t.Helper()
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	return raw
}

// The real client against the real routes: what it sends is accepted, and
// what it is answered it reads as the contract means.
func TestIngest_TheClientsRunIsAcceptedAsTheCallersAndLinksItsAttempt(t *testing.T) {
	w := newWire(t, 100)
	w.store.inserted = &reviewreport.Inserted{Created: true, Attribution: &reviewreport.Attribution{TicketID: 42, Attempt: 2}}
	run := validRun("ticket-42-a2", "standard")

	res := w.reporter(t, agentToken).Send(context.Background(), run)
	require.Empty(t, res.Warning)
	require.NotNil(t, res.Accepted)
	assert.Equal(t, run.ID, res.Accepted.RunID)
	assert.True(t, res.Accepted.Created)
	assert.Equal(t, "https://board.example/tickets/42/attempts/2?tenant="+w.caller.tenant.String(), res.Accepted.URL)
	assert.Equal(t, byAgent(w.caller.agent), w.store.insertedBy, "the run is the token's agent's")
	assert.Equal(t, run.Findings[0].ID, w.store.insertedRn.Findings[0].ID)
}

func TestIngest_ARetryIsAnsweredOKAndAnUnattributedRunHasNoLink(t *testing.T) {
	w := newWire(t, 100)
	w.store.inserted = &reviewreport.Inserted{Created: false}
	status, body := w.post(t, report.IngestPath, encode(t, validRun("main")))
	assert.Equal(t, http.StatusOK, status, body)
	var accepted report.Accepted
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	assert.False(t, accepted.Created)
	assert.Empty(t, accepted.URL)
}

// A failed run sends null for the lists it never filled; a schema derived
// from Run would refuse that, so the body is checked by Validate alone.
func TestIngest_AcceptsAFailedRunAsTheClientEncodesIt(t *testing.T) {
	w := newWire(t, 100)
	run := &report.Run{
		SchemaVersion: report.FailedSchemaVersion, ID: uuid.New(), Kind: report.KindReview, Status: report.StatusFailed,
		Failure:     &report.Failure{Class: report.FailureAuth, Message: "lost login"},
		ToolVersion: report.ToolVersion{Kind: "git_sha", Value: "c4ca88d"},
		StartedAt:   t0, FinishedAt: t0,
	}
	require.NoError(t, run.Validate())
	require.Contains(t, string(encode(t, run)), `"personas":null`)
	status, body := w.post(t, report.IngestPath, encode(t, run))
	assert.Equal(t, http.StatusCreated, status, body)
}

// Each refusal the client must not retry is a 4xx other than 401 and 429;
// the client drops those rather than spooling them.
func TestIngest_RefusesWhatTheContractRefuses(t *testing.T) {
	w := newWire(t, 100)
	secrets := validRun("main")
	secrets.Config.AllowSecrets = true
	unversioned := validRun("main")
	unversioned.SchemaVersion = report.SchemaVersion + 1
	for name, c := range map[string]struct {
		body   []byte
		status int
	}{
		"not JSON":            {[]byte("{"), http.StatusUnprocessableEntity},
		"not a run":           {[]byte(`{"id": "x"}`), http.StatusUnprocessableEntity},
		"a newer schema":      {encode(t, unversioned), http.StatusUnprocessableEntity},
		"secrets shown":       {encode(t, secrets), http.StatusUnprocessableEntity},
		"over the size limit": {bytes.Repeat([]byte(" "), report.MaxReportBytes+1), http.StatusRequestEntityTooLarge},
		"an empty object":     {[]byte(`{}`), http.StatusUnprocessableEntity},
		"an empty finding":    {[]byte(`{"findings": [{}]}`), http.StatusUnprocessableEntity},
	} {
		w.store.insertedRn = nil
		status, body := w.post(t, report.IngestPath, c.body)
		assert.Equal(t, c.status, status, "%s: %s", name, body)
		assert.Nil(t, w.store.insertedRn, "%s: nothing stored", name)
	}

	res := w.reporter(t, agentToken).Send(context.Background(), secrets)
	assert.Contains(t, res.Warning, "rejected the run (422)", "the client drops it rather than spooling it")
	assert.Contains(t, res.Warning, "secret-shaped files", "refused by the server's own check, not Validate")
	secrets.Config.AllowSecrets = false
	assert.Empty(t, w.reporter(t, agentToken).Send(context.Background(), secrets).Warning, "the same run, redacted, is taken")
}

// A refused report's diff and findings are code, which a refusal must not
// quote back.
func TestIngest_ARefusalNeverEchoesTheReport(t *testing.T) {
	w := newWire(t, 100)
	run := validRun("main", "standard")
	run.Diff = "diff-text-not-to-echo"
	run.Findings[0].Detail = "detail-not-to-echo"
	status, body := w.post(t, report.IngestPath, encode(t, run))
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	assert.NotContains(t, body, "not-to-echo")
}

func TestIngest_ConflictsArePermanentAndCarryNoDetail(t *testing.T) {
	w := newWire(t, 100)
	for _, err := range []error{reviewreport.ErrRunTaken, reviewreport.ErrFindingTaken} {
		w.store.err = err
		status, body := w.post(t, report.IngestPath, encode(t, validRun("main")))
		assert.Equal(t, http.StatusConflict, status, body)
	}
}

func TestLabel_CountsAgainstTheSameLimit(t *testing.T) {
	w := newWire(t, 1)
	status, _ := w.post(t, report.IngestPath, encode(t, validRun("main")))
	require.Equal(t, http.StatusCreated, status)
	status, body := w.post(t, reporter.LabelPath(uuid.New()), []byte(`{"verdict": "confirmed"}`))
	assert.Equal(t, http.StatusTooManyRequests, status, body)
	assert.Nil(t, w.store.label)
}

func TestIngest_PastTheLimitIsRetryable(t *testing.T) {
	w := newWire(t, 1)
	status, _ := w.post(t, report.IngestPath, encode(t, validRun("main")))
	require.Equal(t, http.StatusCreated, status)
	status, body := w.post(t, report.IngestPath, encode(t, validRun("main")))
	assert.Equal(t, http.StatusTooManyRequests, status, body)
}

func TestIngest_AnUnknownTokenIsUnauthorized(t *testing.T) {
	w := newWire(t, 100)
	res := w.reporter(t, "wrong").Send(context.Background(), validRun("main"))
	assert.Contains(t, res.Warning, "401")
	assert.Nil(t, w.store.insertedRn)
}

func TestLabel_TheClientsLabelIsTheCallersAndAnUnknownFindingIsNotFound(t *testing.T) {
	w := newWire(t, 100)
	rep := w.reporter(t, agentToken)
	id := uuid.New()
	require.NoError(t, rep.Label(context.Background(), id, &reporter.LabelRequest{Verdict: "confirmed", Reason: "r", FixedIn: "abc1234"}))
	assert.Equal(t, w.caller.agent, w.store.labelledBy)
	assert.Equal(t, id, w.store.labelled)
	assert.Equal(t, &reviewreport.Label{Verdict: "confirmed", Reason: "r", FixedIn: "abc1234"}, w.store.label)

	w.store.err = reviewreport.ErrNoFinding
	err := rep.Label(context.Background(), uuid.New(), &reporter.LabelRequest{Verdict: "duplicate"})
	assert.ErrorContains(t, err, "has no finding")
}

func TestLabel_RefusesAMalformedLabel(t *testing.T) {
	w := newWire(t, 100)
	path := reporter.LabelPath(uuid.New())
	for name, c := range map[string]struct {
		path, body string
		status     int
	}{
		"an unknown verdict":         {path, `{"verdict": "maybe"}`, http.StatusUnprocessableEntity},
		"a fix for a rejection":      {path, `{"verdict": "wont_fix", "fixed_in": "abc"}`, http.StatusUnprocessableEntity},
		"a finding id that is no id": {"/api/v1/findings/nope/labels", `{"verdict": "confirmed"}`, http.StatusNotFound},
	} {
		w.store.label = nil
		status, body := w.post(t, c.path, []byte(c.body))
		assert.Equal(t, c.status, status, "%s: %s", name, body)
		assert.Nil(t, w.store.label, "%s: nothing stored", name)
	}
}

func TestProviderConfig_NoneSetIsWhatTheClientReadsAsNone(t *testing.T) {
	w := newWire(t, 100)
	rep := w.reporter(t, agentToken)
	for name, changes := range map[string][]settings.Change{
		"never set": nil,
		"cleared":   {{ID: 9, To: settings.StrValue("")}},
		// Saved under a release that knew a provider this one does not.
		"no longer valid": {{ID: 9, To: settings.StrValue(`{"primary": {"provider": "gone", "model": "", "reasoning_effort": ""}}`)}},
	} {
		w.history.changes = changes
		_, err := rep.FetchProviderConfig(context.Background())
		assert.ErrorIs(t, err, reporter.ErrNoProviderConfig, name)
	}
}

func TestProviderConfig_ServesTheSettingRevisionedByItsNewestChange(t *testing.T) {
	w := newWire(t, 100)
	w.history.changes = []settings.Change{{ID: 17, To: settings.StrValue(
		`{"primary": {"provider": "claude", "model": "", "reasoning_effort": "high"},
		  "fallback": {"provider": "openrouter", "model": "openai/gpt-5.6-luna", "reasoning_effort": ""},
		  "fallback_on": ["usage_limit"]}`)}}

	cfg, err := w.reporter(t, agentToken).FetchProviderConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(17), cfg.Revision)
	assert.Equal(t, reviewreport.ProviderConfigSetting, w.history.name)
	assert.Equal(t, 0, w.history.before, "from the newest change")
	assert.Equal(t, 1, w.history.limit, "the newest change alone")
	assert.Equal(t, providerconfig.Choice{Provider: "claude", ReasoningEffort: "high"}, cfg.Primary)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, "openrouter", cfg.Fallback.Provider)
	assert.Equal(t, []report.FailureClass{report.FailureUsageLimit}, cfg.FallbackOn)
}

func TestProviderConfigSetting_RefusesWhatTheReviewerWouldIgnore(t *testing.T) {
	validate := reviewreport.SettingSpecs()[0].Validate
	assert.NoError(t, validate(settings.StrValue("")), "none")
	assert.NoError(t, validate(settings.StrValue(`{"primary": {"provider": "codex", "model": "", "reasoning_effort": ""}}`)))
	for name, v := range map[string]string{
		"not JSON":         `{`,
		"two values":       `{"primary": {"provider": "codex"}} {}`,
		"a misspelt field": `{"primary": {"provider": "codex"}, "falback": {"provider": "claude"}}`,
		"an unknown one":   `{"primary": {"provider": "mystery"}}`,
		"a base URL":       `{"primary": {"provider": "codex", "base_url": "https://evil.example"}}`,
		"too long":         `{"primary": {"provider": "codex", "model": "` + strings.Repeat("a", 9000) + `"}}`,
	} {
		err := validate(settings.StrValue(v))
		assert.ErrorIs(t, err, settings.ErrInvalidSetting, name)
	}
}

func TestAttemptReviewRuns_TheResponseCarriesEachFindingsCurrentLabel(t *testing.T) {
	store := &fakeStore{runs: []reviewreport.AttemptRun{{
		ID: uuid.New(), Status: "completed", Personas: []string{"standard"}, Findings: []reviewreport.AttemptFinding{
			{ID: uuid.New(), Personas: []string{"standard"}},
			{ID: uuid.New(), Label: &reviewreport.CurrentLabel{Label: reviewreport.Label{Verdict: "confirmed", FixedIn: "abc1234"}}},
			{ID: uuid.New(), Label: &reviewreport.CurrentLabel{Label: reviewreport.Label{Verdict: "wont_fix", Reason: "later"}}},
		},
	}, {ID: uuid.New(), Status: "failed", FailureClass: "auth", FailureMessage: "lost login"}}}
	svc, err := reviewreport.NewService(&reviewreport.Options{Store: store, Settings: &fakeHistory{},
		Limit: &ratelimit.Config{Name: "r", Limit: 1, Window: time.Hour, MaxKeys: 1}}, slog.DiscardHandler)
	require.NoError(t, err)
	h := reviewreport.NewHandler(svc, slog.DiscardHandler)

	resp, err := h.AttemptReviewRuns(context.Background(), &reviewreport.AttemptReviewRunsRequest{ID: 1, Attempt: 1, Limit: 20})
	require.NoError(t, err)
	runs := resp.Body.Runs
	require.Len(t, runs, 2)
	assert.Nil(t, runs[0].Failure)
	findings := runs[0].Findings
	assert.Nil(t, findings[0].Label)
	require.NotNil(t, findings[1].Label.FixedIn)
	assert.Equal(t, "abc1234", *findings[1].Label.FixedIn)
	assert.Nil(t, findings[2].Label.FixedIn)
	assert.Equal(t, "later", findings[2].Label.Reason)
	assert.Equal(t, &reviewreport.ReviewFailureBody{Class: "auth", Message: "lost login"}, runs[1].Failure)
	assert.NotNil(t, runs[1].Findings, "an empty list, not null")

	store.err = ticket.ErrNoSuchAttempt
	_, err = h.AttemptReviewRuns(context.Background(), &reviewreport.AttemptReviewRunsRequest{ID: 1, Attempt: 9, Limit: 20})
	var se interface{ GetStatus() int }
	require.ErrorAs(t, err, &se)
	assert.Equal(t, http.StatusNotFound, se.GetStatus())
}

// The supervisor runs slop-review for its agents and posts their runs; the
// run is recorded as the supervisor's, which is what the steward trusts.
func TestSupervisor_PostsRunsAndReadsTheConfigButCannotLabel(t *testing.T) {
	w := newWire(t, 100)
	rep := w.reporter(t, supervisorToken)
	run := validRun("ticket-42-a2", "standard")

	res := rep.Send(context.Background(), run)
	require.Empty(t, res.Warning)
	assert.Equal(t, bySupervisor(w.sup.agent), w.store.insertedBy)

	w.history.changes = []settings.Change{{ID: 3, To: settings.StrValue(`{"primary": {"provider": "codex"}}`)}}
	cfg, err := rep.FetchProviderConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), cfg.Revision)

	err = rep.Label(context.Background(), run.Findings[0].ID, &reporter.LabelRequest{Verdict: "confirmed"})
	assert.ErrorContains(t, err, "403")
	assert.Nil(t, w.store.label, "a supervisor labels nothing")
}
