package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// The supervisor's routes on the booted api: the claim response carries the
// attempt, the state reads the lease that claim made, and a poll is
// recorded. The wiring is what this proves; the rules are the packages' own.
func TestServe_AWorkerSupervisorClaimsReadsItsStateAndPolls(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, userToken := b.signedInUser(t)
	token := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "headless-1"})
	ctx := db.WithTenant(context.Background(), who.TenantID)

	created := do(t, &request{method: http.MethodPost, url: b.base + "/api/tickets", token: userToken, body: `{"title":"Headless","paths":[{"path":"server/"}]}`})
	require.Equal(t, http.StatusCreated, created.code, created.body)
	var filed struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(created.body), &filed))

	idle := do(t, &request{method: http.MethodGet, url: b.base + "/api/worker/state", token: token})
	require.Equal(t, http.StatusOK, idle.code, idle.body)
	assert.Contains(t, idle.body, `"lease":null`)

	claim := do(t, &request{method: http.MethodPost, url: b.base + "/api/claims", token: token, body: `{}`})
	require.Equal(t, http.StatusOK, claim.code, claim.body)
	var claimed struct {
		Attempt struct {
			Number int    `json:"number"`
			Branch string `json:"branch"`
		} `json:"attempt"`
	}
	require.NoError(t, json.Unmarshal([]byte(claim.body), &claimed))
	assert.Equal(t, 1, claimed.Attempt.Number)
	assert.NotEmpty(t, claimed.Attempt.Branch)

	state := do(t, &request{method: http.MethodGet, url: b.base + "/api/worker/state", token: token})
	require.Equal(t, http.StatusOK, state.code, state.body)
	var read struct {
		Lease struct {
			TicketID int64 `json:"ticket_id"`
		} `json:"lease"`
		Ticket struct {
			Status string `json:"status"`
		} `json:"ticket"`
		Checks *struct{} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(state.body), &read))
	assert.Equal(t, filed.ID, read.Lease.TicketID)
	assert.Equal(t, "in_progress", read.Ticket.Status)
	assert.Nil(t, read.Checks, "no change is open")

	polled := do(t, &request{method: http.MethodPost, url: b.base + "/api/worker/poll", token: token, body: `{"version":"v1","wait_seconds":0}`})
	require.Equal(t, http.StatusOK, polled.code, polled.body)
	var version string
	require.NoError(t, b.appDB.QueryRow(ctx, `SELECT version FROM worker_polls`).Scan(&version))
	assert.Equal(t, "v1", version, "the poll is the supervisor's liveness")
}

// A slot's report reaches the status the board reads, over the booted api.
func TestServe_AWorkerReportsItsSlotAndTheStatusShowsIt(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, userToken := b.signedInUser(t)
	token := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "headless-1"})

	bad := do(t, &request{method: http.MethodPost, url: b.base + "/api/worker/slot-state", token: token, body: `{"phase":"sleeping","phase_age_seconds":1}`})
	assert.Equal(t, http.StatusUnprocessableEntity, bad.code, bad.body)

	reported := do(t, &request{method: http.MethodPost, url: b.base + "/api/worker/slot-state", token: token,
		body: `{"phase":"backoff","phase_age_seconds":30,"error_class":"claim_failed","error_streak":9}`})
	require.Equal(t, http.StatusNoContent, reported.code, reported.body)

	status := do(t, &request{method: http.MethodGet, url: b.base + "/api/supervisor/status", token: userToken})
	require.Equal(t, http.StatusOK, status.code, status.body)
	var body struct {
		Slots []struct {
			Phase       string `json:"phase"`
			Stalled     bool   `json:"stalled"`
			StallReason string `json:"stall_reason"`
		} `json:"slots"`
	}
	require.NoError(t, json.Unmarshal([]byte(status.body), &body))
	require.Len(t, body.Slots, 1)
	assert.Equal(t, "backoff", body.Slots[0].Phase)
	assert.True(t, body.Slots[0].Stalled)
	assert.Equal(t, "repeating_error", body.Slots[0].StallReason)
}
