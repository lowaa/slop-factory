// Package usersettings holds per-user preferences: an append-only log of
// typed values. A setting is either global (global_user_settings, which
// follows a user into every tenant) or tenant-scoped (user_tenant_settings,
// one user's value within one tenant). It is not internal/settings, which
// holds the switches of a whole tenant.
package usersettings

import (
	"fmt"

	gosettings "github.com/lowaa/slop-factory/go-settings"
)

// The value model is shared with every other settings scope; these are the
// same types under this package's names.
type (
	Type  = gosettings.Type
	Value = gosettings.Value
)

const (
	TypeStr   = gosettings.TypeStr
	TypeInt   = gosettings.TypeInt
	TypeBool  = gosettings.TypeBool
	TypeFloat = gosettings.TypeFloat
)

var (
	ErrUnknownSetting = gosettings.ErrUnknownSetting
	ErrTypeMismatch   = gosettings.ErrTypeMismatch

	StrValue   = gosettings.StrValue
	IntValue   = gosettings.IntValue
	BoolValue  = gosettings.BoolValue
	FloatValue = gosettings.FloatValue
)

const (
	// RemoteControlSessionSwitchExplained is set once the person has asked not
	// to be told again what a switch between sessions carries over.
	RemoteControlSessionSwitchExplained = "remote_control.session_switch_explained"
)

// RenderMarkdown is whether ticket text and Remote Control messages are shown
// as rendered markdown rather than plain text. On unless the person turned it
// off, and the same in every tenant.
const RenderMarkdown = "render_markdown"

// The auto-approve switches: one per proposal kind, per user per tenant. On,
// the person's own console and Remote Control sessions have that kind of
// proposal applied as they propose it.
const (
	ApprovalsAutoStatus           = "approvals.auto.status"
	ApprovalsAutoEdit             = "approvals.auto.edit"
	ApprovalsAutoReady            = "approvals.auto.ready"
	ApprovalsAutoReviewDecision   = "approvals.auto.review_decision"
	ApprovalsAutoDiscussionAnswer = "approvals.auto.discussion_answer"
)

// Scope says where a setting's value applies.
type Scope string

const (
	// ScopeGlobal is the zero value, so a Definition that says nothing is
	// today's behaviour: one value per user, in every tenant.
	ScopeGlobal Scope = ""
	// ScopeTenant is one value per user per tenant.
	ScopeTenant Scope = "tenant"
)

// Definition is one known setting: what it is called, the type it holds, and
// what a user who never set it gets.
type Definition struct {
	Name    string
	Scope   Scope
	Default Value
}

// Registry is the set of known settings, and the only place a name's type
// and default live; gosettings.Registry does the checking, this adds the
// scope.
type Registry struct {
	defs map[string]Definition
	core *gosettings.Registry
}

func NewRegistry(defs ...Definition) (*Registry, error) {
	r := &Registry{defs: make(map[string]Definition, len(defs))}
	core := make([]gosettings.Definition, 0, len(defs))
	for _, d := range defs {
		if d.Scope != ScopeGlobal && d.Scope != ScopeTenant {
			return nil, fmt.Errorf("usersettings: %q has unknown scope %q", d.Name, d.Scope)
		}
		r.defs[d.Name] = d
		core = append(core, gosettings.Definition{Name: d.Name, Default: d.Default})
	}
	var err error
	if r.core, err = gosettings.NewRegistry(core...); err != nil {
		return nil, err
	}
	return r, nil
}

// DefaultRegistry is the production set of settings.
func DefaultRegistry() *Registry {
	r, err := NewRegistry(
		Definition{Name: RemoteControlSessionSwitchExplained, Default: BoolValue(false)},
		Definition{Name: RenderMarkdown, Default: BoolValue(true)},
		Definition{Name: ApprovalsAutoStatus, Scope: ScopeTenant, Default: BoolValue(false)},
		Definition{Name: ApprovalsAutoEdit, Scope: ScopeTenant, Default: BoolValue(false)},
		Definition{Name: ApprovalsAutoReady, Scope: ScopeTenant, Default: BoolValue(false)},
		Definition{Name: ApprovalsAutoReviewDecision, Scope: ScopeTenant, Default: BoolValue(false)},
		Definition{Name: ApprovalsAutoDiscussionAnswer, Scope: ScopeTenant, Default: BoolValue(false)},
	)
	if err != nil {
		panic(err) // a static declaration: a failure is a programming error
	}
	return r
}

// ForScope returns the settings of one scope. A name of the other scope is
// unknown to it, so each route set refuses the other's names as it would a
// typo. A name cannot be declared in both scopes: the registry is keyed by
// name alone, so NewRegistry refuses the clash as a duplicate.
func (r *Registry) ForScope(scope Scope) *Registry {
	var defs []Definition
	for _, name := range r.core.Names() {
		if d := r.defs[name]; d.Scope == scope {
			defs = append(defs, d)
		}
	}
	// The definitions were valid in r, so a subset of them is too.
	out, err := NewRegistry(defs...)
	if err != nil {
		panic(err)
	}
	return out
}

// Names returns the known names, sorted.
func (r *Registry) Names() []string { return r.core.Names() }

func (r *Registry) Lookup(name string) (Definition, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Check refuses a write whose name is unknown or whose value has the wrong type.
func (r *Registry) Check(name string, v Value) error { return r.core.Check(name, v) }

func unknown(name string) error { return gosettings.Unknown(name) }
