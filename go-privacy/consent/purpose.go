package consent

import (
	"context"
	"fmt"
	"regexp"
)

// Kind says what a purpose governs, and so where the app asks for it.
type Kind string

const (
	// CookieCategory purposes are asked in the banner and preferences, and
	// apply to anonymous visitors too.
	CookieCategory Kind = "cookie_category"
	// Processing purposes (health data, sending a document to an LLM) are
	// asked in their own flow and only exist for signed-in users: they are
	// never stored in the anonymous cookie.
	Processing Kind = "processing"
)

// Purpose is one thing a person can consent to. The app declares its
// purposes at startup; this package knows none by name.
type Purpose struct {
	// ID is stored in every event and never renamed.
	ID   string
	Kind Kind
	// Necessary purposes are always granted, never toggled and never
	// recorded.
	Necessary bool
	// Document is the kind of legal document the purpose is shown under.
	// Required unless the purpose is Necessary.
	Document string
	// Capability, if set, names a flag that must be on for the purpose to
	// be offered or granted.
	Capability string
	// SaleOrShare marks a purpose GPC and "Do Not Sell or Share" deny.
	SaleOrShare bool
}

// Capabilities is the app's feature flags. A purpose with a Capability
// exists only while its flag is on.
type Capabilities interface {
	Enabled(ctx context.Context, capability string) bool
}

// StaticCapabilities is a Capabilities with a fixed set of flags on, e.g.
// read from the environment at startup.
type StaticCapabilities map[string]bool

func (c StaticCapabilities) Enabled(_ context.Context, capability string) bool {
	return c[capability]
}

var purposeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Registry is the app's declared purposes, in declaration order.
type Registry struct {
	purposes []*Purpose
	byID     map[string]*Purpose
}

// NewRegistry validates and freezes the declared purposes. It fails on a
// bad declaration so the app does not start with one: a duplicate id, an
// unknown kind, a purpose that must be shown under a document but names
// none, or a necessary purpose that tries to be gated or a sale.
func NewRegistry(purposes ...Purpose) (*Registry, error) {
	if len(purposes) == 0 {
		return nil, fmt.Errorf("consent: a registry needs at least one purpose")
	}
	r := &Registry{byID: map[string]*Purpose{}}
	for _, p := range purposes {
		p := p
		if !purposeIDPattern.MatchString(p.ID) {
			return nil, fmt.Errorf("consent: purpose id %q must match %s", p.ID, purposeIDPattern)
		}
		if _, dup := r.byID[p.ID]; dup {
			return nil, fmt.Errorf("consent: purpose %q declared twice", p.ID)
		}
		if p.Kind != CookieCategory && p.Kind != Processing {
			return nil, fmt.Errorf("consent: purpose %q has unknown kind %q", p.ID, p.Kind)
		}
		if p.Necessary {
			if p.Kind != CookieCategory || p.Capability != "" || p.SaleOrShare {
				return nil, fmt.Errorf("consent: necessary purpose %q must be an ungated, non-sale cookie category", p.ID)
			}
		} else if p.Document == "" {
			return nil, fmt.Errorf("consent: purpose %q names no document", p.ID)
		}
		r.byID[p.ID] = &p
		r.purposes = append(r.purposes, &p)
	}
	return r, nil
}

// Get returns a copy of the declared purpose, or nil. Copies, so nothing
// that holds one can change the policy the service is running.
func (r *Registry) Get(id string) *Purpose {
	p := r.byID[id]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// Purposes returns copies of every declared purpose in declaration order.
func (r *Registry) Purposes() []*Purpose {
	out := make([]*Purpose, len(r.purposes))
	for i, p := range r.purposes {
		cp := *p
		out[i] = &cp
	}
	return out
}

// available returns the purposes whose capability flag, if any, is on.
func (r *Registry) available(ctx context.Context, caps Capabilities) []*Purpose {
	out := make([]*Purpose, 0, len(r.purposes))
	for _, p := range r.purposes {
		if p.Capability == "" || (caps != nil && caps.Enabled(ctx, p.Capability)) {
			out = append(out, p)
		}
	}
	return out
}
