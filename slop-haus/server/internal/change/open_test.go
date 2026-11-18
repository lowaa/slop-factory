package change_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestOpen_OpensInTheManifestsRepoAndRecordsItOnTheTicket(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.approve(h)

	opened := f.open(h, true)

	assert.Equal(t, "https://git.example/acme/app/pull/7", opened.URL)
	assert.True(t, opened.Draft)
	calls := f.host.Opens()
	require.Len(t, calls, 1)
	assert.Equal(t, f.tenant, calls[0].Tenant, "the tenant is the caller's")
	req := calls[0].Request
	assert.Equal(t, appRepo, req.Repo, "the repo is the manifest's, not the agent's")
	assert.Equal(t, f.branchOf(h), req.Head)
	assert.Equal(t, "Add the thing", req.Title)
	assert.Equal(t, "main", req.Base)
	assert.True(t, req.Draft)
	assert.Contains(t, req.Body, "It adds the thing.", "the agent's description")
	assert.Contains(t, req.Body, "slop-haus #"+itoa(h.ticket.ID)+": https://board.example/tickets/"+itoa(h.ticket.ID)+"?tenant="+f.tenant.String())
	assert.Contains(t, req.Body, "Agent: builder-1")
	assert.Contains(t, req.Body, "Ticket filed by: Casey Creator")
	assert.Contains(t, req.Body, "Approved by: Alex Approver")

	got, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PRURL)
	assert.Equal(t, opened.URL, *got.PRURL, "recorded as the ticket's pr_url")
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, change.Record{}.MergedAt, rec.MergedAt)
	assert.Equal(t, f.branchOf(h), rec.Head)
	assert.True(t, rec.Draft)

	lines := f.log.audited(t, audit.ChangeOpened)
	require.Len(t, lines, 1)
	assert.Equal(t, h.agent.String(), lines[0]["agent_id"], "audited with the agent that caused it")
	assert.Equal(t, f.tenant.String(), lines[0]["tenant_id"])
	assert.Equal(t, opened.URL, lines[0]["change"])
}

func itoa(n int64) string { return formatInt(n) }

func TestOpen_TheBodyNamesProposalApprovers(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	svc, err := change.NewService(&change.Options{
		Hosts: hostsOf(f), Tickets: f.tickets, Workspace: f.ws, Store: f.store, Names: f.names,
		Detect: codehost.LineNumberChangeDetectionStrategy{}, Proposals: proposalsFake{approvers: []uuid.UUID{f.approver, f.approver}}, Logger: discard(),
	})
	require.NoError(t, err)

	_, err = svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.NoError(t, err)
	assert.Contains(t, f.host.Opens()[0].Request.Body, "Proposals approved by: Alex Approver (ready)", "once, though approved twice")
}

func TestOpen_RefusesWhatTheRulesRefuse_BeforeCallingTheHost(t *testing.T) {
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	for name, in := range map[string]change.OpenInput{
		"empty branch":          {Branch: "", Title: "t"},
		"the base branch":       {Branch: "main", Title: "t"},
		"a flag":                {Branch: "--upload-pack=x", Title: "t"},
		"a ref path":            {Branch: "refs/heads/x", Title: "t"},
		"a space":               {Branch: "a b", Title: "t"},
		"dot dot":               {Branch: "a..b", Title: "t"},
		"a reflog spec":         {Branch: "a@{1}", Title: "t"},
		"a lock file":           {Branch: "a.lock", Title: "t"},
		"too long":              {Branch: string(long), Title: "t"},
		"a blank title":         {Branch: branch, Title: "  "},
		"a two line title":      {Branch: branch, Title: "a\nb"},
		"a body past the bound": {Branch: branch, Title: "t", Body: string(make([]byte, 30001))},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.hold(false)

			_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &in)

			require.ErrorIs(t, err, change.ErrInvalid)
			assert.Empty(t, f.host.Opens())
		})
	}
}

func TestOpen_NeedsOneRepoOnAHostItCanActOn(t *testing.T) {
	for name, placement := range map[string]*ticket.Placement{
		"no manifest":             {},
		"scopes outside it":       {Declared: true, Unplaced: []string{"elsewhere/"}, Repos: placedIn(hostName + "/acme/app").Repos},
		"scopes in two repos":     placedIn(hostName+"/acme/app", hostName+"/acme/other"),
		"scopes in no repo":       {Declared: true},
		"a host it does not know": placedIn("other.example/acme/app"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.hold(false)
			f.ws.set(placement)

			_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

			require.ErrorIs(t, err, change.ErrNoRepo)
			assert.Empty(t, f.host.Opens())
		})
	}
}

func TestOpen_AHostThatCannotBeActedOnIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	readOnly := hostsReadOnly(f.host)
	svc, err := change.NewService(&change.Options{Hosts: readOnly, Tickets: f.tickets, Workspace: f.ws, Store: f.store, Names: f.names, Detect: codehost.LineNumberChangeDetectionStrategy{}, Logger: discard()})
	require.NoError(t, err)

	_, err = svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.ErrorIs(t, err, change.ErrNoHost)
}

func TestOpen_ATicketThatNeedsDiscussionIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET needs_discussion = true WHERE id = $1`, h.ticket.ID)
	require.NoError(t, err)

	_, err = f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.ErrorIs(t, err, ticket.ErrNeedsDiscussion)
	assert.Empty(t, f.host.Opens())
}

func TestOpen_AnAgentWithNoTicketIsRefused(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.Open(f.ctx, f.tenant, f.newAgent("idle"), &change.OpenInput{Branch: branch, Title: "t"})

	require.ErrorIs(t, err, ticket.ErrNoLease)
	assert.Empty(t, f.host.Opens())
}

func TestOpen_TheTenantMustBeTheContexts(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)

	_, err := f.svc.Open(f.ctx, uuid.New(), h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.Error(t, err)
	assert.Empty(t, f.host.Opens(), "no host was called for a tenant the call is not scoped to")
}

func TestOpen_AHostFailureRecordsNothingAndIsAudited(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.OpenErr = errors.Join(codehost.ErrRejected, errors.New("branch not pushed"))

	_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.ErrorIs(t, err, codehost.ErrRejected)
	_, cerr := f.store.Current(f.ctx, h.ticket.ID, 1)
	assert.ErrorIs(t, cerr, change.ErrNoChange)
	assert.Len(t, f.log.audited(t, audit.ChangeOperationFailed), 1)
	assert.Empty(t, f.log.audited(t, audit.ChangeOpened))
}

func TestOpen_AgainFromTheSameBranchReturnsTheChangeAndMarksADraftReady(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	first := f.open(h, true)

	again, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "Add the thing", Draft: true})
	require.NoError(t, err)
	assert.True(t, again.Existing)
	assert.Equal(t, first.URL, again.URL)
	assert.True(t, again.Draft)
	assert.Empty(t, f.host.Readied(), "still a draft")

	ready, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "Add the thing"})
	require.NoError(t, err)
	assert.True(t, ready.Existing)
	assert.False(t, ready.Draft)
	require.Len(t, f.host.Readied(), 1)
	assert.Equal(t, f.tenant, f.host.Readied()[0].Tenant)
	assert.Len(t, f.host.Opens(), 1, "one change on the host throughout")
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.False(t, rec.Draft)
	assert.Len(t, f.log.audited(t, audit.ChangeMarkedReady), 1)
}

func TestOpen_FromTheOwnBranchSupersedesAChangeRecordedUnderAnotherName(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.recordUnderOldName(h)
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8}

	opened, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.NoError(t, err)
	assert.Equal(t, int64(8), opened.Number)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(8), rec.Change.Number, "the new change is the one that counts")
	var live int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_changes WHERE ticket_id = $1 AND superseded_at IS NULL`, h.ticket.ID).Scan(&live))
	assert.Equal(t, 1, live)
}

// The webhook door finds a ticket by its PR link. A change slop-haus opened
// is found the same way as one an agent reported, with no change there.
func TestOpen_AReviewOnTheHostFindsTheTicketByItsChange(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	opened := f.open(h, false)
	_, err := f.tickets.Review(f.ctx, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: opened.URL, Handoff: "ready for review"})
	require.NoError(t, err)

	approved, err := f.tickets.ApprovePR(f.ctx, &ticket.PRReview{PR: f.host.Opened, UserID: f.approver, SubmittedAt: time.Now().Add(time.Minute)})

	require.NoError(t, err)
	assert.Equal(t, h.ticket.ID, approved.ID)
	assert.NotNil(t, approved.ApprovedAt)
}

func TestOpen_AgainFromTheSameBranchRepairsTheTicketsLink(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	opened := f.open(h, false)
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = NULL WHERE ticket_id = $1 AND number = 1`, h.ticket.ID)
	require.NoError(t, err)

	f.open(h, false)

	got, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PRURL)
	assert.Equal(t, opened.URL, *got.PRURL)
}

// A merge in flight is claimed: the change is neither replaced nor merged a
// second time until it ends.
func TestOpen_AChangeBeingMergedIsNotReplaced(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	claim, err := f.store.BeginMerge(f.ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, claim)

	_, err = f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: "another", Title: "t"})

	require.ErrorIs(t, err, change.ErrMergeInFlight)
	assert.Len(t, f.host.Opens(), 1, "the host was not asked to open a second change")
}

// The link, the dropped review and the record of the change are written
// together: a record that cannot be written leaves the ticket as it was.
func TestOpen_ARecordThatCannotBeWrittenLeavesTheTicketAsItWas(t *testing.T) {
	f := newFixture(t)
	other := f.hold2()
	_, err := f.openRecord(f.ctx, &change.Record{TicketID: other.ticket.ID, Change: f.host.Opened, Head: "x", Base: "main", OpenedBy: other.agent})
	require.NoError(t, err, "another ticket already has that change")
	h := f.hold(true)
	f.approve(h)

	_, err = f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "https://git.example/acme/app/pull/7", "the agent is told where the orphan is")
	got, gerr := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, gerr)
	assert.Nil(t, got.PRURL)
	assert.NotNil(t, got.ApprovedAt, "the approval stands: nothing replaced the reviewed change")
}

func TestNewService_RequiresAChangeDetectionStrategy(t *testing.T) {
	f := newFixture(t)
	_, err := change.NewService(&change.Options{Hosts: hostsOf(f), Tickets: f.tickets, Workspace: f.ws, Store: f.store, Names: f.names, Logger: discard()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Detect")
}
