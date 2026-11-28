package config

import (
	"os"
	"strings"
	"testing"
)

// TestMain runs the tests without the SLOP_REVIEW_* settings of whoever
// runs them: a machine with, say, SLOP_REVIEW_FALLBACK_PROVIDER exported
// would otherwise hand every test a fallback it never asked for.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "SLOP_REVIEW_") {
			_ = os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}
