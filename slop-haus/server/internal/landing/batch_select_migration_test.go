package landing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

func migrationSelector(host *batchHostFake) *landing.BatchSelector {
	return landing.NewBatchSelector(&landing.PathPolicy{
		Apps:              []string{"app/", "other/"},
		MigrationPrefixes: []string{"app/migrations/", "other/migrations/"},
	}, host)
}

func TestBatchSelector_DoesNotPairTwoChangesAddingToTheSameMigrationsDirectory(t *testing.T) {
	host := newBatchHost()
	host.files[1] = []string{"app/migrations/0005_a.sql", "app/migrations/LATEST"}
	host.files[2] = []string{"app/migrations/0005_b.sql", "app/migrations/LATEST"}
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(2, "app/b/")}

	got, err := migrationSelector(host).Select(context.Background(), cands, 3)
	assert.Equal(t, []int64{1}, selected(t, got, err), "the second still lands, serially")
}

func TestBatchSelector_PairsAMigrationChangeWithANonMigrationOne(t *testing.T) {
	host := newBatchHost()
	host.files[1] = []string{"app/migrations/0005_a.sql"}
	host.files[2] = []string{"app/b.go"}
	host.files[3] = []string{"app/migrations/0005_c.sql"}
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(3, "app/c/"), candidate(2, "app/b/")}

	got, err := migrationSelector(host).Select(context.Background(), cands, 3)

	assert.Equal(t, []int64{1, 2}, selected(t, got, err), "the clashing one is skipped, not the one behind it")
}

func TestBatchSelector_PairsMigrationChangesOfDifferentDirectories(t *testing.T) {
	host := newBatchHost()
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(2, "app/b/")}
	sel := landing.NewBatchSelector(&landing.PathPolicy{
		Apps:              []string{"app/"},
		MigrationPrefixes: []string{"app/migrations/a/", "app/migrations/b/"},
	}, host)
	host.files[1] = []string{"app/migrations/a/0005.sql"}
	host.files[2] = []string{"app/migrations/b/0006.sql"}

	got, err := sel.Select(context.Background(), cands, 3)

	assert.Equal(t, []int64{1, 2}, selected(t, got, err))
}

func TestBatchSelector_PairsAtMostOneChangeAddingToADirectoryBesideTheHead(t *testing.T) {
	host := newBatchHost()
	host.files[1] = []string{"app/a.go"}
	host.files[2] = []string{"app/migrations/0005_b.sql"}
	host.files[3] = []string{"app/migrations/0005_c.sql"}
	cands := []landing.Candidate{candidate(1, "app/a/"), candidate(2, "app/b/"), candidate(3, "app/c/")}

	got, err := migrationSelector(host).Select(context.Background(), cands, 3)

	assert.Equal(t, []int64{1, 2}, selected(t, got, err))
}

func TestPathPolicy_MigrationIsOnlyAFileUnderAMigrationPrefix(t *testing.T) {
	p := &landing.PathPolicy{MigrationPrefixes: []string{"app/migrations/"}}

	dir, ok := p.Migration("app/migrations/0001.sql")
	assert.True(t, ok)
	assert.Equal(t, "app/migrations/", dir)
	_, ok = p.Migration("app/main.go")
	assert.False(t, ok)
}
