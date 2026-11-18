package inventorytest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/inventory/inventorytest"
)

// The suite is what stops an app's drift, so it is tested for failing: each
// check, given an app that has drifted, must say so.
func TestCheck(t *testing.T) {
	inventoryYAML, err := os.ReadFile("../testdata/synthetic.yaml")
	require.NoError(t, err)
	const config = `package config
type flagSpec struct{ flag, key, def string }
var flagSpecs = []flagSpec{
	{"spellco-api-key", "SPELLCO_API_KEY", ""},
	{"spellco-base-url", "SPELLCO_BASE_URL", ""},
	{"public-base-url", "PUBLIC_BASE_URL", ""},
	{"database-url", "DATABASE_URL", ""},
}`
	tables := []string{"goose_db_version", "users", "notes", "feature_flags"}

	run := func(t *testing.T, inv, cfg string, tbls []string, tblErr error) map[string]error {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.go")
		require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
		out := map[string]error{}
		for _, r := range inventorytest.Check(context.Background(), &inventorytest.Config{
			Inventory:    []byte(inv),
			ConfigSource: path,
			Tables:       func(context.Context) ([]string, error) { return tbls, tblErr },
		}) {
			out[r.Name] = r.Err
		}
		return out
	}
	failing := func(res map[string]error) []string {
		var names []string
		for name, err := range res {
			if err != nil {
				names = append(names, name)
			}
		}
		return names
	}

	t.Run("an app that matches its inventory passes every check", func(t *testing.T) {
		res := run(t, string(inventoryYAML), config, tables, nil)
		assert.Len(t, res, 4)
		assert.Empty(t, failing(res))
	})

	t.Run("an inventory that doesn't validate fails", func(t *testing.T) {
		res := run(t, strings.Replace(string(inventoryYAML), "schema_version: 1", "schema_version: 9", 1), config, tables, nil)
		assert.Equal(t, []string{"the inventory validates"}, failing(res))
	})

	t.Run("a table the inventory lacks fails the table check", func(t *testing.T) {
		res := run(t, string(inventoryYAML), config, append([]string{"invoices"}, tables...), nil)
		assert.Equal(t, []string{"every table is in the inventory"}, failing(res))
	})

	t.Run("a database that can't be read fails the table check", func(t *testing.T) {
		res := run(t, string(inventoryYAML), config, nil, errors.New("down"))
		assert.Equal(t, []string{"every table is in the inventory"}, failing(res))
	})

	t.Run("an empty database fails rather than passing vacuously", func(t *testing.T) {
		res := run(t, string(inventoryYAML), config, nil, nil)
		assert.Equal(t, []string{"every table is in the inventory"}, failing(res))
	})

	t.Run("a service the inventory lacks fails the config check", func(t *testing.T) {
		res := run(t, string(inventoryYAML), strings.TrimSuffix(config, "}")+"\t{\"newllm-api-key\", \"NEWLLM_API_KEY\", \"\"},\n}", tables, nil)
		assert.Equal(t, []string{"every service the config can call is in the inventory"}, failing(res))
		assert.ErrorContains(t, res["every service the config can call is in the inventory"], "NEWLLM_API_KEY")
	})

	t.Run("a config that can't be read fails the config check", func(t *testing.T) {
		res := run(t, string(inventoryYAML), "not go", tables, nil)
		assert.Equal(t, []string{"every service the config can call is in the inventory"}, failing(res))
	})

	t.Run("retention periods the code enforces", func(t *testing.T) {
		const day = 24 * time.Hour
		check := func(enforced map[string]time.Duration) error {
			for _, r := range inventorytest.Check(context.Background(), &inventorytest.Config{
				Inventory:         inventoryYAML,
				ConfigSource:      writeConfig(t, config),
				Tables:            func(context.Context) ([]string, error) { return tables, nil },
				EnforcedRetention: enforced,
			}) {
				if r.Name == "the retention periods the code enforces are the inventory's" {
					return r.Err
				}
			}
			return errors.New("the retention check did not run")
		}
		assert.NoError(t, check(map[string]time.Duration{"note_data": 90 * day}))
		assert.ErrorContains(t, check(map[string]time.Duration{"note_data": 30 * day}), "enforced as 720h0m0s in code but the inventory says 90d")
		assert.ErrorContains(t, check(map[string]time.Duration{"account_data": 30 * day}), `its period in the inventory is "indefinite"`)
		assert.ErrorContains(t, check(map[string]time.Duration{"ghost": day}), `"ghost", which is not in the inventory`)
		assert.Error(t, check(nil), "an app that passes nothing doesn't get the check")
	})
}

func writeConfig(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	return path
}
