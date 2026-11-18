package githubhook_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The door authenticates and records; what a payload means is the Host's.
// These run the door over a Host that is not GitHub, with a body GitHub
// would never send, to show nothing past the signature reads the payload.

var prRef = codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12}

func TestReceive_ActsOnWhatTheHostReports(t *testing.T) {
	host := &codehosttest.Fake{Domain: "git.example.test", Events: map[string]*codehost.Event{
		"review": {Kind: codehost.EventReview, Change: prRef, Verdict: codehost.VerdictApproved,
			Actor: codehost.Actor{ID: reviewerGitHubID, Login: "rev"}, SubmittedAt: time.Now().Add(time.Hour)},
		"merged": {Kind: codehost.EventMerged, Change: prRef, Actor: codehost.Actor{ID: reviewerGitHubID, Login: "rev"}},
	}}
	f := newFixtureWithHost(t, host)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "review", body: `{"opaque": true}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, fmt.Sprintf("applied: #%d approved\n", reviewed.ID), rec.Body.String())
	got, err := f.a.tickets.Get(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ReviewApproved, got.ReviewOutcome())

	rec = f.deliver(delivery{app: ep, event: "merged", body: `{"opaque": true}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, fmt.Sprintf("applied: #%d closed as merged\n", reviewed.ID), rec.Body.String())
	assert.Equal(t, ticket.StatusDone, f.status(f.a, reviewed.ID))

	delivered := []byte(withInstallation(`{"opaque": true}`, ep.id))
	assert.Equal(t, []codehosttest.ParsedEvent{
		{Name: "review", Body: delivered},
		{Name: "merged", Body: delivered},
	}, host.ParsedEvents(), "the host saw the event name and the body as delivered")
}

func TestReceive_AnEventTheHostIgnoresMovesNothing(t *testing.T) {
	host := &codehosttest.Fake{Domain: "git.example.test", Events: map[string]*codehost.Event{
		"review": {Kind: codehost.EventIgnored, Ignore: "the host has its reasons"},
	}}
	f := newFixtureWithHost(t, host)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "review", body: `{}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "ignored: the host has its reasons\n", rec.Body.String())
	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
	ds := f.deliveries(f.a)
	require.Len(t, ds, 1)
	assert.Equal(t, githubhook.OutcomeIgnored, ds[0].Outcome)
}

// The signature is checked before the host is asked anything.
func TestReceive_AnUnsignedDeliveryNeverReachesTheHost(t *testing.T) {
	host := &codehosttest.Fake{Domain: "git.example.test"}
	f := newFixtureWithHost(t, host)
	ep := f.install(f.a)

	rec := f.deliver(delivery{app: ep, event: "review", body: `{}`, secret: "wrong"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, host.ParsedEvents())
}

// A ping is the door's own: GitHub's way to say the endpoint is reachable.
func TestReceive_APingIsNotTheHostsToRead(t *testing.T) {
	host := &codehosttest.Fake{Domain: "git.example.test"}
	f := newFixtureWithHost(t, host)
	ep := f.install(f.a)

	rec := f.deliver(delivery{app: ep, event: "ping", body: `{}`})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Empty(t, host.ParsedEvents())
}
