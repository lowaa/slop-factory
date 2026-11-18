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

// The close-attempt endpoint is a live API, so its wiring is under test: only
// a signed-in member reaches it, never an agent's token, and the server
// setting decides whether it exists at all.
func TestServe_CloseAttemptIsAMembersAndBehindTheSetting(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			b := boot(t, &bootOptions{attemptsAddOff: !enabled})
			member, memberToken := b.signedInUser(t)
			agentToken := b.agentTokenFor(t, member, &oauth.Decision{Approve: true, AgentName: "closer"})

			created := do(t, &request{method: http.MethodPost, url: b.base + "/api/tickets", token: memberToken,
				body: `{"title": "Close one", "paths": [{"path": "a/"}]}`})
			require.Equal(t, http.StatusCreated, created.code, created.body)
			var tk struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal([]byte(created.body), &tk))
			tenantCtx := db.WithTenant(context.Background(), member.TenantID)
			if enabled {
				add := do(t, &request{method: http.MethodPost, token: memberToken, body: `{}`,
					url: fmt.Sprintf("%s/api/tickets/%d/attempts", b.base, tk.ID)})
				require.Equal(t, http.StatusCreated, add.code, add.body)
			} else {
				_, err := b.appDB.Exec(tenantCtx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`, tk.ID)
				require.NoError(t, err)
			}
			closeIt := func(token string) *response {
				return do(t, &request{method: http.MethodPost, token: token,
					url: fmt.Sprintf("%s/api/tickets/%d/attempts/2/close", b.base, tk.ID)})
			}
			status := func() string {
				var s string
				require.NoError(t, b.appDB.QueryRow(tenantCtx,
					`SELECT status FROM attempts WHERE ticket_id = $1 AND number = 2`, tk.ID).Scan(&s))
				return s
			}

			assert.Equal(t, http.StatusUnauthorized, closeIt("").code, "no member")
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, closeIt(agentToken).code, "an agent")
			require.Equal(t, "queued", status(), "neither closed it")

			resp := closeIt(memberToken)
			if !enabled {
				assert.Equal(t, http.StatusNotFound, resp.code, resp.body)
				assert.JSONEq(t, `{"code":"feature_disabled"}`, onlyCode(t, resp.body))
				assert.Equal(t, "queued", status(), "off closes nothing")
				return
			}
			assert.Equal(t, http.StatusOK, resp.code, resp.body)
			assert.Equal(t, "closed", status())
		})
	}
}
