package migrations_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/migrations"
)

func seqFS(latest string, names ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, n := range names {
		fsys[n] = &fstest.MapFile{Data: []byte("-- +goose Up\n")}
	}
	if latest != "" {
		fsys[migrations.LatestFileName] = &fstest.MapFile{Data: []byte(latest + "\n")}
	}
	return fsys
}

func TestLintSequence(t *testing.T) {
	tests := []struct {
		name    string
		lint    migrations.SequenceLint
		fsys    fstest.MapFS
		wantErr string // substring of the single expected error; empty means clean
	}{
		{name: "clean", fsys: seqFS("000002_b", "000001_a.sql", "000002_b.sql")},
		{name: "clean with first version", lint: migrations.SequenceLint{FirstVersion: 7}, fsys: seqFS("000008_b", "000007_a.sql", "000008_b.sql")},
		{name: "duplicate version", fsys: seqFS("000002_b", "000001_a.sql", "000002_b.sql", "000002_c.sql"), wantErr: "version 2 is used by more than one migration"},
		{name: "gap", fsys: seqFS("000003_c", "000001_a.sql", "000003_c.sql"), wantErr: "2 is missing before 3"},
		{name: "starts above one", fsys: seqFS("000002_b", "000002_b.sql"), wantErr: "1 is missing before 2"},
		{name: "below first version", lint: migrations.SequenceLint{FirstVersion: 7}, fsys: seqFS("000007_a", "000006_x.sql", "000007_a.sql"), wantErr: "below the first version 7"},
		{name: "latest without any migrations", fsys: seqFS("000001_a"), wantErr: "no migrations found"},
		{name: "latest missing", fsys: seqFS("", "000001_a.sql"), wantErr: "LATEST is missing"},
		{name: "latest names a non-highest migration", fsys: seqFS("000001_a", "000001_a.sql", "000002_b.sql"), wantErr: `set LATEST to "000002_b"`},
		{name: "latest names a nonexistent migration", fsys: seqFS("000002_zzz", "000001_a.sql", "000002_b.sql"), wantErr: `set LATEST to "000002_b"`},
		{name: "malformed name", fsys: seqFS("000001_a", "000001_a.sql", "2_b.sql"), wantErr: `"2_b.sql" is malformed`},
		{name: "uppercase name", fsys: seqFS("000001_a", "000001_a.sql", "000002_A.sql"), wantErr: "is malformed"},
		{name: "unrelated files are ignored", fsys: func() fstest.MapFS {
			f := seqFS("000001_a", "000001_a.sql")
			f["README.md"] = &fstest.MapFile{}
			return f
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.lint.Lint(tc.fsys)
			if tc.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.ErrorContains(t, errs[0], tc.wantErr)
		})
	}
}

func TestLintSequenceExplainsAConflictedLatest(t *testing.T) {
	errs := (&migrations.SequenceLint{}).Lint(seqFS("000001_a", "000001_a.sql", "000002_b.sql"))
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "renumber yours after theirs")
}
