// Package legaldocstest holds what an app needs to test its legal document
// table against go-privacy: the test schema and StoreSuite.
package legaldocstest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs/schema"
)

// Migrations is a migration source for go-db's dbtest with just the legal
// document table.
func Migrations() fs.FS {
	return fstest.MapFS{"000001_legaldocs.sql": {Data: []byte(schema.LegalDocs)}}
}

func version(kind, ver string, effective time.Time, body string) *legaldocs.Version {
	return &legaldocs.Version{Kind: kind, Version: ver, Locale: "en", Effective: effective, Material: true, Body: body}
}

// StoreSuite is the contract every Store passes: an app runs it against its
// own migrated database, so drift between its table and the SQL here fails
// its build. Each run uses kinds of its own, so it can share a database.
func StoreSuite(t *testing.T, store legaldocs.Store) {
	ctx := context.Background()
	d1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	t.Run("a published version reads back as published", func(t *testing.T) {
		on := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)
		v := version("suite_roundtrip", "v1", d1, "# Synthetic\n\nBody text.")
		v.Material, v.Reviewed, v.ReviewedBy, v.ReviewedOn = false, true, "Synthetic Reviewer", &on
		require.NoError(t, store.Publish(ctx, v))
		got, err := store.Get(ctx, "suite_roundtrip", "v1", "en")
		require.NoError(t, err)
		assert.Equal(t, v, got)
	})

	t.Run("publishing the same content again changes nothing", func(t *testing.T) {
		v := version("suite_again", "v1", d1, "same")
		require.NoError(t, store.Publish(ctx, v))
		require.NoError(t, store.Publish(ctx, version("suite_again", "v1", d1, "same")))
		all, err := store.List(ctx, "suite_again", "en")
		require.NoError(t, err)
		assert.Len(t, all, 1)
	})

	t.Run("a published version keeps its text when published again with different content", func(t *testing.T) {
		require.NoError(t, store.Publish(ctx, version("suite_kept", "v1", d1, "the text a person agreed to")))

		for name, changed := range map[string]*legaldocs.Version{
			"text":      version("suite_kept", "v1", d1, "edited text"),
			"effective": version("suite_kept", "v1", d2, "the text a person agreed to"),
			"material": func() *legaldocs.Version {
				v := version("suite_kept", "v1", d1, "the text a person agreed to")
				v.Material = false
				return v
			}(),
			"reviewed": func() *legaldocs.Version {
				on := d1
				v := version("suite_kept", "v1", d1, "the text a person agreed to")
				v.Reviewed, v.ReviewedBy, v.ReviewedOn = true, "x", &on
				return v
			}(),
		} {
			require.ErrorIs(t, store.Publish(ctx, changed), legaldocs.ErrVersionChanged, name)
		}
		got, err := store.Get(ctx, "suite_kept", "v1", "en")
		require.NoError(t, err)
		assert.Equal(t, "the text a person agreed to", got.Body)
		assert.Equal(t, d1, got.Effective)
	})

	t.Run("versions are listed oldest first by effective date then version, per locale", func(t *testing.T) {
		require.NoError(t, store.Publish(ctx, version("suite_list", "b", d2, "b")))
		require.NoError(t, store.Publish(ctx, version("suite_list", "z", d1, "z")))
		require.NoError(t, store.Publish(ctx, version("suite_list", "a", d2, "a")))
		fr := version("suite_list", "a", d2, "texte")
		fr.Locale = "fr"
		require.NoError(t, store.Publish(ctx, fr))

		en, err := store.List(ctx, "suite_list", "en")
		require.NoError(t, err)
		var got []string
		for _, v := range en {
			got = append(got, v.Version)
		}
		assert.Equal(t, []string{"z", "a", "b"}, got)

		frs, err := store.List(ctx, "suite_list", "fr")
		require.NoError(t, err)
		require.Len(t, frs, 1)
		assert.Empty(t, frs[0].Body, "a list leaves the bodies out")
		full, err := store.Get(ctx, "suite_list", "a", "fr")
		require.NoError(t, err)
		assert.Equal(t, "texte", full.Body)
	})

	t.Run("an unknown version is not found", func(t *testing.T) {
		_, err := store.Get(ctx, "suite_none", "v1", "en")
		require.ErrorIs(t, err, legaldocs.ErrNotFound)
		none, err := store.List(ctx, "suite_none", "en")
		require.NoError(t, err)
		assert.Empty(t, none)
	})
}

// UpdateLockEnv, set to 1, makes CheckLock add the lines for versions that
// have none. It never rewrites an existing line: a published version's lock
// is the point.
const UpdateLockEnv = "UPDATE_VERSIONS_LOCK"

// CheckLock is an app's versions.lock test: it fails if a locked version's
// source changed, if the version in force now renders differently, or if a
// locked version is gone (see legaldocs.Lock). Versions with no line fail
// too, unless UpdateLockEnv is 1, which appends their lines.
func CheckLock(t *testing.T, lockPath string, versions []*legaldocs.Version) {
	t.Helper()
	problems, err := ReconcileLock(lockPath, versions, os.Getenv(UpdateLockEnv) == "1")
	require.NoError(t, err)
	for _, p := range problems {
		t.Error(p)
	}
}

// ReconcileLock is CheckLock without a testing.T: the problems found, and,
// when update is set, the lines for unlocked versions appended to the file
// (created if missing).
func ReconcileLock(lockPath string, versions []*legaldocs.Version, update bool) ([]string, error) {
	raw, err := os.ReadFile(lockPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	lock, err := legaldocs.ParseLock(raw)
	if err != nil {
		return nil, err
	}
	problems := lock.Check(versions)
	unlocked := lock.Unlocked(versions)
	if len(unlocked) == 0 {
		return problems, nil
	}
	lines := make([]string, len(unlocked))
	for i, v := range unlocked {
		lines[i] = legaldocs.LockLine(v)
	}
	slices.Sort(lines)
	if !update {
		return append(problems, fmt.Sprintf("versions with no lock line (run with %s=1):\n%s", UpdateLockEnv, strings.Join(lines, "\n"))), nil
	}
	out := string(raw)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return problems, os.WriteFile(lockPath, []byte(out+strings.Join(lines, "\n")+"\n"), 0o644)
}
