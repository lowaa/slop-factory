package githubhook_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A signed delivery for an archived tenant is acknowledged (so GitHub does
// not retry or disable the hook) and does nothing, leaving no record; once
// unarchived the same delivery is handled.
func TestReceive_AnArchivedTenantsDeliveryIsAcknowledgedAndDropped(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	d := delivery{app: ep, event: "pull_request_review", id: "while-archived",
		body: reviewBody("approved", reviewerGitHubID, time.Now(), "")}

	dbtest.ArchiveTenant(t, f.database, f.a.id)
	rec := f.deliver(d)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "ignored: this workspace is archived\n", rec.Body.String())
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
	assert.Empty(t, f.deliveries(f.a))

	dbtest.UnarchiveTenant(t, f.database, f.a.id)
	rec = f.deliver(d)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, ticket.StatusOpen, f.status(f.a, reviewed.ID))
}

// The answer is for GitHub alone: an unsigned delivery learns nothing of the
// tenant's state.
func TestReceive_AnArchivedTenantsUnsignedDeliveryIsStillRefused(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	dbtest.ArchiveTenant(t, f.database, f.a.id)

	rec := f.deliver(delivery{app: ep, event: "ping", body: `{}`, secret: "wrong"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
