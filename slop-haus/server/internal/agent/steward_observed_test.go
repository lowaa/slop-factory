package agent_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (p *provisioning) mintSteward(t *testing.T) string {
	t.Helper()
	rec := p.do(http.MethodPut, stewardPath, p.bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[slotJSON](t, rec).ID
}

func (p *provisioning) reportSteward(id, phase, reason string) int {
	return p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer,
		fmt.Sprintf(`{"agents":[{"id":%q,"phase":%q,"reason":%q}]}`, id, phase, reason)).Code
}

func (p *provisioning) stewardReport(t *testing.T, id string) (agent.ObservedSteward, bool) {
	t.Helper()
	all, err := p.provision.StewardReports(p.ctx)
	require.NoError(t, err)
	got, ok := all[uuid.MustParse(id)]
	return got, ok
}

func TestTheSupervisorReportsTheStewardByAgentAlone(t *testing.T) {
	p := newProvisioning(t)
	id := p.mintSteward(t)
	_, ok := p.stewardReport(t, id)
	assert.False(t, ok, "nothing is reported before the supervisor says something")

	const reason = "sandbox probe refused `network`"
	require.Equal(t, http.StatusNoContent, p.reportSteward(id, "failed", reason))
	require.Equal(t, http.StatusNoContent, p.reportSteward(id, "failed", reason+" again"), "the latest report wins")
	got, ok := p.stewardReport(t, id)
	require.True(t, ok)
	assert.Equal(t, agent.PhaseFailed, got.Phase)
	assert.Equal(t, reason+" again", got.Reason)
	assert.False(t, got.ReportedAt.IsZero())

	require.Equal(t, http.StatusNoContent, p.reportSteward(id, "running", ""))
	got, _ = p.stewardReport(t, id)
	assert.Equal(t, agent.PhaseRunning, got.Phase)
	assert.Empty(t, got.Reason)
}

func TestAStewardReportWorksWhileProvisionFromBoardIsOff(t *testing.T) {
	p := newProvisioning(t)
	id := p.mintSteward(t)
	// the board's setting gates the document, not the steward's status
	_, err := p.settings.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(false), p.user.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, p.reportSteward(id, "failed", "x"))
}

func TestAStewardReportCannotRetireOrNameAnotherAgent(t *testing.T) {
	p := newProvisioning(t)
	id := p.mintSteward(t)
	assert.Equal(t, http.StatusUnprocessableEntity, p.reportSteward(id, "retired", ""),
		"retired is for a removed document entry; the steward is not in the document")
	assert.Equal(t, http.StatusUnprocessableEntity, p.reportSteward(p.sup.ID.String(), "failed", "x"),
		"only the steward is keyed by the agent alone")
}

func TestAStewardReportIsRefusedWithoutASupervisorCredential(t *testing.T) {
	p := newProvisioning(t)
	id := p.mintSteward(t)
	rec := p.do(http.MethodPost, "/api/supervisor/desired_state/observed", "user",
		fmt.Sprintf(`{"agents":[{"id":%q,"phase":"failed"}]}`, id))
	assert.GreaterOrEqual(t, rec.Code, 400)
}

func TestAStewardReportIsStoredWithItsBatchOrNotAtAll(t *testing.T) {
	p := newProvisioning(t)
	id := p.mintSteward(t)
	rec := p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer, fmt.Sprintf(
		`{"agents":[{"id":%q,"phase":"failed","reason":"x"},{"id":%q,"generation":1,"phase":"running"}]}`, id, uuid.NewString()))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	_, ok := p.stewardReport(t, id)
	assert.False(t, ok, "the steward's report of a rejected batch was not stored")
}

func TestADocumentEntrysReportNeedsAGeneration(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	rec := p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer,
		fmt.Sprintf(`{"agents":[{"id":%q,"phase":"running"}]}`, doc.Agents[0].ID))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Empty(t, p.read(t).Agents[0].Observed.Phase)
}
