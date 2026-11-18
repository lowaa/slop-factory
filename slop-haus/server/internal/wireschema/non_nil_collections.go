package wireschema

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// NonNilCollections is a huma.Transformer that sends every nil slice or map
// in a response body as an empty one, except a field tagged
// nullable:"true" (see the rule in nullability.go). It is what makes
// ExactResponses' "never null" true of every handler, rather than of each
// one that remembered to make() its slices.
//
// It never writes to the value a handler returned, which may be shared --
// a package-level list, say: wherever something changes, it builds a copy
// of that part, and the rest is reused as it is.
func NonNilCollections(_ huma.Context, _ string, v any) (any, error) {
	if v == nil {
		return v, nil
	}
	out, changed := fill(reflect.ValueOf(v))
	if !changed {
		return v, nil
	}
	return out.Interface(), nil
}

// fill returns v with its nil collections filled, and whether that took a
// copy; when it didn't, v is returned as it is.
func fill(v reflect.Value) (reflect.Value, bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v, false
		}
		elem, changed := fill(v.Elem())
		if !changed {
			return v, false
		}
		p := reflect.New(elem.Type())
		p.Elem().Set(elem)
		return p, true
	case reflect.Struct:
		return fillStruct(v)
	case reflect.Slice:
		if !isCollection(v.Type()) {
			return v, false
		}
		return fillSlice(v)
	case reflect.Map:
		return fillMap(v)
	}
	return v, false
}

// fillElem fills a slice element or map value: never left null itself, and
// filled inside.
func fillElem(v reflect.Value) (reflect.Value, bool) {
	if isCollection(v.Type()) && v.IsNil() {
		return emptyCollection(v.Type()), true
	}
	return fill(v)
}

func fillStruct(v reflect.Value) (reflect.Value, bool) {
	t := v.Type()
	if opaque(t) {
		return v, false
	}
	var out reflect.Value
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Tag.Get("json") == "-" {
			continue
		}
		fv := v.Field(i)
		var filled reflect.Value
		var changed bool
		if isCollection(f.Type) && fv.IsNil() {
			if collectionMayBeNull(&f) {
				continue
			}
			filled, changed = emptyCollection(f.Type), true
		} else {
			filled, changed = fill(fv)
		}
		if !changed {
			continue
		}
		if !out.IsValid() {
			out = reflect.New(t).Elem()
			out.Set(v)
		}
		out.Field(i).Set(filled)
	}
	if !out.IsValid() {
		return v, false
	}
	return out, true
}

func fillSlice(v reflect.Value) (reflect.Value, bool) {
	var out reflect.Value
	for i := range v.Len() {
		filled, changed := fillElem(v.Index(i))
		if !changed {
			continue
		}
		if !out.IsValid() {
			out = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			reflect.Copy(out, v)
		}
		out.Index(i).Set(filled)
	}
	if !out.IsValid() {
		return v, false
	}
	return out, true
}

func fillMap(v reflect.Value) (reflect.Value, bool) {
	var out reflect.Value
	iter := v.MapRange()
	for iter.Next() {
		filled, changed := fillElem(iter.Value())
		if !changed {
			continue
		}
		if !out.IsValid() {
			out = reflect.MakeMapWithSize(v.Type(), v.Len())
			for _, key := range v.MapKeys() {
				out.SetMapIndex(key, v.MapIndex(key))
			}
		}
		out.SetMapIndex(iter.Key(), filled)
	}
	if !out.IsValid() {
		return v, false
	}
	return out, true
}

func emptyCollection(t reflect.Type) reflect.Value {
	if t.Kind() == reflect.Map {
		return reflect.MakeMap(t)
	}
	return reflect.MakeSlice(t, 0, 0)
}
