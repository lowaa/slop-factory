// Package harness is the registry of harnesses agents run in (Claude Code,
// pi, Codex). It is loaded from harnesses.json, a copy of slop-haus/contracts/
// harnesses.json that go:embed can reach; the package's test asserts the two
// are equal. Everything that names a harness -- validation, the OpenAPI
// enums and the database's lookup table -- derives from here, so adding a
// harness is an edit to the contract rather than to each of those.
package harness

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed harnesses.json
var contractJSON []byte

// StewardSupport says whether a harness can run the steward.
type StewardSupport string

const (
	StewardYes StewardSupport = "yes"
	StewardNo  StewardSupport = "no"
	// StewardConditional is steward-capable only on a host where the
	// harness's confinement check passes.
	StewardConditional StewardSupport = "conditional"
)

// Capabilities are the roles a harness can fill.
type Capabilities struct {
	Worker        bool           `json:"worker"`
	Steward       StewardSupport `json:"steward"`
	RemoteControl bool           `json:"remote_control"`
}

// Setting is a typed setting a variant or harness config may hold. Kind is
// "model", "provider" or "enum"; Options are an enum's values in display order.
type Setting struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Options []string `json:"options,omitempty"`
}

// Harness is one registry entry.
type Harness struct {
	Name         string       `json:"name"`
	DisplayName  string       `json:"displayName"`
	Capabilities Capabilities `json:"capabilities"`
	Settings     []Setting    `json:"settings"`
}

// File is the shape of harnesses.json.
type File struct {
	Doc       string     `json:"_doc"`
	Harnesses []*Harness `json:"harnesses"`
}

var registry = mustLoad(contractJSON)

func mustLoad(data []byte) []*Harness {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		panic(fmt.Sprintf("harness: harnesses.json: %v", err))
	}
	seen := map[string]bool{}
	for _, h := range f.Harnesses {
		if h.Name == "" || seen[h.Name] {
			panic(fmt.Sprintf("harness: harnesses.json: empty or duplicate name %q", h.Name))
		}
		seen[h.Name] = true
		switch h.Capabilities.Steward {
		case StewardYes, StewardNo, StewardConditional:
		default:
			panic(fmt.Sprintf("harness: harnesses.json: %s: steward must be yes, no or conditional", h.Name))
		}
	}
	return f.Harnesses
}

// All returns the registry in contract order.
func All() []*Harness { return slices.Clone(registry) }

// Names returns every harness name in contract order.
func Names() []string {
	out := make([]string, len(registry))
	for i, h := range registry {
		out[i] = h.Name
	}
	return out
}

// Lookup finds a harness by name.
func Lookup(name string) (*Harness, bool) {
	for _, h := range registry {
		if h.Name == name {
			return h, true
		}
	}
	return nil, false
}

// Valid reports whether name is a registered harness.
func Valid(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// Setting finds one of the harness's settings by name.
func (h *Harness) Setting(name string) (Setting, bool) {
	for _, s := range h.Settings {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

// Execer is the part of a database connection Sync needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Sync records every registered harness in global_harnesses, which the
// variant and attempt tables reference, so a new harness needs no migration.
// Run it as the owning role, after migrating and before serving.
// It only adds: a harness dropped from the registry stays, so rows that name
// it keep their reference.
func Sync(ctx context.Context, db Execer) error {
	if _, err := db.Exec(ctx,
		`INSERT INTO global_harnesses (name) SELECT unnest($1::text[]) ON CONFLICT DO NOTHING`, Names()); err != nil {
		return fmt.Errorf("harness: record registry: %w", err)
	}
	return nil
}
