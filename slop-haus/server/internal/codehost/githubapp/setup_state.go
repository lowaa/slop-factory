package githubapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-auth/auth"
)

// StateKind is which step of the setup a state was issued for. A state made
// for one step is refused at the other.
type StateKind string

const (
	StateManifest StateKind = "manifest"
	StateInstall  StateKind = "install"
)

// stateLifetime is long enough to fill in GitHub's form or its install page;
// short enough that a token lost in a browser history is soon worthless. An
// org owner's approval of an install never comes back through the state:
// Verify finds the installation once it exists.
const stateLifetime = 10 * time.Minute

// ErrBadState is a state that is unknown, already used, expired, issued to
// someone else or for another step. They are not told apart: what a caller
// learns from the difference is how to probe for one.
var ErrBadState = errors.New("githubapp: the setup link is invalid, already used or expired; start again")

// SetupStates keeps the states issued to the browser, by hash only.
type SetupStates interface {
	// IssueState stores the hash of a state for the context's tenant.
	IssueState(ctx context.Context, hash []byte, userID uuid.UUID, kind StateKind, expires time.Time) error
	// ConsumeState deletes the matching state and reports whether there was
	// one that was issued to userID for kind and had not expired. Atomic:
	// of two concurrent calls with one token, one wins.
	ConsumeState(ctx context.Context, hash []byte, userID uuid.UUID, kind StateKind, now time.Time) (bool, error)
}

// newState is a fresh state token for the browser and the hash the server keeps.
func newState() (token string, hash []byte, err error) {
	return auth.NewOpaqueTokenWithPrefix("")
}

// hashState is the stored form. A token is 256 random bits, so a fast hash
// is enough, and finding the row by the hash leaks nothing an attacker can
// use: a guess has to be the whole token.
func hashState(token string) []byte {
	return auth.HashOpaqueTokenBytes(token)
}

func (s *PostgresStore) IssueState(ctx context.Context, hash []byte, userID uuid.UUID, kind StateKind, expires time.Time) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO codehost_setup_states (token_hash, user_id, kind, expires_at) VALUES ($1, $2, $3, $4)`,
		hash, userID, string(kind), expires)
	if err != nil {
		return fmt.Errorf("githubapp: issue state: %w", err)
	}
	// Rows long expired are swept, by the clock the new row's expiry came from, as new ones are issued, so the table needs no job.
	if _, err := s.db.Exec(ctx, `DELETE FROM codehost_setup_states WHERE expires_at < $1::timestamptz - interval '1 hour'`, expires); err != nil {
		return fmt.Errorf("githubapp: sweep states: %w", err)
	}
	return nil
}

func (s *PostgresStore) ConsumeState(ctx context.Context, hash []byte, userID uuid.UUID, kind StateKind, now time.Time) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx,
		`DELETE FROM codehost_setup_states WHERE token_hash = $1 AND user_id = $2 AND kind = $3 AND expires_at > $4
		 RETURNING 1`, hash, userID, string(kind), now).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("githubapp: consume state: %w", err)
	}
	return true, nil
}
