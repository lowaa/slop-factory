package codehosttest_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
)

type ciWorld struct {
	f      *codehosttest.Fake
	repo   codehost.Repo
	ref    codehost.ChangeRef
	tenant uuid.UUID
}

func newCIWorld(ci *codehosttest.CI) *ciWorld {
	repo := codehost.Repo{Owner: "acme", Name: "app"}
	ref := codehost.ChangeRef{Host: "git.example.test", Owner: "acme", Repo: "app", Number: 1}
	f := &codehosttest.Fake{
		Domain: "git.example.test", Opened: ref, CI: ci,
		BranchAt: map[codehosttest.BranchKey]string{
			{Repo: repo, Branch: "main"}:  "base",
			{Repo: repo, Branch: "batch"}: "headB",
		},
		ChecksOf: map[codehost.ChangeRef]*codehost.Checks{ref: codehost.NewChecks("headB", "main", codehosttest.DefaultChecks())},
		BranchChecksOf: map[codehosttest.BranchKey]*codehost.Checks{
			{Repo: repo, Branch: "batch"}: codehost.NewChecks("headB", "", codehosttest.DefaultChecks()),
		},
	}
	return &ciWorld{f: f, repo: repo, ref: ref, tenant: uuid.New()}
}

func (w *ciWorld) open(t *testing.T, draft bool) {
	t.Helper()
	_, err := w.f.OpenChange(context.Background(), w.tenant, &codehost.OpenRequest{Repo: w.repo, Head: "batch", Base: "main", Title: "t", Draft: draft})
	require.NoError(t, err)
}

func (w *ciWorld) branchChecks(t *testing.T) *codehost.Checks {
	t.Helper()
	c, err := w.f.BranchChecks(context.Background(), w.tenant, w.repo, "batch")
	require.NoError(t, err)
	return c
}

func (w *ciWorld) changeChecks(t *testing.T) *codehost.Checks {
	t.Helper()
	c, err := w.f.Checks(context.Background(), w.tenant, w.ref)
	require.NoError(t, err)
	return c
}

func TestCI_ABareBranchHasNoChecks(t *testing.T) {
	w := newCIWorld(codehosttest.NewCI())

	c := w.branchChecks(t)
	assert.Equal(t, "headB", c.HeadSHA)
	assert.Empty(t, c.Checks)
	assert.False(t, c.Passed(), "no pull request, no pipeline, and no checks is not a pass")
}

func TestCI_ADraftChangeStartsThePipelineOfItsHead(t *testing.T) {
	w := newCIWorld(codehosttest.NewCI())
	w.open(t, true)

	assert.True(t, w.branchChecks(t).Passed(), "the branch's head has the checks of the change opened from it")
	assert.True(t, w.changeChecks(t).Passed())
}

func TestCI_ChecksWaitForThePipeline(t *testing.T) {
	ci := codehosttest.NewCI()
	ci.Manual = true
	w := newCIWorld(ci)
	w.open(t, false)

	assert.Equal(t, codehost.CheckNone, w.changeChecks(t).State, "queued: nothing has reported")
	assert.True(t, ci.Started(w.repo, "headB"))
	ci.Complete(w.repo, "headB")
	assert.True(t, w.changeChecks(t).Passed())
}

func TestCI_APipelineThatErrorsBeforeItStartsHasNoChecksThenAFailure(t *testing.T) {
	ci := codehosttest.NewCI()
	ci.Manual = true
	ci.Script = func(codehost.Repo, string) ([]codehost.Check, bool) {
		return codehosttest.ConfigErrorChecks(), true
	}
	w := newCIWorld(ci)
	w.open(t, false)

	assert.Empty(t, w.changeChecks(t).Checks)
	ci.Complete(w.repo, "headB")
	c := w.changeChecks(t)
	assert.Equal(t, codehost.CheckFailure, c.State)
	assert.Equal(t, codehost.CheckFailure, w.branchChecks(t).State, "the same commit shows the same checks to a branch read")
}

func TestCI_AHeadThatMovedHasNoChecksUntilItsOwnPipeline(t *testing.T) {
	ci := codehosttest.NewCI()
	ci.Manual = true
	w := newCIWorld(ci)
	w.open(t, false)
	w.changeChecks(t)
	ci.Complete(w.repo, "headB")
	require.True(t, w.changeChecks(t).Passed())

	w.f.ChecksOf[w.ref] = codehost.NewChecks("headC", "main", codehosttest.DefaultChecks())
	moved := w.changeChecks(t)
	assert.Equal(t, "headC", moved.HeadSHA)
	assert.Empty(t, moved.Checks, "the old head's checks do not carry over to the new head")
	assert.True(t, ci.Started(w.repo, "headC"))
	ci.Complete(w.repo, "headC")
	assert.True(t, w.changeChecks(t).Passed())
}

func TestCI_ACommitHasOneResultWhicheverWayItIsRead(t *testing.T) {
	w := newCIWorld(codehosttest.NewCI())
	w.f.BranchChecksOf = nil
	w.f.ChecksOf[w.ref] = codehost.NewChecks("headB", "main", []codehost.Check{{Name: "lint", State: codehost.CheckFailure}})
	w.open(t, false)

	assert.Equal(t, codehost.CheckFailure, w.changeChecks(t).State)
	assert.Equal(t, codehost.CheckFailure, w.branchChecks(t).State)
}

func TestCI_ClosingAChangeStartsNoNewPipelineAndCompletesNone(t *testing.T) {
	ci := codehosttest.NewCI()
	ci.Manual = true
	w := newCIWorld(ci)
	w.open(t, false)
	w.changeChecks(t) // starts headB's pipeline
	require.NoError(t, w.f.CloseChange(context.Background(), w.tenant, w.ref))

	assert.False(t, w.changeChecks(t).Passed(), "closing does not make a pending pipeline pass")
	w.f.ChecksOf[w.ref] = codehost.NewChecks("headC", "main", nil)
	assert.Empty(t, w.changeChecks(t).Checks)
	assert.False(t, ci.Started(w.repo, "headC"), "a closed change's new head is not tested")
}

func TestCI_ClosingKeepsTheResultOfAPipelineThatRan(t *testing.T) {
	w := newCIWorld(codehosttest.NewCI())
	w.open(t, false)
	require.True(t, w.changeChecks(t).Passed())
	require.NoError(t, w.f.CloseChange(context.Background(), w.tenant, w.ref))

	assert.True(t, w.branchChecks(t).Passed())
}

func TestCI_AChangeHostNeverOpenedHasNoPipeline(t *testing.T) {
	w := newCIWorld(codehosttest.NewCI())

	c := w.changeChecks(t)
	assert.Empty(t, c.Checks, "ChecksOf alone does not start CI: only OpenChange does")
	assert.False(t, c.Passed())
}

func TestCI_TheChecksATestConfiguredAreWhatAFinishedPipelineReports(t *testing.T) {
	w := newCIWorld(codehosttest.NewCI())
	w.f.ChecksOf[w.ref] = codehost.NewChecks("headB", "main", []codehost.Check{{Name: "lint", State: codehost.CheckFailure}})
	w.open(t, false)

	c := w.changeChecks(t)
	assert.Equal(t, codehost.CheckFailure, c.State)
	assert.Equal(t, "main", c.Base)
}
