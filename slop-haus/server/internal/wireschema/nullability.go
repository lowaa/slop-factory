package wireschema

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
)

// The one rule ExactResponses (what the spec says) and NonNilCollections
// (what the server sends) both follow, so the two cannot drift apart:
//
//   - a pointer field may be null, unless it is omitempty -- then a nil one
//     is left out instead;
//   - a slice or map field is never null -- a nil one goes out empty --
//     unless it is tagged nullable:"true", for a field whose null means
//     something an empty value doesn't (a revision that left the body
//     alone has no body diff, not an empty one).
//
// Slices and maps nested inside one another follow the second rule too.

func isCollection(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Map:
		return true
	case reflect.Slice:
		// []byte goes out as a base64 string; it is not a JSON array.
		return t.Elem().Kind() != reflect.Uint8
	}
	return false
}

// collectionMayBeNull says whether a nil slice or map field keeps its null.
func collectionMayBeNull(f *reflect.StructField) bool {
	return f.Tag.Get("nullable") == "true"
}

// pointerMayBeNull says whether a nil pointer field is sent as null rather
// than left out.
func pointerMayBeNull(f *reflect.StructField) bool {
	return !jsonTagHas(f, "omitempty")
}

// opaque reports whether t chooses its own JSON, so the rule doesn't reach
// inside it: time.Time, uuid.UUID and the like.
func opaque(t reflect.Type) bool {
	marshaler := reflect.TypeFor[json.Marshaler]()
	text := reflect.TypeFor[encoding.TextMarshaler]()
	ptr := reflect.PointerTo(t)
	return t.Implements(marshaler) || ptr.Implements(marshaler) || t.Implements(text) || ptr.Implements(text)
}

func jsonName(f *reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}

func jsonTagHas(f *reflect.StructField, option string) bool {
	_, options, _ := strings.Cut(f.Tag.Get("json"), ",")
	for o := range strings.SplitSeq(options, ",") {
		if o == option {
			return true
		}
	}
	return false
}

// embedded reports whether encoding/json flattens f's fields into its
// parent's, as it does for an untagged anonymous struct.
func embedded(f *reflect.StructField) bool {
	if !f.Anonymous || f.Tag.Get("json") != "" {
		return false
	}
	t := f.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

// jsonFields is t's fields as encoding/json writes them, embedded structs
// flattened, each under its JSON name.
func jsonFields(t reflect.Type) map[string]reflect.StructField {
	fields := map[string]reflect.StructField{}
	for i := range t.NumField() {
		f := t.Field(i)
		if embedded(&f) {
			inner := f.Type
			if inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			for name, innerField := range jsonFields(inner) {
				if _, shadowed := fields[name]; !shadowed {
					fields[name] = innerField
				}
			}
			continue
		}
		if !f.IsExported() || f.Tag.Get("json") == "-" {
			continue
		}
		fields[jsonName(&f)] = f
	}
	return fields
}
