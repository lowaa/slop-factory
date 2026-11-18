package attachment

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// documentUpload puts the multipart body in the spec after the operation is
// registered. It is not declared on the operation: huma reads and validates
// any body an operation declares before the handler runs, which would buffer
// the whole upload (see WithRequest).
func documentUpload(api huma.API, path string) {
	api.OpenAPI().Paths[path].Post.RequestBody = &huma.RequestBody{
		Required: true,
		Content: map[string]*huma.MediaType{"multipart/form-data": {Schema: &huma.Schema{
			Type:     huma.TypeObject,
			Required: []string{"file"},
			Properties: map[string]*huma.Schema{"file": {
				Type: huma.TypeString, Format: "binary",
				Description: "A PNG, JPEG, WebP or GIF image (up to 10 MB) or an MP4, WebM or MOV video (up to 100 MB). The type is decided from the bytes.",
			}},
		}}},
	}
}

// RegisterAgentRoutes mounts the agent-token upload: a session posts a file
// from its working copy with curl and the credential in .slop-haus/token.sh.
// The tenant and the uploader come from the token.
func RegisterAgentRoutes(api huma.API, h *Handler) {
	const path = "/api/agent/tickets/{id}/attachments"
	huma.Register(api, huma.Operation{
		OperationID: "agentUploadTicketAttachment", Metadata: authn.RequireAgent(), Method: http.MethodPost,
		Path: path, Summary: "Attach an image or video to a ticket, as an agent",
		DefaultStatus: http.StatusCreated, Middlewares: huma.Middlewares{WithRequest},
	}, h.AgentUploadToTicket)
	documentUpload(api, path)
	huma.Register(api, huma.Operation{
		OperationID: "agentGetAttachmentContent", Metadata: authn.RequireAgent(), Method: http.MethodGet,
		Path: "/api/agent/attachments/{aid}", Summary: "An attachment's content, as an agent",
		Responses: map[string]*huma.Response{"200": {Description: "The file", Content: map[string]*huma.MediaType{"application/octet-stream": {}}}},
	}, h.AgentContent)
}

// RegisterRoutes mounts the signed-in person's attachment routes. The tenant
// comes from the session, and so does the uploader; neither is in a path or
// body.
func RegisterRoutes(api huma.API, h *Handler) {
	user := authn.RequireUser()
	huma.Register(api, huma.Operation{
		OperationID: "uploadTicketAttachment", Metadata: user, Method: http.MethodPost,
		Path: "/api/tickets/{id}/attachments", Summary: "Attach an image or video to a ticket",
		DefaultStatus: http.StatusCreated, Middlewares: huma.Middlewares{WithRequest},
	}, h.UploadToTicket)
	documentUpload(api, "/api/tickets/{id}/attachments")
	huma.Register(api, huma.Operation{
		OperationID: "listTicketAttachments", Metadata: user, Method: http.MethodGet,
		Path: "/api/tickets/{id}/attachments", Summary: "List a ticket's attachments",
	}, h.List)
	huma.Register(api, huma.Operation{
		OperationID: "uploadConversationAttachment", Metadata: user, Method: http.MethodPost,
		Path: "/api/conversations/{conversation_id}/attachments", Summary: "Stage an image or video in a Remote Control conversation, visible to its owner only",
		DefaultStatus: http.StatusCreated, Middlewares: huma.Middlewares{WithRequest},
	}, h.UploadStaged)
	documentUpload(api, "/api/conversations/{conversation_id}/attachments")
	huma.Register(api, huma.Operation{
		OperationID: "getAttachmentContent", Metadata: user, Method: http.MethodGet,
		Path: "/api/attachments/{aid}", Summary: "An attachment's content",
		Responses: map[string]*huma.Response{"200": {Description: "The file", Content: map[string]*huma.MediaType{"application/octet-stream": {}}}},
	}, h.Content)
	huma.Register(api, huma.Operation{
		OperationID: "deleteAttachment", Metadata: user, Method: http.MethodDelete,
		Path: "/api/attachments/{aid}", Summary: "Delete an attachment", DefaultStatus: http.StatusNoContent,
	}, h.Delete)
}
