package harness

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/wireschema"
)

// Name is a harness's name. As a field type it is the shared "Harness" enum
// in the OpenAPI document, built from the registry.
type Name string

// Schema makes Name the registry's enum in the OpenAPI document.
func (Name) Schema(r huma.Registry) *huma.Schema {
	names := Names()
	values := make([]Name, len(names))
	for i, n := range names {
		values[i] = Name(n)
	}
	return wireschema.Enum(r, "Harness", values)
}
