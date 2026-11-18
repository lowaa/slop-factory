package legaldocs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

// A legaldocs Service is the consent package's Documents: publishing a
// material version asks people again, and a non-material one does not.
func TestAMaterialVersionRepromptsConsent(t *testing.T) {
	ctx := context.Background()
	docs, clk := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	k := legaldocs.CookiePolicy
	require.NoError(t, docs.Publish(ctx, []*legaldocs.Version{versionOf(k, "v1", jan1, true, "one")}))

	reg, err := consent.NewRegistry(consent.Purpose{ID: "analytics", Kind: consent.CookieCategory, Document: k})
	require.NoError(t, err)
	codec, err := consent.NewCookieCodec(&consent.CookieConfig{Name: "c", Secret: []byte("0123456789abcdef0123456789abcdef"), Secure: true})
	require.NoError(t, err)
	var cs consent.Documents = docs
	svc, err := consent.NewService(&consent.Config{
		Registry: reg, Documents: cs, Store: consent.NewPostgresStore(dbtest.AppEngine()), Cookie: codec, Clock: clk.Now,
	})
	require.NoError(t, err)

	user := uuid.New()
	_, err = dbtest.AppEngine().ExecContext(ctx, `INSERT INTO global_users (id, email) VALUES ($1, $2)`, user, user.String()+"@example.test")
	require.NoError(t, err)
	sub := &consent.Subject{UserID: user, HasUser: true}
	_, err = svc.Decide(ctx, sub, &consent.DecideInput{
		Source: consent.SourceBanner, Decisions: []consent.DecisionInput{{Purpose: "analytics", Granted: true, DocumentVersion: "v1"}},
	})
	require.NoError(t, err)

	state := func() *consent.PurposeState {
		eff, err := svc.Effective(ctx, sub)
		require.NoError(t, err)
		require.Len(t, eff.States, 1)
		return eff.States[0]
	}
	assert.True(t, state().Granted)

	require.NoError(t, docs.Publish(ctx, []*legaldocs.Version{versionOf(k, "v1.1", jan1.AddDate(0, 1, 0), false, "typo fix")}))
	assert.True(t, state().Granted, "a non-material version does not ask again")
	assert.False(t, state().NeedsDecision)

	require.NoError(t, docs.Publish(ctx, []*legaldocs.Version{versionOf(k, "v2", jan1.AddDate(0, 2, 0), true, "new purposes")}))
	assert.False(t, state().Granted, "a material version withdraws the earlier grant")
	assert.True(t, state().NeedsDecision)
	assert.Equal(t, "v2", state().DocumentVersion, "and asks about the new text")
}

func TestVersionsReportsEveryPublishedVersionForConsentToFilter(t *testing.T) {
	ctx := context.Background()
	docs, _ := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	k := legaldocs.PrivacyPolicy
	require.NoError(t, docs.Publish(ctx, []*legaldocs.Version{
		versionOf(k, "v1", jan1, true, "one"), versionOf(k, "v2", jun1, false, "later"),
	}))
	vs, err := docs.Versions(ctx, k)
	require.NoError(t, err)
	assert.Equal(t, []consent.DocVersion{
		{Version: "v1", Effective: jan1, Material: true},
		{Version: "v2", Effective: jun1, Material: false},
	}, vs, "consent applies the effective date itself")
}

func TestVersionsErrorsForAKindWithNone(t *testing.T) {
	docs, _ := newService(t, legaldocs.NewPostgresStore(dbtest.AppEngine()), jan1)
	_, err := docs.Versions(context.Background(), "empty_notice")
	require.Error(t, err)
}
