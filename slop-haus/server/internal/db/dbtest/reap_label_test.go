package dbtest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go"
)

func labelsFor(t *testing.T) map[string]string {
	t.Helper()
	req := &testcontainers.GenericContainerRequest{}
	assert.NoError(t, reapOption().Customize(req))
	return req.Labels
}

func TestReapOption_LabelsTheContainerInCIMode(t *testing.T) {
	t.Setenv(reapProjectEnv, "slophaus-dbtest-1234abcd-o0123456789ab")
	assert.Equal(t, "slophaus-dbtest-1234abcd-o0123456789ab", labelsFor(t)[reapComposeLabel])
}

func TestReapOption_AddsNoLabelWithoutAProject(t *testing.T) {
	t.Setenv(reapProjectEnv, "")
	assert.NotContains(t, labelsFor(t), reapComposeLabel)
}

// In CI mode (test.sh) the real container must carry the project, or a
// killed pipeline's Postgres is never reaped now that Ryuk is off.
func TestSharedContainer_CarriesTheReapProjectWhenOneIsSet(t *testing.T) {
	project := os.Getenv(reapProjectEnv)
	if project == "" {
		t.Skip(reapProjectEnv + " is set only in CI mode")
	}
	assert.Equal(t, project, containerLabels[reapComposeLabel])
}
