package settings

import "errors"

// Type names which of a row's value columns is the live one. The tables also
// allow 'jsonb'; no setting needs it yet, so it joins here when one does.
type Type string

const (
	TypeStr   Type = "str"
	TypeInt   Type = "int"
	TypeBool  Type = "bool"
	TypeFloat Type = "float"
)

var (
	ErrUnknownSetting = errors.New("settings: unknown setting")
	ErrTypeMismatch   = errors.New("settings: value does not match the setting's type")
	ErrInvalidValue   = errors.New("settings: value refused by the setting's validation")
)

// Value is a setting's value together with its Type. Build it with the
// constructors, which keep Type and the live field in step. The fields are
// exported for handlers to read, so nothing stops a hand-built mismatch.
// Registry.Check only compares Type with the definition's, and the store's
// switch on Type picks the one field it persists, ignoring the rest.
type Value struct {
	Type  Type
	Str   string
	Int   int64
	Bool  bool
	Float float64
}

func StrValue(s string) Value    { return Value{Type: TypeStr, Str: s} }
func IntValue(i int64) Value     { return Value{Type: TypeInt, Int: i} }
func BoolValue(b bool) Value     { return Value{Type: TypeBool, Bool: b} }
func FloatValue(f float64) Value { return Value{Type: TypeFloat, Float: f} }

// Same reports whether two values are the same setting value: the same type
// and the same live field. The fields a type does not use are ignored, as the
// store ignores them.
func (v Value) Same(o Value) bool {
	if v.Type != o.Type {
		return false
	}
	switch v.Type {
	case TypeStr:
		return v.Str == o.Str
	case TypeInt:
		return v.Int == o.Int
	case TypeBool:
		return v.Bool == o.Bool
	case TypeFloat:
		// NaN equals itself, as the database compares it, so a retried write
		// of one is not a change.
		return v.Float == o.Float || (v.Float != v.Float && o.Float != o.Float)
	default:
		return false
	}
}
