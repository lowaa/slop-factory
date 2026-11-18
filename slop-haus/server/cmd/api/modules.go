package main

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// A module is one domain's wiring, in a file of its own (mod_<domain>.go): it
// builds the domain's service and handler from the container and the modules
// before it, contributes its routes, and closes what shutdown has to end.
type module interface {
	// Routes sets the handlers the domain serves on the route set.
	Routes(r *apiserver.Deps)
	// Close ends the waits the domain holds open, once shutdown begins.
	Close()
}

// moduleDeps is what a module is built from.
type moduleDeps struct {
	*container
	// ctx is the server's, for modules that publish at startup.
	ctx  context.Context
	svcs *services
	// hostChanges is the code host's change operations, nil unless a test
	// supplies its own.
	hostChanges codehost.Changes
	// built are the modules before this one in moduleBuilders.
	built *modules
}

// moduleBuilders is every module, in dependency order: a module may look up
// only those above it. Adding a domain appends its builder.
var moduleBuilders = []func(*moduleDeps) (module, error){
	newSupervisorModule,
	newMessagesModule,
	newAgentModule,
	newSessionModule,
	newWorkspaceModule,
	newCodeHostModule, // reads the workspace manifest
	newOnboardingModule,
	newFleetModule,
	newVariantModule,
	newReportsModule,
	newConversationsModule,
	newTicketModule,
	newEvalSetModule, // runs sets through the ticket service
	newReviewReportModule,
	newWorkerModule, // reads the review requests
	newLandingModule,
	newStewardModule,
	newAgentHandlerModule, // reads the workspace, fleet and code host modules
	newOAuthModule,
	newSettingsModule,
	newUserSettingsModule,
	newUserModule,
	newHarnessModule,
	newLLMUsageModule,
	newUsageModule,
	newIntakeModule,
	newGitHubHookModule,
	newPrivacyModule,
	newAttachmentModule,
}

// modules are the domains wired on top of the services, in build order.
type modules struct {
	list []module
}

func newModules(ctx context.Context, c *container, s *services, hostChanges codehost.Changes) (*modules, error) {
	m := &modules{}
	d := &moduleDeps{container: c, ctx: ctx, svcs: s, hostChanges: hostChanges, built: m}
	for _, build := range moduleBuilders {
		mod, err := build(d)
		if err != nil {
			return nil, err
		}
		m.list = append(m.list, mod)
	}
	return m, nil
}

// Routes gathers every module's routes.
func (m *modules) Routes(r *apiserver.Deps) {
	for _, mod := range m.list {
		mod.Routes(r)
	}
}

// Close ends every module's waits.
func (m *modules) Close() {
	for _, mod := range m.list {
		mod.Close()
	}
}

// lookup is the module of type T. A module asking for one built after it is a
// mistake in moduleBuilders, caught at startup.
func lookup[T module](m *modules) T {
	for _, mod := range m.list {
		if t, ok := mod.(T); ok {
			return t
		}
	}
	panic(fmt.Sprintf("module %T is not built yet: move it up in moduleBuilders", *new(T)))
}
