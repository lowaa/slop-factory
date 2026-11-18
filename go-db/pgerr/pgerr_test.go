package pgerr_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/internal/demo"
	"github.com/lowaa/slop-factory/go-db/pgerr"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, demo.Migrations)
}

func insertAccount(ctx context.Context, t *testing.T, id, tenant uuid.UUID, currency string) error {
	t.Helper()
	tx := dbtest.WithTenantTx(t, tenant)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, 'x', $3)`, id, tenant, currency)
	return err
}

// The violations come from a real server, through the engine's own error
// wrapping, so the helpers are proven against what callers actually receive.
func TestViolationsFromRealDatabase(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()

	fkErr := insertAccount(ctx, t, uuid.New(), tenant, "NOPE")
	require.Error(t, fkErr)
	assert.True(t, pgerr.IsForeignKeyViolation(fkErr))
	assert.True(t, pgerr.IsForeignKeyViolation(fmt.Errorf("wrapped: %w", fkErr)))
	assert.True(t, pgerr.IsForeignKeyViolation(fkErr, "accounts_currency_code_fkey"))
	assert.False(t, pgerr.IsForeignKeyViolation(fkErr, "other"))
	assert.False(t, pgerr.IsUniqueViolation(fkErr))

	tx := dbtest.WithTenantTx(t, tenant)
	id := uuid.New()
	insert := func() error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, 'x', 'USD')`, id, tenant)
		return err
	}
	require.NoError(t, insert())
	uniqueErr := insert()
	require.Error(t, uniqueErr)
	assert.True(t, pgerr.IsUniqueViolation(uniqueErr))
	assert.True(t, pgerr.IsUniqueViolation(uniqueErr, "other", "accounts_pkey"))
	assert.False(t, pgerr.IsUniqueViolation(uniqueErr, "other"))
	assert.False(t, pgerr.IsForeignKeyViolation(uniqueErr))

	assert.False(t, pgerr.IsUniqueViolation(nil))
	assert.False(t, pgerr.IsForeignKeyViolation(nil))
}

type stateErr string

func (e stateErr) Error() string    { return "state " + string(e) }
func (e stateErr) SQLState() string { return string(e) }

// An engine may wrap the driver's error in its own type; the state alone
// classifies, but a constraint filter needs the driver's metadata.
func TestNonDriverErrors(t *testing.T) {
	assert.True(t, pgerr.IsUniqueViolation(stateErr("23505")))
	assert.True(t, pgerr.IsForeignKeyViolation(stateErr("23503")))
	assert.False(t, pgerr.IsUniqueViolation(stateErr("23505"), "a_key"), "no constraint metadata")
	assert.False(t, pgerr.IsUniqueViolation(stateErr("23503")))
	assert.False(t, pgerr.IsUniqueViolation(errors.New("plain")))
	assert.False(t, pgerr.IsForeignKeyViolation(errors.New("plain")))
}
