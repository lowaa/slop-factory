package landing

import (
	"context"
	"errors"
	"time"
)

// ErrNoRetry is Retry on CI that cannot run a pipeline again: the runner goes
// on to a handback instead of a retry.
var ErrNoRetry = errors.New("landing: this CI cannot retry a pipeline")

// ErrUnknown is a fact the CI in use cannot tell.
var ErrUnknown = errors.New("landing: CI does not know")

// HostOnlyPipelines is Pipelines for a tenant with no CI adapter: the code
// host's check statuses are all there is. It sees no pipelines, cannot retry,
// and knows nothing of the base branch's checks, so no failure is retried as
// flaky. The wait is bounded by a fixed timeout, so a tenant without an
// adapter still lands.
type HostOnlyPipelines struct {
	timeout time.Duration
}

func NewHostOnlyPipelines(timeout time.Duration) *HostOnlyPipelines {
	return &HostOnlyPipelines{timeout: timeout}
}

// Newest is ErrUnknown, not nil: nil means the CI answered that the head has
// no pipeline, which a host-only tenant cannot say.
func (*HostOnlyPipelines) Newest(context.Context, string) (*Pipeline, error) {
	return nil, ErrUnknown
}

func (h *HostOnlyPipelines) RepoTimeout(context.Context) (time.Duration, error) {
	return h.timeout, nil
}

func (*HostOnlyPipelines) Restart(context.Context, string, int64) error { return ErrNoRetry }

func (*HostOnlyPipelines) Retry(context.Context, string) error { return ErrNoRetry }

func (*HostOnlyPipelines) PassedOnBase(context.Context, []string) (bool, error) {
	return false, ErrUnknown
}

func (*HostOnlyPipelines) PassedOnCommit(context.Context, string, []string) (bool, error) {
	return false, ErrUnknown
}

func (*HostOnlyPipelines) CheckVerdict(context.Context, string, string) (CheckVerdict, error) {
	return "", ErrUnknown
}

var _ Pipelines = (*HostOnlyPipelines)(nil)
