package secretscan

import (
	"reflect"
	"strings"
)

// Check scans every string in v -- a struct, pointer, slice or map of them,
// as an MCP tool's input is -- and returns an *Error for the first input that
// looks like a credential, or nil. The field is named by its json tag.
func Check(v any) *Error {
	return checkValue(reflect.ValueOf(v), "")
}

func checkValue(v reflect.Value, field string) *Error {
	switch v.Kind() {
	case reflect.String:
		if kinds := Scan(v.String()); len(kinds) > 0 {
			return &Error{Kinds: kinds, Field: field}
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return checkValue(v.Elem(), field)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if err := checkValue(v.Index(i), field); err != nil {
				return err
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			if err := checkValue(it.Value(), field); err != nil {
				return err
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			if !t.Field(i).IsExported() {
				continue
			}
			name := field
			if name == "" {
				name = jsonName(t.Field(i))
			}
			if err := checkValue(v.Field(i), name); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" || name == "-" {
		return f.Name
	}
	return name
}
