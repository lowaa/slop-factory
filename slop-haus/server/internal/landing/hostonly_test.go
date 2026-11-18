package landing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestHostOnlyPipelines_SeesNothingAndKnowsNothing(t *testing.T) {
	p := landing.NewHostOnlyPipelines(7 * time.Minute)
	ctx := context.Background()

	newest, err := p.Newest(ctx, "abc")
	assert.ErrorIs(t, err, landing.ErrUnknown, "not nil, nil: that would say the CI has no pipeline")
	assert.Nil(t, newest)
	timeout, err := p.RepoTimeout(ctx)
	require.NoError(t, err)
	assert.Equal(t, 7*time.Minute, timeout)
	assert.ErrorIs(t, p.Retry(ctx, "abc"), landing.ErrNoRetry)
	_, err = p.PassedOnBase(ctx, []string{"ci/build"})
	assert.ErrorIs(t, err, landing.ErrUnknown)
	_, err = p.PassedOnCommit(ctx, "abc", []string{"ci/build"})
	assert.ErrorIs(t, err, landing.ErrUnknown)
}

func TestLand_WithNoCIAdapterAGreenHeadStillLands(t *testing.T) {
	f := newFixture(t)
	f.ci = landing.NewHostOnlyPipelines(time.Hour)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, green)

	f.land(f.runner(landing.Config{}))

	f.assertMerged(c)
}

// Even a check named known flaky is not run again: there is nothing to run it
// with, and the ticket goes back with its red checks.
func TestLand_WithNoCIAdapterARedHeadIsHandedBackWithoutARetry(t *testing.T) {
	f := newFixture(t)
	f.ci = landing.NewHostOnlyPipelines(time.Hour)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)

	f.land(f.runner(landing.Config{KnownFlaky: []string{"ci/build"}}))

	f.assertHandedBack(c, landing.ChecksRed)
	assert.Empty(t, f.pipes.retried())
	assert.Empty(t, f.host.Merges())
}
