package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

type desiredAgentJSON struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Harness          string `json:"harness"`
	State            string `json:"state"`
	Launch           string `json:"launch"`
	Generation       int64  `json:"generation"`
	ResumeGeneration int64  `json:"resume_generation"`
	Observed         struct {
		Generation int64  `json:"generation"`
		Phase      string `json:"phase"`
		Reason     string `json:"reason"`
		Harness    string `json:"harness"`
	} `json:"observed"`
}

type desiredStateJSON struct {
	Revision int64              `json:"revision"`
	Agents   []desiredAgentJSON `json:"agents"`

	ProvisionFromBoard bool `json:"provision_from_board"`
	WorkspaceReconcile bool `json:"workspace_reconcile"`
}

// provisioning is a supervisor with a token and an owner session, the board's
// setting on.
type provisioning struct {
	*supervisorAPI
	sup    *agent.Agent
	bearer string
}

func newProvisioning(t *testing.T) *provisioning {
	t.Helper()
	a := newSupervisorAPI(t, 100)
	_, err := a.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES (gen_random_uuid(), $1, $2, 'owner')`, a.tenant, a.user.ID)
	require.NoError(t, err)
	_, err = a.settings.Set(a.ctx, settings.ProvisionFromBoard, settings.BoolValue(true), a.user.ID)
	require.NoError(t, err)
	sup := a.supervisor(t, "box", 8)
	return &provisioning{supervisorAPI: a, sup: sup, bearer: a.token(t, sup, "agent")}
}

func (p *provisioning) path() string {
	return "/api/supervisors/" + p.sup.ID.String() + "/desired_state"
}

// put writes the document as the owner.
func (p *provisioning) put(revision int64, agents string) (int, desiredStateJSON) {
	rec := p.do(http.MethodPut, p.path(), "user", fmt.Sprintf(`{"revision":%d,"agents":[%s]}`, revision, agents))
	var out desiredStateJSON
	if rec.Code == http.StatusOK {
		out = decode[desiredStateJSON](&testing.T{}, rec)
	}
	return rec.Code, out
}

func entry(name, state string) string {
	return fmt.Sprintf(`{"kind":"slot","name":%q,"state":%q,"resume_generation":0}`, name, state)
}

func (p *provisioning) mustPut(t *testing.T, revision int64, agents string) desiredStateJSON {
	t.Helper()
	rec := p.do(http.MethodPut, p.path(), "user", fmt.Sprintf(`{"revision":%d,"agents":[%s]}`, revision, agents))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[desiredStateJSON](t, rec)
}

func (p *provisioning) read(t *testing.T) desiredStateJSON {
	t.Helper()
	rec := p.do(http.MethodGet, "/api/supervisor/desired_state", p.bearer, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[desiredStateJSON](t, rec)
}

func (p *provisioning) report(id string, generation int64, phase string) int {
	return p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer,
		fmt.Sprintf(`{"agents":[{"id":%q,"generation":%d,"phase":%q}]}`, id, generation, phase)).Code
}

// echo is the document as a client sends it back: every entry as read.
func echo(doc desiredStateJSON, change func(*desiredAgentJSON)) string {
	out := ""
	for i := range doc.Agents {
		a := doc.Agents[i]
		if change != nil {
			change(&a)
		}
		if out != "" {
			out += ","
		}
		out += fmt.Sprintf(`{"id":%q,"kind":%q,"name":%q,"harness":%q,"state":%q,"resume_generation":%d}`,
			a.ID, a.Kind, a.Name, a.Harness, a.State, a.ResumeGeneration)
	}
	return out
}

func TestTheSupervisorReadsAnEmptyDocumentWhileProvisionFromBoardIsOff(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, entry("slot-1", "running"))
	on := p.read(t)
	require.Len(t, on.Agents, 1)
	assert.True(t, on.ProvisionFromBoard)

	_, err := p.settings.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(false), p.user.ID)
	require.NoError(t, err)

	doc := p.read(t)
	assert.Empty(t, doc.Agents)
	assert.Zero(t, doc.Revision)
	assert.False(t, doc.ProvisionFromBoard)
}

func TestTheSupervisorIsToldProvisionFromBoardIsOnEvenWithNoAgentsYet(t *testing.T) {
	p := newProvisioning(t)
	doc := p.read(t)
	assert.Empty(t, doc.Agents)
	assert.True(t, doc.ProvisionFromBoard)
}

func TestProvisionFromBoardIsOffByDefault(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	on, err := a.settings.ProvisionFromBoardEnabled(a.ctx)
	require.NoError(t, err)
	assert.False(t, on)
}

func TestAWriteOnAStaleRevisionIsRefusedAndChangesNothing(t *testing.T) {
	p := newProvisioning(t)
	first := p.mustPut(t, 0, entry("slot-1", "running"))
	assert.Equal(t, int64(1), first.Revision)
	require.Len(t, first.Agents, 1)
	assert.Equal(t, int64(1), first.Agents[0].Generation)

	code, _ := p.put(0, entry("slot-1", "running")+","+entry("slot-2", "running"))

	assert.Equal(t, http.StatusConflict, code, "a retry of the committed write fails the second time")
	assert.Len(t, p.read(t).Agents, 1)
	code, _ = p.put(5, echo(first, nil))
	assert.Equal(t, http.StatusConflict, code, "a revision from the future is no better")
}

func TestTwoWritersOnTheSameRevisionOneWins(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))

	paused := func(a *desiredAgentJSON) { a.State = "paused" }
	code1, _ := p.put(doc.Revision, echo(doc, paused))
	code2, _ := p.put(doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.Harness = "pi" }))

	assert.Equal(t, http.StatusOK, code1)
	assert.Equal(t, http.StatusConflict, code2)
	got := p.read(t)
	assert.Equal(t, "paused", got.Agents[0].State)
	assert.Equal(t, "claude", got.Agents[0].Harness)
	assert.Equal(t, int64(2), got.Agents[0].Generation, "the generation moved with the revision")
}

func TestGenerationMovesOnlyForTheEntriesThatChanged(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running")+","+entry("slot-2", "running"))

	next := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) {
		if a.Name == "slot-2" {
			a.ResumeGeneration = 1
		}
	}))

	byName := map[string]desiredAgentJSON{}
	for _, a := range next.Agents {
		byName[a.Name] = a
	}
	assert.Equal(t, int64(1), byName["slot-1"].Generation)
	assert.Equal(t, int64(2), byName["slot-2"].Generation)
	assert.Equal(t, int64(1), byName["slot-2"].ResumeGeneration)
}

func TestOnlyTheTenantOwnerEditsTheDocumentAndNeverAnAgent(t *testing.T) {
	p := newProvisioning(t)
	worker, err := p.svc.Create(p.ctx, "plain-worker")
	require.NoError(t, err)
	body := fmt.Sprintf(`{"revision":0,"agents":[%s]}`, entry("slot-1", "running"))

	for who, bearer := range map[string]string{
		"the supervisor itself": p.bearer,
		"a worker":              p.token(t, worker, "agent"),
		"no credential":         "",
	} {
		for _, method := range []string{http.MethodPut, http.MethodGet} {
			rec := p.do(method, p.path(), bearer, body)
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, who+" "+method+" "+rec.Body.String())
		}
	}

	_, err = p.database.Global().Exec(context.Background(),
		`UPDATE global_tenant_memberships SET role = 'member' WHERE user_id = $1`, p.user.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, p.do(http.MethodPut, p.path(), "user", body).Code, "a member is not the owner")
	assert.Equal(t, http.StatusForbidden, p.do(http.MethodGet, p.path(), "user", "").Code)
	assert.Empty(t, p.read(t).Agents, "nothing was written")
}

func TestASupervisorReadsAndReportsOnlyItsOwnDocument(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, entry("mine", "running"))
	other := p.supervisor(t, "other-box", 2)
	otherBearer := p.token(t, other, "agent")
	rec := p.do(http.MethodPut, "/api/supervisors/"+other.ID.String()+"/desired_state", "user",
		fmt.Sprintf(`{"revision":0,"agents":[%s]}`, entry("theirs", "running")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	theirs := decode[desiredStateJSON](t, rec).Agents[0]
	mine := p.read(t).Agents[0]

	rec = p.do(http.MethodGet, "/api/supervisor/desired_state", otherBearer, "")
	got := decode[desiredStateJSON](t, rec)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "theirs", got.Agents[0].Name)

	assert.Equal(t, http.StatusUnprocessableEntity, p.report(theirs.ID, 1, "running"), "cannot report on another supervisor's agent")
	assert.Equal(t, http.StatusUnprocessableEntity, p.report(uuid.NewString(), 1, "running"))
	assert.Equal(t, http.StatusNoContent, p.report(mine.ID, 1, "running"))
	assert.Equal(t, "running", p.read(t).Agents[0].Observed.Phase)
}

func TestAReportCannotSkipAheadOrRetireAnEntryThatIsNotRemoved(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	id := doc.Agents[0].ID

	assert.Equal(t, http.StatusUnprocessableEntity, p.report(id, 2, "running"), "a generation the entry has not reached")
	assert.Equal(t, http.StatusUnprocessableEntity, p.report(id, 1, "retired"), "retiring what the owner did not remove")
	assert.Equal(t, http.StatusUnprocessableEntity, p.report(id, 1, "exploded"))
	assert.Equal(t, http.StatusNoContent, p.report(id, 1, "provisioning"))
	assert.Equal(t, http.StatusNoContent, p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer,
		fmt.Sprintf(`{"agents":[{"id":%q,"generation":1,"phase":"failed","reason":"needs secret X on host"}]}`, id)).Code)
	got := p.read(t).Agents[0].Observed
	assert.Equal(t, "failed", got.Phase)
	assert.Equal(t, "needs secret X on host", got.Reason)
}

func TestAReportCarriesTheHarnessTheAgentRuns(t *testing.T) {
	p := newProvisioning(t)
	id := p.mustPut(t, 0, entry("slot-1", "running")).Agents[0].ID
	post := func(body string) int {
		return p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer, body).Code
	}

	assert.Empty(t, p.read(t).Agents[0].Observed.Harness)
	require.Equal(t, http.StatusNoContent, post(fmt.Sprintf(`{"agents":[{"id":%q,"generation":1,"phase":"running","harness":"pi"}]}`, id)))
	assert.Equal(t, "pi", p.read(t).Agents[0].Observed.Harness)
	require.Equal(t, http.StatusNoContent, post(fmt.Sprintf(`{"agents":[{"id":%q,"generation":1,"phase":"running"}]}`, id)))
	assert.Empty(t, p.read(t).Agents[0].Observed.Harness, "a report without one clears it")
	assert.Equal(t, http.StatusUnprocessableEntity, post(fmt.Sprintf(`{"agents":[{"id":%q,"generation":1,"phase":"running","harness":%q}]}`, id, strings.Repeat("x", 33))))
}

func TestAStaleOrRetiredReportDoesNotStoreItsHarness(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	id := doc.Agents[0].ID
	post := func(generation int64, phase, harness string) {
		t.Helper()
		body := fmt.Sprintf(`{"agents":[{"id":%q,"generation":%d,"phase":%q,"harness":%q}]}`, id, generation, phase, harness)
		require.Equal(t, http.StatusNoContent, p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer, body).Code)
	}
	doc = p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "paused" }))
	gen2 := doc.Agents[0].Generation
	require.Greater(t, gen2, int64(1))

	post(gen2, "paused", "pi")
	post(1, "running", "claude")
	assert.Equal(t, "pi", p.read(t).Agents[0].Observed.Harness, "a delayed report is ignored whole")

	doc = p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "removed" }))
	post(doc.Agents[0].Generation, "retired", "pi")
	var stored string
	require.NoError(t, p.database.QueryRow(p.ctx, `SELECT observed_harness FROM supervisor_desired_agents WHERE id = $1`, id).Scan(&stored))
	assert.Empty(t, stored, "a retired agent keeps no harness")
}

func TestRemovingASlotRetiresItAndRevokesItsGrantsButHoldsTheNameUntilReportedRetired(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	slot := p.slot(t, p.sup, "slot-1")
	p.grant(t, slot)
	require.Equal(t, 1, p.liveGrantCount(t, slot))

	removed := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "removed" }))

	got, err := p.svc.Get(p.ctx, slot.ID)
	require.NoError(t, err)
	assert.True(t, got.Disabled())
	assert.True(t, got.Archived())
	assert.Zero(t, p.liveGrantCount(t, slot), "its grants are revoked")
	require.Len(t, removed.Agents, 1, "the entry stays until the supervisor reports it retired")

	code, _ := p.put(removed.Revision, echo(removed, nil)+","+entry("slot-1", "running"))
	assert.Equal(t, http.StatusConflict, code, "the name is held while the old generation is not reported retired")

	require.Equal(t, http.StatusNoContent, p.report(removed.Agents[0].ID, removed.Agents[0].Generation, "retired"))
	assert.Empty(t, p.read(t).Agents)
	again := p.mustPut(t, removed.Revision, entry("slot-1", "running"))
	require.Len(t, again.Agents, 1)
	assert.NotEqual(t, removed.Agents[0].ID, again.Agents[0].ID, "a new agent, not the old one revived")
	assert.Equal(t, http.StatusNoContent, p.report(removed.Agents[0].ID, removed.Agents[0].Generation, "retired"), "a retried report is a no-op")
}

func TestRemovingAnAgentOfAnotherSupervisorOrKindLeavesItAlone(t *testing.T) {
	p := newProvisioning(t)
	other := p.supervisor(t, "other-box", 2)
	theirs := p.slot(t, other, "shared-name")
	worker, err := p.svc.Create(p.ctx, "plain-name")
	require.NoError(t, err)
	doc := p.mustPut(t, 0, entry("shared-name", "running")+","+entry("plain-name", "running"))

	p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "removed" }))

	for _, id := range []uuid.UUID{theirs.ID, worker.ID} {
		got, err := p.svc.Get(p.ctx, id)
		require.NoError(t, err)
		assert.False(t, got.Disabled(), "an entry's name does not reach an agent that is not its own")
	}
}

func TestEditsThatBreakTheDocumentsRulesAreRefused(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))

	cases := map[string]string{
		"a path as a name":                entry("../etc", "running"),
		"a slash in a name":               entry("a/b", "running"),
		"a dot name":                      entry("..", "running"),
		"a leading dash":                  entry("-rf", "running"),
		"a command as a name":             entry("a;rm -rf", "running"),
		"an empty name":                   entry("", "running"),
		"an unknown kind":                 `{"kind":"conductor","name":"x","state":"running","resume_generation":0}`,
		"an unknown state":                entry("x", "exploded"),
		"a harness that is a path":        `{"kind":"slot","name":"x","harness":"../x","state":"running","resume_generation":0}`,
		"a new agent that starts removed": entry("x", "removed"),
		"a new agent with a resume":       `{"kind":"slot","name":"x","state":"running","resume_generation":3}`,
		"the same name twice":             entry("x", "running") + "," + entry("x", "running"),
		"an entry left out":               entry("other", "running"),
		"a rename":                        echo(doc, func(a *desiredAgentJSON) { a.Name = "slot-9" }),
		"an unknown id":                   fmt.Sprintf(`{"id":%q,"kind":"slot","name":"x","state":"running","resume_generation":0}`, uuid.NewString()),
		"a resume going down":             echo(doc, func(a *desiredAgentJSON) { a.ResumeGeneration = -1 }),
	}
	existing := echo(doc, nil)
	valid, _ := p.put(doc.Revision, existing+","+entry("a-valid-addition", "running"))
	require.Equal(t, http.StatusOK, valid, "the control: the same shape of write with a valid addition is accepted")
	doc = p.read(t)
	existing = echo(doc, nil)
	for name, agents := range cases {
		if name != "an entry left out" && name != "a rename" && name != "an unknown id" && name != "a resume going down" {
			agents = existing + "," + agents
		}
		t.Run(name, func(t *testing.T) {
			code, _ := p.put(doc.Revision, agents)
			assert.Contains(t, []int{http.StatusUnprocessableEntity, http.StatusBadRequest}, code)
		})
	}
	assert.Equal(t, doc.Revision, p.read(t).Revision, "none of them bumped the revision")
}

func TestARemovedAgentStaysRemovedAndAResumeNeverGoesDown(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	up := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.ResumeGeneration = 2 }))

	code, _ := p.put(up.Revision, echo(up, func(a *desiredAgentJSON) { a.ResumeGeneration = 1 }))
	assert.Equal(t, http.StatusUnprocessableEntity, code)

	gone := p.mustPut(t, up.Revision, echo(up, func(a *desiredAgentJSON) { a.State = "removed" }))
	code, _ = p.put(gone.Revision, echo(gone, func(a *desiredAgentJSON) { a.State = "running" }))
	assert.Equal(t, http.StatusUnprocessableEntity, code)
}

func TestTheDocumentIsBoundedAndEditsAreAudited(t *testing.T) {
	p := newProvisioning(t)
	all := ""
	for i := range agent.MaxDesiredAgents + 1 {
		if all != "" {
			all += ","
		}
		all += entry("slot-"+strconv.Itoa(i), "running")
	}
	code, _ := p.put(0, all)
	assert.Contains(t, []int{http.StatusUnprocessableEntity, http.StatusBadRequest}, code)

	p.mustPut(t, 0, entry("slot-1", "running"))
	lines := p.logs.audit(t, "supervisor_desired_state_edited")
	require.Len(t, lines, 1)
	assert.Equal(t, p.user.ID.String(), lines[0]["user_id"])
	assert.Equal(t, p.sup.ID.String(), lines[0]["supervisor"])
}

func TestOnlyALiveSupervisorHasADocument(t *testing.T) {
	p := newProvisioning(t)
	worker, err := p.svc.Create(p.ctx, "plain-worker")
	require.NoError(t, err)

	rec := p.do(http.MethodPut, "/api/supervisors/"+worker.ID.String()+"/desired_state", "user", `{"revision":0,"agents":[]}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = p.do(http.MethodGet, "/api/supervisors/"+uuid.NewString()+"/desired_state", "user", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func (p *provisioning) liveGrantCount(t *testing.T, who *agent.Agent) int {
	t.Helper()
	var n int
	require.NoError(t, p.database.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM global_oauth_grants WHERE agent_id = $1 AND revoked_at IS NULL`, who.ID).Scan(&n))
	return n
}

func TestAStaleOrBatchedReportIsNotHalfApplied(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running")+","+entry("slot-2", "running"))
	one, two := doc.Agents[0], doc.Agents[1]
	bumped := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.ResumeGeneration = 1 }))
	require.Equal(t, int64(2), bumped.Agents[0].Generation)
	require.Equal(t, http.StatusNoContent, p.report(one.ID, 2, "running"))

	assert.Equal(t, http.StatusNoContent, p.report(one.ID, 1, "failed"), "a delayed report is ignored, not an error")
	got := p.read(t).Agents[0].Observed
	assert.Equal(t, "running", got.Phase)
	assert.Equal(t, int64(2), got.Generation, "the delayed report did not overwrite the newer one")

	rec := p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer, fmt.Sprintf(
		`{"agents":[{"id":%q,"generation":2,"phase":"failed","reason":"x"},{"id":%q,"generation":9,"phase":"running"}]}`, two.ID, two.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	for _, a := range p.read(t).Agents {
		if a.ID == two.ID {
			assert.Empty(t, a.Observed.Phase, "the valid first report of a rejected batch was not stored")
		}
	}

	ok := p.do(http.MethodPost, "/api/supervisor/desired_state/observed", p.bearer, fmt.Sprintf(
		`{"agents":[{"id":%q,"generation":2,"phase":"provisioning"},{"id":%q,"generation":1,"phase":"running"}]}`, one.ID, two.ID))
	assert.Equal(t, http.StatusNoContent, ok.Code)
}

func TestAStaleRetiredReportDoesNotFreeTheName(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	paused := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "paused" }))
	gone := p.mustPut(t, paused.Revision, echo(paused, func(a *desiredAgentJSON) { a.State = "removed" }))
	id := gone.Agents[0].ID
	require.Equal(t, int64(3), gone.Agents[0].Generation)

	assert.Equal(t, http.StatusUnprocessableEntity, p.report(id, 2, "retired"), "retired at a generation before the removal")
	assert.Len(t, p.read(t).Agents, 1)
	code, _ := p.put(gone.Revision, echo(gone, nil)+","+entry("slot-1", "running"))
	assert.Equal(t, http.StatusConflict, code, "the name is still held")
}

func TestARemovalWhoseRetireDidNotHappenIsRetriedByTheNextWrite(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	gone := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "removed" }))
	slot := p.slot(t, p.sup, "slot-1") // made after the removal committed, as a retire that missed it would leave it
	p.grant(t, slot)

	p.mustPut(t, gone.Revision, echo(gone, nil))

	got, err := p.svc.Get(p.ctx, slot.ID)
	require.NoError(t, err)
	assert.True(t, got.Archived())
	assert.Zero(t, p.liveGrantCount(t, slot))
}

// A second writer on the same revision must wait for the first, not read the
// revision beside it: the first is held inside its transaction while the
// second starts, and the second has to be seen blocked on the document's lock.
func TestConcurrentWritersOnOneRevisionSerialise(t *testing.T) {
	p := newProvisioning(t)
	base := p.mustPut(t, 0, entry("slot-1", "running")).Revision // the header row exists: only its lock serialises now
	store := agent.NewPostgresProvisionStore(p.database)
	noop := func([]agent.DesiredAgent) ([]agent.DesiredAgent, error) { return nil, nil }
	entered, release := make(chan struct{}), make(chan struct{})
	secondEntered := make(chan struct{})
	results := make(chan error, 2)

	go func() {
		_, err := store.Update(p.ctx, p.sup.ID, base, func([]agent.DesiredAgent) ([]agent.DesiredAgent, error) {
			close(entered)
			<-release
			return nil, nil
		})
		results <- err
	}()
	<-entered
	go func() {
		_, err := store.Update(p.ctx, p.sup.ID, base, func(c []agent.DesiredAgent) ([]agent.DesiredAgent, error) {
			close(secondEntered)
			return noop(c)
		})
		results <- err
	}()
	for waiting := 0; waiting == 0; {
		select {
		case <-secondEntered:
			t.Fatal("the second writer read the document while the first held it")
		default:
		}
		require.NoError(t, p.database.Global().QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting))
	}
	close(release)

	var errs []error
	for range 2 {
		errs = append(errs, <-results)
	}
	assert.Contains(t, errs, nil)
	assert.Contains(t, errs, agent.ErrStaleRevision)
}

func TestTheSupervisorIsToldWhetherToReconcileWorkspaces(t *testing.T) {
	p := newProvisioning(t)
	assert.False(t, p.read(t).WorkspaceReconcile, "off by default")
	raw := p.do(http.MethodGet, "/api/supervisor/desired_state", p.bearer, "").Body.String()
	assert.NotContains(t, raw, "workspace_reconcile", "omitted when off, so an older client reads off")

	for _, provision := range []bool{true, false} {
		_, err := p.settings.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(provision), p.user.ID)
		require.NoError(t, err)
		_, err = p.settings.Set(p.ctx, settings.WorkspaceReconcile, settings.BoolValue(true), p.user.ID)
		require.NoError(t, err)
		assert.True(t, p.read(t).WorkspaceReconcile, "independent of provision_from_board=%v", provision)

		_, err = p.settings.Set(p.ctx, settings.WorkspaceReconcile, settings.BoolValue(false), p.user.ID)
		require.NoError(t, err)
		assert.False(t, p.read(t).WorkspaceReconcile)
	}
}

func TestARemovedSlotWhoseSupervisorNeverReportsItRetiredIsClosedOnceArchivedLongEnough(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	slot := p.slot(t, p.sup, "slot-1")
	removed := p.mustPut(t, doc.Revision, echo(doc, func(a *desiredAgentJSON) { a.State = "removed" }))
	require.Len(t, removed.Agents, 1)
	require.Len(t, p.read(t).Agents, 1, "a fresh archive leaves the supervisor time to report")

	_, err := p.database.Exec(p.ctx, `UPDATE agents SET archived_at = now() - interval '9 minutes' WHERE id = $1`, slot.ID)
	require.NoError(t, err)
	require.Len(t, p.read(t).Agents, 1, "still inside the grace")

	_, err = p.database.Exec(p.ctx, `UPDATE agents SET archived_at = now() - interval '1 hour' WHERE id = $1`, slot.ID)
	require.NoError(t, err)

	assert.Empty(t, p.read(t).Agents, "the supervisor is presumed gone")
	var phase string
	var closed bool
	require.NoError(t, p.database.QueryRow(p.ctx,
		`SELECT observed_phase, retired_at IS NOT NULL FROM supervisor_desired_agents WHERE id = $1`, removed.Agents[0].ID).Scan(&phase, &closed))
	assert.Equal(t, "retired", phase)
	assert.True(t, closed, "closed, not just hidden")
	again := p.mustPut(t, removed.Revision, entry("slot-1", "running"))
	require.Len(t, again.Agents, 1, "the name is free again")
	assert.Equal(t, http.StatusNoContent, p.report(removed.Agents[0].ID, removed.Agents[0].Generation, "retired"), "a late report is a no-op")
}

func TestAnArchivedSlotWhoseEntryIsNotRemovedIsLeftOpen(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("slot-1", "running"))
	slot := p.slot(t, p.sup, "slot-1")
	_, err := p.database.Exec(p.ctx, `UPDATE agents SET disabled_at = now(), archived_at = now() - interval '1 hour' WHERE id = $1`, slot.ID)
	require.NoError(t, err)

	got := p.read(t)
	require.Len(t, got.Agents, 1, "only a removed entry is closed")
	assert.Equal(t, doc.Agents[0].ID, got.Agents[0].ID)
}
