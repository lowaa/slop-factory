package onboarding

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{Unexpected: "unexpected onboarding error"}

type OnboardingStepBody struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Path   string `json:"path" doc:"the board page where the step is done"`
	Done   bool   `json:"done"`
}

type OnboardingStatusBody struct {
	Steps []OnboardingStepBody `json:"steps"`
	// CodeWorkReady is false when workers have nothing to work on: no repo declared and no code host connected.
	CodeWorkReady bool `json:"code_work_ready"`
}

type OnboardingStatusResponse struct {
	Body OnboardingStatusBody
}

func (h *Handler) Get(ctx context.Context, _ *struct{}) (*OnboardingStatusResponse, error) {
	st, err := h.svc.Status(ctx)
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	body := OnboardingStatusBody{Steps: make([]OnboardingStepBody, len(st.Steps)), CodeWorkReady: st.CodeWorkReady()}
	for i, step := range st.Steps {
		body.Steps[i] = OnboardingStepBody{Key: step.Key, Title: step.Title, Detail: step.Detail, Path: step.Path, Done: step.Done}
	}
	return &OnboardingStatusResponse{Body: body}, nil
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getOnboardingStatus",
		Method:      http.MethodGet,
		Path:        "/api/onboarding",
		Summary:     "The setup a tenant still needs before workers can do code work",
	}, h.Get)
}
