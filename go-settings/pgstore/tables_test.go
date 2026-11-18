package pgstore_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SQL TABLES.md documents is what apps copy, and the migration under
// testdata is what the tests run on: they must say the same thing.
func TestTablesDocumentationMatchesTheTestMigration(t *testing.T) {
	doc, err := os.ReadFile("../TABLES.md")
	require.NoError(t, err)
	migration, err := os.ReadFile("testdata/migrations/000001_settings_tables.sql")
	require.NoError(t, err)

	blocks := regexp.MustCompile("(?s)<!-- sql:(\\w+) -->\n```sql\n(.*?)```").FindAllStringSubmatch(string(doc), -1)
	require.Len(t, blocks, 3, "TABLES.md documents the global, user_tenant and tenant tables")

	// Whatever the migration holds from the first documented table on, up to
	// its Down section, is exactly the documented blocks, in order.
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	first := strings.Index(up, strings.TrimSpace(strings.SplitN(blocks[0][2], "\n", 2)[0]))
	require.GreaterOrEqual(t, first, 0, "the first documented table is not in the migration")
	var documented []string
	for _, b := range blocks {
		documented = append(documented, b[2])
	}
	assert.Equal(t, normalizeSQL(strings.Join(documented, "\n")), normalizeSQL(up[first:]))
}

func normalizeSQL(s string) string {
	s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}
