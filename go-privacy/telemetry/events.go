package telemetry

import (
	"fmt"
	"strconv"
)

// AttrSpec is what one attribute of an event may hold. The types are the
// ones that cannot carry text a person typed: a value from a fixed list, a
// flag, or a count on a step with a cap.
type AttrSpec struct {
	Type     string   `json:"type"`
	Values   []string `json:"values,omitempty"`
	Max      int64    `json:"max,omitempty"`
	Step     int64    `json:"step,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

// EventSpecs is an event tier's catalog: event name to its attributes by key.
type EventSpecs map[string]map[string]AttrSpec

const (
	AttrEnum = "enum"
	AttrBool = "bool"
	AttrInt  = "int"
)

// clone is a copy no caller can change under a running policy.
func (e EventSpecs) clone() EventSpecs {
	if e == nil {
		return nil
	}
	out := make(EventSpecs, len(e))
	for name, attrs := range e {
		cp := make(map[string]AttrSpec, len(attrs))
		for k, spec := range attrs {
			spec.Values = append([]string(nil), spec.Values...)
			cp[k] = spec
		}
		out[name] = cp
	}
	return out
}

// Validate refuses a catalog that could weaken the proxy: an attribute the
// logger sets itself, a type nothing implements, an int with no bound.
func (e EventSpecs) Validate() error {
	if len(e) == 0 {
		return fmt.Errorf("no events")
	}
	for name, attrs := range e {
		for attr, spec := range attrs {
			if attr == EventNameAttribute || attr == SessionIDAttribute || attr == TierAttribute || identityKeys[attr] {
				return fmt.Errorf("%s.%s is set by the logger itself or identifies an account", name, attr)
			}
			if err := spec.validate(); err != nil {
				return fmt.Errorf("%s.%s: %w", name, attr, err)
			}
		}
	}
	return nil
}

func (s *AttrSpec) validate() error {
	switch s.Type {
	case AttrEnum:
		if len(s.Values) == 0 {
			return fmt.Errorf("an enum needs values")
		}
		for _, v := range s.Values {
			// Values are short names; a long one would be clipped on the way
			// out and so no longer be the value the catalog lists.
			if len(v) > maxKeyLen {
				return fmt.Errorf("an enum value is over %d bytes", maxKeyLen)
			}
		}
	case AttrBool:
	case AttrInt:
		if s.Max <= 0 || s.Step <= 0 {
			return fmt.Errorf("an int needs a max and a step")
		}
	default:
		return fmt.Errorf("unknown type %q", s.Type)
	}
	return nil
}

// accept re-emits v if it is exactly one value of the spec's type and within
// its bounds.
func (s *AttrSpec) accept(v *anyValue) (outValue, bool) {
	out, ok := scalar(v)
	if !ok {
		return nil, false
	}
	switch s.Type {
	case AttrEnum:
		return out, contains(s.Values, v.stringOrEmpty())
	case AttrBool:
		_, isBool := out["boolValue"].(bool)
		return out, isBool
	case AttrInt:
		raw, isInt := out["intValue"].(string)
		if !isInt {
			return nil, false
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		return out, err == nil && n >= 0 && n <= s.Max && n%s.Step == 0
	}
	return nil, false
}

func (v *anyValue) stringOrEmpty() string {
	if v.StringValue == nil {
		return ""
	}
	return *v.StringValue
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
