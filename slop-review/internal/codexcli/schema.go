package codexcli

import (
	"maps"
	"slices"
)

// StrictSchema returns a copy of schema in the strict form --output-schema
// requires: every object closed with additionalProperties false and every
// property required. A property that was optional becomes one the model
// must fill with an empty or zero value, so the caller has to treat those
// as "not given".
func StrictSchema(schema map[string]any) map[string]any {
	out := maps.Clone(schema)
	if props, ok := out["properties"].(map[string]any); ok {
		strictProps := make(map[string]any, len(props))
		required := make([]string, 0, len(props))
		for name, p := range props {
			if child, ok := p.(map[string]any); ok {
				p = StrictSchema(child)
			}
			strictProps[name] = p
			required = append(required, name)
		}
		out["properties"] = strictProps
		slices.Sort(required)
		out["required"] = required
		out["additionalProperties"] = false
	}
	if out["type"] == "object" {
		out["additionalProperties"] = false
	}
	if items, ok := out["items"].(map[string]any); ok {
		out["items"] = StrictSchema(items)
	}
	return out
}
