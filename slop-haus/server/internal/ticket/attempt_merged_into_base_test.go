package ticket_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var sharedRef = codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 13}

// mergedIntoBaseFixture is a ticket whose attempt 2 is unpromoted and has
// sharedRef as its live change.
func mergedIntoBaseFixture(t *testing.T) (*fixture, *ticket.Service, *audittest.Counter, *bytes.Buffer, int64) {
	t.Helper()
	f := newFixture(t)
	var logged bytes.Buffer
	counter := audittest.NewCounter()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(codehosttest.NewLinkHost(prHost))),
		ticket.WithEvents(audit.New(slog.NewJSONHandler(&logged, nil), counter)))
	tk := f.ticket(t, "Experiment")
	require.Equal(t, 2, f.addAttempt(t, tk.ID, true))
	opener := f.agent(t, "opener")
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, 2, 'github.com', 'acme', 'widgets', 13, 'ticket-1-a2', $2, true, $3)`, tk.ID, ticket.UnpromotedBase(tk.ID), opener)
	require.NoError(t, err)
	return f, svc, counter, &logged, tk.ID
}

func TestMergedIntoBase_MovesOnlyThatAttemptAndIsAudited(t *testing.T) {
	t.Parallel()
	f, svc, counter, logged, id := mergedIntoBaseFixture(t)

	got, err := svc.MergedIntoBase(f.ctx, ticket.AttemptRef{TicketID: id, Number: 2}, sharedRef, ticket.Merger{UserID: &f.human})

	require.NoError(t, err)
	assert.Equal(t, 2, got.MatchedAttempt)
	assert.Equal(t, 1, counter.Count(audit.ChangeMergedIntoBase))
	assert.Contains(t, logged.String(), ticket.UnpromotedBase(id))
	var states string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT string_agg(number || ':' || status, ',' ORDER BY number) FROM attempts WHERE ticket_id = $1`, id).Scan(&states))
	assert.Equal(t, "1:queued,2:discussion", states)
}

func TestMergedIntoBase_ByANonMemberIsAuditedUnderTheirGitHubIdentity(t *testing.T) {
	t.Parallel()
	f, svc, counter, logged, id := mergedIntoBaseFixture(t)

	_, err := svc.MergedIntoBase(f.ctx, ticket.AttemptRef{TicketID: id, Number: 2}, sharedRef,
		ticket.Merger{GitHubLogin: "merge-bot", GitHubID: 4002})

	require.NoError(t, err)
	assert.Equal(t, 1, counter.Count(audit.ChangeMergedIntoBase))
	assert.Contains(t, logged.String(), `"github_login":"merge-bot"`)
	assert.Contains(t, logged.String(), `"github_id":4002`)
	assert.NotContains(t, logged.String(), `"user_id"`)
}

func TestMergedIntoBase_RefusesWhatIsNotTheAttemptsLiveChange(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		attempt int
		ref     codehost.ChangeRef
	}{
		"the promoted attempt": {1, sharedRef},
		"another change":       {2, codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 99}},
		"a missing attempt":    {7, sharedRef},
	} {
		t.Run(name, func(t *testing.T) {
			f, svc, counter, _, id := mergedIntoBaseFixture(t)

			_, err := svc.MergedIntoBase(f.ctx, ticket.AttemptRef{TicketID: id, Number: tc.attempt}, tc.ref, ticket.Merger{UserID: &f.human})

			require.ErrorIs(t, err, ticket.ErrNotMergeable)
			assert.Zero(t, counter.Count(audit.ChangeMergedIntoBase), "a refused call is not recorded")
			var discussing int
			require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM attempts WHERE ticket_id = $1 AND status = 'discussion'`, id).Scan(&discussing))
			assert.Zero(t, discussing)
		})
	}
}
