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
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// What an agent in a variant runs on is the owners' to choose, so the wiring
// is under test: only an owner moves an agent, never an agent's token, and
// what the agent then reads from the same server -- its config, its fleet
// instructions, and whether its session is stale -- comes from the variant.
func TestServe_AgentVariantIsOwnersAloneAndDecidesWhatTheAgentRuns(t *testing.T) {
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
	agentToken := b.agentTokenFor(t, owner, &oauth.Decision{Approve: true, AgentName: "variant-agent"})

	call := func(method, path, token, body string) *response {
		t.Helper()
		return do(t, &request{method: method, url: b.base + path, token: token, body: body})
	}
	idOf := func(resp *response) string {
		t.Helper()
		require.Equal(t, http.StatusCreated, resp.code, resp.body)
		var v struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &v))
		return v.ID
	}

	resp := call(http.MethodGet, "/api/agents", ownerToken, "")
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	var listed struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &listed))
	require.Len(t, listed.Agents, 1)
	agentID := listed.Agents[0].ID

	// The tenant's defaults and an override, and fleet instructions: all
	// of which an agent in a variant must not see.
	require.Equal(t, http.StatusOK, call(http.MethodPut, "/api/harness_config", ownerToken, `{"settings":{"claude":{"model":"opus"}}}`).code)
	require.Equal(t, http.StatusOK, call(http.MethodPut, "/api/agents/"+agentID+"/harness_config", ownerToken, `{"settings":{"claude":{"effort":"high"}}}`).code)
	defaultFleet := idOf(call(http.MethodPost, "/api/fleet_instructions/versions", ownerToken, `{"name":"house","body":"Default text.","make_default":true}`))
	pinnedFleet := idOf(call(http.MethodPost, "/api/fleet_instructions/versions", ownerToken, `{"name":"lean","body":"Pinned text.","make_default":false}`))
	require.NotEmpty(t, defaultFleet)

	pinned := idOf(call(http.MethodPost, "/api/variants/versions", ownerToken,
		`{"name":"pi-fast","harness":"pi","settings":{"provider":"openrouter","model":"qwen/qwen3:free"},"fleet_instructions_version_id":"`+pinnedFleet+`"}`))
	unpinned := idOf(call(http.MethodPost, "/api/variants/versions", ownerToken,
		`{"name":"bare","harness":"claude","settings":{"model":"sonnet"},"fleet_instructions_version_id":null}`))

	agentConfig := func() *response { return call(http.MethodGet, "/api/workspace/harness_config", agentToken, "") }
	agentFleet := func() string {
		resp := call(http.MethodGet, "/api/workspace/update_script", agentToken, "")
		require.Equal(t, http.StatusOK, resp.code, resp.body)
		return resp.body
	}
	setVariant := func(token, version string) int {
		return call(http.MethodPut, "/api/agents/"+agentID+"/variant", token, `{"version_id":`+version+`}`).code
	}

	before := agentConfig()
	require.Equal(t, http.StatusOK, before.code, before.body)
	assert.Contains(t, before.body, `"model":"opus"`)
	assert.NotContains(t, before.body, `"harness"`, "an agent in no variant is told no harness")
	assert.Contains(t, agentFleet(), "> Default text.")

	body := `"` + pinned + `"`
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, setVariant(agentToken, body), "an agent cannot choose its own variant")
	assert.Equal(t, http.StatusForbidden, setVariant(memberToken, body), "a member cannot")
	assert.Contains(t, agentConfig().body, `"model":"opus"`, "refused attempts changed nothing")

	assert.Equal(t, http.StatusNoContent, setVariant(ownerToken, body))
	in := agentConfig()
	require.Equal(t, http.StatusOK, in.code, in.body)
	assert.Contains(t, in.body, `"harness":"pi"`)
	assert.Contains(t, in.body, `"model":"qwen/qwen3:free"`)
	assert.NotContains(t, in.body, "opus", "not the tenant defaults")
	assert.NotContains(t, in.body, "high", "not the agent's override")
	script := agentFleet()
	assert.Contains(t, script, "> Pinned text.")
	assert.NotContains(t, script, "Default text.")

	resp = call(http.MethodGet, "/api/agents", ownerToken, "")
	assert.Contains(t, resp.body, `"harness_override_inactive":true`)
	assert.Contains(t, resp.body, `"name":"pi-fast"`)

	assert.Equal(t, http.StatusNoContent, setVariant(ownerToken, `"`+unpinned+`"`))
	assert.NotContains(t, agentFleet(), "Fleet instructions", "a variant pinning none gives none, not the default")

	require.Less(t, call(http.MethodPost, "/api/variants/versions/"+pinned+"/archive", ownerToken, "").code, 300)
	assert.Equal(t, http.StatusUnprocessableEntity, setVariant(ownerToken, body), "an archived version cannot be newly assigned")

	assert.Equal(t, http.StatusNoContent, setVariant(ownerToken, "null"))
	out := agentConfig()
	assert.Contains(t, out.body, `"model":"opus"`)
	assert.Contains(t, out.body, `"effort":"high"`, "the override applies again")
	assert.Contains(t, agentFleet(), "> Default text.")
	resp = call(http.MethodGet, "/api/agents", ownerToken, "")
	assert.Contains(t, resp.body, `"harness_override_inactive":false`)

	assert.Equal(t, http.StatusNotFound, call(http.MethodPut, "/api/agents/"+uuid.NewString()+"/variant", ownerToken, `{"version_id":null}`).code)
	assert.Equal(t, http.StatusNotFound, setVariant(ownerToken, `"`+uuid.NewString()+`"`))
}
