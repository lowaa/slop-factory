package schema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-db/migrations"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

// baselineVersion is 000172_baseline's: the schema every migration builds on.
const baselineVersion = 172

// A merge conflict in migrations/LATEST means another migration landed
// first: renumber yours after theirs, update LATEST, and check yours still
// makes sense after theirs, not just that it applies.
func TestMigrationSequenceIsGaplessAndLatestIsCurrent(t *testing.T) {
	// 000172_baseline replaced every migration before it; see that file
	// for why the numbering starts at 172.
	lint := &migrations.SequenceLint{FirstVersion: baselineVersion}
	assert.Empty(t, lint.Lint(schema.Migrations))
}
