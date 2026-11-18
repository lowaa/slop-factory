package change_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) openFrom(h *holder, branch string) (*change.Opened, error) {
	return f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: branch, Title: "T", Draft: true})
}

func TestOpenBranch_OnlyTheAttemptsOwnBranchIsAccepted(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	cases := map[string]struct {
		as     *holder
		branch string
		want   string
	}{
		"a sibling's branch":                 {second, f.branchOf(first), f.branchOf(second)},
		"attempt 1 passing attempt 2's name": {first, f.branchOf(second), f.branchOf(first)},
		"another ticket's branch":            {first, "ticket-999-a1", f.branchOf(first)},
		"an invented name":                   {second, "ticket-1-a2-note", f.branchOf(second)},
		"the old title-slug name":            {first, "ticket-1-add-the-thing", f.branchOf(first)},
		"a prefix of the right one":          {second, "ticket-1-a", f.branchOf(second)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.openFrom(tc.as, tc.branch)

			require.ErrorIs(t, err, change.ErrInvalid)
			assert.Contains(t, err.Error(), tc.want, "names the branch to use")
			assert.Contains(t, err.Error(), "git push origin HEAD:"+tc.want, "and how to get there")
			assert.Empty(t, f.host.Opens())
			assert.Empty(t, f.host.BranchCreates(), "refused before the host is touched")
		})
	}
}

func TestOpenBranch_TheAttemptsOwnBranchIsAccepted(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	f.openAs(first, 7, true)
	f.openAs(second, 8, true)

	assert.Equal(t, "ticket-"+itoa(first.ticket.ID)+"-a1", f.currentOf(first, 1).Head)
	assert.Equal(t, "ticket-"+itoa(first.ticket.ID)+"-a2", f.currentOf(first, 2).Head)
}

func TestOpenBranch_ARenamedTicketDoesNotMoveTheBranch(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	want := f.branchOf(h)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET title = 'A different title' WHERE id = $1`, h.ticket.ID)
	require.NoError(t, err)

	_, err = f.openFrom(h, want)

	require.NoError(t, err)
}

func TestOpenBranch_AChangeRecordedUnderAnotherNameMayBeReopenedButNothingElseMay(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, true)
	f.recordUnderOldName(h)

	again, err := f.openFrom(h, "ticket-old-name")
	require.NoError(t, err)
	assert.True(t, again.Existing, "the same change, not a new one")
	assert.Len(t, f.host.Opens(), 1)

	_, err = f.openFrom(h, "ticket-another-old-name")
	require.ErrorIs(t, err, change.ErrInvalid)
	assert.Len(t, f.host.Opens(), 1)
}

func TestOpenBranch_AMergedChangeEarnsNoGrandfatheredReopen(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.mergeLeavingTheTicketHeld(h)
	f.recordUnderOldName(h)

	_, err := f.openFrom(h, "ticket-old-name")

	require.ErrorIs(t, err, change.ErrInvalid)
}

func TestOpenBranch_TheHostRefusingTheBaseBranchIsReportedAsTheHosts(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.host.BranchErr = errors.New("Reference update failed")

	_, err := f.openFrom(second, f.branchOf(second))

	require.Error(t, err)
	assert.Contains(t, err.Error(), ticket.UnpromotedBase(first.ticket.ID))
	assert.Contains(t, err.Error(), "Reference update failed", "the host's reason")
	assert.Contains(t, err.Error(), "not a fault of your branch")
	assert.NotErrorIs(t, err, change.ErrInvalid)
}
