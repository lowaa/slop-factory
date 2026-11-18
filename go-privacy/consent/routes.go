package consent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

// Operations carries the app's authn markers for the two routes, so each
// goes through the app's own policy (DefaultDeny included). Both routes
// serve anonymous visitors as well as signed-in users, so an app marks them
// with whatever lets a visitor through and still resolves a user when there
// is one: Public today, an optional-user marker once go-auth has one.
type Operations struct {
	Read  map[string]any
	Write map[string]any
}

// DefaultPath is where RegisterRoutes mounts the consent routes.
const DefaultPath = "/api/privacy/consent"

type decisionView struct {
	Granted         bool      `json:"granted"`
	DocumentVersion string    `json:"document_version"`
	DecidedAt       time.Time `json:"decided_at"`
	Source          Source    `json:"source"`
}

type purposeView struct {
	ID        string `json:"id"`
	Kind      Kind   `json:"kind"`
	Necessary bool   `json:"necessary"`
	// Granted is what is effectively in force.
	Granted       bool   `json:"granted"`
	NeedsDecision bool   `json:"needs_decision"`
	GPCLocked     bool   `json:"gpc_locked"`
	DocumentKind  string `json:"document_kind,omitempty"`
	// DocumentVersion is the version a decision must cite.
	DocumentVersion string        `json:"document_version,omitempty"`
	Decision        *decisionView `json:"decision,omitempty"`
	// BasedOn is what a decision on this purpose sends back as based_on.
	BasedOn int64 `json:"based_on"`
}

// StateBody is the consent state a person sees.
type StateBody struct {
	Purposes []purposeView `json:"purposes"`
	// NeedsDecision lists the ids of the purposes to ask about.
	NeedsDecision []string `json:"needs_decision"`
	GPC           bool     `json:"gpc"`
	SignedIn      bool     `json:"signed_in"`
}

func viewOf(e *Effective) *StateBody {
	b := &StateBody{Purposes: []purposeView{}, NeedsDecision: []string{}, GPC: e.GPC, SignedIn: e.SignedIn}
	for _, s := range e.States {
		v := purposeView{
			ID: s.Purpose.ID, Kind: s.Purpose.Kind, Necessary: s.Purpose.Necessary, Granted: s.Granted,
			NeedsDecision: s.NeedsDecision, GPCLocked: s.GPCLocked, DocumentKind: s.Purpose.Document,
			DocumentVersion: s.DocumentVersion, BasedOn: s.EventID,
		}
		if s.Decided {
			v.Decision = &decisionView{s.DecisionGranted, s.DecisionVersion, s.DecidedAt, s.Source}
		}
		if s.NeedsDecision {
			b.NeedsDecision = append(b.NeedsDecision, s.Purpose.ID)
		}
		b.Purposes = append(b.Purposes, v)
	}
	return b
}

type stateOutput struct {
	// A person's consent state is not for a shared cache.
	CacheControl string `header:"Cache-Control"`
	SetCookie    string `header:"Set-Cookie"`
	Body         *StateBody
}

type consentDecisionBody struct {
	Purpose         string `json:"purpose" maxLength:"64"`
	Granted         bool   `json:"granted"`
	DocumentVersion string `json:"document_version" maxLength:"128" doc:"The version the person was shown"`
	BasedOn         int64  `json:"based_on" minimum:"0" doc:"The purpose's based_on from the state this decision was made on"`
}

type putInput struct {
	Body struct {
		Source    Source                `json:"source" enum:"banner,settings,sign_up,prompt"`
		Decisions []consentDecisionBody `json:"decisions" minItems:"1" maxItems:"32"`
	}
}

// RegisterRoutes mounts GET and PUT on path (DefaultPath if empty). The
// Handler's Middleware must be installed on api, after the app's authn
// middleware.
func (h *Handler) RegisterRoutes(api huma.API, path string, ops *Operations) error {
	if ops == nil || ops.Read == nil || ops.Write == nil {
		return errors.New("consent: both consent routes need authn markers")
	}
	if path == "" {
		path = DefaultPath
	}
	huma.Register(api, huma.Operation{
		OperationID: "get-consent", Method: http.MethodGet, Path: path, Tags: []string{"privacy"},
		Summary: "The consent state, including the purposes needing a decision", Metadata: ops.Read,
	}, func(ctx context.Context, _ *struct{}) (*stateOutput, error) {
		return h.serve(ctx, true, nil)
	})
	huma.Register(api, huma.Operation{
		OperationID: "put-consent", Method: http.MethodPut, Path: path, Tags: []string{"privacy"},
		Summary: "Record consent decisions", Metadata: ops.Write,
		Errors: []int{http.StatusBadRequest, http.StatusConflict, http.StatusForbidden},
		// Not left to the app: a decision made on a request whose credential
		// was refused must not be filed in the anonymous cookie, and then
		// again as the user when the client refreshes and retries. Every
		// consent write goes through here, so none can be mounted without it.
		Middlewares: huma.Middlewares{authn.RefuseRejectedCredential(api)},
	}, func(ctx context.Context, in *putInput) (*stateOutput, error) {
		d := &DecideInput{Source: in.Body.Source}
		for _, b := range in.Body.Decisions {
			d.Decisions = append(d.Decisions, DecisionInput{b.Purpose, b.Granted, b.DocumentVersion, b.BasedOn})
		}
		return h.serve(ctx, false, d)
	})
	return nil
}

func (h *Handler) serve(ctx context.Context, merge bool, decide *DecideInput) (*stateOutput, error) {
	rq := requestFrom(ctx)
	if rq == nil || rq.failed {
		if rq == nil {
			h.logger.Error("consent route reached without the consent middleware")
		}
		return nil, huma.Error500InternalServerError("consent is unavailable")
	}
	var res *Resolved
	var err error
	if decide != nil {
		res, err = h.svc.Decide(ctx, rq.subject, decide)
	} else {
		res, err = h.svc.Resolve(ctx, rq.subject, merge)
	}
	if err != nil {
		var de *DecisionError
		if errors.As(err, &de) {
			return nil, authn.NewProblem(statusOf(de.Code), de.Code, de.Detail)
		}
		h.logger.Error("consent request failed", "err", errText(err))
		return nil, huma.Error500InternalServerError("consent is unavailable")
	}
	if decide != nil && h.afterDecide != nil && rq.subject.HasUser {
		if err := h.afterDecide(ctx, rq.subject.UserID, decide, res); err != nil {
			h.logger.Error("consent decision hook failed", "err", errText(err))
			return nil, huma.Error500InternalServerError("consent is unavailable")
		}
	}
	out := &stateOutput{CacheControl: "no-store", Body: viewOf(res.Effective)}
	if res.Cookie != nil {
		ck, err := h.svc.cfg.Cookie.HTTPCookie(res.Cookie, h.svc.cfg.Clock())
		if err != nil {
			h.logger.Error("consent cookie not set", "err", errText(err))
			return nil, huma.Error500InternalServerError("consent is unavailable")
		}
		out.SetCookie = ck.String()
	}
	return out, nil
}

func statusOf(code string) int {
	switch code {
	case CodeConsentChanged, CodeDocumentChanged, CodeGPCActive:
		return http.StatusConflict
	case CodeSignInRequired:
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}
