package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// maxInventoryBytes bounds what Load reads. An inventory is a hand-edited
// file of a few dozen KiB; the bound keeps a mistaken path from reading
// something huge.
const maxInventoryBytes = 4 << 20

// Load parses and validates an inventory. It refuses an unknown key, so a
// misspelt field fails rather than being ignored, and trailing documents.
func Load(data []byte) (*Inventory, error) {
	if len(data) > maxInventoryBytes {
		return nil, fmt.Errorf("inventory: file is over %d bytes", maxInventoryBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var inv Inventory
	if err := dec.Decode(&inv); err != nil {
		return nil, fmt.Errorf("inventory: %w", err)
	}
	if err := dec.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("inventory: the file holds more than one YAML document")
	}
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	return &inv, nil
}
