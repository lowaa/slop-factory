package githubhook_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// What becomes of a delivery when handling it goes wrong, or two copies of
// it arrive at once.

// flakyTickets fails every call while failing is set, and otherwise hands
// the call to the real ticket service.
type flakyTickets struct {
	githubhook.Tickets
	failing bool
}

var errDatabaseDown = errors.New("database down")

func (f *flakyTickets) ApprovePR(ctx context.Context, r *ticket.PRReview) (*ticket.Ticket, error) {
	if f.failing {
		return nil, errDatabaseDown
	}
	return f.Tickets.ApprovePR(ctx, r)
}

// A failure on our side is recorded as one, and GitHub's redelivery of the
// same delivery is handled afresh rather than turned away as a duplicate.
func TestReceive_AFailedDeliveryIsHandledAgainOnRedelivery(t *testing.T) {
	f := newFixture(t)
	flaky := &flakyTickets{Tickets: f.a.tickets, failing: true}
	f.svc = newService(t, githubhook.NewPostgresStore(f.database), flaky,
		&githubhook.Config{AppSecrets: openerFromKeyring{f.keyring}, Host: github.New()})
	f.handler = githubhook.NewReceiver(f.svc, slog.DiscardHandler).Handler()
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	d := delivery{app: ep, event: "pull_request_review", id: "retried",
		body: reviewBody("approved", reviewerGitHubID, time.Now(), "")}

	rec := f.deliver(d)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	ds := f.deliveries(f.a)
	require.Len(t, ds, 1)
	assert.Equal(t, githubhook.OutcomeError, ds[0].Outcome)

	flaky.failing = false
	rec = f.deliver(d)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, ticket.StatusOpen, f.status(f.a, reviewed.ID))
	ds = f.deliveries(f.a)
	require.Len(t, ds, 1)
	assert.Equal(t, githubhook.OutcomeApplied, ds[0].Outcome)
}

// A delivery whose handling was cut off -- the process died -- is not
// turned away for ever: once it has been processing for long enough, a
// redelivery handles it.
func TestReceive_AnAbandonedDeliveryIsHandledAgainOnRedelivery(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	store := githubhook.NewPostgresStore(f.database)
	begun, err := store.BeginDelivery(f.a.ctx, &githubhook.Delivery{ID: "cut-off", InstallationID: ep.id, Event: "pull_request_review"})
	require.NoError(t, err)
	require.True(t, begun)
	d := delivery{app: ep, event: "pull_request_review", id: "cut-off",
		body: reviewBody("approved", reviewerGitHubID, time.Now(), "")}

	assert.Equal(t, "duplicate: already handled\n", f.deliver(d).Body.String(), "still being handled, for all anyone knows")

	_, err = f.database.Exec(f.a.ctx, `UPDATE github_deliveries SET received_at = now() - interval '1 hour'`)
	require.NoError(t, err)
	rec := f.deliver(d)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, ticket.StatusOpen, f.status(f.a, reviewed.ID))
}

// Copies of one delivery racing each other: exactly one is handled.
func TestBeginDelivery_OnlyOneOfConcurrentCopiesBegins(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	store := githubhook.NewPostgresStore(f.database)

	const copies = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan bool, copies)
	for range copies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			begun, err := store.BeginDelivery(f.a.ctx, &githubhook.Delivery{ID: "raced", InstallationID: ep.id, Event: "ping"})
			assert.NoError(t, err)
			results <- begun
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	begun := 0
	for b := range results {
		if b {
			begun++
		}
	}
	assert.Equal(t, 1, begun)
}

// GitHub's delivery ids are its own; two tenants each record theirs, and
// finishing one never touches the other's.
func TestReceive_TenantsKeepTheirOwnRecordOfTheSameDeliveryID(t *testing.T) {
	f := newFixture(t)
	b := f.newTenant()
	aApp, bApp := f.install(f.a), f.install(b)

	require.Equal(t, http.StatusOK, f.deliver(delivery{app: aApp, event: "ping", id: "shared", body: `{}`}).Code)
	rec := f.deliver(delivery{app: bApp, event: "push", id: "shared", body: `{}`})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ignored", "b's copy is b's own, not a duplicate of a's")

	aDeliveries, bDeliveries := f.deliveries(f.a), f.deliveries(b)
	require.Len(t, aDeliveries, 1)
	require.Len(t, bDeliveries, 1)
	assert.Equal(t, githubhook.OutcomeApplied, aDeliveries[0].Outcome, "b's outcome did not overwrite a's")
	assert.Equal(t, aApp.id, aDeliveries[0].InstallationID)
	assert.Equal(t, githubhook.OutcomeIgnored, bDeliveries[0].Outcome)
}
