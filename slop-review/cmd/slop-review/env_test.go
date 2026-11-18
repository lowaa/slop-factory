package main

import (
	"os"
	"strings"
	"testing"
)

// TestMain runs the tests without the SLOP_REVIEW_* settings of whoever
// runs them: an agent's environment carries a real report URL and ingest
// token, and a test that doesn't set its own would post its fixture runs to
// the real server. The SLOP_REVIEW_E2E_* gates for the live tests are kept.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "SLOP_REVIEW_") && !strings.HasPrefix(name, "SLOP_REVIEW_E2E_") {
			_ = os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}
