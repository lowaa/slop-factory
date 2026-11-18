package consent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/consent"
)

var ctx = context.Background()

func user(id uuid.UUID) *consent.Subject {
	return &consent.Subject{HasUser: true, UserID: id, SessionID: "session-a"}
}

func decisionCode(t *testing.T, err error) string {
	t.Helper()
	var de *consent.DecisionError
	require.True(t, errors.As(err, &de), "want a decision error, got %v", err)
	return de.Code
}

func TestNothingIsGrantedUntilDecided(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.Resolve(ctx, user(newUser(t)), false)
	require.NoError(t, err)

	assert.True(t, res.Effective.Granted("necessary"))
	for _, p := range []string{"analytics", "interaction_recording", "marketing", "health_data_processing"} {
		assert.False(t, res.Effective.Granted(p), p)
	}
	var ids []string
	for _, s := range res.Effective.NeedingDecision() {
		ids = append(ids, s.Purpose.ID)
	}
	assert.Equal(t, []string{"analytics", "interaction_recording", "marketing", "health_data_processing"}, ids,
		"a purpose with no decision needs one; necessary never does")
}

func TestDecisionsAreAppendedAndTheLatestIsCurrent(t *testing.T) {
	f := newFixture(t)
	u := newUser(t)
	sub := user(u)

	res, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)
	assert.True(t, res.Effective.Granted("analytics"))
	assert.NotZero(t, res.Effective.State("analytics").EventID)

	f.clock.Advance(time.Minute)
	based := res.Effective.State("analytics").EventID
	res, err = f.svc.Decide(ctx, sub, in(consent.SourceSettings,
		consent.DecisionInput{Purpose: "analytics", Granted: false, DocumentVersion: "v1", BasedOn: based}))
	require.NoError(t, err)
	assert.False(t, res.Effective.Granted("analytics"))
	assert.False(t, res.Effective.State("analytics").NeedsDecision, "a denial is a decision")

	hist, err := f.svc.History(ctx, u)
	require.NoError(t, err)
	require.Len(t, hist, 2, "the grant is still on record: that is the proof of consent")
	assert.True(t, hist[0].Granted)
	assert.Equal(t, consent.SourceBanner, hist[0].Source)
	assert.Equal(t, consent.SourceSettings, hist[1].Source)
	assert.Equal(t, "session-a", hist[1].SessionID)
}

func TestRePromptOnANewMaterialDocumentVersion(t *testing.T) {
	f := newFixture(t)
	sub := user(newUser(t))
	_, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("analytics", true), grant("health_data_processing", true)))
	require.NoError(t, err)

	t.Run("a non-material version does not ask again, but new decisions cite it", func(t *testing.T) {
		f.docs.Publish("cookie_policy", consent.DocVersion{Version: "v1.1", Effective: jan1.Add(24 * time.Hour), Material: false})
		res, err := f.svc.Resolve(ctx, sub, false)
		require.NoError(t, err)
		assert.True(t, res.Effective.Granted("analytics"))
		assert.False(t, res.Effective.State("analytics").NeedsDecision)
		assert.Equal(t, "v1.1", res.Effective.State("analytics").DocumentVersion)

		_, err = f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("marketing", true))) // cites v1
		assert.Equal(t, consent.CodeDocumentChanged, decisionCode(t, err))
	})

	t.Run("a material version needs a decision, and until then the purpose is denied", func(t *testing.T) {
		f.docs.Publish("cookie_policy", consent.DocVersion{Version: "v2", Effective: jan1.Add(48 * time.Hour), Material: true})
		res, err := f.svc.Resolve(ctx, sub, false)
		require.NoError(t, err)
		assert.False(t, res.Effective.Granted("analytics"))
		assert.True(t, res.Effective.State("analytics").NeedsDecision)
		assert.True(t, res.Effective.State("analytics").DecisionGranted, "the old grant is still the record")

		assert.True(t, res.Effective.Granted("health_data_processing"), "another document's purposes are not asked again")
		assert.False(t, res.Effective.State("health_data_processing").NeedsDecision)
	})

	t.Run("deciding under the new version clears it", func(t *testing.T) {
		res, err := f.svc.Resolve(ctx, sub, false)
		require.NoError(t, err)
		based := res.Effective.State("analytics").EventID
		res, err = f.svc.Decide(ctx, sub, in(consent.SourceBanner,
			consent.DecisionInput{Purpose: "analytics", Granted: true, DocumentVersion: "v2", BasedOn: based}))
		require.NoError(t, err)
		assert.True(t, res.Effective.Granted("analytics"))
		assert.False(t, res.Effective.State("analytics").NeedsDecision)
	})
}

func TestACapabilityGatedPurposeDisappearsWhileItsFlagIsOff(t *testing.T) {
	f := newFixture(t)
	sub := user(newUser(t))
	_, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("interaction_recording", true)))
	require.NoError(t, err)

	f.caps["interaction_recording"] = false
	res, err := f.svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.Nil(t, res.Effective.State("interaction_recording"), "not offered")
	assert.False(t, res.Effective.Granted("interaction_recording"), "and denied, whatever was granted")
	assert.Empty(t, filter(res.Effective.NeedingDecision(), "interaction_recording"))

	_, err = f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("interaction_recording", true)))
	assert.Equal(t, consent.CodeInvalidPurpose, decisionCode(t, err))

	f.caps["interaction_recording"] = true
	res, err = f.svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.True(t, res.Effective.Granted("interaction_recording"), "a grant still current is back with its flag")
}

func filter(ss []*consent.PurposeState, id string) []*consent.PurposeState {
	var out []*consent.PurposeState
	for _, s := range ss {
		if s.Purpose.ID == id {
			out = append(out, s)
		}
	}
	return out
}

func TestDecisionsAreValidated(t *testing.T) {
	f := newFixture(t)
	sub := user(newUser(t))
	anon := &consent.Subject{}

	cases := []struct {
		name string
		sub  *consent.Subject
		in   *consent.DecideInput
		code string
	}{
		{"unknown purpose", sub, in(consent.SourceBanner, grant("nope", true)), consent.CodeInvalidPurpose},
		{"necessary purpose", sub, in(consent.SourceBanner, grant("necessary", false)), consent.CodeNecessary},
		{"a client can't claim gpc as its source", sub, in(consent.SourceGPC, grant("analytics", true)), consent.CodeInvalidSource},
		{"duplicate purpose", sub, in(consent.SourceBanner, grant("analytics", true), grant("analytics", false)), consent.CodeDuplicatePurpose},
		{"processing needs a signed-in user", anon, in(consent.SourceBanner, grant("health_data_processing", true)), consent.CodeSignInRequired},
		{"stale document", sub, in(consent.SourceBanner, consent.DecisionInput{Purpose: "analytics", Granted: true, DocumentVersion: "v0"}), consent.CodeDocumentChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.Decide(ctx, c.sub, c.in)
			assert.Equal(t, c.code, decisionCode(t, err))
		})
	}
	hist, err := f.svc.History(ctx, sub.UserID)
	require.NoError(t, err)
	assert.Empty(t, hist, "a refused batch records nothing")
}

func TestAStaleDecisionIsRefused(t *testing.T) {
	f := newFixture(t)
	sub := user(newUser(t))
	_, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)

	// A second tab that loaded the state before the first decision.
	_, err = f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("analytics", false)))
	assert.Equal(t, consent.CodeConsentChanged, decisionCode(t, err))

	res, err := f.svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.True(t, res.Effective.Granted("analytics"), "the stale write did not bring an old choice back")
}

func TestConcurrentDecisionsDoNotLoseAnEvent(t *testing.T) {
	t.Run("on different purposes both land", func(t *testing.T) {
		f := newFixture(t)
		sub := user(newUser(t))
		purposes := []string{"analytics", "marketing", "interaction_recording", "health_data_processing"}
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, len(purposes))
		for i, p := range purposes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				d := consent.DecisionInput{Purpose: p, Granted: true, DocumentVersion: "v1"}
				_, errs[i] = f.svc.Decide(ctx, sub, in(consent.SourceBanner, d))
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		hist, err := f.svc.History(ctx, sub.UserID)
		require.NoError(t, err)
		var got []string
		for _, e := range hist {
			assert.True(t, e.Granted)
			got = append(got, e.Purpose)
		}
		assert.ElementsMatch(t, purposes, got)
	})

	t.Run("on one purpose exactly one wins and the rest are told", func(t *testing.T) {
		f := newFixture(t)
		sub := user(newUser(t))
		const n = 8
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("analytics", i%2 == 0)))
			}()
		}
		close(start)
		wg.Wait()
		won := 0
		for _, err := range errs {
			if err == nil {
				won++
				continue
			}
			assert.Equal(t, consent.CodeConsentChanged, decisionCode(t, err))
		}
		assert.Equal(t, 1, won)
		hist, err := f.svc.History(ctx, sub.UserID)
		require.NoError(t, err)
		assert.Len(t, hist, 1)
	})
}

func TestGlobalPrivacyControl(t *testing.T) {
	t.Run("a signed-in user's sale/share purposes are denied and the opt-out is recorded once", func(t *testing.T) {
		f := newFixture(t)
		u := newUser(t)
		sub := user(u)
		_, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("marketing", true), grant("analytics", true)))
		require.NoError(t, err)

		gpc := user(u)
		gpc.GPC = true
		for i := 0; i < 3; i++ {
			res, err := f.svc.Resolve(ctx, gpc, false)
			require.NoError(t, err)
			assert.False(t, res.Effective.Granted("marketing"))
			assert.True(t, res.Effective.State("marketing").GPCLocked)
			assert.True(t, res.Effective.Granted("analytics"), "GPC does not deny first-party analytics")
		}
		hist, err := f.svc.History(ctx, u)
		require.NoError(t, err)
		require.Len(t, hist, 3, "two grants and one gpc denial, however many requests carried the signal")
		assert.Equal(t, consent.SourceGPC, hist[2].Source)
		assert.Equal(t, "marketing", hist[2].Purpose)
		assert.False(t, hist[2].Granted)

		// The opt-out outlives the signal: another browser, no GPC.
		res, err := f.svc.Resolve(ctx, user(u), false)
		require.NoError(t, err)
		assert.False(t, res.Effective.Granted("marketing"))
	})

	t.Run("granting a sale/share purpose under the signal is refused", func(t *testing.T) {
		f := newFixture(t)
		for _, sub := range []*consent.Subject{user(newUser(t)), {}} {
			sub.GPC = true
			_, err := f.svc.Decide(ctx, sub, in(consent.SourceSettings, grant("marketing", true)))
			assert.Equal(t, consent.CodeGPCActive, decisionCode(t, err))
		}
	})

	t.Run("an anonymous visitor is denied per request and nothing is recorded", func(t *testing.T) {
		f := newFixture(t)
		anon := &consent.Subject{}
		res, err := f.svc.Decide(ctx, anon, in(consent.SourceBanner, grant("marketing", true)))
		require.NoError(t, err)
		require.True(t, res.Effective.Granted("marketing"))

		anon.Cookie = f.openCookie(t, res.Cookie)
		anon.GPC = true
		res, err = f.svc.Resolve(ctx, anon, true)
		require.NoError(t, err)
		assert.False(t, res.Effective.Granted("marketing"))
		assert.Nil(t, res.Cookie, "the cookie is not rewritten")
	})
}

func (f *fixture) openCookie(t *testing.T, s *consent.CookieState) *consent.CookieState {
	t.Helper()
	v, err := f.codec.Encode(s)
	require.NoError(t, err)
	got := f.codec.OpenValue(v, f.clock.Now())
	require.NotNil(t, got)
	return got
}

func TestAnonymousVisitorsMergeIntoTheRecordAtSignIn(t *testing.T) {
	// anonymousDecides returns the cookie a visitor holds after deciding.
	anonymousDecides := func(t *testing.T, f *fixture, prev *consent.CookieState, ds ...consent.DecisionInput) *consent.CookieState {
		t.Helper()
		res, err := f.svc.Decide(ctx, &consent.Subject{Cookie: prev}, in(consent.SourceBanner, ds...))
		require.NoError(t, err)
		return f.openCookie(t, res.Cookie)
	}

	t.Run("a newer cookie decision is merged", func(t *testing.T) {
		f := newFixture(t)
		u := newUser(t)
		_, err := f.svc.Decide(ctx, user(u), in(consent.SourceSettings, grant("analytics", false)))
		require.NoError(t, err)
		f.clock.Advance(time.Hour)
		cookie := anonymousDecides(t, f, nil, grant("analytics", true), grant("marketing", true))

		sub := user(u)
		sub.Cookie = cookie
		res, err := f.svc.Resolve(ctx, sub, true)
		require.NoError(t, err)
		assert.True(t, res.Effective.Granted("analytics"))
		assert.True(t, res.Effective.Granted("marketing"))

		hist, err := f.svc.History(ctx, u)
		require.NoError(t, err)
		require.Len(t, hist, 3)
		assert.True(t, hist[1].MergedFromAnonymous)
		assert.Equal(t, consent.SourceBanner, hist[1].Source, "the original source is kept")

		require.NotNil(t, res.Cookie, "the cookie is rewritten from the record")
		assert.Empty(t, res.Cookie.MergeToken)
		for _, d := range res.Cookie.Decisions {
			assert.False(t, d.Unmerged)
		}
	})

	t.Run("the record wins when it is newer", func(t *testing.T) {
		f := newFixture(t)
		u := newUser(t)
		cookie := anonymousDecides(t, f, nil, grant("analytics", true))
		f.clock.Advance(time.Hour)
		_, err := f.svc.Decide(ctx, user(u), in(consent.SourceSettings, grant("analytics", false)))
		require.NoError(t, err)

		sub := user(u)
		sub.Cookie = cookie
		res, err := f.svc.Resolve(ctx, sub, true)
		require.NoError(t, err)
		assert.False(t, res.Effective.Granted("analytics"))
		hist, err := f.svc.History(ctx, u)
		require.NoError(t, err)
		assert.Len(t, hist, 1, "nothing merged")
	})

	t.Run("a cookie is merged once, so a second account on the browser gets nothing", func(t *testing.T) {
		f := newFixture(t)
		cookie := anonymousDecides(t, f, nil, grant("analytics", true))
		first, second := newUser(t), newUser(t)

		sub := user(first)
		sub.Cookie = cookie
		_, err := f.svc.Resolve(ctx, sub, true)
		require.NoError(t, err)

		// The first account's response never reached the browser, or the
		// visitor signs in as someone else with the same cookie.
		sub = user(second)
		sub.Cookie = cookie
		res, err := f.svc.Resolve(ctx, sub, true)
		require.NoError(t, err)
		assert.False(t, res.Effective.Granted("analytics"))
		hist, err := f.svc.History(ctx, second)
		require.NoError(t, err)
		assert.Empty(t, hist)
		require.NotNil(t, res.Cookie)
		assert.Empty(t, res.Cookie.MergeToken, "the spent token is dropped")
	})

	t.Run("choices carried across a sign-in are not merged into the next person's record", func(t *testing.T) {
		f := newFixture(t)
		first, second := newUser(t), newUser(t)
		res, err := f.svc.Decide(ctx, user(first), in(consent.SourceBanner, grant("analytics", true)))
		require.NoError(t, err)
		require.NotNil(t, res.Cookie, "a signed-in decision refreshes the cookie")
		afterSignIn := f.openCookie(t, res.Cookie)

		// Signed out, the visitor decides something else: the old choice
		// stays in the cookie, flagged as not made while signed out.
		f.clock.Advance(time.Hour)
		cookie := anonymousDecides(t, f, afterSignIn, grant("marketing", true))
		require.True(t, cookie.Decisions["marketing"].Unmerged)
		require.False(t, cookie.Decisions["analytics"].Unmerged)

		sub := user(second)
		sub.Cookie = cookie
		_, err = f.svc.Resolve(ctx, sub, true)
		require.NoError(t, err)
		hist, err := f.svc.History(ctx, second)
		require.NoError(t, err)
		require.Len(t, hist, 1)
		assert.Equal(t, "marketing", hist[0].Purpose)
	})

	t.Run("processing purposes are never in an anonymous cookie", func(t *testing.T) {
		f := newFixture(t)
		cookie := anonymousDecides(t, f, nil, grant("analytics", true))
		assert.NotContains(t, cookie.Decisions, "health_data_processing")
		res, err := f.svc.Effective(ctx, &consent.Subject{Cookie: cookie})
		require.NoError(t, err)
		assert.Nil(t, res.State("health_data_processing"))
	})

	t.Run("a grant of a sale purpose is not merged under the signal", func(t *testing.T) {
		f := newFixture(t)
		u := newUser(t)
		cookie := anonymousDecides(t, f, nil, grant("marketing", true))
		sub := user(u)
		sub.Cookie, sub.GPC = cookie, true
		res, err := f.svc.Resolve(ctx, sub, true)
		require.NoError(t, err)
		assert.False(t, res.Effective.Granted("marketing"))
		hist, err := f.svc.History(ctx, u)
		require.NoError(t, err)
		for _, e := range hist {
			assert.False(t, e.Granted && e.Purpose == "marketing")
		}
	})
}

func TestAnAnonymousCookieExpiresIntoNeedingADecision(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.Decide(ctx, &consent.Subject{}, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)
	value, err := f.codec.Encode(res.Cookie)
	require.NoError(t, err)

	assert.NotNil(t, f.codec.OpenValue(value, f.clock.Now().Add(consent.DefaultCookieLifetime-time.Hour)))
	assert.Nil(t, f.codec.OpenValue(value, f.clock.Now().Add(consent.DefaultCookieLifetime+time.Hour)),
		"expired: the visitor is asked again")
}

func TestADecisionBeforeAnyGETStillMergesTheCookie(t *testing.T) {
	f := newFixture(t)
	u := newUser(t)
	res, err := f.svc.Decide(ctx, &consent.Subject{}, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)

	f.clock.Advance(time.Minute)
	sub := user(u)
	sub.Cookie = f.openCookie(t, res.Cookie)
	res, err = f.svc.Decide(ctx, sub, in(consent.SourceSettings, grant("marketing", true)))
	require.NoError(t, err)

	assert.True(t, res.Effective.Granted("analytics"), "what the visitor chose signed out is in the record")
	assert.True(t, res.Effective.Granted("marketing"))
	assert.True(t, res.Cookie.Decisions["analytics"].Granted, "and in the rewritten cookie")
}

// applyFails is a store whose writes fail and whose reads work.
type applyFails struct{ consent.Store }

func (applyFails) Apply(context.Context, uuid.UUID, func(map[string]*consent.Event) (*consent.Plan, error)) (*consent.Applied, error) {
	return nil, errors.New("write failed")
}

func TestAFailedGPCWriteStillDeniesWithoutLosingTheRestOfTheConsent(t *testing.T) {
	f := newFixture(t)
	u := newUser(t)
	_, err := f.svc.Decide(ctx, user(u), in(consent.SourceBanner, grant("analytics", true), grant("marketing", true)))
	require.NoError(t, err)

	svc, err := consent.NewService(&consent.Config{
		Registry: f.svc.Registry(), Capabilities: f.caps, Documents: f.docs, Store: applyFails{f.store}, Cookie: f.codec, Clock: f.clock.Now,
	})
	require.NoError(t, err)
	sub := user(u)
	sub.GPC = true
	res, err := svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.False(t, res.Effective.Granted("marketing"))
	assert.True(t, res.Effective.Granted("analytics"))
}

func TestAPurposeDecidedWithTheMergeIsNotMergedOverIt(t *testing.T) {
	f := newFixture(t)
	u := newUser(t)
	res, err := f.svc.Decide(ctx, &consent.Subject{}, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)

	f.clock.Advance(time.Minute)
	sub := user(u)
	sub.Cookie = f.openCookie(t, res.Cookie)
	// The client never loaded the merged state: based_on is 0 for analytics.
	res, err = f.svc.Decide(ctx, sub, in(consent.SourceSettings, grant("analytics", false)))
	require.NoError(t, err, "the decision is not refused because the merge wrote an event first")
	assert.False(t, res.Effective.Granted("analytics"))
	hist, err := f.svc.History(ctx, u)
	require.NoError(t, err)
	assert.Len(t, hist, 1, "the cookie's older choice for the same purpose is superseded, not recorded")
}

func TestASpentMergeTokenDoesNotSwallowADecision(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.Decide(ctx, &consent.Subject{}, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)
	cookie := f.openCookie(t, res.Cookie)

	first, second := newUser(t), newUser(t)
	sub := user(first)
	sub.Cookie = cookie
	_, err = f.svc.Resolve(ctx, sub, true)
	require.NoError(t, err)

	f.clock.Advance(time.Minute)
	sub = user(second)
	sub.Cookie = cookie
	res2, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("marketing", true)))
	require.NoError(t, err)
	assert.True(t, res2.Effective.Granted("marketing"), "the person's own decision lands")
	assert.False(t, res2.Effective.Granted("analytics"), "the spent cookie's does not")
}

func TestOnlyVersionsInForceCount(t *testing.T) {
	f := newFixture(t)
	sub := user(newUser(t))
	_, err := f.svc.Decide(ctx, sub, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)

	f.docs.Publish("cookie_policy", consent.DocVersion{Version: "v2", Effective: f.clock.Now().Add(24 * time.Hour), Material: true})
	res, err := f.svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.True(t, res.Effective.Granted("analytics"), "a scheduled version doesn't deny anyone early")
	assert.Equal(t, "v1", res.Effective.State("analytics").DocumentVersion)

	f.clock.Advance(25 * time.Hour)
	res, err = f.svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.True(t, res.Effective.State("analytics").NeedsDecision, "and when it takes effect, it asks")
}

func TestEveryOfferedSaleOrSharePurposeIsHandled(t *testing.T) {
	reg, err := consent.NewRegistry(
		consent.Purpose{ID: "marketing", Kind: consent.CookieCategory, Document: "cookie_policy", SaleOrShare: true},
		consent.Purpose{ID: "ad_measurement", Kind: consent.CookieCategory, Document: "cookie_policy", SaleOrShare: true},
		consent.Purpose{ID: "analytics", Kind: consent.CookieCategory, Document: "cookie_policy"},
	)
	require.NoError(t, err)
	f := newFixture(t)
	svc, err := consent.NewService(&consent.Config{Registry: reg, Documents: f.docs, Store: f.store, Cookie: f.codec, Clock: f.clock.Now})
	require.NoError(t, err)

	u := newUser(t)
	_, err = svc.Decide(ctx, user(u), in(consent.SourceBanner, grant("marketing", true), grant("ad_measurement", true), grant("analytics", true)))
	require.NoError(t, err)
	sub := user(u)
	sub.GPC = true
	res, err := svc.Resolve(ctx, sub, false)
	require.NoError(t, err)
	assert.False(t, res.Effective.Granted("marketing"))
	assert.False(t, res.Effective.Granted("ad_measurement"))
	assert.True(t, res.Effective.Granted("analytics"))
	hist, err := svc.History(ctx, u)
	require.NoError(t, err)
	gpc := map[string]bool{}
	for _, e := range hist {
		if e.Source == consent.SourceGPC {
			gpc[e.Purpose] = true
		}
	}
	assert.Equal(t, map[string]bool{"marketing": true, "ad_measurement": true}, gpc)

	// And neither is merged as a grant while the signal is on.
	anon, err := svc.Decide(ctx, &consent.Subject{}, in(consent.SourceBanner, grant("marketing", true), grant("ad_measurement", true)))
	require.NoError(t, err)
	other := newUser(t)
	sub = user(other)
	sub.GPC = true
	sub.Cookie = f.openCookie(t, anon.Cookie)
	_, err = svc.Resolve(ctx, sub, true)
	require.NoError(t, err)
	hist, err = svc.History(ctx, other)
	require.NoError(t, err)
	for _, e := range hist {
		assert.False(t, e.Granted, e.Purpose)
	}
}

func TestTheRegistryHandsOutCopies(t *testing.T) {
	f := newFixture(t)
	f.svc.Registry().Get("marketing").SaleOrShare = false
	f.svc.Registry().Purposes()[0].Necessary = false
	res, err := f.svc.Effective(ctx, &consent.Subject{GPC: true})
	require.NoError(t, err)
	assert.True(t, res.State("marketing").GPCLocked)
	res.State("marketing").Purpose.SaleOrShare = false
	res2, err := f.svc.Effective(ctx, &consent.Subject{GPC: true})
	require.NoError(t, err)
	assert.True(t, res2.State("marketing").GPCLocked)
	assert.True(t, res2.Granted("necessary"))
}

func TestGPCOptOutIsKeptWhileASalePurposeIsSwitchedOff(t *testing.T) {
	reg, err := consent.NewRegistry(
		consent.Purpose{ID: "ad_measurement", Kind: consent.CookieCategory, Document: "cookie_policy", SaleOrShare: true, Capability: "ads"},
	)
	require.NoError(t, err)
	f := newFixture(t)
	caps := consent.StaticCapabilities{"ads": true}
	svc, err := consent.NewService(&consent.Config{Registry: reg, Capabilities: caps, Documents: f.docs, Store: f.store, Cookie: f.codec, Clock: f.clock.Now})
	require.NoError(t, err)

	u := newUser(t)
	_, err = svc.Decide(ctx, user(u), in(consent.SourceBanner, grant("ad_measurement", true)))
	require.NoError(t, err)
	caps["ads"] = false
	sub := user(u)
	sub.GPC = true
	_, err = svc.Resolve(ctx, sub, false)
	require.NoError(t, err)

	caps["ads"] = true
	res, err := svc.Resolve(ctx, user(u), false)
	require.NoError(t, err)
	assert.False(t, res.Effective.Granted("ad_measurement"), "the opt-out outlived both the signal and the flag")
}

func TestAnAnonymousVisitorDoesNotDependOnAProcessingDocument(t *testing.T) {
	f := newFixture(t)
	docs := &perKindDocuments{inner: f.docs, failing: "health_data_notice"}
	svc, err := consent.NewService(&consent.Config{
		Registry: f.svc.Registry(), Capabilities: f.caps, Documents: docs, Store: f.store, Cookie: f.codec, Clock: f.clock.Now,
	})
	require.NoError(t, err)

	res, err := svc.Decide(ctx, &consent.Subject{}, in(consent.SourceBanner, grant("analytics", true)))
	require.NoError(t, err)
	assert.True(t, res.Effective.Granted("analytics"))

	_, err = svc.Resolve(ctx, user(newUser(t)), false)
	assert.Error(t, err, "a signed-in user is shown that purpose, so the failure counts")
}

type perKindDocuments struct {
	inner   consent.Documents
	failing string
}

func (d *perKindDocuments) Versions(c context.Context, kind string) ([]consent.DocVersion, error) {
	if kind == d.failing {
		return nil, errors.New("documents unavailable")
	}
	return d.inner.Versions(c, kind)
}
