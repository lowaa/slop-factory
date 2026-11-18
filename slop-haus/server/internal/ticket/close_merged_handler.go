package ticket

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// registerCloseMergedRoutes mounts Close as merged: the facts a person needs
// and the close itself. Both are RequireUser with no agent route -- a worker
// closing its own ticket on the strength of a merge is exactly what the
// webhook's refusal exists to prevent -- and the service holds both to the
// tenant's owner.
func registerCloseMergedRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getTicketMergeFacts",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/merge_facts",
		Summary:     "Read from the code host whether the ticket's change was merged, and what a merge ignored here left unsaid",
	}, h.GetTicketMergeFacts)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "closeTicketAsMerged",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/close_as_merged",
		Summary:     "Close a ticket as done because its change was merged outside slop-haus",
	}, h.CloseTicketAsMerged)
}

type IgnoredMergeBody struct {
	MergerLogin string    `json:"merger_login" doc:"the code host account that merged the change"`
	MergeSHA    string    `json:"merge_sha,omitempty"`
	Reason      string    `json:"reason" doc:"why the merge did not close the ticket"`
	At          time.Time `json:"at"`
}

type MergeFactsBody struct {
	PRURL string `json:"pr_url"`
	Base  string `json:"base" doc:"the branch the ticket merges into"`
	// Merged is whether the host shows the change merged at all; only
	// merged_into_base lets the ticket be closed.
	Merged         bool              `json:"merged"`
	MergedIntoBase bool              `json:"merged_into_base"`
	MergedBy       string            `json:"merged_by,omitempty"`
	MergeSHA       string            `json:"merge_sha,omitempty"`
	MergedHead     string            `json:"merged_head,omitempty"`
	ApprovedHead   string            `json:"approved_head,omitempty" doc:"the commit an approval covers; empty when there is none"`
	HeadApproved   bool              `json:"head_approved"`
	Checks         string            `json:"checks,omitempty" enum:"passed,failed,not_run" doc:"the checks on the merged head; empty unless merged into the base"`
	MergeOnBase    bool              `json:"merge_on_base"`
	Ignored        *IgnoredMergeBody `json:"ignored,omitempty" doc:"the merge this server ignored, if one was recorded"`
	Confirmations  []string          `json:"confirmations" doc:"what to confirm before closing; empty when nothing is in doubt"`
}

func mergeFactsToBody(f *MergeFacts, prURL string) MergeFactsBody {
	b := MergeFactsBody{
		PRURL: prURL, Base: f.Base, Merged: f.Merged, MergedIntoBase: f.MergedIntoBase,
		MergedBy: f.MergedBy, MergeSHA: f.MergeSHA, MergedHead: f.MergedHead, ApprovedHead: f.ApprovedHead,
		HeadApproved: f.HeadApproved, Checks: string(f.Checks), MergeOnBase: f.MergeOnBase,
		Confirmations: []string{},
	}
	if f.MergedIntoBase {
		b.Confirmations = append(b.Confirmations, f.Confirmations()...)
	}
	if f.Ignored != nil {
		b.Ignored = &IgnoredMergeBody{MergerLogin: f.Ignored.MergerLogin, MergeSHA: f.Ignored.MergeSHA, Reason: f.Ignored.Reason, At: f.Ignored.At}
	}
	return b
}

type MergeFactsRequest struct {
	ID int64 `path:"id"`
}

type MergeFactsResponse struct {
	Body MergeFactsBody
}

func (h *Handler) GetTicketMergeFacts(ctx context.Context, input *MergeFactsRequest) (*MergeFactsResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	f, err := h.svc.MergeFactsForOwner(ctx, input.ID, user.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &MergeFactsResponse{Body: mergeFactsToBody(f, h.svc.hosts.ChangeURL(f.PR))}, nil
}

type CloseTicketAsMergedBody struct {
	Confirm bool `json:"confirm,omitempty" doc:"accept what the facts leave in doubt (a head whose checks did not pass, or that was not the approved one)"`
}

type CloseTicketAsMergedRequest struct {
	ID   int64 `path:"id"`
	Body CloseTicketAsMergedBody
}

type CloseTicketAsMergedResultBody struct {
	Closed bool `json:"closed"`
	// NeedsConfirmation is true when nothing was closed: ask again with
	// confirm once the person has read the confirmations.
	NeedsConfirmation bool        `json:"needs_confirmation"`
	Confirmations     []string    `json:"confirmations"`
	Ticket            *TicketBody `json:"ticket,omitempty"`
}

type CloseTicketAsMergedResponse struct {
	Body CloseTicketAsMergedResultBody
}

func (h *Handler) CloseTicketAsMerged(ctx context.Context, input *CloseTicketAsMergedRequest) (*CloseTicketAsMergedResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	res, err := h.svc.CloseAsMerged(ctx, input.ID, user.ID, input.Body.Confirm)
	if err != nil {
		return nil, h.mapError(err)
	}
	out := CloseTicketAsMergedResultBody{NeedsConfirmation: res.NeedsConfirmation, Confirmations: res.Confirmations}
	if out.Confirmations == nil {
		out.Confirmations = []string{}
	}
	if res.Ticket != nil {
		body := ticketToBody(*res.Ticket)
		out.Closed, out.Ticket = true, &body
	}
	return &CloseTicketAsMergedResponse{Body: out}, nil
}
