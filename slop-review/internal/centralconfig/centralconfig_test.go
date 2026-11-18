package centralconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"
)

const serverURL = "https://slop-review.example"

type fakeFetcher struct {
	cfg *providerconfig.Config
	err error
}

func (f *fakeFetcher) FetchProviderConfig(context.Context) (*providerconfig.Config, error) {
	return f.cfg, f.err
}

func source(f *fakeFetcher) *Source {
	return &Source{
		NewFetcher: func(string, reporter.TokenSource) (Fetcher, error) { return f, nil },
		Now:        func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) },
	}
}

func revision(n int64, provider string) *providerconfig.Config {
	return &providerconfig.Config{Revision: n, Settings: providerconfig.Settings{Primary: providerconfig.Choice{Provider: provider}}}
}

func TestLookup_UsesTheServersConfigAndSavesIt(t *testing.T) {
	dir := t.TempDir()
	got := source(&fakeFetcher{cfg: revision(3, "openrouter")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	require.NotNil(t, got.Config)
	assert.Equal(t, config.SourceCentral, got.Source)
	assert.Equal(t, int64(3), got.Config.Revision)
	assert.Empty(t, got.Notes)

	info, err := os.Stat(cachePath(dir, serverURL, reporter.StaticToken("tok").Key()))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(cacheFilePerm), info.Mode().Perm(), "the saved copy is the user's alone")
}

func TestLookup_UnreachableServerUsesTheSavedCopy(t *testing.T) {
	dir := t.TempDir()
	source(&fakeFetcher{cfg: revision(3, "openrouter")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)

	got := source(&fakeFetcher{err: errors.New("connection refused")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	require.NotNil(t, got.Config)
	assert.Equal(t, config.SourceSaved, got.Source)
	assert.Equal(t, "openrouter", got.Config.Primary.Provider)
	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], "connection refused")
	assert.Contains(t, got.Notes[0], "revision 3")
}

func TestLookup_UnreachableServerWithNothingSavedMeansLocalSettings(t *testing.T) {
	got := source(&fakeFetcher{err: errors.New("connection refused")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), t.TempDir())
	assert.Nil(t, got.Config)
	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], "local settings")
}

// An invalid answer from the server is ignored, like an unreachable one:
// the last good copy is kept, and not overwritten.
func TestLookup_AnInvalidAnswerKeepsTheSavedCopy(t *testing.T) {
	dir := t.TempDir()
	source(&fakeFetcher{cfg: revision(3, "openrouter")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)

	got := source(&fakeFetcher{err: errors.New("the server's provider config is not valid")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	require.NotNil(t, got.Config)
	assert.Equal(t, int64(3), got.Config.Revision)
}

func TestLookup_NoConfigOnTheServerDropsTheSavedCopy(t *testing.T) {
	dir := t.TempDir()
	source(&fakeFetcher{cfg: revision(3, "openrouter")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)

	got := source(&fakeFetcher{err: reporter.ErrNoProviderConfig}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	assert.Nil(t, got.Config)
	assert.Empty(t, got.Notes)
	assert.NoFileExists(t, cachePath(dir, serverURL, reporter.StaticToken("tok").Key()))

	got = source(&fakeFetcher{err: errors.New("down")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	assert.Nil(t, got.Config, "nothing is saved any more")
}

// A copy saved from one server, or for one token's tenant, is never applied
// to runs reporting to another.
func TestLookup_SavedCopiesAreKeptPerServerAndToken(t *testing.T) {
	dir := t.TempDir()
	source(&fakeFetcher{cfg: revision(3, "openrouter")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)

	got := source(&fakeFetcher{err: errors.New("down")}).Lookup(context.Background(), "https://other.example", reporter.StaticToken("tok"), dir)
	assert.Nil(t, got.Config)
	got = source(&fakeFetcher{err: errors.New("down")}).Lookup(context.Background(), serverURL, reporter.StaticToken("another-tenants-token"), dir)
	assert.Nil(t, got.Config)
	got = source(&fakeFetcher{err: errors.New("down")}).Lookup(context.Background(), serverURL+"/", reporter.StaticToken("tok"), dir)
	assert.NotNil(t, got.Config, "the same server, however its URL ends")
}

// The saved file is outside this process's control between runs: one
// edited to name an unknown provider is ignored like an invalid answer.
func TestLookup_ATamperedSavedCopyIsIgnored(t *testing.T) {
	dir := t.TempDir()
	path := cachePath(dir, serverURL, reporter.StaticToken("tok").Key())
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"report_url":"`+serverURL+`","config":{"revision":1,"primary":{"provider":"evil"}}}`), 0o600))

	got := source(&fakeFetcher{err: errors.New("down")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	assert.Nil(t, got.Config)
	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], "not valid")
}

func TestLookup_ARefusedURLFallsBackToTheSavedCopy(t *testing.T) {
	// The real fetcher refuses plain http to a non-loopback host, as
	// reporting does, so the token never travels in the clear.
	got := (&Source{}).Lookup(context.Background(), "http://slop-review.example", reporter.StaticToken("tok"), t.TempDir())
	assert.Nil(t, got.Config)
	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], "https")
}

// A run that fetched an older revision than the one saved (it started
// first, finished later) doesn't roll the saved copy back.
func TestLookup_AnOlderRevisionNeverReplacesANewerSavedCopy(t *testing.T) {
	dir := t.TempDir()
	source(&fakeFetcher{cfg: revision(6, "openrouter")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	source(&fakeFetcher{cfg: revision(5, "chatgpt")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)

	got := source(&fakeFetcher{err: errors.New("down")}).Lookup(context.Background(), serverURL, reporter.StaticToken("tok"), dir)
	require.NotNil(t, got.Config)
	assert.Equal(t, int64(6), got.Config.Revision)
}
