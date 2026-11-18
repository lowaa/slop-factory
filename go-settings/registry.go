package settings

import (
	"errors"
	"fmt"
	"sort"
)

// Definition is one known setting: its name, the type it holds, and what a
// reader who never set it gets.
type Definition struct {
	Name    string
	Default Value
	// Validate, when set, refuses a value of the right type that the app
	// still does not accept ("must be a valid IANA zone"). It runs after the
	// type check, so it can rely on the value's live field being the right
	// one. Its error is wrapped in ErrInvalidValue.
	Validate func(Value) error
}

// Registry is the set of known settings, and the only place a name's type
// and default live. A name outside it is refused on write and ignored on
// read, so retiring a setting needs no migration and a typo cannot create
// one nothing reads.
type Registry struct {
	defs  map[string]Definition
	names []string
}

func NewRegistry(defs ...Definition) (*Registry, error) {
	r := &Registry{defs: make(map[string]Definition, len(defs))}
	for _, d := range defs {
		if d.Name == "" {
			return nil, errors.New("settings: a setting needs a name")
		}
		if _, dup := r.defs[d.Name]; dup {
			return nil, fmt.Errorf("settings: %q is declared twice", d.Name)
		}
		switch d.Default.Type {
		case TypeStr, TypeInt, TypeBool, TypeFloat:
		default:
			return nil, fmt.Errorf("settings: %q has unsupported type %q", d.Name, d.Default.Type)
		}
		if d.Validate != nil {
			// A default its own hook refuses would be served to every reader who
			// never set the setting.
			if err := d.Validate(d.Default); err != nil {
				return nil, fmt.Errorf("settings: the default of %q is refused by its validation: %w", d.Name, err)
			}
		}
		r.defs[d.Name] = d
		r.names = append(r.names, d.Name)
	}
	sort.Strings(r.names)
	return r, nil
}

// Names returns the known names, sorted.
func (r *Registry) Names() []string { return append([]string(nil), r.names...) }

func (r *Registry) Lookup(name string) (Definition, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Check refuses a write whose name is unknown, whose value has the wrong
// type, or that the setting's Validate hook rejects.
func (r *Registry) Check(name string, v Value) error {
	d, ok := r.defs[name]
	if !ok {
		return Unknown(name)
	}
	if d.Default.Type != v.Type {
		return fmt.Errorf("%w: %q holds %s, got %s", ErrTypeMismatch, name, d.Default.Type, v.Type)
	}
	if d.Validate != nil {
		if err := d.Validate(v); err != nil {
			return fmt.Errorf("%w: %q: %w", ErrInvalidValue, name, err)
		}
	}
	return nil
}

func Unknown(name string) error { return fmt.Errorf("%w: %q", ErrUnknownSetting, name) }
