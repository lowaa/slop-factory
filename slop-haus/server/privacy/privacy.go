// Package privacy holds slop-haus's data inventory, its consent purposes and
// its legal documents (go-privacy/README.md, sections 9 and 10).
//
// The inventory is documentation and CI input, and it fills the generated
// sections of the legal documents, which the server renders and publishes at
// startup (Setup). It is embedded, so what CI checked is what runs: a
// mistake in it fails the build, never a deploy. The consent purposes
// themselves are declared in Go (Registry), not read from the inventory.
package privacy

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/lowaa/slop-factory/go-privacy/inventory"
)

// The tables section lives in inventory.d, one file per domain, so a feature
// that adds a table does not edit the file every other feature edits. Each
// fragment is the list items of `tables:` and nothing else.
//
//go:embed inventory.yaml inventory.d/*.yaml
var inventoryFS embed.FS

var inventoryYAML = mustMergeInventory(inventoryFS)

// InventoryYAML is the inventory as embedded: inventory.yaml with the
// inventory.d fragments joined as its tables section.
func InventoryYAML() []byte { return inventoryYAML }

func mustMergeInventory(fsys embed.FS) []byte {
	base, err := fsys.ReadFile("inventory.yaml")
	if err != nil {
		panic(fmt.Sprintf("privacy: %v", err))
	}
	entries, err := fsys.ReadDir("inventory.d")
	if err != nil {
		panic(fmt.Sprintf("privacy: %v", err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var b strings.Builder
	b.Write(base)
	b.WriteString("\ntables:\n")
	for _, name := range names {
		fragment, err := fsys.ReadFile("inventory.d/" + name)
		if err != nil {
			panic(fmt.Sprintf("privacy: %v", err))
		}
		b.Write(fragment)
		if !strings.HasSuffix(string(fragment), "\n") {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// Inventory loads and validates the embedded inventory.
func Inventory() (*inventory.Inventory, error) {
	return inventory.Load(inventoryYAML)
}
