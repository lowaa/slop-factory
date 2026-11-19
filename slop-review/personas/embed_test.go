package personas

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The embedded copy is what a bundled binary reviews with, so it must be
// the files in this directory, not a stale snapshot.
func TestFS_EmbedsEveryPersonaFileInTheDirectory(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var found int
	for _, e := range entries {
		if e.IsDir() || e.Name()[len(e.Name())-3:] != ".md" {
			continue
		}
		found++
		want, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		got, err := FS.ReadFile(e.Name())
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got))
	}
	assert.NotZero(t, found)
}
