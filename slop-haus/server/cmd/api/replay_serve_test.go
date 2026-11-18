package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Replays are dormant until REPLAYS_ENABLED: the add-attempt endpoint then
// answers a replay request as if the feature did not exist.
func TestServe_ReplayIsBehindTheSetting(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			b := boot(t, &bootOptions{replays: enabled})
			member, memberToken := b.signedInUser(t)

			created := do(t, &request{method: http.MethodPost, url: b.base + "/api/tickets", token: memberToken,
				body: `{"title": "Replayed", "paths": [{"path": "a/"}]}`})
			require.Equal(t, http.StatusCreated, created.code, created.body)
			var tk struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal([]byte(created.body), &tk))
			tenant := db.WithTenant(context.Background(), member.TenantID)
			for _, stmt := range []string{
				`UPDATE tickets SET base_commit = repeat('1', 40) WHERE id = $1`,
				`INSERT INTO ticket_snapshots (ticket_id, revision, title, body, paths, comments_through_id, as_of, source)
				 VALUES ($1, 0, 'Replayed', '', '[]', 0, now(), 'first_claim')`,
				`UPDATE attempts SET status = 'done' WHERE ticket_id = $1`,
				`UPDATE tickets SET status = 'done' WHERE id = $1`,
			} {
				_, err := b.appDB.Exec(tenant, stmt, tk.ID)
				require.NoError(t, err, stmt)
			}
			attempts := func() int {
				var n int
				require.NoError(t, b.appDB.QueryRow(tenant, `SELECT count(*) FROM attempts WHERE ticket_id = $1`, tk.ID).Scan(&n))
				return n
			}

			resp := do(t, &request{method: http.MethodPost, token: memberToken, body: `{"replay": true}`,
				url: fmt.Sprintf("%s/api/tickets/%d/attempts", b.base, tk.ID)})
			if !enabled {
				assert.Equal(t, http.StatusNotFound, resp.code, resp.body)
				assert.JSONEq(t, `{"code":"feature_disabled"}`, onlyCode(t, resp.body))
				assert.Equal(t, 1, attempts(), "off inserts nothing")
				return
			}
			assert.Equal(t, http.StatusCreated, resp.code, resp.body)
			assert.Equal(t, 2, attempts())
		})
	}
}
