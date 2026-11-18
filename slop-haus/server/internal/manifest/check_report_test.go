package manifest_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func TestNewReconcileReport_BoundsEveryList(t *testing.T) {
	var paths []string
	var held []manifest.Held
	for i := 0; i < 100; i++ {
		paths = append(paths, fmt.Sprintf("repo-%d", i))
		held = append(held, manifest.Held{Path: "p", Reason: "r"})
	}
	got := manifest.NewReconcileReport(manifest.ReconcileReport{
		StaleRemoved:         manifest.Bounded[string]{Count: 100, Entries: paths},
		KeptWithUnpushedWork: manifest.Bounded[manifest.Held]{Count: 100, Entries: held},
	})

	assert.Equal(t, 100, got.StaleRemoved.Count)
	assert.Equal(t, paths[:manifest.MaxReconcileEntries], got.StaleRemoved.Entries)
	assert.Len(t, got.KeptWithUnpushedWork.Entries, manifest.MaxReconcileEntries)
}

func TestNewReconcileReport_ClipsTextAndFixesCount(t *testing.T) {
	long := strings.Repeat("é", manifest.MaxReconcileText+10)
	got := manifest.NewReconcileReport(manifest.ReconcileReport{
		MoveConflict: manifest.Bounded[manifest.Held]{Count: 0, Entries: []manifest.Held{{Path: long, Reason: long}}},
	})

	assert.Equal(t, 1, got.MoveConflict.Count, "a count never reads lower than what is listed")
	assert.Len(t, []rune(got.MoveConflict.Entries[0].Path), manifest.MaxReconcileText)
	assert.Len(t, []rune(got.MoveConflict.Entries[0].Reason), manifest.MaxReconcileText)
}

func TestNewReconcileReport_ZeroValueIsEmptyNotNull(t *testing.T) {
	b, err := json.Marshal(manifest.NewReconcileReport(manifest.ReconcileReport{}))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"stale_removed":{"count":0,"entries":[]}`)
	assert.NotContains(t, string(b), "null")
}

func TestNewReconcileReport_KeepsEveryFieldSeparate(t *testing.T) {
	long := strings.Repeat("x", manifest.MaxReconcileText+5)
	clipped := strings.Repeat("x", manifest.MaxReconcileText)
	held := func(p string) manifest.Bounded[manifest.Held] {
		return manifest.Bounded[manifest.Held]{Count: 1, Entries: []manifest.Held{{Path: p, Reason: "why"}}}
	}
	got := manifest.NewReconcileReport(manifest.ReconcileReport{
		StaleRemoved:          manifest.Bounded[string]{Count: 1, Entries: []string{"a"}},
		Moved:                 manifest.Bounded[manifest.Moved]{Count: 1, Entries: []manifest.Moved{{From: long, To: "b"}}},
		MovedWithUnpushedWork: held("c"),
		KeptWithUnpushedWork:  held("d"),
		RemoteRepointed:       manifest.Bounded[string]{Count: 1, Entries: []string{long}},
		MoveConflict:          held("e"),
	})

	assert.Equal(t, []string{"a"}, got.StaleRemoved.Entries)
	assert.Equal(t, []manifest.Moved{{From: clipped, To: "b"}}, got.Moved.Entries)
	assert.Equal(t, "c", got.MovedWithUnpushedWork.Entries[0].Path)
	assert.Equal(t, "d", got.KeptWithUnpushedWork.Entries[0].Path)
	assert.Equal(t, []string{clipped}, got.RemoteRepointed.Entries)
	assert.Equal(t, "e", got.MoveConflict.Entries[0].Path)
}
