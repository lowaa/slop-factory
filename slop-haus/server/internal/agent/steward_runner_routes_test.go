package agent_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var runnerRoutes = []struct{ method, path string }{
	{http.MethodGet, stewardPath + "/briefing"},
	{http.MethodGet, stewardPath + "/events"},
	{http.MethodPost, stewardPath + "/lease"},
	{http.MethodDelete, stewardPath + "/lease"},
	{http.MethodGet, stewardPath + "/agreement"},
}

func TestStewardRunnerRoutesAreForSupervisorsOnly(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	console := a.conductor(t, "not-a-supervisor")
	sup := a.supervisor(t, "box", 1)
	for _, r := range runnerRoutes {
		assert.Equal(t, http.StatusUnauthorized, a.do(r.method, r.path, "", ``).Code, r.path)
		assert.Equal(t, http.StatusForbidden, a.do(r.method, r.path, a.token(t, console, "agent"), ``).Code, r.path)
		assert.Equal(t, http.StatusForbidden, a.do(r.method, r.path, a.token(t, sup, "read"), ``).Code, r.path+" read-only")
	}
}

func TestStewardBriefingNamesTheTenantsSteward(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	rec := a.do(http.MethodGet, stewardPath+"/briefing", a.token(t, sup, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode[struct{ Text string }](t, rec)
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)
	assert.Contains(t, body.Text, "# slop-haus steward: "+steward.Name)
}

// The pinned fleet and steward instructions reach the prompt the steward
// runs under, in that order after the protocol.
func TestStewardBriefingCarriesThePinnedInstructions(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	a.fleet.pinned = map[fleetinstructions.Kind]*fleetinstructions.Version{
		fleetinstructions.KindFleet:   {Name: "house", Version: 3, Body: "Open draft PRs."},
		fleetinstructions.KindSteward: {Name: "judgment", Version: 2, Body: "Be brief."},
	}
	rec := a.do(http.MethodGet, stewardPath+"/briefing", a.token(t, sup, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	text := decode[struct{ Text string }](t, rec).Text

	fleet, steward := strings.Index(text, `("house", version 3)`), strings.Index(text, `("judgment", version 2)`)
	require.True(t, fleet > 0 && fleet < steward, "fleet, then steward instructions")
	assert.Contains(t, text, "> Open draft PRs.\n")
	assert.Contains(t, text, "> Be brief.\n")

	a.fleet.pinned = nil
	rec = a.do(http.MethodGet, stewardPath+"/briefing", a.token(t, sup, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code)
	text = decode[struct{ Text string }](t, rec).Text
	assert.NotContains(t, text, "## Fleet instructions")
	assert.NotContains(t, text, "## Steward instructions")
}

type eventsJSON struct {
	Events []struct {
		Kind     string `json:"kind"`
		TicketID int64  `json:"ticket_id"`
	} `json:"events"`
	Cursor int64 `json:"cursor"`
}

func TestStewardEventsStartFromNowAndAreNotRepeated(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	bearer := a.token(t, sup, "agent")

	_, err := a.tickets.Create(a.ctx, &ticket.CreateParams{Title: "before", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	_, err = a.database.Exec(a.ctx, `UPDATE tickets SET status = 'discussion'`)
	require.NoError(t, err)

	rec := a.do(http.MethodGet, stewardPath+"/events", bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	first := decode[eventsJSON](t, rec)
	assert.Empty(t, first.Events, "without a cursor, history is not replayed")

	late, err := a.tickets.Create(a.ctx, &ticket.CreateParams{Title: "after", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	_, err = a.database.Exec(a.ctx, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, late.ID)
	require.NoError(t, err)

	rec = a.do(http.MethodGet, stewardPath+"/events?after="+strconv.FormatInt(first.Cursor, 10), bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	second := decode[eventsJSON](t, rec)
	require.Len(t, second.Events, 1)
	assert.Equal(t, "discussion", second.Events[0].Kind)
	assert.Equal(t, late.ID, second.Events[0].TicketID)

	rec = a.do(http.MethodGet, stewardPath+"/events?after="+strconv.FormatInt(second.Cursor, 10), bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decode[eventsJSON](t, rec).Events)
}

func TestStewardLeaseIsHeldByOneSupervisorAtATime(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	first, second := a.supervisor(t, "box-1", 1), a.supervisor(t, "box-2", 1)
	t1, t2 := a.token(t, first, "agent"), a.token(t, second, "agent")
	type lease struct {
		Acquired bool `json:"acquired"`
	}
	acquire := func(bearer string) bool {
		rec := a.do(http.MethodPost, stewardPath+"/lease", bearer, ``)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decode[lease](t, rec).Acquired
	}
	assert.True(t, acquire(t1))
	assert.False(t, acquire(t2))
	assert.Equal(t, http.StatusNoContent, a.do(http.MethodDelete, stewardPath+"/lease", t2, ``).Code)
	assert.False(t, acquire(t2), "the other supervisor's release does not free it")
	assert.Equal(t, http.StatusNoContent, a.do(http.MethodDelete, stewardPath+"/lease", t1, ``).Code)
	assert.True(t, acquire(t2))
}

func TestStewardAgreementIsEmptyBeforeAnyAssessment(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	rec := a.do(http.MethodGet, stewardPath+"/agreement", a.token(t, sup, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decode[struct{ Text string }](t, rec).Text)
}
