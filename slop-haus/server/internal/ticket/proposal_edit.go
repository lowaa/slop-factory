package ticket

import (
	"context"
	"fmt"
)

// PrepareUpdate is what Update checks before it opens its transaction: the
// enums, and the paths, which it normalises in place and holds to the
// workspace's one-repo rule. That rule reads the workspace through a
// connection of its own, so a caller composing UpdateInTx with writes of its
// own runs this ahead of its transaction.
func (s *Service) PrepareUpdate(ctx context.Context, params *UpdateParams) error {
	if params.Priority != nil && !params.Priority.Valid() {
		return fmt.Errorf("%w: priority %q", ErrInvalidEnum, *params.Priority)
	}
	if params.Status != nil && !params.Status.Valid() {
		return fmt.Errorf("%w: status %q", ErrInvalidEnum, *params.Status)
	}
	if params.Paths != nil {
		normalized, err := normalizePaths(params.Paths)
		if err != nil {
			return err
		}
		params.Paths = normalized
		if err := s.checkOneRepo(ctx, scopePaths(normalized)); err != nil {
			return err
		}
	}
	return nil
}
