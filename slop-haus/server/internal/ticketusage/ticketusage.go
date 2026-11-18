// Package ticketusage prices a ticket's attempts from the LLM usage agents reported for them.
package ticketusage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Usage is one ticket's LLM usage, a figure per billing mode. Worker is what
// the ticket's own agents spent; Guidance what consoles and conductors
// declared they spent on it. They are never summed: a subscription figure is
// an API-equivalent estimate and the two kinds are read differently.
type Usage struct {
	Worker   []reports.UsageSums
	Guidance []reports.UsageSums
}

// The cost rule (reported, else estimated; unpriced rows counted, not zeroed)
// is the Reports tab's, by sharing its column list.
var queryUsage = `
SELECT 'worker' AS kind, billing_mode, ` + reports.UsageSumsSelect + `
  FROM llm_usage WHERE ticket_id = $1 GROUP BY billing_mode
UNION ALL
SELECT 'guidance', billing_mode, ` + reports.UsageSumsSelect + `
  FROM llm_guidance_usage WHERE ticket_id = $1 GROUP BY billing_mode
ORDER BY 1 DESC, 2`

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

type Store interface {
	TicketUsage(ctx context.Context, ticketID int64) (*Usage, error)
}

var _ Store = (*PostgresStore)(nil)

// TicketUsage reads the usage of a ticket of the caller's tenant; another
// tenant's ticket is row-level hidden, so it is as absent as a missing one.
func (s *PostgresStore) TicketUsage(ctx context.Context, ticketID int64) (*Usage, error) {
	out := &Usage{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1)`, ticketID).Scan(&exists); err != nil {
			return fmt.Errorf("ticketusage: ticket: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: #%d", ticket.ErrNotFound, ticketID)
		}
		rows, err := q.Query(ctx, queryUsage, ticketID)
		if err != nil {
			return fmt.Errorf("ticketusage: usage: %w", err)
		}
		defer rows.Close()
		return scanKinds(rows, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanKinds(rows pgx.Rows, out *Usage) error {
	for rows.Next() {
		var (
			kind string
			sums reports.UsageSums
		)
		if err := reports.ScanUsageSums(rows, &sums, &kind); err != nil {
			return fmt.Errorf("ticketusage: usage: %w", err)
		}
		if kind == "guidance" {
			out.Guidance = append(out.Guidance, sums)
		} else {
			out.Worker = append(out.Worker, sums)
		}
	}
	return rows.Err()
}

type Handler struct {
	store  Store
	logger *slog.Logger
}

func NewHandler(store Store, logHandler slog.Handler) *Handler {
	return &Handler{store: store, logger: slog.New(logHandler)}
}

type GetTicketUsageRequest struct {
	ID int64 `path:"id"`
}

type TicketUsageBody struct {
	// Worker is what the ticket's agents spent, a figure per billing mode;
	// empty when none did.
	Worker []reports.UsageSumsBody `json:"worker"`
	// Guidance is what consoles and Remote Control sessions declared they
	// spent on the ticket (working_on), kept apart from Worker.
	Guidance []reports.UsageSumsBody `json:"guidance"`
}

type GetTicketUsageResponse struct {
	Body *TicketUsageBody
}

func (h *Handler) GetTicketUsage(ctx context.Context, in *GetTicketUsageRequest) (*GetTicketUsageResponse, error) {
	u, err := h.store.TicketUsage(ctx, in.ID)
	switch {
	case errors.Is(err, ticket.ErrNotFound):
		return nil, huma.Error404NotFound(err.Error())
	case err != nil:
		h.logger.Error("ticket usage", "ticket", in.ID, "err", err)
		return nil, huma.Error500InternalServerError("internal server error")
	}
	return &GetTicketUsageResponse{Body: &TicketUsageBody{
		Worker:   reports.UsageSumsBodies(u.Worker),
		Guidance: reports.UsageSumsBodies(u.Guidance),
	}}, nil
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getTicketLLMUsage",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/llm-usage",
		Summary:     "A ticket's LLM usage: the worker's and the guidance declared on it, apart",
		Description: "Each figure is per billing mode; subscription cost is an API-equivalent estimate.",
	}, h.GetTicketUsage)
}
