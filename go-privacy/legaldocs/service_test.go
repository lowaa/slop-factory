package legaldocs_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

// The tests share one database, so each publishes to a kind of its own; the
// app-declared ones are for tests that would otherwise run out of built-ins.
func newService(t *testing.T, store legaldocs.Store, now time.Time) (*legaldocs.Service, *clock) {
	t.Helper()
	kinds, err := legaldocs.NewKinds("routes_notice", "empty_notice")
	require.NoError(t, err)
	c := &clock{now: now}
	svc, err := legaldocs.NewService(&legaldocs.Config{Kinds: kinds, Store: store, Clock: c.Now})
	require.NoError(t, err)
	return svc, c
}

func versionOf(kind, ver string, effective time.Time, material bool, body string) *legaldocs.Version {
	return &legaldocs.Version{Kind: kind, Version: ver, Locale: "en", Effective: effective, Material: material, Body: body}
}

func TestVersionSelection(t *testing.T) {
	ctx := context.Background()
	store := legaldocs.NewPostgresStore(dbtest.AppEngine())
	svc, clk := newService(t, store, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	k := legaldocs.Terms
	require.NoError(t, svc.Publish(ctx, []*legaldocs.Version{
		versionOf(k, "2026-01-01.1", jan1, true, "first"),
		versionOf(k, "2026-06-01.1", jun1, true, "scheduled"),
		versionOf(k, "2026-01-01.2", jan1, false, "same day, later version"),
	}))

	cur, err := svc.Current(ctx, k, "")
	require.NoError(t, err)
	assert.Equal(t, "2026-01-01.2", cur.Version, "latest by (effective, version), not by when it was published")

	hist, err := svc.History(ctx, k, "")
	require.NoError(t, err)
	require.Len(t, hist, 2, "a scheduled version isn't public yet")
	assert.Equal(t, "2026-01-01.2", hist[0].Version, "newest first")
	assert.Equal(t, "2026-01-01.1", hist[1].Version)

	_, err = svc.Get(ctx, k, "2026-06-01.1", "")
	require.ErrorIs(t, err, legaldocs.ErrNotFound)

	clk.now = jun1.Add(time.Hour)
	cur, err = svc.Current(ctx, k, "")
	require.NoError(t, err)
	assert.Equal(t, "2026-06-01.1", cur.Version, "it comes into force on its date")
	got, err := svc.Get(ctx, k, "2026-01-01.1", "")
	require.NoError(t, err)
	assert.Equal(t, "first", got.Body, "and every older version stays readable")
}

func TestUnknownKindLocaleAndEmptyKindAreNotFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), jan1)
	for name, call := range map[string]func() error{
		"unknown kind":  func() error { _, err := svc.Current(ctx, "nope", ""); return err },
		"no versions":   func() error { _, err := svc.Current(ctx, "empty_notice", ""); return err },
		"bad locale":    func() error { _, err := svc.Current(ctx, legaldocs.Terms, "../x"); return err },
		"unknown get":   func() error { _, err := svc.Get(ctx, "nope", "v1", ""); return err },
		"unknown vers.": func() error { _, err := svc.Get(ctx, legaldocs.Terms, "nope", ""); return err },
	} {
		assert.ErrorIs(t, call(), legaldocs.ErrNotFound, name)
	}
}

func TestPublishedVersionKeepsItsTextAfterTheFileChanges(t *testing.T) {
	ctx := context.Background()
	store := legaldocs.NewPostgresStore(dbtest.AppEngine())
	svc, _ := newService(t, store, jun1)
	k := legaldocs.HealthDataNotice
	loader := newLoader(t, nil)

	src := fstest.MapFS{"legal/health_data_notice/v1.md": {Data: file("kind: health_data_notice", "version: v1", "effective: 2026-01-01", "locale: en", "material: true")}}
	vs, err := loader.Load(src, "legal")
	require.NoError(t, err)
	require.NoError(t, svc.Publish(ctx, vs))
	require.NoError(t, svc.Publish(ctx, vs), "a restart with the same file is fine")

	// Someone edits the published version's file in place.
	src["legal/health_data_notice/v1.md"] = &fstest.MapFile{Data: []byte(
		"---\nkind: health_data_notice\nversion: v1\neffective: 2026-01-01\nlocale: en\nmaterial: true\n---\n# Quietly rewritten\n")}
	edited, err := loader.Load(src, "legal")
	require.NoError(t, err)
	require.NoError(t, svc.Publish(ctx, edited), "a published version is not compared with its file: CI's lock catches the edit")

	got, err := svc.Get(ctx, k, "v1", "")
	require.NoError(t, err)
	assert.Contains(t, got.Body, "Synthetic body.", "what a person consented to is still what they can read")
	assert.NotContains(t, got.Body, "Quietly rewritten")

	cur, err := svc.Current(ctx, k, "")
	require.NoError(t, err)
	assert.Equal(t, got.Body, cur.Body)
}

func TestPublishRefusesAnUndeclaredKind(t *testing.T) {
	svc, _ := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), jan1)
	err := svc.Publish(context.Background(), []*legaldocs.Version{versionOf("nope", "v1", jan1, true, "x")})
	require.Error(t, err)
}

func TestNewServiceNeedsKindsAndStore(t *testing.T) {
	_, err := legaldocs.NewService(&legaldocs.Config{})
	require.Error(t, err)
}
