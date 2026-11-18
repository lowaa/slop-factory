package supervisorpkg

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// ServerURL is where the request arrived, which is the address the person
// running the script can reach (agent.BaseURLMiddleware knows it).
type ServerURL func(ctx context.Context) (string, error)

// TenantName is the name the signed-in user's tenant goes by; "" when it
// cannot be read, which only costs the script its suggestion.
type TenantName func(ctx context.Context, who *authn.User) (string, error)

// Handler serves the package and the script that installs it.
type Handler struct {
	pkg       *Package
	serverURL ServerURL
	tenant    TenantName
	logger    *slog.Logger
}

// NewHandler serves pkg, which is nil on a server that does not ship the
// supervisor (a dev stack not built from the image): the routes then answer
// 404 rather than the server failing to start.
func NewHandler(pkg *Package, serverURL ServerURL, tenant TenantName, logger *slog.Logger) *Handler {
	return &Handler{pkg: pkg, serverURL: serverURL, tenant: tenant, logger: logger}
}

const (
	// VersionHeader names the package's version, so an updater can tell
	// whether what it holds is behind.
	VersionHeader     = "X-Supervisor-Version"
	archiveMediaType  = "application/gzip"
	scriptMediaType   = "text/x-shellscript"
	notShippedMessage = "this server does not ship the supervisor"
)

type PackageResponse struct {
	ContentType string `header:"Content-Type"`
	Version     string `header:"X-Supervisor-Version"`
	// The package changes with the server's deploys.
	CacheControl string `header:"Cache-Control"`
	Body         []byte
}

// Package returns the supervisor archive. The caller is an agent: the
// updater fetches it with a slot's credentials.
func (h *Handler) Package(_ context.Context, _ *struct{}) (*PackageResponse, error) {
	if h.pkg == nil {
		return nil, huma.Error404NotFound(notShippedMessage)
	}
	return &PackageResponse{ContentType: archiveMediaType, Version: h.pkg.Version, CacheControl: "no-store", Body: h.pkg.Archive}, nil
}

type SetupScriptResponse struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	CacheControl       string `header:"Cache-Control"`
	Body               []byte
}

// SetupScript returns the script that sets up a supervisor for the calling
// user's tenant, with the package inside it.
func (h *Handler) SetupScript(ctx context.Context, _ *struct{}) (*SetupScriptResponse, error) {
	if h.pkg == nil {
		return nil, huma.Error404NotFound(notShippedMessage)
	}
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("supervisor setup served without authentication", err)
	}
	server, err := h.serverURL(ctx)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	name, err := h.tenant(ctx, who)
	if err != nil {
		// The name is only a suggestion: the script falls back to the host label.
		h.logger.Warn("could not read the tenant's name for the supervisor setup script", "err", err)
		name = ""
	}
	script, err := workspace.SupervisorScript(workspace.SupervisorParams{
		Server: server, TenantID: who.TenantID.String(), TenantName: name, Archive: h.pkg.Archive,
	})
	if err != nil {
		h.logger.Error("could not build the supervisor setup script", "err", err)
		return nil, huma.Error500InternalServerError("could not build the supervisor setup script")
	}
	return &SetupScriptResponse{
		ContentType:        scriptMediaType + "; charset=utf-8",
		ContentDisposition: fmt.Sprintf("attachment; filename=%q", "slop-haus-supervisor-setup.sh"),
		// Names this tenant and carries this deploy's package.
		CacheControl: "no-store",
		Body:         []byte(script),
	}, nil
}

// RegisterRoutes mounts the routes. middlewares go on the setup script's:
// the caller passes the one that records the address the request arrived on.
func RegisterRoutes(api huma.API, h *Handler, middlewares huma.Middlewares) {
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorPackage",
		// The agent's own token: the updater runs as a slot's agent, and a
		// package carries no secret.
		Metadata:  authn.RequireAgent(),
		Method:    http.MethodGet,
		Path:      "/api/supervisor/package",
		Summary:   "Return the supervisor package, versioned with this server",
		Responses: rawResponses(archiveMediaType),
	}, h.Package)
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorSetupScript",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/setup_script",
		Summary:     "Return a shell script that sets up a supervisor for the caller's tenant",
		Description: "Checks for uv, writes the supervisor's root and unpacks the package in it. " +
			"Carries no credential.",
		Responses:   rawResponses(scriptMediaType),
		Middlewares: middlewares,
	}, h.SetupScript)
}

// rawResponses declares a success as the file it is: left alone, huma
// documents a []byte body as base64 in application/json.
func rawResponses(mediaType string) map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: http.StatusText(http.StatusOK),
			Content:     map[string]*huma.MediaType{mediaType: {Schema: &huma.Schema{Type: huma.TypeString}}},
		},
	}
}
