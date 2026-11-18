package reviewreport

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The client (slop-review/report/reporter) retries a 429 or 5xx and drops any
// other 4xx, so a refusal that retrying cannot fix must never be a 5xx.
var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ErrRateLimited, Kind: httperr.TooMany},
		{Err: ErrRunTaken, Kind: httperr.Conflict, Message: "the run id is already used", Warn: "review run id posted by a second poster"},
		{Err: ErrFindingTaken, Kind: httperr.Conflict, Message: "a finding id is already used", Warn: "review finding id posted twice"},
		{Err: ErrNoFinding, Kind: httperr.NotFound, Message: "no such finding"},
		{Err: ErrNoProviderConfig, Kind: httperr.NotFound, Message: "no provider config is set for this tenant"},
		{Err: ticket.ErrNoSuchAttempt, Kind: httperr.NotFound},
	},
	Unexpected: "unexpected review report error",
}

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

// IngestRunRequest takes the run raw: it is decoded and checked with the
// contract's own Validate, not against a schema huma would derive from it
// (which would refuse the nulls a failed run sends for its empty lists, and
// quote a refused body's values -- a diff -- back in the error).
type IngestRunRequest struct {
	RawBody []byte `contentType:"application/json"`
}

type IngestRunResponse struct {
	// Status is 201 for a new run, 200 for one already stored.
	Status int
	Body   report.Accepted
}

func (h *Handler) IngestRun(ctx context.Context, in *IngestRunRequest) (*IngestRunResponse, error) {
	accepted, created, err := h.svc.Ingest(ctx, in.RawBody)
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return &IngestRunResponse{Status: status, Body: *accepted}, nil
}

type LabelFindingRequest struct {
	// A string, not a uuid: a malformed id is answered as no finding, like
	// any other id the caller has no finding under.
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Verdict string `json:"verdict" enum:"confirmed,false_positive,fabricated,duplicate,wont_fix"`
		Reason  string `json:"reason,omitempty" maxLength:"4096"`
		FixedIn string `json:"fixed_in,omitempty" maxLength:"256" doc:"the commit that fixed a confirmed finding"`
	}
}

type LabelFindingResponse struct {
	Status int
}

func (h *Handler) LabelFinding(ctx context.Context, in *LabelFindingRequest) (*LabelFindingResponse, error) {
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, httperr.Map(h.logger, ErrNoFinding, &errTable)
	}
	l := &Label{Verdict: in.Body.Verdict, Reason: in.Body.Reason, FixedIn: in.Body.FixedIn}
	if err := h.svc.Label(ctx, id, l); err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	return &LabelFindingResponse{Status: http.StatusCreated}, nil
}

type ProviderConfigResponse struct {
	Body providerconfig.Config
}

func (h *Handler) GetProviderConfig(ctx context.Context, _ *struct{}) (*ProviderConfigResponse, error) {
	cfg, err := h.svc.ProviderConfig(ctx)
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	return &ProviderConfigResponse{Body: *cfg}, nil
}

type AttemptReviewRunsRequest struct {
	ID      int64 `path:"id"`
	Attempt int   `path:"n" minimum:"1"`
	Limit   int   `query:"limit" default:"20" minimum:"1" maximum:"100"`
}

type ReviewLabelBody struct {
	Verdict    string    `json:"verdict" enum:"confirmed,false_positive,fabricated,duplicate,wont_fix"`
	Reason     string    `json:"reason"`
	FixedIn    *string   `json:"fixed_in" doc:"the commit that fixed a confirmed finding; null for none"`
	LabelledAt time.Time `json:"labelled_at"`
}

type ReviewFindingBody struct {
	ID       uuid.UUID `json:"id"`
	File     string    `json:"file"`
	Line     int       `json:"line" doc:"0 when the finding names no line"`
	Severity string    `json:"severity" enum:"blocker,major,minor,nit"`
	Summary  string    `json:"summary"`
	Detail   string    `json:"detail"`
	Personas []string  `json:"personas"`
	// Label is the finding's current label: its agent's newest.
	Label *ReviewLabelBody `json:"label" doc:"null when the finding has no label"`
}

type ReviewFailureBody struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

type ReviewRunBody struct {
	ID           uuid.UUID           `json:"id"`
	PostedByKind string              `json:"posted_by_kind" enum:"supervisor,agent" doc:"a supervisor's run is one it ran for its agent; an agent's is the agent's own word"`
	PostedByID   uuid.UUID           `json:"posted_by_id"`
	PostedByName string              `json:"posted_by_name"`
	Kind         string              `json:"kind" enum:"review,bench"`
	Status       string              `json:"status" enum:"completed,failed"`
	Failure      *ReviewFailureBody  `json:"failure" doc:"why a failed run failed; null for a completed one"`
	Branch       string              `json:"branch"`
	HeadSHA      string              `json:"head_sha"`
	Personas     []string            `json:"personas"`
	Verdict      string              `json:"verdict" doc:"empty for a failed run"`
	Summary      string              `json:"summary"`
	Incomplete   bool                `json:"incomplete"`
	Provider     string              `json:"provider"`
	Model        string              `json:"model"`
	StartedAt    time.Time           `json:"started_at"`
	FinishedAt   time.Time           `json:"finished_at"`
	ReceivedAt   time.Time           `json:"received_at"`
	Findings     []ReviewFindingBody `json:"findings"`
}

type AttemptReviewRunsResponse struct {
	Body struct {
		Runs []ReviewRunBody `json:"runs" doc:"newest first"`
	}
}

func (h *Handler) AttemptReviewRuns(ctx context.Context, in *AttemptReviewRunsRequest) (*AttemptReviewRunsResponse, error) {
	runs, err := h.svc.AttemptRuns(ctx, ticket.AttemptRef{TicketID: in.ID, Number: in.Attempt}, in.Limit)
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	resp := &AttemptReviewRunsResponse{}
	resp.Body.Runs = make([]ReviewRunBody, 0, len(runs))
	for i := range runs {
		resp.Body.Runs = append(resp.Body.Runs, runBody(&runs[i]))
	}
	return resp, nil
}

func runBody(r *AttemptRun) ReviewRunBody {
	b := ReviewRunBody{
		ID: r.ID, PostedByKind: string(r.Poster.Kind), PostedByID: r.Poster.ID, PostedByName: r.PosterName, Kind: r.Kind, Status: r.Status,
		Branch: r.Branch, HeadSHA: r.HeadSHA, Personas: r.Personas, Verdict: r.Verdict, Summary: r.Summary,
		Incomplete: r.Incomplete, Provider: r.Provider, Model: r.Model,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ReceivedAt: r.ReceivedAt,
		Findings: make([]ReviewFindingBody, 0, len(r.Findings)),
	}
	if r.Status == statusFailed {
		b.Failure = &ReviewFailureBody{Class: r.FailureClass, Message: r.FailureMessage}
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		fb := ReviewFindingBody{ID: f.ID, File: f.File, Line: f.Line, Severity: f.Severity, Summary: f.Summary,
			Detail: f.Detail, Personas: f.Personas}
		if l := f.Label; l != nil {
			fb.Label = &ReviewLabelBody{Verdict: l.Verdict, Reason: l.Reason, LabelledAt: l.LabelledAt}
			if l.FixedIn != "" {
				fb.Label.FixedIn = &l.FixedIn
			}
		}
		b.Findings = append(b.Findings, fb)
	}
	return b
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireAgentOrSupervisor(),
		OperationID: "ingestReviewRun",
		Method:      http.MethodPost,
		Path:        report.IngestPath,
		Summary:     "Report a slop-review run (slop-review/report's Run)",
		Description: "201 for a new run, 200 for one already stored (a retry, which changes nothing). The run is filed " +
			"under the attempt its branch names only while that attempt is held by the posting agent, or by an agent " +
			"the posting supervisor runs. 422 for a run the " +
			"contract refuses, 409 for a run or finding id that is already another's, 413 over the size limit, " +
			"429 past the per-poster rate limit.",
		MaxBodyBytes: report.MaxReportBytes,
		// See IngestRunRequest: huma's own check would echo the body back.
		SkipValidateBody: true,
	}, h.IngestRun)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireAgent(),
		OperationID:   "labelReviewFinding",
		Method:        http.MethodPost,
		Path:          "/api/v1/findings/{id}/labels",
		Summary:       "Label a finding of a review run of the attempt you hold, or of one you posted; the newest label is its current one",
		Description:   "201. 404 for any other finding, as for one that does not exist. A supervisor cannot label (403).",
		DefaultStatus: http.StatusCreated,
		MaxBodyBytes:  16 << 10,
	}, h.LabelFinding)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireAgentOrSupervisor(),
		OperationID: "getReviewProviderConfig",
		Method:      http.MethodGet,
		Path:        providerconfig.Path,
		Summary:     "The tenant's central provider config for slop-review",
		Description: "404 when the tenant has none, which a reviewer reads as: use the local settings.",
	}, h.GetProviderConfig)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listAttemptReviewRuns",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/attempts/{n}/review-runs",
		Summary:     "An attempt's slop-review runs, newest first, with their findings and each finding's current label",
		Description: "404 for a ticket or attempt the tenant does not have.",
	}, h.AttemptReviewRuns)
}
