package workspace_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Headless runs export SLOP_HAUS_HOST_SESSION and friends, which the hook
// scripts treat as "this is a hosted session, do nothing". Tests spawn those
// scripts with the inherited environment, so the process starts from a clean
// one and each test adds only what it means to exercise.
func TestMain(m *testing.M) {
	// Seeding canaries makes the scrub observable even where CI exports
	// nothing, so TestMain_ScrubbedAmbientRunEnv fails if the scrub is dropped.
	for _, name := range []string{"SLOP_HAUS_HOST_SESSION", "SLOP_HAUS_STOP_RETRY_FOR", "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "CLAUDE_CONFIG_DIR"} {
		_ = os.Setenv(name, "1")
	}
	scrubAmbientRunEnv()
	os.Exit(m.Run())
}

func isAmbientRunEnv(name string) bool {
	return strings.HasPrefix(name, "SLOP_HAUS_") ||
		strings.HasPrefix(name, "CLAUDE_CODE_") ||
		name == "CLAUDE_CONFIG_DIR"
}

func scrubAmbientRunEnv() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if isAmbientRunEnv(name) {
			_ = os.Unsetenv(name)
		}
	}
}

func TestScrubAmbientRunEnv_DropsRunVariablesOnly(t *testing.T) {
	t.Setenv("SLOP_HAUS_HOST_SESSION", "1")
	t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", "/x")
	t.Setenv("UNRELATED_KEEP", "1")

	scrubAmbientRunEnv()

	for _, name := range []string{"SLOP_HAUS_HOST_SESSION", "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "CLAUDE_CONFIG_DIR"} {
		_, ok := os.LookupEnv(name)
		assert.False(t, ok, "%s survived the scrub", name)
	}
	assert.Equal(t, "1", os.Getenv("UNRELATED_KEEP"), "scrub removed an unrelated variable")
}

// Pins that TestMain's scrub ran for the whole package.
func TestMain_ScrubbedAmbientRunEnv(t *testing.T) {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		assert.False(t, isAmbientRunEnv(name), "%s leaked into the test process", name)
	}
}
