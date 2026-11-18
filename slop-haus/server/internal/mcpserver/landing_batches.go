package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

const (
	defaultBatchLimit = 20
	maxBatchLimit     = 100
)

// Batches is the slice of landing.BatchStore landing_batches reads. The tenant
// is the caller's, taken from the request's context: there is no way to name
// another.
type Batches interface {
	Recent(ctx context.Context, limit int, since *time.Time) ([]landing.BatchSummary, error)
}

func registerLandingBatches(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "landing_batches",
		Description: "List your tenant's recent merge-queue batches, newest first, done ones included: the members in the order they were tested " +
			"with the head each was tested at, the batch PR, the CI pipeline testing it (null while none is recorded), and the state and times. " +
			"Read-only; limit defaults to 20 (max 100); since (RFC 3339) keeps batches created at or after it.",
	}, leaseFree, h.landingBatches)
}

type landingBatchesInput struct {
	Limit int    `json:"limit,omitempty" jsonschema:"how many batches, newest first; default 20, at most 100"`
	Since string `json:"since,omitempty" jsonschema:"RFC 3339 time: only batches created at or after it"`
}

type landingBatchesOutput struct {
	toolNotes
	Batches []landingBatch `json:"batches"`
}

type landingBatch struct {
	ID      int64  `json:"id"`
	State   string `json:"state" jsonschema:"building, testing, merging or done"`
	Branch  string `json:"branch"`
	BaseSHA string `json:"base_sha"`
	// Members are in tested order.
	Members    []landingBatchMember `json:"members" jsonschema:"the tickets the batch was built with, in the order they were tested and merge; a member handed back or requeued stays listed"`
	PRNumber   *int64               `json:"pr_number" jsonschema:"the batch's draft change; null until opened"`
	CIPipeline *string              `json:"ci_pipeline" jsonschema:"the CI pipeline testing the batch head; null when none was recorded"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
	FinishedAt *time.Time           `json:"finished_at" jsonschema:"when the batch ended; null while it is live"`
	Reason     string               `json:"reason,omitempty" jsonschema:"why the batch ended without merging all its members, where it did"`
}

type landingBatchMember struct {
	TicketID   int64   `json:"ticket_id"`
	TestedHead *string `json:"tested_head" jsonschema:"the head the member was tested at; null until the batch is tested"`
}

func (h *Handler) landingBatches(ctx context.Context, _ uuid.UUID, in landingBatchesInput) (*mcp.CallToolResult, landingBatchesOutput, error) {
	limit := in.Limit
	switch {
	case limit < 0:
		return nil, landingBatchesOutput{}, fmt.Errorf("limit must not be negative")
	case limit == 0:
		limit = defaultBatchLimit
	case limit > maxBatchLimit:
		limit = maxBatchLimit
	}
	var since *time.Time
	if in.Since != "" {
		t, err := time.Parse(time.RFC3339, in.Since)
		if err != nil {
			return nil, landingBatchesOutput{}, fmt.Errorf("since must be an RFC 3339 time: %w", err)
		}
		since = &t
	}
	recent, err := h.batches.Recent(ctx, limit, since)
	if err != nil {
		return nil, landingBatchesOutput{}, err
	}
	out := landingBatchesOutput{Batches: make([]landingBatch, 0, len(recent))}
	var b strings.Builder
	for i := range recent {
		v := batchView(&recent[i])
		out.Batches = append(out.Batches, v)
		pipeline := "none"
		if v.CIPipeline != nil {
			pipeline = *v.CIPipeline
		}
		fmt.Fprintf(&b, "batch %d  %s  members %v  ci_pipeline %s\n", v.ID, v.State, memberIDs(v.Members), pipeline)
	}
	if len(recent) == 0 {
		return text("No batches."), out, nil
	}
	return text("%s", strings.TrimRight(b.String(), "\n")), out, nil
}

func batchView(s *landing.BatchSummary) landingBatch {
	v := landingBatch{
		ID: s.ID, State: string(s.State), Branch: s.Branch, BaseSHA: s.BaseSHA,
		Members: make([]landingBatchMember, len(s.Members)), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Reason: s.Reason,
	}
	for i, id := range s.Members {
		v.Members[i] = landingBatchMember{TicketID: id}
		if i < len(s.Heads) {
			head := s.Heads[i]
			v.Members[i].TestedHead = &head
		}
	}
	if s.Change != nil {
		n := s.Change.Number
		v.PRNumber = &n
	}
	if s.CIPipeline != "" {
		p := s.CIPipeline
		v.CIPipeline = &p
	}
	if s.State == landing.BatchDone {
		t := s.UpdatedAt
		v.FinishedAt = &t
	}
	return v
}

func memberIDs(ms []landingBatchMember) []int64 {
	ids := make([]int64, len(ms))
	for i, m := range ms {
		ids[i] = m.TicketID
	}
	return ids
}
