package attemptsignals_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type fakeChanges struct {
	change.Store
	rec *change.Record
	err error
}

func (f fakeChanges) Current(context.Context, int64, int) (*change.Record, error) {
	return f.rec, f.err
}

type oneHost struct{ host codehost.Changes }

func (h oneHost) Changes(string) (codehost.Changes, bool) { return h.host, true }

var changeRef = codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "app", Number: 7}

func newSource(t *testing.T) (*attemptsignals.HostSource, *codehosttest.Fake, *fakeChanges, ticket.AttemptRef, context.Context, *db.DB) {
	t.Helper()
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	var id int64
	require.NoError(t, database.QueryRow(ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	_, err := database.Exec(ctx, `INSERT INTO ticket_paths (ticket_id, path) VALUES ($1, 'pkg/')`, id)
	require.NoError(t, err)
	host := &codehosttest.Fake{Domain: "github.com"}
	changes := &fakeChanges{rec: &change.Record{Change: changeRef}}
	return attemptsignals.NewHostSource(database, changes, oneHost{host}), host, changes, ticket.AttemptRef{TicketID: id, Number: 1}, ctx, database
}

func TestHostSourceReadsDiffChecksAndScopes(t *testing.T) {
	src, host, _, ref, ctx, _ := newSource(t)
	host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{changeRef: codehost.NewChecks("h1", "main", []codehost.Check{{Name: "ci", State: codehost.CheckFailure}})}
	host.DiffOf = map[string]*codehost.Diff{"h1": {Files: []codehost.FileDiff{{Path: "pkg/a.go", Status: codehost.DiffModified, Patch: "@@ -1 +1 @@\n-a\n+b"}}}}

	in, err := src.Inputs(ctx, ref)

	require.NoError(t, err)
	assert.Equal(t, "h1", in.HeadSHA)
	assert.Equal(t, []string{"pkg/"}, in.Scopes)
	assert.Contains(t, in.Diff, "diff --git a/pkg/a.go b/pkg/a.go")
	require.NotNil(t, in.CI)
	assert.False(t, in.CI.Runs[0].Passed)
	assert.False(t, in.CI.Complete)
	assert.False(t, in.Final)
}

func TestHostSourceWithoutAChangeHasNoDiffOrChecks(t *testing.T) {
	src, _, changes, ref, ctx, _ := newSource(t)
	changes.rec, changes.err = nil, change.ErrNoChange

	in, err := src.Inputs(ctx, ref)

	require.NoError(t, err)
	assert.Empty(t, in.Diff)
	assert.Nil(t, in.CI)
	assert.Equal(t, []string{"pkg/"}, in.Scopes)
}

func TestHostSourceChecksNotYetReportedAreUnknownAndAMergedChangeIsFinal(t *testing.T) {
	src, host, changes, ref, ctx, _ := newSource(t)
	merged := now
	changes.rec.MergedAt = &merged
	host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{changeRef: codehost.NewChecks("h1", "main", []codehost.Check{{Name: "ci", State: codehost.CheckPending}})}
	host.DiffErr = codehost.ErrDiffUnsupported

	in, err := src.Inputs(ctx, ref)

	require.NoError(t, err)
	assert.Nil(t, in.CI, "pending is not a result")
	assert.Empty(t, in.Diff, "an unsupported diff is unknown")
	assert.True(t, in.Final)
}

func TestHostSourceHostFailureIsAnErrorNotUnknown(t *testing.T) {
	src, host, _, ref, ctx, _ := newSource(t)
	host.ChecksErr = errors.New("boom")

	_, err := src.Inputs(ctx, ref)

	assert.Error(t, err)
}

func TestHostSourceUnknownAttempt(t *testing.T) {
	src, _, _, ref, ctx, _ := newSource(t)

	_, err := src.Inputs(ctx, ticket.AttemptRef{TicketID: ref.TicketID, Number: 9})

	assert.ErrorIs(t, err, attemptsignals.ErrNoAttempt)
}
