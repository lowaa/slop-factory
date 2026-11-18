package privacy_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-privacy/inventory/inventorytest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/prune"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/privacy"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// A table added by a migration with no entry in inventory.yaml, or a
// service added to the config with no processor, fails here.
func TestInventory(t *testing.T) {
	database := dbtest.WithDB(t)
	inventorytest.Run(t, &inventorytest.Config{
		Inventory:         privacy.InventoryYAML(),
		ConfigSource:      "../internal/config/config.go",
		EnforcedRetention: enforcedRetention(t),
		Tables: func(ctx context.Context) ([]string, error) {
			rows, err := database.Global().Query(ctx,
				`SELECT table_name FROM information_schema.tables
				 WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
			if err != nil {
				return nil, err
			}
			return pgx.CollectRows(rows, pgx.RowTo[string])
		},
	})
}

// enforcedRetention is what a server with default config deletes on a timer,
// read through config.Load so a default rewired in config is seen as well as
// one changed in the prune package.
func enforcedRetention(t *testing.T) map[string]time.Duration {
	t.Helper()
	for _, key := range []string{"SESSION_RETENTION", "LEASE_RETENTION", "GITHUB_DELIVERY_RETENTION", "LLM_USAGE_RETENTION", "CONVERSATION_RETENTION", "PENDING_ACTION_RETENTION"} {
		t.Setenv(key, "")
	}
	cfg, err := config.Load([]string{
		"--database-url=postgres://owner:pw@db/slop", "--app-db-password=pw",
		"--public-base-url=https://slop.example", "--jwt-secret=" + strings.Repeat("s", 32), "--email-backend=log",
	})
	require.NoError(t, err)
	// One inventory class covers both, so they must not diverge unseen.
	require.Equal(t, cfg.Prune.SessionRetention, cfg.Prune.LeaseRetention, "agent_activity covers sessions and leases")
	return enforcedPeriods(prune.Rules(&cfg.Prune))
}

// enforcedPeriods is the period each inventory class is deleted at. A class
// with rules of different periods reports the longest: for expired_credentials
// that is the day credentials outlive their expiry, not the hour of an
// unclaimed client.
func enforcedPeriods(rules []*prune.Rule) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, r := range rules {
		// Not an inventory class: pending_actions is filed under work_record,
		// which is never pruned.
		if r.Class == "pending_actions" {
			continue
		}
		out[r.Class] = max(out[r.Class], r.Period)
	}
	return out
}

func TestEmbeddedInventoryLoads(t *testing.T) {
	inv, err := privacy.Inventory()
	require.NoError(t, err)
	require.NotEmpty(t, inv.Tables)
}
