package ticket

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// normalizePaths normalizes and de-duplicates a scope set, and rejects one
// that overlaps itself -- ["server/", "server/main.go"] is a filing mistake
// worth failing loudly on rather than silently collapsing, since the
// narrower entry has no effect and its author clearly expected it to.
func normalizePaths(paths []model.Path) ([]model.Path, error) {
	out := make([]model.Path, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))

	for _, p := range paths {
		normalized, err := scope.NormalizePath(p.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", err, p.Path)
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		if p.Source != "" && !p.Source.Valid() {
			return nil, fmt.Errorf("%w: path source %q", ErrInvalidEnum, p.Source)
		}
		for _, existing := range out {
			if scope.Conflicts(normalized, existing.Path) {
				return nil, fmt.Errorf("%w: %q overlaps %q in the same ticket",
					scope.ErrInvalidPath, normalized, existing.Path)
			}
		}
		source := p.Source
		if source == "" {
			source = model.PathSourceFiled
		}
		seen[normalized] = struct{}{}
		out = append(out, model.Path{Path: normalized, Source: source})
	}
	return out, nil
}

// ExtendScope adds paths to the ticket the agent holds, normalizing them
// first so a scope only ever reaches the conflict rule in one form.
func (s *Service) ExtendScope(ctx context.Context, agentID uuid.UUID, paths []model.Path) (*ExtendResult, error) {
	normalized, err := normalizePaths(paths)
	if err != nil {
		return nil, err
	}
	// Checked in the store's transaction, against the scopes the held ticket
	// has there: a lease swapped or paths edited since a read out here would
	// be judged on the wrong ticket.
	together := func(held []string) error {
		return s.checkOneRepo(ctx, append(held, scopePaths(normalized)...))
	}
	return s.store.ExtendScope(ctx, agentID, normalized, together)
}

// NarrowScope gives up paths of the ticket the agent holds, optionally
// keeping narrower paths inside the ones it drops.
//
// Giving scope up cannot conflict with another ticket, so unlike
// ExtendScope it asks nothing of them; it is refused only where it would
// add scope outside what was removed, or leave the ticket with none.
func (s *Service) NarrowScope(ctx context.Context, agentID uuid.UUID, remove, keep []string) (*NarrowResult, error) {
	removed, err := normalizeScopeList(remove)
	if err != nil {
		return nil, err
	}
	kept, err := normalizeScopeList(keep)
	if err != nil {
		return nil, err
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("%w: nothing to remove", scope.ErrInvalidPath)
	}
	return s.store.NarrowScope(ctx, agentID, removed, kept)
}

func normalizeScopeList(raw []string) ([]string, error) {
	paths := make([]model.Path, 0, len(raw))
	for _, p := range raw {
		paths = append(paths, model.Path{Path: p})
	}
	normalized, err := normalizePaths(paths)
	if err != nil {
		return nil, err
	}
	return scopePaths(normalized), nil
}

// filedScopeChange describes a person's replacement of a ticket's filed
// paths, and reports whether it changed anything. Extended paths are an
// agent's grant and survive the edit, so they are left out of both sides.
func filedScopeChange(before, after []model.Path) (string, bool) {
	old := filedPaths(before)
	// Resending a path an agent extended keeps it extended (the insert
	// ignores the duplicate), so it is not a filed path of the new scope.
	extended := map[string]bool{}
	for _, p := range before {
		if p.Source == model.PathSourceExtended {
			extended[p.Path] = true
		}
	}
	next := slices.DeleteFunc(filedPaths(after), func(p string) bool { return extended[p] })
	if slices.Equal(old, next) {
		return "", false
	}
	return fmt.Sprintf("Path scopes changed by a person. Was: %s. Now: %s.", scopeList(old), scopeList(next)), true
}

func filedPaths(paths []model.Path) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		// A path new to the edit has no source yet; only an extended one is
		// the agent's.
		if p.Source != model.PathSourceExtended {
			out = append(out, p.Path)
		}
	}
	slices.Sort(out)
	return out
}

func scopeList(paths []string) string {
	if len(paths) == 0 {
		return "(none)"
	}
	return strings.Join(paths, ", ")
}
