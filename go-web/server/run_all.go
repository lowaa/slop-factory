package server

import (
	"context"
	"errors"
)

// RunAll runs every server until ctx is cancelled or one of them stops on
// its own -- a listen failure -- and then shuts the rest down, so a process
// is never left half-serving: the api without its webhook door, or the
// door without the api. It returns once all have stopped, with every error
// they gave.
func RunAll(ctx context.Context, servers ...*Server) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			errs <- s.Run(ctx)
			cancel()
		}()
	}
	var all []error
	for range servers {
		all = append(all, <-errs)
	}
	return errors.Join(all...)
}
