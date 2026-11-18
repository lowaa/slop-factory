package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// The credential module lives in token/ and is shipped as three byte-identical files: the source, the
// copy embedded here, and the supervisor's vendored copy. The supervisor's own suite checks the same
// three; this one keeps a server-only change from passing. Refresh with token/vendor.sh.
func TestTokenModuleCopiesAreIdentical(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "token", "slop_haus_token.py"))
	require.NoError(t, err)
	vendored, err := os.ReadFile(filepath.Join("..", "..", "..", "supervisor", "src", "slop_haus_supervisor", "slophaus", "slop_haus_token.py"))
	require.NoError(t, err)

	files, err := workspace.Files(workspace.Params{})
	require.NoError(t, err)
	var delivered []byte
	for _, f := range files {
		if f.Path == workspace.TokenModule {
			delivered = f.Body
		}
	}
	require.NotEmpty(t, delivered, "Files does not deliver the module")

	assert.Equal(t, string(source), string(delivered), "delivered copy differs from token/slop_haus_token.py; run token/vendor.sh")
	assert.Equal(t, string(source), string(vendored), "supervisor copy differs from token/slop_haus_token.py; run token/vendor.sh")
}
