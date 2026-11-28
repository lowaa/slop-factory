package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// bundleFile is the one file of the schema output that holds every
// definition: the v1 messages at the top and the v2 protocol under
// definitions.v2.
const bundleFile = "codex_app_server_protocol.schemas.json"

// node is the subset of JSON Schema (draft-07) that Codex's schema uses.
type node struct {
	Ref                  string           `json:"$ref"`
	Type                 typeList         `json:"type"`
	Enum                 []any            `json:"enum"`
	Properties           map[string]*node `json:"properties"`
	Required             []string         `json:"required"`
	Items                *node            `json:"items"`
	AdditionalProperties *node            `json:"additionalProperties"`
	OneOf                []*node          `json:"oneOf"`
	AnyOf                []*node          `json:"anyOf"`
	AllOf                []*node          `json:"allOf"`
	Description          string           `json:"description"`
	Title                string           `json:"title"`
	Format               string           `json:"format"`
	// boolean is set for the schemas `true` and `false`: anything, or
	// nothing.
	boolean *bool
}

func (n *node) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("true")) || bytes.Equal(b, []byte("false")) {
		v := b[0] == 't'
		n.boolean = &v
		return nil
	}
	type plain node
	return json.Unmarshal(b, (*plain)(n))
}

// typeList is a schema's "type": one name or several.
type typeList []string

func (t *typeList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*t = typeList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*t = many
	return nil
}

// nonNull is the types other than "null", and whether "null" was one.
func (t typeList) nonNull() (types []string, nullable bool) {
	for _, s := range t {
		if s == "null" {
			nullable = true
			continue
		}
		types = append(types, s)
	}
	return types, nullable
}

// loadBundle reads the bundle's definitions into one namespace. The v1 and
// v2 names don't collide except for two identical helpers (AbsolutePathBuf,
// RequestId), so references resolve by their last path segment.
func loadBundle(dir string) (map[string]*node, error) {
	b, err := os.ReadFile(filepath.Join(dir, bundleFile)) // #nosec G304 -- the schema directory is the generator's own input
	if err != nil {
		return nil, err
	}
	var top struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", bundleFile, err)
	}
	defs := map[string]*node{}
	if err := addDefs(defs, top.Definitions, "v2"); err != nil {
		return nil, err
	}
	if v2, ok := top.Definitions["v2"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(v2, &inner); err != nil {
			return nil, fmt.Errorf("parsing definitions.v2: %w", err)
		}
		if err := addDefs(defs, inner, ""); err != nil {
			return nil, err
		}
	}
	return defs, nil
}

func addDefs(defs map[string]*node, raw map[string]json.RawMessage, skip string) error {
	for name, r := range raw {
		if name == skip {
			continue
		}
		n := &node{}
		if err := json.Unmarshal(r, n); err != nil {
			return fmt.Errorf("parsing definition %s: %w", name, err)
		}
		defs[name] = n
	}
	return nil
}

// refName is the definition a $ref names.
func refName(ref string) string {
	return ref[strings.LastIndex(ref, "/")+1:]
}
