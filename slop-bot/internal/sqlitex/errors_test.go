package sqlitex_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex/sqlitextest"
)

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()
	db := sqlitextest.Open(t, migrations(t))
	ctx := context.Background()
	_, err := db.NewRaw("INSERT INTO widgets (id, name) VALUES (1, 'a')").Exec(ctx)
	require.NoError(t, err)

	_, err = db.NewRaw("INSERT INTO widgets (id, name) VALUES (2, 'a')").Exec(ctx)
	assert.True(t, sqlitex.IsUniqueViolation(err), "UNIQUE: %v", err)
	_, err = db.NewRaw("INSERT INTO widgets (id, name) VALUES (1, 'b')").Exec(ctx)
	assert.True(t, sqlitex.IsUniqueViolation(err), "PRIMARY KEY: %v", err)
	_, err = db.NewRaw("INSERT INTO parts (widget_id) VALUES (999)").Exec(ctx)
	assert.False(t, sqlitex.IsUniqueViolation(err), "FOREIGN KEY: %v", err)
	assert.False(t, sqlitex.IsUniqueViolation(nil))
}
