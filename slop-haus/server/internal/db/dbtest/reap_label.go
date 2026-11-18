package dbtest

import (
	"os"

	"github.com/testcontainers/testcontainers-go"
)

const (
	// The label dev-tools/reap/reap_lib.sh (REAP_COMPOSE_LABEL) removes
	// abandoned runs' resources by.
	reapComposeLabel = "com.docker.compose.project"
	// Set by test.sh in a CI step, where it also disables Ryuk.
	reapProjectEnv = "DBTEST_REAP_PROJECT"
)

// reapOption labels the container with the run's reap project when test.sh
// named one, and is a no-op otherwise (Ryuk reaps the container instead).
func reapOption() testcontainers.CustomizeRequestOption {
	labels := map[string]string{}
	if project := os.Getenv(reapProjectEnv); project != "" {
		labels[reapComposeLabel] = project
	}
	return testcontainers.WithLabels(labels)
}
