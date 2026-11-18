package legaldocs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs/legaldocstest"
)

const lockKind = "lock_notice"

func lockFile(version, effective, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nkind: " + lockKind + "\nversion: " + version + "\neffective: " + effective +
		"\nlocale: en\nmaterial: true\n---\n" + body + "\n")}
}

// Two versions, both with the retention table in them, so a change to the
// inventory's retention moves what each renders.
func lockSource() fstest.MapFS {
	return fstest.MapFS{
		"legal/" + lockKind + "/v1.md": lockFile("v1", "2026-01-01", "# Old\n\n{{inventory:retention}}"),
		"legal/" + lockKind + "/v2.md": lockFile("v2", "2026-06-01", "# New\n\nRetention is described elsewhere."),
	}
}

func loadLock(t *testing.T, src fstest.MapFS, retention string) []*legaldocs.Version {
	t.Helper()
	kinds, err := legaldocs.NewKinds(lockKind)
	require.NoError(t, err)
	l, err := legaldocs.NewLoader(&legaldocs.LoaderConfig{Kinds: kinds, Fragments: fixedFragments{"retention": retention}})
	require.NoError(t, err)
	vs, err := l.Load(src, "legal")
	require.NoError(t, err)
	return vs
}

func lockFor(t *testing.T, vs []*legaldocs.Version) *legaldocs.Lock {
	t.Helper()
	var lines []string
	for _, v := range vs {
		lines = append(lines, legaldocs.LockLine(v))
	}
	lock, err := legaldocs.ParseLock([]byte(strings.Join(lines, "\n")))
	require.NoError(t, err)
	return lock
}

func TestLockSurvivesAnInventoryChangeThatOnlyMovesAnOldVersion(t *testing.T) {
	src := lockSource()
	lock := lockFor(t, loadLock(t, src, "kept 30 days"))

	after := loadLock(t, src, "kept 90 days")
	assert.Empty(t, lock.Check(after))
	assert.Empty(t, lock.Unlocked(after))
}

func TestLockFailsWhenAnInventoryChangeMovesTheVersionInForce(t *testing.T) {
	src := lockSource()
	src["legal/"+lockKind+"/v2.md"] = lockFile("v2", "2026-06-01", "# New\n\n{{inventory:retention}}")
	lock := lockFor(t, loadLock(t, src, "kept 30 days"))

	problems := lock.Check(loadLock(t, src, "kept 90 days"))
	require.Len(t, problems, 1, "the old version also moved, and is not what is locked")
	assert.Contains(t, problems[0], "v2")
	assert.Contains(t, problems[0], "in force")
}

func TestLockFailsWhenAnOldVersionsSourceChanges(t *testing.T) {
	src := lockSource()
	lock := lockFor(t, loadLock(t, src, "kept 30 days"))

	src["legal/"+lockKind+"/v1.md"] = lockFile("v1", "2026-01-01", "# Old\n\n{{inventory:retention}}\n\nQuietly added.")
	problems := lock.Check(loadLock(t, src, "kept 30 days"))
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "v1")
	assert.Contains(t, problems[0], "file changed")
}

func TestLockFailsWhenAnOldVersionsDirectiveIsExpandedInTheSource(t *testing.T) {
	src := lockSource()
	lock := lockFor(t, loadLock(t, src, "kept 30 days"))

	// The same rendered text, written out: still an edit to a published file.
	src["legal/"+lockKind+"/v1.md"] = lockFile("v1", "2026-01-01", "# Old\n\nkept 30 days")
	assert.Len(t, lock.Check(loadLock(t, src, "kept 30 days")), 1)
}

func TestLockFailsWhenAVersionIsRemovedAndNotesOneWithNoLine(t *testing.T) {
	src := lockSource()
	lock := lockFor(t, loadLock(t, src, "x"))

	delete(src, "legal/"+lockKind+"/v1.md")
	src["legal/"+lockKind+"/v3.md"] = lockFile("v3", "2026-07-01", "# Newer")
	vs := loadLock(t, src, "x")
	problems := lock.Check(vs)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "v1")
	assert.Contains(t, problems[0], "no longer exists")
	unlocked := lock.Unlocked(vs)
	require.Len(t, unlocked, 1)
	assert.Equal(t, "v3", unlocked[0].Version)
}

func TestAnAddedVersionDoesNotRelockTheOneItReplacedAsHead(t *testing.T) {
	src := lockSource()
	src["legal/"+lockKind+"/v2.md"] = lockFile("v2", "2026-06-01", "{{inventory:retention}}")
	lock := lockFor(t, loadLock(t, src, "kept 30 days"))

	src["legal/"+lockKind+"/v3.md"] = lockFile("v3", "2026-07-01", "# Newer")
	assert.Empty(t, lock.Check(loadLock(t, src, "kept 90 days")), "v2 is old now: the change that needed a new version got one")
}

func TestParseLockRefusesMalformedAndDuplicateLines(t *testing.T) {
	_, err := legaldocs.ParseLock([]byte("terms v1 en onlythree"))
	require.Error(t, err)
	_, err = legaldocs.ParseLock([]byte("terms v1 en a b\nterms v1 en a b"))
	require.Error(t, err)
	l, err := legaldocs.ParseLock([]byte("# a comment\n\nterms v1 en a b\n"))
	require.NoError(t, err)
	assert.Len(t, l.Check(nil), 1, "the locked version is gone")
}

func TestReconcileLockWritesNewLinesOnlyWhenAskedAndNeverRewritesOne(t *testing.T) {
	src := lockSource()
	vs := loadLock(t, src, "kept 30 days")
	path := filepath.Join(t.TempDir(), "versions.lock")

	problems, err := legaldocstest.ReconcileLock(path, vs, false)
	require.NoError(t, err)
	require.Len(t, problems, 1, "a version with no line fails")
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr))

	problems, err = legaldocstest.ReconcileLock(path, vs, true)
	require.NoError(t, err)
	assert.Empty(t, problems)
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(written)), "\n"), 2)

	// Updating over an edited published source reports it and leaves the line.
	src["legal/"+lockKind+"/v1.md"] = lockFile("v1", "2026-01-01", "# Edited")
	problems, err = legaldocstest.ReconcileLock(path, loadLock(t, src, "kept 30 days"), true)
	require.NoError(t, err)
	assert.Len(t, problems, 1)
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(written), string(again))
}

func lockService(t *testing.T) *legaldocs.Service {
	t.Helper()
	kinds, err := legaldocs.NewKinds(lockKind)
	require.NoError(t, err)
	svc, err := legaldocs.NewService(&legaldocs.Config{Kinds: kinds, Store: legaldocs.NewPostgresStore(dbtest.AppEngine()), Clock: (&clock{now: jun1.AddDate(0, 3, 0)}).Now})
	require.NoError(t, err)
	return svc
}

// The startup half of the same rule: after an inventory change that moves an
// old version's render, publishing neither fails nor touches the row, and a
// new version still goes in.
func TestPublishLeavesPublishedVersionsAloneWhateverTheyNowRenderAs(t *testing.T) {
	ctx := context.Background()
	svc := lockService(t)
	src := lockSource()
	require.NoError(t, svc.Publish(ctx, loadLock(t, src, "kept 30 days")))

	src["legal/"+lockKind+"/v3.md"] = lockFile("v3", "2026-07-01", "# Newer")
	require.NoError(t, svc.Publish(ctx, loadLock(t, src, "kept 90 days")))

	old, err := svc.Get(ctx, lockKind, "v1", "")
	require.NoError(t, err)
	assert.Contains(t, old.Body, "kept 30 days")
	assert.NotContains(t, old.Body, "kept 90 days")
	cur, err := svc.Current(ctx, lockKind, "")
	require.NoError(t, err)
	assert.Equal(t, "v3", cur.Version, "the version the database lacked was published")
}

// recordingStore decides nothing: it holds what it is given and records the
// calls, for the cases a database can't show (that a call was not made, or
// what a concurrent publisher's conflict looks like).
type recordingStore struct {
	legaldocs.Store
	held         map[string]bool
	publishErr   error
	publishCalls []string
}

func (s *recordingStore) Get(_ context.Context, kind, version, locale string) (*legaldocs.Version, error) {
	if s.held[kind+"/"+version] {
		return &legaldocs.Version{Kind: kind, Version: version, Locale: locale}, nil
	}
	return nil, legaldocs.ErrNotFound
}

func (s *recordingStore) Publish(_ context.Context, v *legaldocs.Version) error {
	s.publishCalls = append(s.publishCalls, v.Kind+"/"+v.Version)
	return s.publishErr
}

func TestPublishNeverOffersAPublishedVersionToTheStore(t *testing.T) {
	store := &recordingStore{held: map[string]bool{lockKind + "/v1": true}}
	svc, err := legaldocs.NewService(&legaldocs.Config{Kinds: mustKinds(t), Store: store})
	require.NoError(t, err)
	require.NoError(t, svc.Publish(context.Background(), loadLock(t, lockSource(), "x")))
	assert.Equal(t, []string{lockKind + "/v2"}, store.publishCalls)
}

func TestPublishTreatsALostRaceForANewVersionAsPublished(t *testing.T) {
	store := &recordingStore{publishErr: legaldocs.ErrVersionChanged}
	svc, err := legaldocs.NewService(&legaldocs.Config{Kinds: mustKinds(t), Store: store})
	require.NoError(t, err)
	require.NoError(t, svc.Publish(context.Background(), loadLock(t, lockSource(), "x")), "the other replica's row stands")

	store.publishErr = os.ErrPermission
	require.ErrorIs(t, svc.Publish(context.Background(), loadLock(t, lockSource(), "x")), os.ErrPermission)
}

func mustKinds(t *testing.T) *legaldocs.Kinds {
	t.Helper()
	kinds, err := legaldocs.NewKinds(lockKind)
	require.NoError(t, err)
	return kinds
}

func TestTheHeadOfTwoVersionsOnTheSameDateIsTheGreaterVersion(t *testing.T) {
	src := lockSource()
	src["legal/"+lockKind+"/v1.md"] = lockFile("v1", "2026-06-01", "{{inventory:retention}}")
	src["legal/"+lockKind+"/v2.md"] = lockFile("v2", "2026-06-01", "# Same day")
	lock := lockFor(t, loadLock(t, src, "kept 30 days"))
	assert.Empty(t, lock.Check(loadLock(t, src, "kept 90 days")), "v1 is not the head, so its render is free to move")

	src["legal/"+lockKind+"/v2.md"] = lockFile("v2", "2026-06-01", "{{inventory:retention}}")
	lock = lockFor(t, loadLock(t, src, "kept 30 days"))
	assert.Len(t, lock.Check(loadLock(t, src, "kept 90 days")), 1, "v2 is")
}
