package legaldocs_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs/legaldocstest"
)

func TestPostgresStoreSuite(t *testing.T) {
	legaldocstest.StoreSuite(t, legaldocs.NewPostgresStore(dbtest.AppEngine()))
}

func TestPublishedRowCannotBeChangedByAnyone(t *testing.T) {
	ctx := context.Background()
	store := legaldocs.NewPostgresStore(dbtest.AppEngine())
	require.NoError(t, store.Publish(ctx, &legaldocs.Version{
		Kind: "trigger_kind", Version: "v1", Locale: "en", Effective: jan1, Material: true, Body: "kept",
	}))

	_, err := dbtest.AppEngine().ExecContext(ctx, `UPDATE global_legal_document_versions SET body = 'edited' WHERE kind = 'trigger_kind'`)
	require.Error(t, err, "the table, not just the store, refuses it")
	_, err = dbtest.AppEngine().ExecContext(ctx, `DELETE FROM global_legal_document_versions WHERE kind = 'trigger_kind'`)
	require.Error(t, err)

	_, err = dbtest.AppEngine().ExecContext(ctx, `TRUNCATE global_legal_document_versions`)
	require.Error(t, err)

	got, err := store.Get(ctx, "trigger_kind", "v1", "en")
	require.NoError(t, err)
	assert.Equal(t, "kept", got.Body)
}

func TestTableRefusesAReviewedRowWithoutAReviewer(t *testing.T) {
	for name, cols := range map[string]string{
		"no reviewer": `reviewed_by, reviewed_on`,
		"no date":     `reviewed_by`,
	} {
		vals := map[string]string{"no reviewer": `'', '2026-01-01'`, "no date": `'A'`}[name]
		_, err := dbtest.AppEngine().ExecContext(context.Background(),
			`INSERT INTO global_legal_document_versions (kind, version, locale, effective, material, reviewed, body, content_sha256, `+cols+`)
			 VALUES ('check_kind', 'v-`+name+`', 'en', '2026-01-01', true, true, 'x', 'h', `+vals+`)`)
		require.Error(t, err, name)
	}
}
