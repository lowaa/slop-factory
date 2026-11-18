package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// Fleet instructions end in every agent's prompt, so the wiring is what is
// under test: the routes are the owners' alone, an agent's token never opens
// them, and a saved default reaches the scripts the same server renders.
func TestServe_FleetInstructionsAreOwnersAloneAndReachTheScripts(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	owner, ownerToken := b.signedInUser(t)

	member := &authn.User{ID: dbtest.NewUser(t, b.appDB), TenantID: owner.TenantID}
	for _, m := range []struct {
		user uuid.UUID
		role string
	}{{owner.ID, "owner"}, {member.ID, "member"}} {
		_, err := b.appDB.Global().Exec(t.Context(),
			`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
			uuid.New(), owner.TenantID, m.user, m.role)
		require.NoError(t, err)
	}
	memberToken, err := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL).
		IssueAccessToken(member.ID, member.TenantID)
	require.NoError(t, err)
	agentToken := b.agentToken(t)

	url := b.base + "/api/fleet_instructions"
	save := `{"name":"house","body":"Open draft PRs.","make_default":true}`

	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden},
		status(t, &request{method: http.MethodGet, url: url, token: agentToken}), "an agent cannot read them")
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden},
		status(t, &request{method: http.MethodPost, url: url + "/versions", body: save, token: agentToken}), "an agent cannot write them")
	assert.Equal(t, http.StatusForbidden,
		status(t, &request{method: http.MethodPost, url: url + "/versions", body: save, token: memberToken}), "a member cannot")

	resp := do(t, &request{method: http.MethodPost, url: url + "/versions", body: save, token: ownerToken})
	require.Equal(t, http.StatusCreated, resp.code, resp.body)
	var saved struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &saved))
	resp = do(t, &request{method: http.MethodGet, url: url, token: memberToken})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.Contains(t, resp.body, "Open draft PRs.")

	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden},
		status(t, &request{method: http.MethodGet, url: url + "/briefings", token: agentToken}), "an agent cannot read the preview")
	resp = do(t, &request{method: http.MethodGet, url: url + "/briefings?version_id=" + saved.ID, token: memberToken})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.Contains(t, resp.body, "> Open draft PRs.")

	resp = do(t, &request{method: http.MethodPost, url: b.base + "/api/agents/setup_script", body: `{"name":"builder-1"}`, token: ownerToken})
	require.Equal(t, http.StatusCreated, resp.code, resp.body)
	assert.Contains(t, resp.body, "## Fleet instructions")
	assert.Contains(t, resp.body, "> Open draft PRs.")
}
