package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// The add-attempt endpoint is a live API, so its wiring is under test: only
// a signed-in member reaches it, never an agent's token, and the server
// setting decides whether it exists at all.
func TestServe_AddAttemptIsAMembersAndBehindTheSetting(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			b := boot(t, &bootOptions{attemptsAddOff: !enabled})
			member, memberToken := b.signedInUser(t)
			agentToken := b.agentTokenFor(t, member, &oauth.Decision{Approve: true, AgentName: "adder"})

			created := do(t, &request{method: http.MethodPost, url: b.base + "/api/tickets", token: memberToken,
				body: `{"title": "Several attempts", "paths": [{"path": "a/"}]}`})
			require.Equal(t, http.StatusCreated, created.code, created.body)
			var tk struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal([]byte(created.body), &tk))
			add := func(token string) *response {
				return do(t, &request{method: http.MethodPost, token: token, body: `{}`,
					url: fmt.Sprintf("%s/api/tickets/%d/attempts", b.base, tk.ID)})
			}
			attempts := func() int {
				var n int
				require.NoError(t, b.appDB.QueryRow(db.WithTenant(context.Background(), member.TenantID),
					`SELECT count(*) FROM attempts WHERE ticket_id = $1`, tk.ID).Scan(&n))
				return n
			}

			assert.Equal(t, http.StatusUnauthorized, add("").code, "no member")
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, add(agentToken).code, "an agent")
			require.Equal(t, 1, attempts(), "neither added one")

			resp := add(memberToken)
			if !enabled {
				assert.Equal(t, http.StatusNotFound, resp.code, resp.body)
				assert.JSONEq(t, `{"code":"feature_disabled"}`, onlyCode(t, resp.body))
				assert.Equal(t, 1, attempts(), "off inserts nothing")
				return
			}
			assert.Equal(t, http.StatusCreated, resp.code, resp.body)
			assert.Equal(t, 2, attempts())
		})
	}
}
