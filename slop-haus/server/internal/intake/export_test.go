package intake

import "github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"

func NativeAdapter() Adapter { return nativeAdapter{} }

func CheckEvent(e *Event) error { return e.check() }

var AdapterFor = adapterFor

// Decision is the part of the unexported decision the tests look at.
type Decision struct {
	Plan       *ticket.IntakePlan
	Downgraded bool
}

func PlanFor(s *Source, ev *Event, id string, st *ticket.IntakeState, c *Caps) Decision {
	d := s.Plan(ev, id, st, c)
	return Decision{Plan: d.plan, Downgraded: d.downgraded}
}
