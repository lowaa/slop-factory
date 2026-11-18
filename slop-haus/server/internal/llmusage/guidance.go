package llmusage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var (
	// ErrTicketNotFound is a declaration naming a ticket the caller's tenant
	// does not have.
	ErrTicketNotFound = errors.New("llmusage: no such ticket")
	// ErrNotDeclarer is a caller that is neither an enabled console nor an
	// enabled conductor.
	ErrNotDeclarer = errors.New("llmusage: only a console or a conductor may declare what it is working on")
	// ErrNoLiveConversation is a conductor declaring with no conversation to
	// follow: its declaration is per conversation.
	ErrNoLiveConversation = errors.New("llmusage: a conductor's declaration follows its live conversation, and it has none")
)

// Declaration is what a console's or conductor's usage goes to now.
type Declaration struct {
	// TicketID is nil when the usage goes to overhead.
	TicketID    *int64
	TicketTitle string
	// Attempt is the ticket's attempt the declaration guides; nil for
	// overhead, and for a ticket with no live attempt.
	Attempt *int
	// ConversationID is the conductor conversation the declaration follows;
	// nil for a console, whose declaration covers the whole agent.
	ConversationID *uuid.UUID
}

// Declare says which ticket the calling console's or conductor's usage goes
// to from now on, or none (nil) for overhead. attempt names which of the
// ticket's attempts it guides; left out, it is the ticket's one live attempt,
// and a ticket with several is refused (ticket.ErrAttemptAmbiguous).
//
// A conductor's declaration follows its one live conversation. A console's
// covers every transcript of the agent, because the request it arrives on
// carries no harness session id to narrow it: two sessions of one console
// agent share the latest declaration.
func (s *Service) Declare(ctx context.Context, ticketID *int64, attempt *int) (*Declaration, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.Declare(ctx, caller.AgentID(), ticketID, attempt)
}

// scopeKey is the scope a declaration (and the usage it governs) belongs to:
// a conversation, or the agent as a whole when nil.
type scopeKey struct {
	AgentID        uuid.UUID
	ConversationID *uuid.UUID
}

func (s *PostgresStore) Declare(ctx context.Context, agentID uuid.UUID, ticketID *int64, attempt *int) (*Declaration, error) {
	var out *Declaration
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// Reports for this agent wait for the declaration (and its snapshot),
		// and it waits for the ones in flight, so none is split across it.
		if err := db.TakeSubjectAdvisoryLock(ctx, q, db.LLMUsageLockKey, agentLockSubject(agentID)); err != nil {
			return err
		}
		var kind string
		var disabled bool
		err := q.QueryRow(ctx, `SELECT kind, disabled_at IS NOT NULL FROM agents WHERE id = $1`, agentID).
			Scan(&kind, &disabled)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (disabled || (kind != "conductor" && kind != "console"))) {
			return ErrNotDeclarer
		}
		if err != nil {
			return fmt.Errorf("load declaring agent: %w", err)
		}
		scope := scopeKey{AgentID: agentID}
		if kind == "conductor" {
			var conversation uuid.UUID
			err := q.QueryRow(ctx, `SELECT id FROM conductor_conversations
				WHERE conductor_id = $1 AND state IN ('starting', 'ready')`, agentID).Scan(&conversation)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoLiveConversation
			}
			if err != nil {
				return fmt.Errorf("find live conversation: %w", err)
			}
			scope.ConversationID = &conversation
		}
		out = &Declaration{TicketID: ticketID, ConversationID: scope.ConversationID}
		if ticketID != nil {
			err := q.QueryRow(ctx, `SELECT title FROM tickets WHERE id = $1`, *ticketID).Scan(&out.TicketTitle)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			if err != nil {
				return fmt.Errorf("find declared ticket: %w", err)
			}
			// Resolved again here, in the declaration's own transaction, so
			// the attempt recorded is one that was live when it was declared.
			// It is not locked: an attempt that closes straight after is
			// still the one that was guided, and one deleted straight after
			// fails the foreign key, which is the right answer too.
			ref, err := ticket.ResolveLiveAttempt(ctx, q, *ticketID, attempt)
			if err != nil {
				return err
			}
			if ref != nil {
				out.Attempt = &ref.Number
			}
		}

		var declared bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usage_declarations
			WHERE agent_id = $1 AND conversation_id IS NOT DISTINCT FROM $2)`, scope.AgentID, scope.ConversationID).Scan(&declared); err != nil {
			return fmt.Errorf("find earlier declarations: %w", err)
		}
		if !declared {
			// What the scope used before it first declared stays overhead:
			// the first report after this is charged only what it adds.
			if _, err := q.Exec(ctx, `
				INSERT INTO llm_usage_marks (agent_id, transcript_key, provider, model, harness, billing_mode,
					requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
					reported_cost_microdollars, estimated_cost_microdollars)
				SELECT agent_id, transcript_key, provider, model, harness, billing_mode,
					requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
					reported_cost_microdollars, estimated_cost_microdollars
				  FROM llm_usage
				 WHERE agent_id = $1 AND conversation_id IS NOT DISTINCT FROM $2
				ON CONFLICT DO NOTHING`, scope.AgentID, scope.ConversationID); err != nil {
				return fmt.Errorf("snapshot usage marks: %w", err)
			}
		}
		if _, err := q.Exec(ctx, `INSERT INTO usage_declarations (agent_id, conversation_id, ticket_id, attempt_number) VALUES ($1, $2, $3, $4)`,
			scope.AgentID, scope.ConversationID, ticketID, out.Attempt); err != nil {
			return fmt.Errorf("record declaration: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func agentLockSubject(agentID uuid.UUID) string { return "declarations/" + agentID.String() }

// markFields are the totals a mark and a delta hold, in one place so the
// max, the subtraction and the SQL cannot disagree on which they are.
type markFields struct {
	Requests, Input, Output, CacheRead, CacheWrite, Reasoning int64
	Reported                                                  *int64
	Estimated                                                 *int64
}

func fieldsOf(r *usagelib.Record) markFields {
	return markFields{r.Requests, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.ReasoningTokens,
		r.ReportedCostMicros, r.EstimatedCostMicros}
}

func (m markFields) isZero() bool {
	return m.Requests == 0 && m.Input == 0 && m.Output == 0 && m.CacheRead == 0 && m.CacheWrite == 0 && m.Reasoning == 0 &&
		(m.Reported == nil || *m.Reported == 0)
}

// since is what total adds to the mark, per field, never below zero.
func (m markFields) since(total markFields) markFields {
	d := markFields{
		Requests: max(0, total.Requests-m.Requests), Input: max(0, total.Input-m.Input), Output: max(0, total.Output-m.Output),
		CacheRead: max(0, total.CacheRead-m.CacheRead), CacheWrite: max(0, total.CacheWrite-m.CacheWrite),
		Reasoning: max(0, total.Reasoning-m.Reasoning),
	}
	if total.Reported != nil {
		var before int64
		if m.Reported != nil {
			before = *m.Reported
		}
		v := max(0, *total.Reported-before)
		d.Reported = &v
	}
	return d
}

// splitGuidance charges what a report adds beyond its record keys' marks to
// the ticket the scope's current declaration names, and raises the marks.
// It runs in the report's transaction, under the agent's lock.
//
// Only overhead is charged: a transcript a lease attributed to a ticket is
// that ticket's already. Its marks are still raised, so the same tokens are
// not charged to a declaration if the transcript is later attributed to
// overhead.
func (s *PostgresStore) splitGuidance(ctx context.Context, q db.Querier, t *Transcript, a *Attribution, records []usagelib.Record) error {
	var ticketID *int64
	var attempt *int
	err := q.QueryRow(ctx, `SELECT ticket_id, attempt_number FROM usage_declarations
		WHERE agent_id = $1 AND conversation_id IS NOT DISTINCT FROM $2
		ORDER BY id DESC LIMIT 1`, t.AgentID, t.ConversationID).Scan(&ticketID, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find current declaration: %w", err)
	}
	marks, err := loadMarks(ctx, q, t)
	if err != nil {
		return err
	}
	for i := range records {
		r := &records[i]
		total := fieldsOf(r)
		mark := marks[r.Key()]
		delta := mark.since(total)
		if _, err := q.Exec(ctx, `
			INSERT INTO llm_usage_marks (agent_id, transcript_key, provider, model, harness, billing_mode,
				requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
				reported_cost_microdollars, estimated_cost_microdollars)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode) DO UPDATE SET
				requests = GREATEST(llm_usage_marks.requests, EXCLUDED.requests),
				input_tokens = GREATEST(llm_usage_marks.input_tokens, EXCLUDED.input_tokens),
				output_tokens = GREATEST(llm_usage_marks.output_tokens, EXCLUDED.output_tokens),
				cache_read_tokens = GREATEST(llm_usage_marks.cache_read_tokens, EXCLUDED.cache_read_tokens),
				cache_write_tokens = GREATEST(llm_usage_marks.cache_write_tokens, EXCLUDED.cache_write_tokens),
				reasoning_tokens = GREATEST(llm_usage_marks.reasoning_tokens, EXCLUDED.reasoning_tokens),
				reported_cost_microdollars = GREATEST(llm_usage_marks.reported_cost_microdollars, EXCLUDED.reported_cost_microdollars),
				estimated_cost_microdollars = GREATEST(llm_usage_marks.estimated_cost_microdollars, EXCLUDED.estimated_cost_microdollars),
				updated_at = now()`,
			t.AgentID, string(t.Key), r.Provider, r.Model, string(r.Harness), string(r.BillingMode),
			total.Requests, total.Input, total.Output, total.CacheRead, total.CacheWrite, total.Reasoning,
			total.Reported, total.Estimated); err != nil {
			return fmt.Errorf("raise usage mark: %w", err)
		}
		if a.Scope != ScopeOverhead || ticketID == nil || delta.isZero() {
			continue
		}
		// As llmusage prices it now, on the delta's own tokens: the
		// estimate of the totals cannot be subtracted, as the price table
		// may have changed between the two reports.
		priced := usagelib.Record{
			SchemaVersion: usagelib.SchemaVersion, Provider: r.Provider, Model: r.Model, Harness: r.Harness, BillingMode: r.BillingMode,
			InputTokens: delta.Input, OutputTokens: delta.Output, CacheReadTokens: delta.CacheRead, CacheWriteTokens: delta.CacheWrite,
		}
		s.pricer.Estimate(&priced)
		if _, err := q.Exec(ctx, `
			INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, attempt_number, provider, model, harness, billing_mode,
				requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
				reported_cost_microdollars, estimated_cost_microdollars, price_version, unpriced, ended_at, unreliable)
			VALUES ($1, $2, $3, $19, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $20)
			ON CONFLICT (tenant_id, agent_id, transcript_key, ticket_id, COALESCE(attempt_number, 0), provider, model, harness, billing_mode) DO UPDATE SET
				requests = llm_guidance_usage.requests + EXCLUDED.requests,
				input_tokens = llm_guidance_usage.input_tokens + EXCLUDED.input_tokens,
				output_tokens = llm_guidance_usage.output_tokens + EXCLUDED.output_tokens,
				cache_read_tokens = llm_guidance_usage.cache_read_tokens + EXCLUDED.cache_read_tokens,
				cache_write_tokens = llm_guidance_usage.cache_write_tokens + EXCLUDED.cache_write_tokens,
				reasoning_tokens = llm_guidance_usage.reasoning_tokens + EXCLUDED.reasoning_tokens,
				reported_cost_microdollars = CASE WHEN llm_guidance_usage.reported_cost_microdollars IS NULL AND EXCLUDED.reported_cost_microdollars IS NULL
					THEN NULL ELSE COALESCE(llm_guidance_usage.reported_cost_microdollars, 0) + COALESCE(EXCLUDED.reported_cost_microdollars, 0) END,
				estimated_cost_microdollars = CASE WHEN llm_guidance_usage.estimated_cost_microdollars IS NULL AND EXCLUDED.estimated_cost_microdollars IS NULL
					THEN NULL ELSE COALESCE(llm_guidance_usage.estimated_cost_microdollars, 0) + COALESCE(EXCLUDED.estimated_cost_microdollars, 0) END,
				price_version = EXCLUDED.price_version,
				unpriced = llm_guidance_usage.unpriced OR EXCLUDED.unpriced,
				unreliable = llm_guidance_usage.unreliable OR EXCLUDED.unreliable,
				ended_at = GREATEST(llm_guidance_usage.ended_at, EXCLUDED.ended_at),
				updated_at = now()`,
			t.AgentID, string(t.Key), *ticketID, r.Provider, r.Model, string(r.Harness), string(r.BillingMode),
			delta.Requests, delta.Input, delta.Output, delta.CacheRead, delta.CacheWrite, delta.Reasoning,
			delta.Reported, priced.EstimatedCostMicros, priced.PriceVersion, priced.Unpriced, r.EndedAt, attempt, t.Unreliable); err != nil {
			return fmt.Errorf("charge guidance usage: %w", err)
		}
	}
	return nil
}

func loadMarks(ctx context.Context, q db.Querier, t *Transcript) (map[usagelib.RecordKey]markFields, error) {
	rows, err := q.Query(ctx, `
		SELECT provider, model, harness, billing_mode, requests, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, reasoning_tokens, reported_cost_microdollars, estimated_cost_microdollars
		  FROM llm_usage_marks WHERE agent_id = $1 AND transcript_key = $2`, t.AgentID, string(t.Key))
	if err != nil {
		return nil, fmt.Errorf("load usage marks: %w", err)
	}
	defer rows.Close()
	marks := map[usagelib.RecordKey]markFields{}
	for rows.Next() {
		var k usagelib.RecordKey
		var m markFields
		if err := rows.Scan(&k.Provider, &k.Model, &k.Harness, &k.BillingMode, &m.Requests, &m.Input, &m.Output,
			&m.CacheRead, &m.CacheWrite, &m.Reasoning, &m.Reported, &m.Estimated); err != nil {
			return nil, fmt.Errorf("scan usage mark: %w", err)
		}
		marks[k] = m
	}
	return marks, rows.Err()
}
