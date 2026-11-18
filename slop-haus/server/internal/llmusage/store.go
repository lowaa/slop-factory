package llmusage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store is the persistence Service needs. Every call runs in the
// context's tenant; row level security hides every other tenant's rows.
type Store interface {
	// Upsert stores records as the transcript's full totals, replacing what
	// was stored for it, attributes it to a ticket from the agent's leases,
	// and returns what was stored before -- atomically, as go-llm-usage's
	// Store contract asks: two reports for one transcript run one after
	// the other, and each sees what the other stored.
	Upsert(ctx context.Context, t *Transcript, records []usagelib.Record) (previous []usagelib.Record, attribution *Attribution, err error)

	// Declare records the agent's declaration of the ticket its usage goes
	// to (nil: overhead), for the scope it covers.
	Declare(ctx context.Context, agentID uuid.UUID, ticketID *int64, attempt *int) (*Declaration, error)
}

type PostgresStore struct {
	db *db.DB
	// pricer prices the deltas charged to a declaration.
	pricer usagelib.Pricer
}

func NewPostgresStore(database *db.DB, pricer usagelib.Pricer) *PostgresStore {
	return &PostgresStore{db: database, pricer: pricer}
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Upsert(ctx context.Context, t *Transcript, records []usagelib.Record) ([]usagelib.Record, *Attribution, error) {
	var (
		previous    []usagelib.Record
		attribution *Attribution
	)
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// The agent's lock first, as Declare takes it: a declaration is
		// then made between two reports, never in the middle of one.
		if err := db.TakeSubjectAdvisoryLock(ctx, q, db.LLMUsageLockKey, agentLockSubject(t.AgentID)); err != nil {
			return err
		}
		if err := db.TakeSubjectAdvisoryLock(ctx, q, db.LLMUsageLockKey, t.AgentID.String()+"/"+string(t.Key)); err != nil {
			return err
		}
		var err error
		if previous, err = loadTranscript(ctx, q, t); err != nil {
			return err
		}
		if err := checkPlausible(previous, records); err != nil {
			return err
		}
		// Cumulative totals that include a flagged report stay flagged.
		var flagged bool
		if err := q.QueryRow(ctx, `SELECT COALESCE(bool_or(unreliable), false) FROM llm_usage
			WHERE agent_id = $1 AND transcript_key = $2`, t.AgentID, string(t.Key)).Scan(&flagged); err != nil {
			return fmt.Errorf("load llm usage flag: %w", err)
		}
		rowFlag := flagged || t.Unreliable
		// Replaced whole, tools by cascade: a model or tool missing from
		// the new totals is gone, as the Store contract says.
		if _, err := q.Exec(ctx, `DELETE FROM llm_usage WHERE agent_id = $1 AND transcript_key = $2`,
			t.AgentID, string(t.Key)); err != nil {
			return fmt.Errorf("replace llm usage: %w", err)
		}
		// Worked out again on every report, since each one can widen the
		// transcript's window into another ticket's.
		if t.ConversationID != nil {
			attribution, err = attributeConversation(ctx, q, t.AgentID, *t.ConversationID)
		} else {
			start, end := window(records)
			attribution, err = attribute(ctx, q, t.AgentID, start, end)
		}
		if err != nil {
			return err
		}
		if err := insertTranscript(ctx, q, t, attribution, records, rowFlag); err != nil {
			return err
		}
		if err := s.addDaily(ctx, q, t, previous, records); err != nil {
			return err
		}
		return s.splitGuidance(ctx, q, t, attribution, records)
	})
	if err != nil {
		return nil, nil, err
	}
	return previous, attribution, nil
}

// attribute charges the window to the one ticket whose lease, held by this
// agent, overlaps it -- or to overhead when none does or several do.
//
// A lease's window runs from its claim to its end. Expiry is recorded
// lazily, by whichever claim next sweeps it, so an expired lease ends at
// its deadline rather than at the sweep, and an unreleased one past its
// deadline has already ended unless the integration lock pins it (as the
// ticket store's activeLeaseClause has it). leases keeps released rows for LEASE_RETENTION, far longer than
// any transcript lasts, and what was attributed is stored on the usage row
// rather than worked out again later.
func attribute(ctx context.Context, q db.Querier, agentID uuid.UUID, start, end time.Time) (*Attribution, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ticket_id, attempt_number FROM leases
		 WHERE agent_id = $1
		   AND acquired_at <= $3
		   AND CASE WHEN released_at IS NOT NULL AND release_reason = 'expired' THEN LEAST(released_at, expires_at)
		            WHEN released_at IS NOT NULL THEN released_at
		            WHEN EXISTS (SELECT 1 FROM locks lk WHERE lk.holder_lease_id = leases.id) THEN 'infinity'::timestamptz
		            ELSE expires_at END >= $2
		 LIMIT 2`, agentID, start, end)
	if err != nil {
		return nil, fmt.Errorf("attribute llm usage: %w", err)
	}
	defer rows.Close()
	type leased struct {
		ticket  int64
		attempt int
	}
	var attempts []leased
	for rows.Next() {
		var l leased
		if err := rows.Scan(&l.ticket, &l.attempt); err != nil {
			return nil, fmt.Errorf("attribute llm usage: %w", err)
		}
		attempts = append(attempts, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attribute llm usage: %w", err)
	}
	if len(attempts) != 1 {
		return &Attribution{Scope: ScopeOverhead}, nil
	}
	return &Attribution{Scope: ScopeTicket, TicketID: &attempts[0].ticket, Attempt: &attempts[0].attempt}, nil
}

// attributeConversation charges a conductor's conversation to overhead: a
// Remote Control session is the owner's own and is about no ticket. A
// conversation that is not this conductor's is not found, as for everything
// else that names one.
func attributeConversation(ctx context.Context, q db.Querier, agentID, conversationID uuid.UUID) (*Attribution, error) {
	var found bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_conversations WHERE id = $1 AND conductor_id = $2)`,
		conversationID, agentID).Scan(&found); err != nil {
		return nil, fmt.Errorf("attribute conversation usage: %w", err)
	}
	if !found {
		return nil, invalid("conversation %s is not this conductor's", conversationID)
	}
	return &Attribution{Scope: ScopeOverhead}, nil
}

const recordColumns = `provider, model, harness, billing_mode, requests, input_tokens, output_tokens,
	cache_read_tokens, cache_write_tokens, reasoning_tokens, reported_cost_microdollars,
	estimated_cost_microdollars, price_version, unpriced, started_at, ended_at`

func loadTranscript(ctx context.Context, q db.Querier, t *Transcript) ([]usagelib.Record, error) {
	rows, err := q.Query(ctx, `SELECT `+recordColumns+` FROM llm_usage
		WHERE agent_id = $1 AND transcript_key = $2`, t.AgentID, string(t.Key))
	if err != nil {
		return nil, fmt.Errorf("load llm usage: %w", err)
	}
	var records []usagelib.Record
	byKey := map[usagelib.RecordKey]int{}
	for rows.Next() {
		r := usagelib.Record{SchemaVersion: usagelib.SchemaVersion}
		if err := rows.Scan(&r.Provider, &r.Model, &r.Harness, &r.BillingMode, &r.Requests,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.ReasoningTokens,
			&r.ReportedCostMicros, &r.EstimatedCostMicros, &r.PriceVersion, &r.Unpriced,
			&r.StartedAt, &r.EndedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan llm usage: %w", err)
		}
		byKey[r.Key()] = len(records)
		records = append(records, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load llm usage: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}

	rows, err = q.Query(ctx, `
		SELECT provider, model, harness, billing_mode, category, tool, command, calls, errors, duration_ms, result_bytes
		  FROM llm_tool_usage
		 WHERE agent_id = $1 AND transcript_key = $2`, t.AgentID, string(t.Key))
	if err != nil {
		return nil, fmt.Errorf("load llm tool usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k usagelib.RecordKey
		var u usagelib.ToolUsage
		if err := rows.Scan(&k.Provider, &k.Model, &k.Harness, &k.BillingMode,
			&u.Category, &u.Tool, &u.Command, &u.Calls, &u.Errors, &u.DurationMs, &u.ResultBytes); err != nil {
			return nil, fmt.Errorf("scan llm tool usage: %w", err)
		}
		i, ok := byKey[k]
		if !ok {
			// Unreachable: the foreign key ties every tool row to a record.
			return nil, fmt.Errorf("llm tool usage %q has no record", u.Tool)
		}
		records[i].Tools = append(records[i].Tools, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load llm tool usage: %w", err)
	}
	return records, nil
}

func insertTranscript(ctx context.Context, q db.Querier, t *Transcript, a *Attribution, records []usagelib.Record, unreliable bool) error {
	var tools struct {
		provider, model, harness, billingMode, category, tool, command []string
		calls, errors, durationMs, resultBytes                         []int64
		endedAt                                                        []time.Time
	}
	for i := range records {
		r := &records[i]
		if _, err := q.Exec(ctx, `
			INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number,
				provider, model, billing_mode, requests, input_tokens, output_tokens,
				cache_read_tokens, cache_write_tokens, reasoning_tokens, reported_cost_microdollars,
				estimated_cost_microdollars, price_version, unpriced, started_at, ended_at, conversation_id, unreliable)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
			t.AgentID, string(t.Key), string(r.Harness), t.SessionID, t.ScopeHint, string(a.Scope), a.TicketID, a.Attempt,
			r.Provider, r.Model, string(r.BillingMode), r.Requests, r.InputTokens, r.OutputTokens,
			r.CacheReadTokens, r.CacheWriteTokens, r.ReasoningTokens, r.ReportedCostMicros,
			r.EstimatedCostMicros, r.PriceVersion, r.Unpriced, r.StartedAt, r.EndedAt, t.ConversationID, unreliable); err != nil {
			return fmt.Errorf("store llm usage: %w", err)
		}
		for j := range r.Tools {
			u := &r.Tools[j]
			tools.provider = append(tools.provider, r.Provider)
			tools.model = append(tools.model, r.Model)
			tools.harness = append(tools.harness, string(r.Harness))
			tools.billingMode = append(tools.billingMode, string(r.BillingMode))
			tools.category = append(tools.category, u.Category)
			tools.tool = append(tools.tool, u.Tool)
			tools.command = append(tools.command, u.Command)
			tools.calls = append(tools.calls, u.Calls)
			tools.errors = append(tools.errors, u.Errors)
			tools.durationMs = append(tools.durationMs, u.DurationMs)
			tools.resultBytes = append(tools.resultBytes, u.ResultBytes)
			tools.endedAt = append(tools.endedAt, r.EndedAt)
		}
	}
	if len(tools.tool) == 0 {
		return nil
	}
	// One statement for every tool row, however many there are.
	if _, err := q.Exec(ctx, `
		INSERT INTO llm_tool_usage (agent_id, transcript_key, provider, model, harness, billing_mode,
			category, tool, command, calls, errors, duration_ms, result_bytes, ended_at)
		SELECT $1, $2, u.* FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[],
			$9::text[], $10::bigint[], $11::bigint[], $12::bigint[], $13::bigint[], $14::timestamptz[]) AS u`,
		t.AgentID, string(t.Key), tools.provider, tools.model, tools.harness, tools.billingMode,
		tools.category, tools.tool, tools.command, tools.calls, tools.errors, tools.durationMs, tools.resultBytes, tools.endedAt); err != nil {
		return fmt.Errorf("store llm tool usage: %w", err)
	}
	return nil
}
