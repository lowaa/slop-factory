package githubapp

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

const routePath = "/api/codehost/github-app"

// RegisterRoutes mounts the board's GitHub App section. Every operation is
// for a signed-in person, never an agent: this credential lets slop-haus
// act on the tenant's repositories, and an agent that could set it would
// choose where the server acts. Writes are owner-only (checked in Service)
// and, like every session-authenticated write, CSRF-guarded by the user
// middleware.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getGitHubAppCredential",
		Method:      http.MethodGet,
		Path:        routePath,
		Summary:     "Whether a GitHub App is connected, and which (never its key)",
	}, h.Get)
	// The body's validation is switched off and its schema documented by
	// hand: see GitHubAppConnectRequest.
	huma.Register(api, huma.Operation{
		Metadata:         authn.RequireUser(),
		OperationID:      "connectGitHubAppCredential",
		Method:           http.MethodPut,
		Path:             routePath,
		Summary:          "Connect a GitHub App installation, or replace the one connected (owner; needs a recent sign-in)",
		SkipValidateBody: true,
		MaxBodyBytes:     maxConnectBody,
	}, h.Connect)
	documentConnectBody(api)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "beginGitHubAppManifest",
		Method:      http.MethodPost,
		Path:        routePath + "/manifest",
		Summary:     "Start creating the GitHub App on GitHub from a manifest (owner; needs a recent sign-in)",
	}, h.BeginManifest)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "completeGitHubAppManifest",
		Method:      http.MethodPost,
		Path:        routePath + "/manifest/complete",
		Summary:     "Exchange the code GitHub redirected back with for the new App and store it (owner; needs a recent sign-in)",
	}, h.CompleteManifest)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getGitHubAppInstallURL",
		Method:      http.MethodPost,
		Path:        routePath + "/install-url",
		Summary:     "The page to install the created App from (owner; needs a recent sign-in)",
	}, h.InstallURL)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "completeGitHubAppInstall",
		Method:      http.MethodPost,
		Path:        routePath + "/install/complete",
		Summary:     "Record the installation GitHub redirected back with, once GitHub confirms it is the App's (owner; needs a recent sign-in)",
	}, h.CompleteInstall)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "verifyGitHubAppCredential",
		Method:      http.MethodPost,
		Path:        routePath + "/verify",
		Summary:     "Check the connected GitHub App against GitHub and the workspace manifest (owner)",
	}, h.Verify)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getGitHubAppWebhook",
		Method:      http.MethodGet,
		Path:        routePath + "/webhook",
		Summary:     "Whether the App's webhook is set up and delivering (never its secret)",
	}, h.WebhookStatus)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "checkGitHubAppWebhook",
		Method:      http.MethodPost,
		Path:        routePath + "/webhook/check",
		Summary:     "Read the App's webhook from GitHub and set it right if it differs (owner)",
	}, h.CheckWebhook)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "rotateGitHubAppWebhookSecret",
		Method:      http.MethodPost,
		Path:        routePath + "/webhook/rotate",
		Summary:     "Replace the secret the App's deliveries are signed with (owner; needs a recent sign-in)",
	}, h.RotateWebhook)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "removeGitHubAppCredential",
		Method:        http.MethodDelete,
		Path:          routePath,
		Summary:       "Remove the connected GitHub App (owner; needs a recent sign-in)",
		DefaultStatus: http.StatusNoContent,
	}, h.Remove)
}

func documentConnectBody(api huma.API) {
	spec := api.OpenAPI()
	media := spec.Paths[routePath].Put.RequestBody.Content["application/json"]
	media.Schema = huma.SchemaFromType(spec.Components.Schemas, reflect.TypeFor[GitHubAppConnectBody]())
}
