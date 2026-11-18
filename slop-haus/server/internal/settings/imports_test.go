package settings_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The packages that own a setting register it with settings; settings must
// not depend on them.
func TestSettingsDoesNotImportSteward(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, string(out))
	for _, dep := range strings.Fields(string(out)) {
		require.NotContains(t, dep, "/internal/steward", "settings must not depend on the steward")
	}
}
