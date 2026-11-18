package legaldocs

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// DefaultPath is where RegisterRoutes mounts the document routes.
const DefaultPath = "/api/privacy/documents"

// Documents are public text, so these are readable signed out and cacheable.
// The current version changes when a scheduled one comes into force, so it
// is cached briefly; a given version never changes once published.
const (
	currentCache = "public, max-age=60"
	versionCache = "public, max-age=3600"
)

// DocumentBody is one version with its text.
type DocumentBody struct {
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	Locale    string `json:"locale"`
	Effective string `json:"effective" doc:"Date, YYYY-MM-DD"`
	Material  bool   `json:"material"`
	// Reviewed is false for placeholder text: show a DRAFT banner.
	Reviewed   bool   `json:"reviewed"`
	ReviewedBy string `json:"reviewed_by,omitempty"`
	ReviewedOn string `json:"reviewed_on,omitempty"`
	// Current is whether this is the version in force.
	Current bool `json:"current"`
	// Body is Markdown, and must be rendered as untrusted.
	Body string `json:"body"`
}

// VersionSummary is one entry of a version history.
type VersionSummary struct {
	Version    string `json:"version"`
	Effective  string `json:"effective"`
	Material   bool   `json:"material"`
	Reviewed   bool   `json:"reviewed"`
	ReviewedOn string `json:"reviewed_on,omitempty"`
	Current    bool   `json:"current"`
}

// HistoryBody lists a kind's versions, newest first.
type HistoryBody struct {
	Kind     string           `json:"kind"`
	Locale   string           `json:"locale"`
	Versions []VersionSummary `json:"versions"`
}

type documentOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         *DocumentBody
}

type historyOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         *HistoryBody
}

type kindInput struct {
	Kind   string `path:"kind" maxLength:"64"`
	Locale string `query:"locale" maxLength:"35" doc:"Defaults to the app's default locale"`
}

type versionInput struct {
	Kind    string `path:"kind" maxLength:"64"`
	Version string `path:"version" maxLength:"64"`
	Locale  string `query:"locale" maxLength:"35"`
}

// Operations carries the app's authn marker for the routes, so they go
// through its own policy (DefaultDeny included). Documents are public.
type Operations struct {
	Read map[string]any
}

// Handler serves the document routes.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func date(t time.Time) string { return t.Format(time.DateOnly) }

func (h *Handler) bodyOf(v, current *Version) *DocumentBody {
	b := &DocumentBody{
		Kind: v.Kind, Version: v.Version, Locale: v.Locale, Effective: date(v.Effective),
		Material: v.Material, Reviewed: v.Reviewed, ReviewedBy: v.ReviewedBy,
		Current: current != nil && current.Version == v.Version, Body: v.Body,
	}
	if v.ReviewedOn != nil {
		b.ReviewedOn = date(*v.ReviewedOn)
	}
	return b
}

// RegisterRoutes mounts the routes under path (DefaultPath if empty).
func (h *Handler) RegisterRoutes(api huma.API, path string, ops *Operations) error {
	if ops == nil || ops.Read == nil {
		return errors.New("legaldocs: the document routes need an authn marker")
	}
	if path == "" {
		path = DefaultPath
	}
	huma.Register(api, huma.Operation{
		OperationID: "get-legal-document", Method: http.MethodGet, Path: path + "/{kind}", Tags: []string{"privacy"},
		Summary: "The legal document version in force", Metadata: ops.Read,
		Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *kindInput) (*documentOutput, error) {
		v, err := h.svc.Current(ctx, in.Kind, in.Locale)
		if err != nil {
			return nil, h.fail(err)
		}
		return &documentOutput{CacheControl: currentCache, Body: h.bodyOf(v, v)}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-legal-document-versions", Method: http.MethodGet, Path: path + "/{kind}/versions", Tags: []string{"privacy"},
		Summary: "The version history of a legal document", Metadata: ops.Read,
		Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *kindInput) (*historyOutput, error) {
		vs, err := h.svc.History(ctx, in.Kind, in.Locale)
		if err != nil {
			return nil, h.fail(err)
		}
		if len(vs) == 0 {
			return nil, h.fail(ErrNotFound)
		}
		out := &HistoryBody{Kind: in.Kind, Locale: vs[0].Locale, Versions: make([]VersionSummary, len(vs))}
		for i, v := range vs {
			s := VersionSummary{Version: v.Version, Effective: date(v.Effective), Material: v.Material, Reviewed: v.Reviewed, Current: i == 0}
			if v.ReviewedOn != nil {
				s.ReviewedOn = date(*v.ReviewedOn)
			}
			out.Versions[i] = s
		}
		return &historyOutput{CacheControl: currentCache, Body: out}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-legal-document-version", Method: http.MethodGet, Path: path + "/{kind}/versions/{version}", Tags: []string{"privacy"},
		Summary: "One exact version of a legal document", Metadata: ops.Read,
		Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *versionInput) (*documentOutput, error) {
		v, err := h.svc.Get(ctx, in.Kind, in.Version, in.Locale)
		if err != nil {
			return nil, h.fail(err)
		}
		cur, err := h.svc.currentSummary(ctx, in.Kind, v.Locale)
		if err != nil {
			return nil, h.fail(err)
		}
		return &documentOutput{CacheControl: versionCache, Body: h.bodyOf(v, cur)}, nil
	})
	return nil
}

func (h *Handler) fail(err error) error {
	if errors.Is(err, ErrNotFound) {
		return huma.Error404NotFound("no such document")
	}
	h.svc.logger.Error("legal document request failed", "err", errText(err))
	return huma.Error500InternalServerError("documents are unavailable")
}
