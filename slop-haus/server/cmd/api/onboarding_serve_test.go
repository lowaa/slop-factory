package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// A tenant that has declared no repo and connected no code host cannot have a
// worker claim: it would be handed a ticket with nothing to clone. Declaring
// a repo lifts it, and the board's checklist reads the same state.
func TestServe_ANewTenantsClaimIsRefusedUntilItDeclaresARepo(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{setupClaimGate: true})
	who, userToken := b.signedInUser(t)
	agent := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "worker"})
	claim := &request{method: http.MethodPost, url: b.base + "/api/claims", body: `{}`, token: agent}
	status := &request{method: http.MethodGet, url: b.base + "/api/onboarding", token: userToken}

	refused := do(t, claim)
	assert.Equal(t, http.StatusConflict, refused.code, refused.body)
	assert.Contains(t, refused.body, "no repo in its workspace manifest")

	board := do(t, status)
	require.Equal(t, http.StatusOK, board.code, board.body)
	assert.Contains(t, board.body, `"code_work_ready":false`)

	_, err := b.appDB.Exec(db.WithTenant(t.Context(), who.TenantID),
		`INSERT INTO workspace_manifests (repos, updated_by) VALUES ('[{"path":"app","remote":"github.com/acme/app"}]', $1)`, who.ID)
	require.NoError(t, err)

	claimed := do(t, claim)
	assert.Equal(t, http.StatusOK, claimed.code, claimed.body)
	board = do(t, status)
	assert.Contains(t, board.body, `"code_work_ready":true`)
}
