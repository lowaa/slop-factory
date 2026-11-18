package wireschema

import (
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// ExactResponses rewrites every component schema that only responses use,
// so it marks null exactly where the server sends it (see the rule in
// nullability.go): huma marks every slice nullable, and a pointer to a
// struct or to a uuid.UUID never. It runs once, after every operation is
// registered.
//
// A schema any request also uses is left as huma made it. huma validates
// requests against these same schemas, so tightening one would refuse a
// null a client may send today; loosening one would let through a null a
// handler may not expect. Request and response bodies are separate types
// throughout this API, so today no schema is shared.
func ExactResponses(oapi *huma.OpenAPI) {
	registry := oapi.Components.Schemas
	requests := map[string]bool{}
	for _, op := range operations(oapi) {
		for _, p := range op.Parameters {
			reach(registry, p.Schema, requests)
		}
		if op.RequestBody != nil {
			for _, media := range op.RequestBody.Content {
				reach(registry, media.Schema, requests)
			}
		}
	}

	for name, schema := range registry.Map() {
		if requests[name] {
			continue
		}
		t := registry.TypeFromRef(schemasPrefix + name)
		if t == nil {
			continue
		}
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || opaque(t) {
			continue
		}
		exactProperties(schema, t)
	}
}

func exactProperties(schema *huma.Schema, t reflect.Type) {
	for name, f := range jsonFields(t) {
		prop, ok := schema.Properties[name]
		if !ok {
			continue
		}
		switch {
		case f.Type.Kind() == reflect.Pointer:
			if pointerMayBeNull(&f) {
				schema.Properties[name] = nullable(prop)
			}
		case isCollection(f.Type):
			if collectionMayBeNull(&f) {
				prop.Nullable = true
			} else {
				nonNullCollection(prop)
			}
			if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Pointer && prop.Items != nil {
				prop.Items = nullable(prop.Items)
			}
		}
	}
}

// nullable marks s as allowing null. huma's own Nullable flag only works on
// an inline schema -- it panics on a nullable:"true" tag for a field that
// references an object -- so a reference becomes anyOf the object or null,
// which openapi-typescript turns into `Body | null`.
func nullable(s *huma.Schema) *huma.Schema {
	if s.Ref == "" {
		s.Nullable = true
		return s
	}
	return &huma.Schema{
		Description: s.Description,
		AnyOf:       []*huma.Schema{{Ref: s.Ref}, {Type: "null"}},
	}
}

// nonNullCollection marks an array or object schema, and any array or
// object nested inline in it, as never null.
func nonNullCollection(s *huma.Schema) {
	if s == nil || s.Ref != "" {
		return
	}
	s.Nullable = false
	if s.Items != nil {
		nonNullCollection(s.Items)
	}
	if values, ok := s.AdditionalProperties.(*huma.Schema); ok {
		nonNullCollection(values)
	}
}

func operations(oapi *huma.OpenAPI) []*huma.Operation {
	var ops []*huma.Operation
	for _, item := range oapi.Paths {
		for _, op := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete, item.Options, item.Head, item.Patch, item.Trace,
		} {
			if op != nil {
				ops = append(ops, op)
			}
		}
	}
	return ops
}

// reach adds every component schema s refers to, directly or through
// another, to seen.
func reach(registry huma.Registry, s *huma.Schema, seen map[string]bool) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, schemasPrefix)
		if seen[name] {
			return
		}
		seen[name] = true
		reach(registry, registry.SchemaFromRef(s.Ref), seen)
		return
	}
	reach(registry, s.Items, seen)
	if values, ok := s.AdditionalProperties.(*huma.Schema); ok {
		reach(registry, values, seen)
	}
	for _, p := range s.Properties {
		reach(registry, p, seen)
	}
	for _, group := range [][]*huma.Schema{s.OneOf, s.AnyOf, s.AllOf} {
		for _, sub := range group {
			reach(registry, sub, seen)
		}
	}
	reach(registry, s.Not, seen)
}
