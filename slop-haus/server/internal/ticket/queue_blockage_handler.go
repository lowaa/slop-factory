package ticket

import (
	"context"
)

// QueueBlockageBody says the queue is waiting on scopes, and on what.
type QueueBlockageBody struct {
	TicketID        int64  `json:"ticket_id" doc:"the ticket waiting"`
	TicketTitle     string `json:"ticket_title"`
	HolderTicketID  int64  `json:"holder_ticket_id" doc:"the ticket whose scope it collides with"`
	HolderTitle     string `json:"holder_title"`
	HolderStatus    Status `json:"holder_status"`
	HolderAgentName string `json:"holder_agent_name" doc:"empty when no agent is known to hold it"`
	Path            string `json:"path" doc:"the held scope it collides with"`
}

type queueBlockageResponse struct {
	Body struct {
		// Blockage is null when nothing is waiting on scopes.
		Blockage *QueueBlockageBody `json:"blockage"`
	}
}

func (h *Handler) GetQueueBlockage(ctx context.Context, _ *struct{}) (*queueBlockageResponse, error) {
	found, err := h.svc.QueueBlockage(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &queueBlockageResponse{}
	if found != nil {
		resp.Body.Blockage = &QueueBlockageBody{
			TicketID: found.Blocked.TicketID, TicketTitle: found.TicketTitle,
			HolderTicketID: found.Blocked.ConflictingTicketID, HolderTitle: found.HolderTitle,
			HolderStatus: found.HolderStatus, HolderAgentName: found.HolderAgentName,
			Path: found.Blocked.ConflictingPath,
		}
	}
	return resp, nil
}
