package consent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/consent/consenttest"
)

func TestPostgresStore(t *testing.T) {
	consenttest.StoreSuite(t, consent.NewPostgresStore(dbtest.AppEngine()), newUser)
}

// The record is append-only in the database itself, for the application's
// own role: the role is granted UPDATE and DELETE on every table, so only
// the trigger stands in the way.
func TestConsentEventsAreAppendOnlyInTheDatabase(t *testing.T) {
	ctx := context.Background()
	eng := dbtest.AppEngine()
	store := consent.NewPostgresStore(eng)
	u := newUser(t)
	_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
		return &consent.Plan{Events: []consent.NewEvent{{Purpose: "analytics", Granted: true, DocumentKind: "cookie_policy",
			DocumentVersion: "v1", Source: consent.SourceBanner, DecidedAt: time.Now()}}}, nil
	})
	require.NoError(t, err)

	for name, stmt := range map[string]string{
		"update":   `UPDATE global_consent_events SET granted = false WHERE user_id = $1`,
		"delete":   `DELETE FROM global_consent_events WHERE user_id = $1`,
		"truncate": `TRUNCATE global_consent_events`,
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if name == "truncate" {
				_, err = eng.ExecContext(ctx, stmt)
			} else {
				_, err = eng.ExecContext(ctx, stmt, u)
			}
			require.Error(t, err)
			if name != "truncate" { // the app role has no TRUNCATE grant; the trigger covers the owner
				assert.Contains(t, err.Error(), "append-only")
			}
		})
	}

	hist, err := store.History(ctx, u)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.True(t, hist[0].Granted)
}

func TestDeletingAUserWithConsentHistoryIsRefused(t *testing.T) {
	ctx := context.Background()
	store := consent.NewPostgresStore(dbtest.AppEngine())
	u := newUser(t)
	_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
		return &consent.Plan{Events: []consent.NewEvent{{Purpose: "analytics", Granted: true, DocumentKind: "cookie_policy",
			DocumentVersion: "v1", Source: consent.SourceBanner, DecidedAt: time.Now()}}}, nil
	})
	require.NoError(t, err)
	_, err = dbtest.AppEngine().ExecContext(ctx, `DELETE FROM global_users WHERE id = $1`, u)
	require.Error(t, err, "erasing the account must decide what happens to its proof of consent")
}

func TestStoreErrorsDoNotCarryRowValues(t *testing.T) {
	store := consent.NewPostgresStore(dbtest.AppEngine())
	u := newUser(t)
	_, err := store.Apply(context.Background(), u, func(map[string]*consent.Event) (*consent.Plan, error) {
		// An unknown source breaks the CHECK constraint, whose detail would
		// quote the row.
		return &consent.Plan{Events: []consent.NewEvent{{Purpose: "analytics", DocumentKind: "k", DocumentVersion: "v",
			Source: "not-a-source", DecidedAt: time.Now(), SessionID: "session-secret-value"}}}, nil
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), u.String())
	assert.NotContains(t, err.Error(), "session-secret-value")
	assert.NotContains(t, err.Error(), "not-a-source")
}

func TestMergeTokensAreStoredHashed(t *testing.T) {
	ctx := context.Background()
	eng := dbtest.AppEngine()
	store := consent.NewPostgresStore(eng)
	u := newUser(t)
	token := "plain-merge-token-" + u.String()
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])

	_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
		return &consent.Plan{MergeToken: token, Events: []consent.NewEvent{{Purpose: "analytics", Granted: true,
			DocumentKind: "cookie_policy", DocumentVersion: "v1", Source: consent.SourceBanner, DecidedAt: time.Now(),
			MergedFromAnonymous: true, MergeToken: token}}}, nil
	})
	require.NoError(t, err)

	var claimed, onEvent string
	require.NoError(t, eng.QueryRowContext(ctx, `SELECT token_hash FROM global_consent_merge_tokens WHERE token_hash = $1`, want).Scan(&claimed))
	require.NoError(t, eng.QueryRowContext(ctx, `SELECT merge_token FROM global_consent_events WHERE user_id = $1`, u).Scan(&onEvent))
	assert.Equal(t, want, claimed)
	assert.Equal(t, want, onEvent)
	assert.False(t, strings.Contains(claimed+onEvent, token), "the cookie's token itself is not stored")
}

func TestAMergedEventNeedsItsTokenAndAPlainOneHasNone(t *testing.T) {
	store := consent.NewPostgresStore(dbtest.AppEngine())
	u := newUser(t)
	base := consent.NewEvent{Purpose: "analytics", DocumentKind: "k", DocumentVersion: "v", Source: consent.SourceBanner, DecidedAt: time.Now()}
	for name, e := range map[string]consent.NewEvent{
		"merged without a token": func() consent.NewEvent { e := base; e.MergedFromAnonymous = true; return e }(),
		"a token on a plain one": func() consent.NewEvent { e := base; e.MergeToken = "x"; return e }(),
	} {
		_, err := store.Apply(context.Background(), u, func(map[string]*consent.Event) (*consent.Plan, error) {
			return &consent.Plan{Events: []consent.NewEvent{e}}, nil
		})
		assert.Error(t, err, name)
	}
}

// Writes for one user run one after another. The second Apply has to be
// seen waiting on the database's lock while the first holds its plan open;
// nothing here depends on timing, only on that wait being observed.
func TestApplyWaitsForAnotherApplyOnTheSameUser(t *testing.T) {
	ctx := context.Background()
	eng := dbtest.AppEngine()
	store := consent.NewPostgresStore(eng)
	u := newUser(t)
	event := func(p string) consent.NewEvent {
		return consent.NewEvent{Purpose: p, Granted: true, DocumentKind: "k", DocumentVersion: "v", Source: consent.SourceBanner, DecidedAt: time.Now()}
	}

	inFirst, releaseFirst := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var firstDone sync.WaitGroup
	firstDone.Add(1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer firstDone.Done()
		_, err := store.Apply(ctx, u, func(map[string]*consent.Event) (*consent.Plan, error) {
			close(inFirst)
			<-releaseFirst
			return &consent.Plan{Events: []consent.NewEvent{event("first")}}, nil
		})
		assert.NoError(t, err)
	}()
	<-inFirst

	var sawFirst int
	go func() {
		defer wg.Done()
		_, err := store.Apply(ctx, u, func(cur map[string]*consent.Event) (*consent.Plan, error) {
			sawFirst = len(cur)
			return &consent.Plan{Events: []consent.NewEvent{event("second")}}, nil
		})
		assert.NoError(t, err)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		require.NoError(t, eng.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%global_users%FOR NO KEY UPDATE%'`).Scan(&waiting))
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			close(releaseFirst)
			wg.Wait()
			t.Fatal("the second Apply was never seen waiting on the first")
		}
		time.Sleep(10 * time.Millisecond) // a poll for a condition, not an assertion on timing
	}
	close(releaseFirst)
	wg.Wait()
	assert.Equal(t, 1, sawFirst, "the second plan saw the first's event")
}
