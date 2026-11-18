package settings_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// The owner is told every level or override change is on record, so each owner-only
// steward setting must write a history entry like any other setting.
func TestHistory_StewardLevelAndOverridesAreRecorded(t *testing.T) {
	r := newHistoryRig(t)
	tenant := dbtest.NewTenant(t, r.database)
	ctx := db.WithTenant(context.Background(), tenant)
	ada := r.member(t, tenant, "Ada")
	token := r.signIn(ada, tenant)
	svc := stewardService(r.store, &fakeOwners{owner: true})

	for name, writes := range map[string][]settings.Value{
		steward.SettingLevel:         {settings.IntValue(3), settings.IntValue(5)},
		steward.SettingCategoryTiers: {settings.StrValue(`{"priority":"human"}`), settings.StrValue(`{}`)},
	} {
		for _, v := range writes {
			_, err := svc.Set(ctx, name, v, ada)
			require.NoError(t, err, name)
		}

		rec := r.get("/api/settings/"+name+"/history", token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		page := decodeHistory(t, rec.Body.Bytes())
		require.Len(t, page.Changes, len(writes), name)
		assert.Equal(t, ada, *page.Changes[0].ChangedByID, name)
		assert.Equal(t, "Ada", *page.Changes[0].ChangedByName, name)
	}
}

func decodeHistory(t *testing.T, body []byte) historyPage {
	t.Helper()
	var page historyPage
	require.NoError(t, json.Unmarshal(body, &page))
	return page
}
