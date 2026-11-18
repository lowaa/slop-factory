package agent_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

const ownPath = "/api/remote_control/supervisors"

type ownSupervisorsJSON struct {
	ProvisionFromBoard bool `json:"provision_from_board"`
	Supervisors        []struct {
		ID             string             `json:"id"`
		Name           string             `json:"name"`
		RemoteControls []desiredAgentJSON `json:"remote_controls"`
	} `json:"supervisors"`
}

func (p *provisioning) own(t *testing.T) ownSupervisorsJSON {
	t.Helper()
	rec := p.do(http.MethodGet, ownPath, "user", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[ownSupervisorsJSON](t, rec)
}

func (p *provisioning) addOwn(supervisorID uuid.UUID, name string) (int, desiredAgentJSON) {
	rec := p.do(http.MethodPost, ownPath+"/"+supervisorID.String()+"/remote_controls", "user", `{"name":"`+name+`"}`)
	var out desiredAgentJSON
	if rec.Code == http.StatusCreated {
		out = decode[desiredAgentJSON](&testing.T{}, rec)
	}
	return rec.Code, out
}

// othersSupervisor is a supervisor whose sign-in another member approved.
func (p *provisioning) othersSupervisor(t *testing.T, name string) *agent.Agent {
	t.Helper()
	sup := p.supervisor(t, name, 1)
	_, err := p.database.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, approved_by, scope)
		VALUES (gen_random_uuid(), $1, $2, 'slop-haus-workspace', 'r', $3, 'agent')`, p.tenant, sup.ID, p.otherMember(t))
	require.NoError(t, err)
	return sup
}

func TestTheRemoteControlPageListsOnlyTheSupervisorsYouApprovedWithTheirRemoteControls(t *testing.T) {
	p := newProvisioning(t)
	p.othersSupervisor(t, "their-box")
	p.mustPut(t, 0, entry("a-slot", "running")+","+rcEntry("rc-run", "supervisor")+","+rcEntry("rc-hand", "manual"))

	got := p.own(t)

	assert.True(t, got.ProvisionFromBoard)
	require.Len(t, got.Supervisors, 1)
	assert.Equal(t, p.sup.ID.String(), got.Supervisors[0].ID)
	require.Len(t, got.Supervisors[0].RemoteControls, 1, "only the remote controls it runs")
	assert.Equal(t, "rc-run", got.Supervisors[0].RemoteControls[0].Name)
}

func TestWithTheBoardNotProvisioningThePageOffersNoSupervisor(t *testing.T) {
	p := newProvisioning(t)
	_, err := p.settings.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(false), p.user.ID)
	require.NoError(t, err)

	got := p.own(t)

	assert.False(t, got.ProvisionFromBoard)
	assert.Empty(t, got.Supervisors)
	code, _ := p.addOwn(p.sup.ID, "rc-1")
	assert.Equal(t, http.StatusConflict, code)
}

func TestAnAddedRemoteControlIsEnrolledByTheSupervisorAsYourConductor(t *testing.T) {
	p := newProvisioning(t)

	code, added := p.addOwn(p.sup.ID, " rc-1 ")

	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "rc-1", added.Name)
	assert.Equal(t, "supervisor", added.Launch)
	host := p.enrolled(t, "rc-1")
	assert.True(t, host.OwnedBy(p.user.ID))
	assert.Len(t, p.read(t).Agents, 1, "the supervisor reads it in its document")
}

func TestAddingKeepsTheDocumentsOtherEntries(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, entry("a-slot", "paused"))

	code, _ := p.addOwn(p.sup.ID, "rc-1")

	require.Equal(t, http.StatusCreated, code)
	doc := p.read(t)
	require.Len(t, doc.Agents, 2)
	assert.Equal(t, "paused", doc.Agents[0].State, "the slot as it was")
}

func TestOnlyTheApproverAddsToASupervisor(t *testing.T) {
	p := newProvisioning(t)
	theirs := p.othersSupervisor(t, "their-box")

	code, _ := p.addOwn(theirs.ID, "rc-1")

	assert.Equal(t, http.StatusForbidden, code, "the tenant's owner is refused too: it would be the approver's conductor")
	assert.Equal(t, http.StatusForbidden, p.do(http.MethodDelete, ownPath+"/"+theirs.ID.String()+"/remote_controls/"+uuid.NewString(), "user", "").Code)
	code, _ = p.addOwn(uuid.New(), "rc-1")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestANameAnAgentHoldsIsRefusedAtTheAdd(t *testing.T) {
	p := newProvisioning(t)
	_, err := p.svc.Create(p.ctx, "taken")
	require.NoError(t, err)

	code, _ := p.addOwn(p.sup.ID, "taken")

	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Empty(t, p.read(t).Agents)
}

func TestAddingNeedsARecentSignIn(t *testing.T) {
	p := newProvisioning(t)
	p.user.AuthTime = time.Now().Add(-2 * user.StepUpMaxAge)

	code, _ := p.addOwn(p.sup.ID, "rc-1")

	assert.Equal(t, http.StatusForbidden, code)
	assert.Empty(t, p.read(t).Agents)
}

func TestRemovingYourRemoteControlRetiresItsConductor(t *testing.T) {
	p := newProvisioning(t)
	_, added := p.addOwn(p.sup.ID, "rc-1")
	host := p.enrolled(t, "rc-1")

	rec := p.do(http.MethodDelete, ownPath+"/"+p.sup.ID.String()+"/remote_controls/"+added.ID, "user", "")

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got, err := p.svc.Get(p.ctx, host.ID)
	require.NoError(t, err)
	assert.True(t, got.Archived())
	assert.Equal(t, "removed", p.read(t).Agents[0].State)
}

func TestOnlyARemoteControlTheSupervisorRunsIsRemovedThisWay(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, entry("a-slot", "running")+","+rcEntry("rc-hand", "manual"))

	for _, e := range doc.Agents {
		rec := p.do(http.MethodDelete, ownPath+"/"+p.sup.ID.String()+"/remote_controls/"+e.ID, "user", "")
		assert.Equal(t, http.StatusNotFound, rec.Code, e.Name)
	}
	for _, e := range p.read(t).Agents {
		assert.Equal(t, "running", e.State, e.Name)
	}
}

func TestASupervisorRunRemoteControlIsNotArchivedDirectly(t *testing.T) {
	p := newProvisioning(t)
	p.addOwn(p.sup.ID, "rc-1")
	host := p.enrolled(t, "rc-1")
	conductors := agent.NewConductorService(p.svc, tenantRoles{user.NewPostgresStore(p.database)}, slog.DiscardHandler)

	_, err := conductors.SetArchived(p.ctx, p.user, host.ID, true)

	require.ErrorIs(t, err, agent.ErrRunBySupervisor)
	mine := p.conductorOf(t, "by-hand", p.user.ID)
	_, err = p.database.Exec(p.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, mine.ID)
	require.NoError(t, err)
	_, err = conductors.SetArchived(p.ctx, p.user, mine.ID, true)
	assert.NoError(t, err, "one run by hand is archived as before")
}

// racingStore commits another writer's slot entry before each of the first
// `races` updates, as a board edit landing between an own add's read and its
// write would.
type racingStore struct {
	agent.ProvisionStore
	races int
	wrote []string
}

func (s *racingStore) Update(ctx context.Context, supervisorID uuid.UUID, baseRevision int64, edit func([]agent.DesiredAgent) ([]agent.DesiredAgent, error)) (*agent.DesiredState, error) {
	if s.races > 0 {
		s.races--
		name := fmt.Sprintf("racer-%d", len(s.wrote))
		s.wrote = append(s.wrote, name)
		if _, err := s.ProvisionStore.Update(ctx, supervisorID, baseRevision, func(current []agent.DesiredAgent) ([]agent.DesiredAgent, error) {
			return []agent.DesiredAgent{{ID: uuid.New(), Kind: agent.ProvisionSlot, Name: name, Harness: "claude", State: agent.StateRunning, Launch: agent.LaunchManual, Generation: 1}}, nil
		}); err != nil {
			return nil, err
		}
	}
	return s.ProvisionStore.Update(ctx, supervisorID, baseRevision, edit)
}

func (p *provisioning) racedProvision(races int) (*agent.ProvisionService, *racingStore) {
	store := &racingStore{ProvisionStore: agent.NewPostgresProvisionStore(p.database), races: races}
	return agent.NewProvisionService(store, p.svc, tenantRoles{user.NewPostgresStore(p.database)}, p.settings, slog.DiscardHandler), store
}

func TestAnAddRacedByAnotherWriteIsMadeOnTheDocumentAsItThenIs(t *testing.T) {
	p := newProvisioning(t)
	svc, store := p.racedProvision(1)

	added, err := svc.AddOwnRemoteControl(p.ctx, p.user, p.sup.ID, "rc-1", "")

	require.NoError(t, err)
	assert.Equal(t, "rc-1", added.Name)
	names := map[string]bool{}
	for _, a := range p.read(t).Agents {
		names[a.Name] = true
	}
	assert.Equal(t, map[string]bool{store.wrote[0]: true, "rc-1": true}, names, "the other writer's entry is kept")
}

func TestAnAddThatKeepsLosingTheRaceGivesUp(t *testing.T) {
	p := newProvisioning(t)
	svc, store := p.racedProvision(100)

	_, err := svc.AddOwnRemoteControl(p.ctx, p.user, p.sup.ID, "rc-1", "")

	require.ErrorIs(t, err, agent.ErrStaleRevision)
	assert.Len(t, store.wrote, 3, "three tries, then the caller is told")
}

func TestAnApproverWhoDoesNotOwnTheTeamManagesTheirOwnRemoteControls(t *testing.T) {
	p := newProvisioning(t)
	_, err := p.database.Global().Exec(context.Background(),
		`UPDATE global_tenant_memberships SET role = 'member' WHERE tenant = $1 AND user_id = $2`, p.tenant, p.user.ID)
	require.NoError(t, err)

	require.Len(t, p.own(t).Supervisors, 1)
	code, added := p.addOwn(p.sup.ID, "rc-1")
	require.Equal(t, http.StatusCreated, code)
	host := p.enrolled(t, "rc-1")
	assert.True(t, host.OwnedBy(p.user.ID))
	rec := p.do(http.MethodDelete, ownPath+"/"+p.sup.ID.String()+"/remote_controls/"+added.ID, "user", "")
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	code, _ = p.put(p.read(t).Revision, entry("a-slot", "running"))
	assert.Equal(t, http.StatusForbidden, code, "the rest of the document is still the team owner's")
}
