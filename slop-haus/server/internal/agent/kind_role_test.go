package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

func TestRoleFor(t *testing.T) {
	assert.Equal(t, workspace.RoleConductor, roleFor(KindConductor))
	assert.Equal(t, workspace.RoleConsole, roleFor(KindConsole))
	for _, k := range []Kind{KindWorker, KindSupervisor, KindSteward} {
		assert.Equal(t, workspace.RoleHeadless, roleFor(k), string(k))
	}
}
