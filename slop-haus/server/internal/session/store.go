package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

const sessionColumns = `id, agent_id, claude_session_id, cwd, started_at,
	last_seen_at, ended_at, last_progress_at, last_block_at, stop_block_count,
	harness, harness_version, provider, model, labels, config_revision, bundle_hash,
	stuck_reason, stuck_at`

func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	var labels []byte
	var stuckReason *string
	var stuckAt *time.Time
	if err := row.Scan(&s.ID, &s.AgentID, &s.ClaudeSessionID, &s.CWD, &s.StartedAt,
		&s.LastSeenAt, &s.EndedAt, &s.LastProgressAt, &s.LastBlockAt, &s.StopBlockCount,
		&s.Description.Harness, &s.Description.HarnessVersion, &s.Description.Provider,
		&s.Description.Model, &labels, &s.Description.ConfigRevision, &s.Description.BundleHash,
		&stuckReason, &stuckAt); err != nil {
		return nil, err
	}
	if stuckReason != nil && stuckAt != nil {
		s.Stuck = &Stuck{Reason: *stuckReason, At: *stuckAt}
	}
	if err := json.Unmarshal(labels, &s.Description.Labels); err != nil {
		return nil, fmt.Errorf("decode session labels: %w", err)
	}
	return &s, nil
}

// Start binds a `claude` run to an agent.
//
// It serializes on the agent row and then looks up by identity, rather than
// inserting blind and letting a unique violation sort out the mess. Three
// arrivals need three different answers and a suppressed insert cannot tell
// them apart; a unique violation would also abort the whole transaction,
// so "retry the statement" is not available.
func (s *PostgresStore) Start(ctx context.Context, agentID uuid.UUID, claudeSessionID, cwd string) (*StartResult, error) {
	result := &StartResult{}

	err := s.db.InTx(ctx, func(q db.Querier) error {
		// One binder per agent at a time. READ COMMITTED is enough
		// precisely because of this lock: every binder queues behind it and
		// sees the previous one's committed result, so there is no
		// lost-update window.
		if _, err := q.Exec(ctx, `SELECT id FROM agents WHERE id = $1 FOR UPDATE`, agentID); err != nil {
			return fmt.Errorf("lock agent: %w", err)
		}

		existing, err := scanSession(q.QueryRow(ctx,
			`SELECT `+sessionColumns+` FROM sessions
			  WHERE agent_id = $1 AND claude_session_id = $2`, agentID, claudeSessionID))
		switch {
		case err == nil && existing.Current():
			// Ordinary retry: adopt it and write nothing. A
			// close-then-insert here would shut the session it had just
			// created, open a third, and reset the loop counters.
			result.Session, result.Arrival = existing, ArrivalResumed
			return nil
		case err == nil:
			// A retry that arrived after a newer session superseded it.
			current, err := s.currentIn(ctx, q, agentID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			result.Session, result.Arrival = current, ArrivalStale
			if current == nil {
				result.Session = existing
			}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("look up session: %w", err)
		}

		// Genuinely new. Close the agent's current session first; if it was
		// still within its liveness window, two runs are live at once.
		previous, err := s.currentIn(ctx, q, agentID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if previous != nil {
			result.ConcurrentRun = time.Since(previous.LastSeenAt) < LivenessWindow
			if _, err := q.Exec(ctx,
				`UPDATE sessions SET ended_at = now() WHERE id = $1`, previous.ID); err != nil {
				return fmt.Errorf("end previous session: %w", err)
			}
		}

		started, err := scanSession(q.QueryRow(ctx, `
			INSERT INTO sessions (agent_id, claude_session_id, cwd)
			VALUES ($1, $2, $3)
			RETURNING `+sessionColumns, agentID, claudeSessionID, cwd))
		if err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		result.Session, result.Arrival = started, ArrivalNew
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PostgresStore) currentIn(ctx context.Context, q db.Querier, agentID uuid.UUID) (*Session, error) {
	found, err := scanSession(q.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM sessions
		  WHERE agent_id = $1 AND ended_at IS NULL`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load current session: %w", err)
	}
	return found, nil
}

func (s *PostgresStore) Current(ctx context.Context, agentID uuid.UUID) (*Session, error) {
	return s.currentIn(ctx, s.db, agentID)
}

func (s *PostgresStore) List(ctx context.Context) ([]Session, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sessionColumns+` FROM sessions
		 WHERE ended_at IS NULL ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		found, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, *found)
	}
	return sessions, rows.Err()
}

// Touch records liveness against whichever session is current. Every MCP
// tool call does this, so there is no separate heartbeat to forget.
//
// Unscoped because a tool call carries no Claude session id -- only the
// agent's token, which a superseded process still holds. The hook path can
// do better (see TouchSession) because its payload names the session.
func (s *PostgresStore) Touch(ctx context.Context, agentID uuid.UUID) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE sessions SET last_seen_at = now()
		  WHERE agent_id = $1 AND ended_at IS NULL`, agentID); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// TouchSession records liveness for one named Claude run, and reports
// whether that run is still the agent's current session.
//
// The scoping is the point. A superseded `claude` process keeps its
// terminal, its token, and its Stop hook -- so without matching on the
// session id its callbacks would refresh the *replacement* session's
// last_seen_at, making a dead process keep a live one looking healthy.
func (s *PostgresStore) TouchSession(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (State, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE sessions SET last_seen_at = now()
		 WHERE agent_id = $1 AND claude_session_id = $2 AND ended_at IS NULL`,
		agentID, claudeSessionID)
	if err != nil {
		return StateUnknown, fmt.Errorf("touch session: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return StateLive, nil
	}

	// No live row for this session. Superseded or never registered?
	//
	// The distinction is whether this session has been seen *before*. A
	// row that exists but has ended is a run that was already replaced --
	// its callbacks must not drag it back, or two live processes would
	// ping-pong, each rebinding over the other forever.
	//
	// No row at all is a new run. It takes over, ending whatever was
	// current. That has to be the rule because SessionStart does not fire
	// in Claude Code (it runs before project settings load), so a Stop
	// callback is the only evidence a session exists -- and an earlier
	// version that kept the incumbent meant the first session an agent
	// ever bound stayed current forever and every later run was dismissed.
	var seenBefore bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM sessions WHERE agent_id = $1 AND claude_session_id = $2)`,
		agentID, claudeSessionID).Scan(&seenBefore); err != nil {
		return StateUnknown, fmt.Errorf("check session history: %w", err)
	}
	if seenBefore {
		return StateSuperseded, nil
	}
	return StateUnknown, nil
}

// Describe records what a live run says about itself, replacing whatever
// it said before -- a model switched mid-run lands on the next hook.
//
// The model and harness version, which Claude Code's Stop reads from the
// transcript, keep what was recorded when a hook finds none: a lookup that
// came up empty has not seen the model change, and blanking it would lose
// the one thing the Agents screen knew about the run. The config revision
// is kept once recorded: a run's is fixed when it starts.
//
// Scoped to the named run like TouchSession, and to a live one: a
// superseded process must not relabel its replacement, and a stale retry
// must not rewrite history. A run with no live row is simply not described;
// ErrNotFound says so.
func (s *PostgresStore) Describe(ctx context.Context, agentID uuid.UUID, claudeSessionID string, d *Description) error {
	given := d.Labels
	if given == nil {
		given = map[string]string{}
	}
	labels, err := json.Marshal(given)
	if err != nil {
		return fmt.Errorf("encode session labels: %w", err)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE sessions
		   SET harness = $3,
		       harness_version = COALESCE(NULLIF($4, ''), harness_version),
		       provider = $5,
		       model = COALESCE(NULLIF($6, ''), model),
		       labels = $7,
		       config_revision = COALESCE(config_revision, $8),
		       bundle_hash = COALESCE(NULLIF($9, ''), bundle_hash)
		 WHERE agent_id = $1 AND claude_session_id = $2 AND ended_at IS NULL`,
		agentID, claudeSessionID, d.Harness, d.HarnessVersion, d.Provider, d.Model, labels, d.ConfigRevision, d.BundleHash)
	if err != nil {
		return fmt.Errorf("describe session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordProgress marks that the agent actually changed something, which
// resets the loop breaker.
func (s *PostgresStore) RecordProgress(ctx context.Context, agentID uuid.UUID) error {
	if _, err := s.db.Exec(ctx, `
		UPDATE sessions
		   SET last_seen_at = now(), last_progress_at = now(), stop_block_count = 0,
		       stuck_reason = NULL, stuck_at = NULL
		 WHERE agent_id = $1 AND ended_at IS NULL`, agentID); err != nil {
		return fmt.Errorf("record progress: %w", err)
	}
	return nil
}

// ResetBlocks gives a run a fresh stop-block budget without calling it
// progress: last_progress_at and the stuck notice stay as they are, so a
// nudge that does not help still shows as stuck until the agent does
// something.
//
// Scoped to one Claude run like RecordBlock, and for the same reason: a
// superseded process must not hand its replacement more blocks.
func (s *PostgresStore) ResetBlocks(ctx context.Context, agentID uuid.UUID, claudeSessionID string) error {
	if _, err := s.db.Exec(ctx, `
		UPDATE sessions
		   SET stop_block_count = 0, last_seen_at = now()
		 WHERE agent_id = $1 AND claude_session_id = $2 AND ended_at IS NULL`,
		agentID, claudeSessionID); err != nil {
		return fmt.Errorf("reset stop blocks: %w", err)
	}
	return nil
}

// RecordBlock counts a stop-block and returns the running count.
//
// It counts blocks *since the last progress*, not blocks in a row -- an
// agent working steadily through a queue would otherwise accumulate blocks
// and eventually be declared stuck for doing exactly what it should. That
// rule lives entirely in RecordProgress zeroing the counter, so this is a
// plain increment.
//
// An earlier version also re-derived it here, comparing last_progress_at
// against last_block_at. Two expressions of one rule, and a negative test
// run showed only one of them was load-bearing: breaking this arithmetic
// changed nothing, because the reset had already happened. Keeping the
// redundant half would have meant a rule that could drift out of agreement
// with itself and a test that could not tell.
// It is scoped to one Claude run for the same reason TouchSession is:
// repeated callbacks from a superseded process would otherwise increment
// the *replacement* session's counter and trip its loop breaker, letting a
// live agent stop because a dead one kept calling.
func (s *PostgresStore) RecordBlock(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		UPDATE sessions
		   SET stop_block_count = stop_block_count + 1,
		       last_block_at = now(),
		       last_seen_at = now()
		 WHERE agent_id = $1 AND claude_session_id = $2 AND ended_at IS NULL
		 RETURNING stop_block_count`, agentID, claudeSessionID).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("record block: %w", err)
	}
	return count, nil
}

// MarkStuck is scoped to one Claude run, like RecordBlock, so a superseded
// process cannot mark its replacement stuck. It also requires the breaker
// to still stand: Stop counts the block and marks in two statements, and
// progress landing between them has already reset the counter, so writing
// the reason then would report a stuck agent that is not.
func (s *PostgresStore) MarkStuck(ctx context.Context, agentID uuid.UUID, claudeSessionID, reason string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE sessions
		   SET stuck_reason = $3, stuck_at = now()
		 WHERE agent_id = $1 AND claude_session_id = $2 AND ended_at IS NULL
		   AND stop_block_count >= $4`,
		agentID, claudeSessionID, reason, MaxStopBlocks)
	if err != nil {
		return fmt.Errorf("mark stuck: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
