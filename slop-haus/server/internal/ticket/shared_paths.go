package ticket

import (
	"context"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// sharedPaths reads the manifest's shared paths. It is read outside the
// claim's transaction: it is configuration an owner changes rarely, and a
// claim racing that edit sees either side of it.
func (s *PostgresStore) sharedPaths(ctx context.Context) (*scope.SharedPaths, error) {
	if s.workspace == nil {
		return nil, nil
	}
	pl, err := s.workspace.Place(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("load shared paths: %w", err)
	}
	return scope.NewSharedPaths(pl.All), nil
}

// SharedOverlapNotice tells a worker granted a scope about the overlaps that
// did not block it, or "" when there are none.
func SharedOverlapNotice(overlaps []SharedOverlap) string {
	if len(overlaps) == 0 {
		return ""
	}
	lines := make([]string, len(overlaps))
	for i, o := range overlaps {
		lines[i] = fmt.Sprintf("  - %s, also held by #%d (%s): if merging the base branch conflicts there, %s", o.Path, o.TicketID, o.Resolve, scope.ResolveAdvice(o.Resolve))
	}
	return "SHARED PATHS: these overlap scopes other tickets hold, but lie inside the workspace's shared paths, so they did not block you:\n" +
		strings.Join(lines, "\n")
}
