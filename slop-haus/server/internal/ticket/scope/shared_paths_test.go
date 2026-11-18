package scope_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func TestSharedPaths_CoversByPrefixPatternAndRepo(t *testing.T) {
	t.Parallel()
	sp := scope.NewSharedPaths([]scope.WorkspaceRepo{
		{Path: "mono", Shared: []scope.SharedPathRule{
			{Path: "db/migrations/", Resolve: scope.ResolveMigration},
			{Path: "gen/*.ts", Resolve: scope.ResolveRegenerate},
		}},
		{Path: "app", Shared: []scope.SharedPathRule{{Path: "out/", Resolve: scope.ResolveRegenerate}}},
	})
	for path, want := range map[string]string{
		"mono/db/migrations/":      scope.ResolveMigration,
		"mono/db/migrations/1.sql": scope.ResolveMigration,
		"mono/gen/a.ts":            scope.ResolveRegenerate,
		"app/out/x.js":             scope.ResolveRegenerate,
		"app/out/":                 scope.ResolveRegenerate,
		"mono/db/":                 "",
		"mono/db/migrations":       "",
		"mono/gen/":                "",
		"mono/gen/sub/a.ts":        "",
		"out/x.js":                 "",
		"app/db/migrations/":       "",
		"monolith/db/migrations/":  "",
		"mono":                     "",
	} {
		got, ok := sp.Covers(path)
		assert.Equal(t, want, got, path)
		assert.Equal(t, want != "", ok, path)
	}
	_, ok := (*scope.SharedPaths)(nil).Covers("mono/db/migrations/")
	assert.False(t, ok)
}
