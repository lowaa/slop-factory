package authn

import "github.com/danielgtaylor/huma/v2"

// supervisorRouteKey says how an agent route treats a supervisor's
// credential. A supervisor creates workers and mints their credentials, so
// the default for every agent route is a refusal: a route that never heard of
// supervisors is closed to one, rather than open until someone remembers.
const supervisorRouteKey = "slop-haus:supervisor-route"

const (
	// supervisorOnly routes are the supervisor's own, and refuse every other
	// kind of agent.
	supervisorOnly = "only"
	// supervisorAlso routes serve workers and supervisors alike.
	supervisorAlso = "also"
)

// RequireSupervisor marks an agent route as the supervisor's own: it is
// served to a supervisor's credential and to nothing else.
func RequireSupervisor() map[string]any {
	md := RequireAgent()
	md[supervisorRouteKey] = supervisorOnly
	return md
}

// RequireAgentOrSupervisor marks an agent route a supervisor's credential may
// also use, for the few things it needs besides its own routes.
func RequireAgentOrSupervisor() map[string]any {
	md := RequireAgent()
	md[supervisorRouteKey] = supervisorAlso
	return md
}

// IsSupervisor reports whether the caller is a supervisor. It is asked of the
// identity rather than declared on Identity, as for the other kinds, so
// the interface every transport satisfies stays small.
func IsSupervisor(id Identity) bool {
	s, ok := id.(interface{ IsSupervisor() bool })
	return ok && s.IsSupervisor()
}

// SupervisorAllowed reports whether an operation serves the caller, given
// whether the caller is a supervisor.
func SupervisorAllowed(op *huma.Operation, supervisor bool) bool {
	mode, _ := op.Metadata[supervisorRouteKey].(string)
	switch mode {
	case supervisorOnly:
		return supervisor
	case supervisorAlso:
		return true
	default:
		return !supervisor
	}
}
