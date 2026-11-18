package inventory_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/inventory"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func syntheticYAML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/synthetic.yaml")
	require.NoError(t, err)
	return string(b)
}

func loadSynthetic(t *testing.T) *inventory.Inventory {
	t.Helper()
	inv, err := inventory.Load([]byte(syntheticYAML(t)))
	require.NoError(t, err)
	return inv
}

// mutate returns the synthetic inventory with the first occurrence of old
// replaced, failing if there is none, so a case can't pass because its edit
// silently missed.
func mutate(t *testing.T, old, replacement string) []byte {
	t.Helper()
	src := syntheticYAML(t)
	require.Contains(t, src, old)
	return []byte(strings.Replace(src, old, replacement, 1))
}

func TestLoadRefusesInvalidInventories(t *testing.T) {
	cases := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{"wrong schema version", "schema_version: 1", "schema_version: 2", "schema_version is 2"},
		{"unknown field", "app: notes-demo", "app: notes-demo\nretention_days: 30", "retention_days"},
		{"missing legal basis", "    description: Account details\n", "    description: Account details\n    legal_basis: x\n", "legal_basis"},
		{"purpose without legal basis", "    legal_basis: TODO-LEGAL\n  - id: analytics", "    legal_basis: \"\"\n  - id: analytics", "purpose necessary: legal_basis is empty"},
		{"bad purpose kind", "kind: operational", "kind: whim", `kind "whim"`},
		{"category names an unlisted purpose", "purposes: [notes_service]\n    legal_basis: TODO-LEGAL\n    stores:\n      - kind: table\n        name: users", "purposes: [ghost]\n    legal_basis: TODO-LEGAL\n    stores:\n      - kind: table\n        name: users", `names "ghost"`},
		{"category names an unlisted processor", "processors: [spellco]", "processors: [ghost]", `names "ghost"`},
		{"unknown special category", "special_category: health", "special_category: biometric", "special_category"},
		{"category without stores", "    stores:\n      - kind: table\n        name: users\n      - kind: backups\n        name: db-backups\n        note: not configured\n", "    stores: []\n", "has no stores"},
		{"table without categories", "  - name: users\n    categories: [account]", "  - name: users", "no_personal_data"},
		{"table claims both", "  - name: feature_flags\n    no_personal_data: true", "  - name: feature_flags\n    no_personal_data: true\n    categories: [account]", "lists categories"},
		{"table store names a table that isn't listed", "name: users\n      - kind: backups", "name: userz\n      - kind: backups", `"userz", which is not under tables`},
		{"table store names a table of another category", "        name: notes\n      - kind: object_store", "        name: users\n      - kind: object_store", "doesn't list this category"},
		{"table lists a category whose stores omit it", "  - name: feature_flags\n    no_personal_data: true", "  - name: feature_flags\n    categories: [account]", "whose stores don't name it"},
		{"duplicate table", "  - name: notes\n    categories: [note_text]", "  - name: notes\n    categories: [note_text]\n  - name: notes\n    categories: [note_text]", `"notes" is listed twice`},
		{"bad table name", "  - name: feature_flags", "  - name: Feature Flags", "not a table name"},
		{"processor with a bad role", "role: processor", "role: friend", `role "friend"`},
		{"processor shares nothing", "data_shared: [note_text]\n    location: unknown", "data_shared: []\n    location: unknown", "shares no data categories"},
		{"processor without a transfer mechanism", "transfer_mechanism: TODO-LEGAL\n    sub_processors: [cloudhost]", "transfer_mechanism: \"\"\n    sub_processors: [cloudhost]", "transfer_mechanism is empty"},
		{"unlisted sub-processor", "sub_processors: [cloudhost]", "sub_processors: [ghost]", `names "ghost"`},
		{"bad config key", "SPELLCO_API_KEY", "spellco-key", "upper-case env var name"},
		{"config key claimed twice", "config_keys: [PUBLIC_BASE_URL, DATABASE_URL]", "config_keys: [PUBLIC_BASE_URL, SPELLCO_BASE_URL]", "claimed by both"},
		{"storage with a non-cookie purpose", "    purpose: analytics\n    first_party: true\n    lifetime: 1y", "    purpose: notes_service\n    first_party: true\n    lifetime: 1y", "not a cookie_category"},
		{"storage with an unlisted purpose", "purpose: analytics\n    first_party", "purpose: ghost\n    first_party", `names "ghost"`},
		{"storage without a lifetime", "lifetime: 15m", `lifetime: ""`, "lifetime is empty"},
		{"duplicate storage key", "  - key: notes-demo:draft\n    type: session_storage", "  - key: notes_session\n    type: cookie", "listed twice"},
		{"retention period neither duration nor placeholder", "period: 90d", "period: soon", `period "soon"`},
		{"retention period without a mechanism", "mechanism: nightly purge job", "mechanism: none", "no mechanism enforcing it"},
		{"retention class unlisted", "retention: note_data", "retention: ghost", `names "ghost"`},
		{"two documents", "app: notes-demo", "app: notes-demo\n---\napp: other", "more than one YAML document"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inventory.Load(mutate(t, tc.old, tc.new))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	src := mutate(t, "role: processor", "role: friend")
	src = []byte(strings.Replace(string(src), "kind: operational", "kind: whim", 1))
	_, err := inventory.Load(src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `role "friend"`)
	assert.Contains(t, err.Error(), `kind "whim"`)
}

func TestLoadAcceptsTheSyntheticInventory(t *testing.T) {
	inv := loadSynthetic(t)
	assert.Equal(t, "notes-demo", inv.App)
	assert.Len(t, inv.Tables, 3)
}

func TestCheckTables(t *testing.T) {
	inv := loadSynthetic(t)
	good := []string{"goose_db_version", "users", "notes", "feature_flags"}

	t.Run("passes when every table is listed", func(t *testing.T) {
		require.NoError(t, inv.CheckTables(good, "goose_db_version"))
	})

	// The negative run: a table added by a migration, with no inventory
	// entry, must fail the build.
	t.Run("fails on a table the inventory lacks", func(t *testing.T) {
		err := inv.CheckTables(append([]string{"invoice_lines"}, good...), "goose_db_version")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `table "invoice_lines" is in the database but not in the inventory`)
	})

	t.Run("fails on an inventory table the database lacks", func(t *testing.T) {
		err := inv.CheckTables([]string{"users", "notes"}, "goose_db_version")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `table "feature_flags" is in the inventory but not in the database`)
	})

	t.Run("an ignored table may still be listed", func(t *testing.T) {
		require.NoError(t, inv.CheckTables(good, "goose_db_version", "feature_flags"))
	})

	t.Run("an unlisted migration version table fails unless ignored", func(t *testing.T) {
		require.Error(t, inv.CheckTables(good))
	})
}

func TestCheckConfigKeys(t *testing.T) {
	inv := loadSynthetic(t)
	good := []string{"SPELLCO_API_KEY", "SPELLCO_BASE_URL", "PUBLIC_BASE_URL", "DATABASE_URL", "LISTEN_ADDR", "ACCESS_TOKEN_TTL"}

	t.Run("passes when every service is accounted for", func(t *testing.T) {
		require.NoError(t, inv.CheckConfigKeys(good))
	})

	// The negative run: a new LLM provider appearing in the config without
	// a processor entry must fail the build.
	t.Run("fails on a service key nobody lists", func(t *testing.T) {
		err := inv.CheckConfigKeys(append([]string{"NEWLLM_API_KEY", "NEWLLM_BASE_URL"}, good...))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NEWLLM_API_KEY")
		assert.Contains(t, err.Error(), "NEWLLM_BASE_URL")
	})

	t.Run("fails on a listed key the config dropped", func(t *testing.T) {
		err := inv.CheckConfigKeys([]string{"SPELLCO_API_KEY", "PUBLIC_BASE_URL", "DATABASE_URL"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SPELLCO_BASE_URL")
	})

	t.Run("fails when no keys were read", func(t *testing.T) {
		require.Error(t, inv.CheckConfigKeys(nil))
	})
}

func TestConfigKeysFromSource(t *testing.T) {
	const header = "package config\n\ntype flagSpec struct{ flag, key, def string }\n\n"

	t.Run("reads the table whatever its formatting", func(t *testing.T) {
		src := header + `var flagSpecs = []flagSpec{
	{"database-url", "DATABASE_URL", ""},
	{  "smtp-host",
		"SMTP_HOST", "x"},
	// a comment between rows
	{"smtp-host", "SMTP_HOST", ""},
}
var other = []string{"NOT_A_KEY"}`
		keys, err := inventory.ConfigKeysFromSource([]byte(src))
		require.NoError(t, err)
		assert.Equal(t, []string{"DATABASE_URL", "SMTP_HOST"}, keys)
	})

	t.Run("reads a []KeySpec table of keyed rows", func(t *testing.T) {
		src := "package config\n\ntype KeySpec struct{ Flag, Key, Def string }\n\n" + `var keySpecs = []KeySpec{
	{Flag: "database-url", Key: "DATABASE_URL"},
	{Def: "x", Key: "SMTP_HOST", Flag: "smtp-host"},
}`
		keys, err := inventory.ConfigKeysFromSource([]byte(src))
		require.NoError(t, err)
		assert.Equal(t, []string{"DATABASE_URL", "SMTP_HOST"}, keys)
	})

	t.Run("refuses a keyed row without a Key or with a computed one", func(t *testing.T) {
		const keyed = "package config\n\ntype KeySpec struct{ Flag, Key string }\n\n"
		_, err := inventory.ConfigKeysFromSource([]byte(keyed + `var k = []KeySpec{{Flag: "a"}}`))
		require.Error(t, err)
		_, err = inventory.ConfigKeysFromSource([]byte(keyed + `var k = []KeySpec{{Flag: "a", Key: name}}`))
		require.Error(t, err)
	})

	// A row that isn't a literal can't be read, and reading around it would
	// let the service it names pass the processor check unseen.
	t.Run("refuses a row whose env var isn't a literal", func(t *testing.T) {
		_, err := inventory.ConfigKeysFromSource([]byte(header + `const k = "X_URL"
var flagSpecs = []flagSpec{
	{"database-url", "DATABASE_URL", ""},
	{"x-url", k, ""},
}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "string literal")
	})

	t.Run("refuses a row that isn't a literal row", func(t *testing.T) {
		_, err := inventory.ConfigKeysFromSource([]byte(header + `var flagSpecs = []flagSpec{
	{"database-url", "DATABASE_URL", ""},
	makeSpec(),
}`))
		require.Error(t, err)
	})

	t.Run("finds nothing without a table, and refuses source that doesn't parse", func(t *testing.T) {
		keys, err := inventory.ConfigKeysFromSource([]byte("package config"))
		require.NoError(t, err)
		assert.Empty(t, keys)
		_, err = inventory.ConfigKeysFromSource([]byte("not go"))
		require.Error(t, err)
	})
}

func TestPlaceholders(t *testing.T) {
	assert.Equal(t, []string{
		"purpose necessary: legal_basis",
		"purpose analytics: legal_basis",
		"purpose notes_service: legal_basis",
		"purpose spell_check: legal_basis",
		"data category account: legal_basis",
		"data category note_text: legal_basis",
		"processor spellco: transfer_mechanism",
		"processor cloudhost: transfer_mechanism",
	}, loadSynthetic(t).Placeholders())
}

func TestFragmentsAreGolden(t *testing.T) {
	frags := inventory.NewFragments(loadSynthetic(t))
	for _, name := range []string{
		inventory.FragmentCookieTable, inventory.FragmentSubProcessors,
		inventory.FragmentRetention, inventory.FragmentDataFlow,
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := frags.Fragment(name)
			require.True(t, ok)
			golden := filepath.Join("testdata", name+".golden.md")
			if *update {
				require.NoError(t, os.WriteFile(golden, []byte(got+"\n"), 0o644))
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err)
			assert.Equal(t, strings.TrimRight(string(want), "\n"), got)
		})
	}
}

func TestFragmentsEscapeCellText(t *testing.T) {
	got, _ := inventory.NewFragments(loadSynthetic(t)).Fragment(inventory.FragmentSubProcessors)
	assert.Contains(t, got, `CloudHost \| Inc`)
}

func TestUnknownFragment(t *testing.T) {
	_, ok := inventory.NewFragments(loadSynthetic(t)).Fragment("nope")
	assert.False(t, ok)
}

func TestParsePeriod(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90m": 90 * time.Minute, "36h": 36 * time.Hour, "14d": 14 * 24 * time.Hour,
		"2w": 14 * 24 * time.Hour, "12mo": 360 * 24 * time.Hour, "6y": 6 * 365 * 24 * time.Hour,
	} {
		got, ok := inventory.ParsePeriod(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "0d", "d", "indefinite", "unknown", "TODO-LEGAL", "-1d", "1.5d", "7days"} {
		_, ok := inventory.ParsePeriod(in)
		assert.False(t, ok, in)
	}
}

func TestUnresolvedListsWhatEachFragmentShowsAsAPlaceholder(t *testing.T) {
	frags := inventory.NewFragments(loadSynthetic(t))
	assert.Equal(t, []string{
		"processor spellco: location",
		"processor spellco: transfer_mechanism",
		"processor cloudhost: transfer_mechanism",
		"processor cloudhost: sub_processors",
	}, frags.Unresolved(inventory.FragmentSubProcessors))
	assert.Equal(t, []string{"retention class note_data: period (provisional)"}, frags.Unresolved(inventory.FragmentRetention))
	assert.Equal(t, []string{"retention class note_data: period (provisional)"}, frags.Unresolved(inventory.FragmentDataFlow))
	assert.Empty(t, frags.Unresolved(inventory.FragmentCookieTable))
	assert.NotEmpty(t, frags.Unresolved("nope"), "a name it doesn't generate is not complete")
}

// The list and the text come from one pass, but the claim worth pinning is
// the one legaldocs relies on: a fragment that shows a placeholder word is
// never reported complete.
func TestUnresolvedIsEmptyOnlyWhenTheTextShowsNoPlaceholder(t *testing.T) {
	marks := []string{"To be confirmed", "Unknown", "unknown", "TODO-LEGAL", "(provisional)"}
	check := func(t *testing.T, inv *inventory.Inventory) {
		frags := inventory.NewFragments(inv)
		for _, name := range []string{
			inventory.FragmentCookieTable, inventory.FragmentSubProcessors,
			inventory.FragmentRetention, inventory.FragmentDataFlow,
		} {
			text, ok := frags.Fragment(name)
			require.True(t, ok)
			shows := false
			for _, m := range marks {
				shows = shows || strings.Contains(text, m)
			}
			assert.Equal(t, shows, len(frags.Unresolved(name)) > 0, name)
		}
	}
	t.Run("as it is", func(t *testing.T) { check(t, loadSynthetic(t)) })
	for name, edit := range map[string]func(*inventory.Inventory){
		"period todo":             func(i *inventory.Inventory) { i.RetentionClasses[0].Period = inventory.TodoLegal },
		"period unknown":          func(i *inventory.Inventory) { i.RetentionClasses[0].Period = inventory.Unknown },
		"mechanism unknown":       func(i *inventory.Inventory) { i.RetentionClasses[0].Mechanism = inventory.Unknown },
		"provisional":             func(i *inventory.Inventory) { i.RetentionClasses[0].Placeholder = true },
		"role unknown":            func(i *inventory.Inventory) { i.Processors[0].Role = inventory.RoleUnknown },
		"location todo":           func(i *inventory.Inventory) { i.Processors[1].Location = inventory.TodoLegal },
		"storage lifetime":        func(i *inventory.Inventory) { i.Storage[0].Lifetime = inventory.Unknown },
		"processor purpose":       func(i *inventory.Inventory) { i.Processors[0].Purpose = inventory.Unknown },
		"processor name":          func(i *inventory.Inventory) { i.Processors[0].Name = "Unknown" },
		"category text":           func(i *inventory.Inventory) { i.DataCategories[0].Description = inventory.Unknown },
		"class description":       func(i *inventory.Inventory) { i.RetentionClasses[0].Description = inventory.TodoLegal },
		"store name":              func(i *inventory.Inventory) { i.DataCategories[0].Stores[0].Name = inventory.Unknown },
		"data shared":             func(i *inventory.Inventory) { i.Processors[0].DataShared = []string{inventory.Unknown} },
		"resolved processor name": func(i *inventory.Inventory) { i.Processors[1].Name = "unknown" },
		"category processor":      func(i *inventory.Inventory) { i.DataCategories[1].Processors = []string{inventory.Unknown} },
	} {
		t.Run(name, func(t *testing.T) {
			inv := loadSynthetic(t)
			edit(inv)
			check(t, inv)
		})
	}
}
