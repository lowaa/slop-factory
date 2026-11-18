package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
)

type unmanagedJSON struct {
	Slots []struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Phase   string `json:"phase"`
		Offline bool   `json:"offline"`
	} `json:"slots"`
}

// reportedSlot makes a slot of sup, as `slop-haus slot add` does, and has it
// report phase.
func (p *provisioning) reportedSlot(t *testing.T, sup *agent.Agent, name string, phase supervisorwatch.Phase) *agent.Agent {
	t.Helper()
	slot := p.slot(t, sup, name)
	require.NoError(t, p.watch.ReportSlot(p.ctx, slot.ID, &supervisorwatch.SlotReport{Phase: phase}))
	return slot
}

func (p *provisioning) importPath() string { return p.path() + "/import" }

func (p *provisioning) importNames(revision int64, names string) *httptest.ResponseRecorder {
	return p.do(http.MethodPost, p.importPath(), "user", fmt.Sprintf(`{"revision":%d,"names":[%s]}`, revision, names))
}

func (p *provisioning) unmanaged(t *testing.T) unmanagedJSON {
	t.Helper()
	rec := p.do(http.MethodGet, "/api/supervisors/"+p.sup.ID.String()+"/unmanaged_slots", "user", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[unmanagedJSON](t, rec)
}

func TestUnmanagedSlotsAreTheSupervisorsReportedSlotsTheDocumentDoesNotName(t *testing.T) {
	p := newProvisioning(t)
	p.reportedSlot(t, p.sup, "idle-slot", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, p.sup, "paused-slot", supervisorwatch.PhasePaused)
	p.reportedSlot(t, p.sup, "managed-slot", supervisorwatch.PhaseIdle)
	other := p.supervisor(t, "other-box", 4)
	p.reportedSlot(t, other, "someone-elses", supervisorwatch.PhaseIdle)
	p.slot(t, p.sup, "never-reported")
	retired := p.reportedSlot(t, p.sup, "retired-slot", supervisorwatch.PhaseIdle)
	_, err := p.svc.RetireSlot(p.ctx, p.sup, retired.ID)
	require.NoError(t, err)
	p.mustPut(t, 0, entry("managed-slot", "running"))

	got := p.unmanaged(t)

	require.Len(t, got.Slots, 2)
	assert.Equal(t, "idle-slot", got.Slots[0].Name)
	assert.Equal(t, "slot", got.Slots[0].Kind)
	assert.Equal(t, "idle", got.Slots[0].Phase)
	assert.False(t, got.Slots[0].Offline)
	assert.Equal(t, "paused-slot", got.Slots[1].Name)
	assert.Equal(t, "paused", got.Slots[1].Phase)
}

func TestImportAddsEntriesMatchingTheReportAndChangesNothingOnTheHost(t *testing.T) {
	p := newProvisioning(t)
	p.reportedSlot(t, p.sup, "idle-slot", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, p.sup, "paused-slot", supervisorwatch.PhasePaused)

	rec := p.importNames(0, `"idle-slot","paused-slot"`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	doc := decode[desiredStateJSON](t, rec)
	assert.Equal(t, int64(1), doc.Revision)
	byName := map[string]desiredAgentJSON{}
	for _, a := range doc.Agents {
		byName[a.Name] = a
	}
	require.Len(t, byName, 2)
	assert.Equal(t, "running", byName["idle-slot"].State)
	assert.Equal(t, "paused", byName["paused-slot"].State)
	for _, a := range byName {
		assert.Equal(t, "slot", a.Kind)
		assert.Equal(t, "claude", a.Harness)
		assert.Equal(t, int64(1), a.Generation)
	}
	assert.Empty(t, p.unmanaged(t).Slots)
	assert.Len(t, p.logs.audit(t, "supervisor_desired_state_imported"), 1, "audited")
}

func TestImportOnAStaleRevisionIsRefusedAndAddsNothing(t *testing.T) {
	p := newProvisioning(t)
	p.reportedSlot(t, p.sup, "a", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, p.sup, "b", supervisorwatch.PhaseIdle)
	require.Equal(t, http.StatusOK, p.importNames(0, `"a"`).Code)

	assert.Equal(t, http.StatusConflict, p.importNames(0, `"b"`).Code)

	assert.Len(t, p.read(t).Agents, 1)
	assert.Equal(t, http.StatusOK, p.importNames(1, `"b"`).Code)
}

func TestImportRefusesNamesThatAreNotReportedAreHeldOrRepeated(t *testing.T) {
	p := newProvisioning(t)
	p.reportedSlot(t, p.sup, "a", supervisorwatch.PhaseIdle)
	other := p.supervisor(t, "other-box", 4)
	p.reportedSlot(t, other, "theirs", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, p.sup, "held", supervisorwatch.PhaseIdle)
	doc := p.mustPut(t, 0, entry("held", "running"))

	cases := map[string]struct {
		names string
		code  int
	}{
		"not reported at all":          {`"nope"`, http.StatusUnprocessableEntity},
		"another supervisor's slot":    {`"theirs"`, http.StatusUnprocessableEntity},
		"already in the document":      {`"held"`, http.StatusConflict},
		"twice":                        {`"a","a"`, http.StatusUnprocessableEntity},
		"one good name with a bad one": {`"a","nope"`, http.StatusUnprocessableEntity},
	}
	for name, c := range cases {
		assert.Equal(t, c.code, p.importNames(doc.Revision, c.names).Code, name)
	}
	assert.Len(t, p.read(t).Agents, 1, "a refused import writes nothing")
	assert.Equal(t, doc.Revision, p.read(t).Revision)
}

func TestOnlyTheTenantOwnerListsAndImports(t *testing.T) {
	p := newProvisioning(t)
	p.reportedSlot(t, p.sup, "a", supervisorwatch.PhaseIdle)
	listPath := "/api/supervisors/" + p.sup.ID.String() + "/unmanaged_slots"

	for who, bearer := range map[string]string{"the supervisor": p.bearer, "no credential": ""} {
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, p.do(http.MethodGet, listPath, bearer, "").Code, who)
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden},
			p.do(http.MethodPost, p.importPath(), bearer, `{"revision":0,"names":["a"]}`).Code, who)
	}
	_, err := p.database.Global().Exec(context.Background(),
		`UPDATE global_tenant_memberships SET role = 'member' WHERE user_id = $1`, p.user.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, p.do(http.MethodGet, listPath, "user", "").Code)
	assert.Equal(t, http.StatusForbidden, p.importNames(0, `"a"`).Code)
	assert.Empty(t, p.read(t).Agents)
}

func TestImportAcrossTenantsIsNotFound(t *testing.T) {
	p := newProvisioning(t)
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, p.database))
	foreign := p.supervisorIn(t, otherCtx, "foreign-box", 4)
	path := "/api/supervisors/" + foreign.ID.String()

	assert.Equal(t, http.StatusNotFound, p.do(http.MethodGet, path+"/unmanaged_slots", "user", "").Code)
	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, path+"/desired_state/import", "user", `{"revision":0,"names":["a"]}`).Code)
}

func TestImportAllAdoptsEverySupervisorsUnmanagedSlotsWithOneRevisionBumpEach(t *testing.T) {
	p := newProvisioning(t)
	other := p.supervisor(t, "other-box", 4)
	idle := p.supervisor(t, "idle-box", 4)
	p.reportedSlot(t, p.sup, "a", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, p.sup, "b", supervisorwatch.PhasePaused)
	p.reportedSlot(t, p.sup, "managed", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, other, "c", supervisorwatch.PhaseRunning)
	p.mustPut(t, 0, entry("managed", "running"))
	foreignCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, p.database))
	foreign := p.supervisorIn(t, foreignCtx, "foreign-box", 4)
	foreignSlot, err := p.svc.CreateSlot(foreignCtx, foreign, "foreign-slot")
	require.NoError(t, err)
	require.NoError(t, p.watch.ReportSlot(foreignCtx, foreignSlot.ID, &supervisorwatch.SlotReport{Phase: supervisorwatch.PhaseIdle}))

	n, err := p.provision.ImportAll(p.ctx)

	require.NoError(t, err)
	assert.Equal(t, 3, n)
	mine := p.read(t)
	assert.Equal(t, int64(2), mine.Revision, "one bump for the two slots")
	assert.Len(t, mine.Agents, 3)
	states := map[string]string{}
	for _, a := range mine.Agents {
		states[a.Name] = a.State
	}
	assert.Equal(t, map[string]string{"managed": "running", "a": "running", "b": "paused"}, states)
	theirs, err := p.provision.OwnerView(p.ctx, p.user, other.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), theirs.Revision)
	assert.Len(t, theirs.Agents, 1)
	untouched, err := p.provision.OwnerView(p.ctx, p.user, idle.ID)
	require.NoError(t, err)
	assert.Zero(t, untouched.Revision, "a supervisor with nothing to import is not written")

	again, err := p.provision.ImportAll(p.ctx)
	require.NoError(t, err)
	assert.Zero(t, again, "a second run has nothing left")
	foreignDoc, err := p.provision.OwnerView(foreignCtx, p.user, foreign.ID)
	require.NoError(t, err)
	assert.Zero(t, foreignDoc.Revision, "the other tenant's supervisor is not this tenant's to import")
}
