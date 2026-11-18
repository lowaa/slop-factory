package legaldocs_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/inventory"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

const reviewedHeader = valid + "\nreviewed: true\nreviewed_by: Synthetic Reviewer\nreviewed_on: 2026-01-02"

func reviewedDoc(body string) []byte {
	return []byte("---\n" + reviewedHeader + "\n---\n" + body)
}

func draftDoc(body string) []byte {
	return []byte("---\n" + valid + "\n---\n" + body)
}

func syntheticInventory() *inventory.Inventory {
	return &inventory.Inventory{
		RetentionClasses: []inventory.RetentionClass{
			{ID: "logs", Description: "Server logs", Period: "14d", Mechanism: "log_rotation"},
		},
		Processors: []inventory.Processor{
			{ID: "spellco", Name: "SpellCo", Role: inventory.RoleProcessor, Purpose: "Spell checking",
				Location: "Examplestan", TransferMechanism: "SCCs"},
		},
		Storage: []inventory.StorageKey{
			{Key: "session", Type: inventory.Cookie, Purpose: "necessary", FirstParty: true, Lifetime: "session", Description: "Keeps you signed in"},
		},
		DataCategories: []inventory.DataCategory{
			{ID: "note_text", Description: "The text of notes", Purposes: []string{"spell_check"},
				Stores:    []inventory.Store{{Kind: inventory.StoreTable, Name: "notes"}},
				Retention: "logs", Processors: []string{"spellco"}},
		},
	}
}

func inventoryLoader(t *testing.T, inv *inventory.Inventory) *legaldocs.Loader {
	return newLoader(t, inventory.NewFragments(inv))
}

func TestReviewedDocumentWithUnresolvedInventoryValueFailsToLoad(t *testing.T) {
	body := "## Sub-processors\n{{inventory:sub_processors}}\n"
	inv := syntheticInventory()

	_, err := inventoryLoader(t, inv).Parse("legal/terms/v1.md", reviewedDoc(body))
	require.NoError(t, err, "resolved: loads")

	inv.Processors[0].TransferMechanism = inventory.TodoLegal
	_, err = inventoryLoader(t, inv).Parse("legal/terms/v1.md", reviewedDoc(body))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terms/v1", "names the document")
	assert.Contains(t, err.Error(), "sub_processors", "the fragment")
	assert.Contains(t, err.Error(), "processor spellco: transfer_mechanism", "and what is unresolved")

	v, err := inventoryLoader(t, inv).Parse("legal/terms/v1.md", draftDoc(body))
	require.NoError(t, err, "a draft says so with its banner instead")
	assert.Contains(t, v.Body, "To be confirmed")
	assert.False(t, v.Reviewed)
}

func TestReviewedDocumentRefusesEveryKindOfPlaceholderTheInventoryRenders(t *testing.T) {
	cases := map[string]struct {
		section string
		edit    func(*inventory.Inventory)
		want    string
	}{
		"transfer mechanism in a table cell":  {"sub_processors", func(i *inventory.Inventory) { i.Processors[0].TransferMechanism = inventory.TodoLegal }, "transfer_mechanism"},
		"unknown location":                    {"sub_processors", func(i *inventory.Inventory) { i.Processors[0].Location = inventory.Unknown }, "location"},
		"unknown free text":                   {"sub_processors", func(i *inventory.Inventory) { i.Processors[0].Purpose = inventory.Unknown }, "purpose"},
		"unknown cookie lifetime":             {"cookie_table", func(i *inventory.Inventory) { i.Storage[0].Lifetime = inventory.Unknown }, "lifetime"},
		"referenced processor named unknown":  {"data_flow", func(i *inventory.Inventory) { i.Processors[0].Name = "Unknown" }, "name"},
		"unknown role":                        {"sub_processors", func(i *inventory.Inventory) { i.Processors[0].Role = inventory.RoleUnknown }, "role"},
		"retention period":                    {"retention", func(i *inventory.Inventory) { i.RetentionClasses[0].Period = inventory.TodoLegal }, "period"},
		"provisional retention":               {"retention", func(i *inventory.Inventory) { i.RetentionClasses[0].Placeholder = true }, "provisional"},
		"retention reached through data_flow": {"data_flow", func(i *inventory.Inventory) { i.RetentionClasses[0].Period = inventory.TodoLegal }, "period"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inv := syntheticInventory()
			c.edit(inv)
			_, err := inventoryLoader(t, inv).Parse("legal/terms/v1.md", reviewedDoc("{{inventory:"+c.section+"}}\n"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.section)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestReviewedDocumentChecksASectionUsedTwiceAndAmongOthers(t *testing.T) {
	inv := syntheticInventory()
	inv.RetentionClasses[0].Period = inventory.TodoLegal
	body := "{{inventory:sub_processors}}\n\n## A\n{{inventory:cookie_table}}\n\n## B\n{{inventory:retention}}\n\n## C\n{{inventory:retention}}\n"
	_, err := inventoryLoader(t, inv).Parse("legal/terms/v1.md", reviewedDoc(body))
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(err.Error(), "retention: retention class logs: period"), "reported once")
}

func TestReviewedDocumentIgnoresValuesNoSectionOfItShows(t *testing.T) {
	inv := syntheticInventory()
	inv.RetentionClasses[0].Period = inventory.TodoLegal
	_, err := inventoryLoader(t, inv).Parse("legal/terms/v1.md", reviewedDoc("{{inventory:sub_processors}}\n"))
	require.NoError(t, err, "the retention placeholder is not in this document")
}

func TestReviewedDocumentRefusesFragmentsThatCannotSayTheyAreComplete(t *testing.T) {
	frags := fixedFragments{"retention": "| 14 days |"}
	body := "{{inventory:retention}}\n"
	_, err := newLoader(t, frags).Parse("legal/terms/v1.md", reviewedDoc(body))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can't say whether they are complete")

	_, err = newLoader(t, frags).Parse("legal/terms/v1.md", reviewedDoc("No generated section.\n"))
	require.NoError(t, err, "nothing generated, nothing to vouch for")
	_, err = newLoader(t, frags).Parse("legal/terms/v1.md", draftDoc(body))
	require.NoError(t, err)
}

type reportingFragments struct {
	unresolved []string
}

func (f reportingFragments) Fragment(name string) (string, bool) { return "| 14 days |", true }
func (f reportingFragments) Checked(name string) (string, []string, bool) {
	return "| 14 days |", f.unresolved, true
}

// The generator's word, not the look of the text, is what blocks: text with
// no placeholder wording at all is still refused when it is reported.
func TestReviewedDocumentTrustsTheReportNotTheText(t *testing.T) {
	body := "{{inventory:retention}}\n"
	_, err := newLoader(t, reportingFragments{unresolved: []string{"retention class x: period"}}).Parse("legal/terms/v1.md", reviewedDoc(body))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retention class x: period")
	_, err = newLoader(t, reportingFragments{unresolved: []string{"retention class x: period"}}).Parse("legal/terms/v1.md", draftDoc(body))
	require.NoError(t, err)
	_, err = newLoader(t, reportingFragments{}).Parse("legal/terms/v1.md", reviewedDoc(body))
	require.NoError(t, err, "reporting nothing is complete, not unchecked")
}

func TestInventoryTextThatIsItselfAMarkerIsStillRefused(t *testing.T) {
	inv := syntheticInventory()
	inv.Processors[0].Purpose = "[[TODO-LEGAL: purpose]]"
	_, err := inventoryLoader(t, inv).Parse("legal/terms/v1.md", reviewedDoc("{{inventory:sub_processors}}\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TODO-LEGAL")
}

func TestDataFlowRendersThroughALoader(t *testing.T) {
	v, err := inventoryLoader(t, syntheticInventory()).Parse("legal/terms/v1.md", draftDoc("## Data flow\n{{inventory:data_flow}}\n"))
	require.NoError(t, err)
	assert.Contains(t, v.Body, "| Data | Special category |")
	assert.Contains(t, v.Body, "The text of notes")
	assert.Contains(t, v.Body, "database: notes")
	assert.NotContains(t, v.Body, legaldocs.PendingInventory)
}

func TestDataFlowIsPendingWithoutFragments(t *testing.T) {
	v, err := newLoader(t, nil).Parse("legal/terms/v1.md", draftDoc("{{inventory:data_flow}}\n"))
	require.NoError(t, err)
	assert.Contains(t, v.Body, legaldocs.PendingInventory)
}
