package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
)

// agentModule is the services around the agents that nothing in the
// agents/OAuth/tickets cycle (see lateDeps) needs: the agent, conductor and
// OAuth services themselves stay in newServices. Its handler is
// agentHandlerModule's, which needs the modules built after this one.
type agentModule struct {
	Layouts    *agent.PostgresLayoutStore
	Provision  *agent.ProvisionService
	HostLeases *agent.HostLeaseService
	Roles      *agent.RoleService
}

func newAgentModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	provision := agent.NewProvisionService(agent.NewPostgresProvisionStore(c.database), s.agents, c.users, s.settings, c.logHandler,
		agent.WithSlotReports(lookup[*supervisorModule](d.built).Service))
	s.provisionImport.Bind(provision)
	return &agentModule{
		Layouts:    agent.NewPostgresLayoutStore(c.database),
		Provision:  provision,
		HostLeases: agent.NewHostLeaseService(agent.NewPostgresHostLeaseStore(c.database), s.agents, 0, c.logHandler),
		Roles:      agent.NewRoleService(agent.NewPostgresRoleStore(c.database), c.logHandler),
	}, nil
}

func (*agentModule) Routes(*apiserver.Deps) {}

func (*agentModule) Close() {}

// agentHandlerModule is the agents' REST handler, which reads the workspace,
// fleet and code host modules.
type agentHandlerModule struct {
	Handler *agent.Handler
}

func newAgentHandlerModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	a := lookup[*agentModule](d.built)
	return &agentHandlerModule{Handler: agent.NewHandler(&agent.HandlerDeps{
		Agents:        s.agents,
		Conductors:    s.conductors,
		Roles:         a.Roles,
		Tickets:       s.tickets,
		Sessions:      lookup[*sessionModule](d.built).Service,
		HarnessConfig: s.harness,
		Manifest:      lookup[*workspaceModule](d.built).Service,
		Fleet:         lookup[*fleetModule](d.built).Service,
		CodeHost:      lookup[*codeHostModule](d.built).Credentials,
		Steward:       steward.NewSupervision(c.database, agreement.NewReporter(agreement.NewScorer(s.stewardStore, c.database)), steward.NewTenantSettings(s.settings)),
		Provision:     a.Provision,
		HostLeases:    a.HostLeases,
	}, c.logHandler)}, nil
}

func (m *agentHandlerModule) Routes(r *apiserver.Deps) { r.AgentHandler = m.Handler }

func (*agentHandlerModule) Close() {}
