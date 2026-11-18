package privacy_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/consent/consenttest"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs/legaldocstest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// go-privacy's stores run against slop-haus's own migrated tables, so a
// drift between the consent tables and the SQL the packages run fails here.
func TestConsentStoreSuiteOnSlopHausTables(t *testing.T) {
	database := dbtest.WithDB(t)
	consenttest.StoreSuite(t, consent.NewPostgresStore(database.Engine()), func(t *testing.T) uuid.UUID {
		t.Helper()
		id := uuid.New()
		_, err := database.Global().Exec(context.Background(),
			`INSERT INTO global_users (id, email, password_hash) VALUES ($1, $2, '')`, id, id.String()+"@example.test")
		require.NoError(t, err)
		return id
	})
}

func TestLegalDocsStoreSuiteOnSlopHausTables(t *testing.T) {
	database := dbtest.WithDB(t)
	legaldocstest.StoreSuite(t, legaldocs.NewPostgresStore(database.Engine()))
}

// The append-only and immutable guarantees live in the database, so they are
// checked there: the app's own connection, which holds every privilege on the
// tables, is still refused.
func TestConsentEventsAndPublishedDocumentsCannotBeChanged(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := context.Background()
	uid := uuid.New()
	_, err := database.Global().Exec(ctx, `INSERT INTO global_users (id, email, password_hash) VALUES ($1, $2, '')`, uid, "a@example.test")
	require.NoError(t, err)
	_, err = database.Global().Exec(ctx, `INSERT INTO global_consent_events (user_id, purpose, granted, document_kind, document_version, source, decided_at)
		VALUES ($1, 'analytics', true, 'cookie_policy', 'v1', 'banner', now())`, uid)
	require.NoError(t, err)
	_, err = database.Global().Exec(ctx, `INSERT INTO global_legal_document_versions (kind, version, locale, effective, material, reviewed, body, content_sha256)
		VALUES ('terms', 'v1', 'en', '2026-01-01', true, false, 'text', 'x')`)
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE global_consent_events SET granted = false`,
		`DELETE FROM global_consent_events`,
		`TRUNCATE global_consent_events`,
		`UPDATE global_legal_document_versions SET body = 'edited'`,
		`DELETE FROM global_legal_document_versions`,
		`TRUNCATE global_legal_document_versions`,
		// The user's row can't take the proof of consent with it.
		`DELETE FROM global_users WHERE id = '` + uid.String() + `'`,
	} {
		_, err := database.Global().Exec(ctx, stmt)
		require.Error(t, err, stmt)
	}
	var granted bool
	require.NoError(t, database.Global().QueryRow(ctx, `SELECT granted FROM global_consent_events`).Scan(&granted))
	assert.True(t, granted)
}
