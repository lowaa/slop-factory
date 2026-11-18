package ticket_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// gapCheck answers with whatever the test set.
type gapCheck struct{ gap string }

func (g *gapCheck) SetupGap(context.Context) string { return g.gap }

func (f *fixture) withSetupCheck(c ticket.SetupCheck) {
	opts := append([]ticket.ServiceOption{ticket.WithSetupCheck(c)}, f.baseOpts...)
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler, opts...)
}

func TestClaim_RefusedWhileTheTenantHasNoSetup_AndTheTicketStaysQueued(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	check := &gapCheck{gap: "no repo declared"}
	f.withSetupCheck(check)
	a := f.agent(t, "builder-1")
	tk := f.ticket(t, "fix it", "a/")

	result, err := f.svc.Claim(f.ctx, a, nil)

	require.ErrorIs(t, err, ticket.ErrSetupIncomplete)
	assert.Nil(t, result)
	assert.EqualError(t, err, "no repo declared", "the agent is told what is missing")

	check.gap = ""
	got := f.claim(t, a)
	require.NotNil(t, got, "the ticket was never taken, so it is claimable once the tenant is set up")
	assert.Equal(t, tk.ID, got.ID)
}

func TestClaimNext_SetupIncomplete_Returns409WithTheGap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withSetupCheck(&gapCheck{gap: "no repo declared"})

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimReq(f.agent(t, "builder-1"), `{}`))

	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "no repo declared")
}
