package ticket

import (
	"context"
	"errors"
)

// ErrSetupIncomplete is a claim refused because the tenant has not set up
// anything for a worker to work on.
var ErrSetupIncomplete = errors.New("ticket: the tenant's setup is incomplete")

// SetupCheck says whether the tenant can have code work done. Declared here
// because the onboarding package reads the manifest, which sits above this one.
type SetupCheck interface {
	// SetupGap is what is missing, worded for the agent that asked; "" when nothing is.
	SetupGap(ctx context.Context) string
}

// SetupIncompleteError carries the gap as the agent is told it. It is an
// answer, not a failure: retrying is refused again until a person sets the
// tenant up. The ticket is never granted, so it stays in the queue as it was.
type SetupIncompleteError struct{ Gap string }

func (e *SetupIncompleteError) Error() string { return e.Gap }

func (e *SetupIncompleteError) Is(target error) bool { return target == ErrSetupIncomplete }

// WithSetupCheck makes a claim refuse, with the check's message, while the
// tenant has nothing a worker could work on. Without it nothing is checked.
func WithSetupCheck(c SetupCheck) ServiceOption {
	return func(s *Service) { s.setup = c }
}
