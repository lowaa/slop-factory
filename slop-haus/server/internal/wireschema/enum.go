// Package wireschema makes the OpenAPI document say exactly what the server
// sends, so the frontend can use the types generated from it as they are.
// huma, left to itself, is looser than the wire in three ways: a Go string
// enum is a plain string, every Go slice may be null, and a nil pointer to
// a struct (or to a uuid.UUID) is never null. Enum fixes the first;
// ExactResponses and NonNilCollections fix the other two between them.
package wireschema

import (
	"github.com/danielgtaylor/huma/v2"
)

const schemasPrefix = "#/components/schemas/"

// Enum registers name as a string schema allowing only values, the first
// time it is asked for, and returns a reference to it. A Go string enum
// type returns it from a huma.SchemaProvider Schema method, so every field
// of that type -- request or response -- names the one shared component,
// built from the type's own list of values rather than an enum tag
// repeated at each field.
//
// On a request field this is validation: huma refuses any other value with
// a 422 before the handler runs.
func Enum[T ~string](r huma.Registry, name string, values []T) *huma.Schema {
	if _, ok := r.Map()[name]; !ok {
		enum := make([]any, 0, len(values))
		for _, v := range values {
			enum = append(enum, string(v))
		}
		s := &huma.Schema{Type: huma.TypeString, Enum: enum}
		s.PrecomputeMessages()
		r.Map()[name] = s
	}
	return &huma.Schema{Ref: schemasPrefix + name}
}
