// Package harnessconfig is the configuration a human hands out to the
// harnesses agents run in (Claude Code, pi, Codex): which model, how hard to
// think. Set per tenant, with optional per-agent overrides, and fetched by
// each workspace's run_remote_control.sh when it starts a harness.
//
// It is a trust boundary. Whatever run_remote_control.sh receives ends up in the
// environment or on the command line of a process that runs tools, so the
// server stores named, typed settings -- never raw variables -- and each is
// validated against a strict pattern here and again by the supervisor
// package's workspace/harness_config.py, which alone maps a setting to a
// variable or flag. Adding a setting means a row in definitions below and a
// mapping there.
package harnessconfig

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
)

// Harness names, as run_remote_control.sh names them.
const (
	HarnessClaude = "claude"
	HarnessPi     = "pi"
	HarnessCodex  = "codex"
)

// Kind is a setting's type: what values it accepts.
type Kind string

const (
	// KindModel is a model id, as a harness takes it: "sonnet",
	// "claude-opus-5-5", "openrouter/qwen/qwen3:free", "opus[1m]".
	KindModel Kind = "model"
	// KindProvider is a pi provider name: "anthropic", "openrouter".
	KindProvider Kind = "provider"
	// KindEnum is one of a fixed list of options.
	KindEnum Kind = "enum"
)

// Both patterns start with an alphanumeric: pi takes these as arguments,
// and a value starting with '-' would be read as another flag.
var (
	modelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$`)
	providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

// Definition is one setting the server knows.
type Definition struct {
	Harness string
	Name    string
	Kind    Kind
	// Options are a KindEnum's allowed values, in the order to offer them.
	Options []string
	// Label and Help are what the UI shows.
	Label string
	Help  string
}

// options reads an enum setting's values from the harness registry, so the
// contract is their one home.
func options(harnessName, setting string) []string {
	h, ok := harness.Lookup(harnessName)
	if !ok {
		panic("harnessconfig: unregistered harness " + harnessName)
	}
	s, ok := h.Setting(setting)
	if !ok {
		panic("harnessconfig: unregistered setting " + harnessName + "." + setting)
	}
	return s.Options
}

// definitions is the registry. A setting not in here is refused on write
// and dropped on read, so a typo cannot quietly store something nothing
// reads, and a setting removed in a later release stops being handed out.
var definitions = []Definition{
	{
		Harness: HarnessClaude, Name: "model", Kind: KindModel,
		Label: "Main model",
		Help:  "The main session's model (ANTHROPIC_MODEL).",
	},
	{
		// One setting rather than a model and a separate force switch: the
		// A subagent definition that pins its own model beats
		// CLAUDE_CODE_SUBAGENT_MODEL unless force is on, so a subagent
		// model without force would silently do nothing for it.
		Harness: HarnessClaude, Name: "subagent_model", Kind: KindModel,
		Label: "Subagent model",
		Help: "Every subagent's model, pins in an agent definition and per-call models included " +
			"(CLAUDE_CODE_SUBAGENT_MODEL, forced).",
	},
	{
		Harness: HarnessClaude, Name: "effort", Kind: KindEnum,
		Options: options(HarnessClaude, "effort"),
		Label:   "Effort",
		Help:    "Reasoning effort (CLAUDE_CODE_EFFORT_LEVEL).",
	},
	{
		Harness: HarnessPi, Name: "provider", Kind: KindProvider,
		Label: "Provider",
		Help:  "pi's --provider.",
	},
	{
		Harness: HarnessPi, Name: "model", Kind: KindModel,
		Label: "Model",
		Help:  "pi's --model.",
	},
	{
		Harness: HarnessPi, Name: "thinking", Kind: KindEnum,
		Options: options(HarnessPi, "thinking"),
		Label:   "Thinking",
		Help:    "pi's --thinking.",
	},
	{
		Harness: HarnessCodex, Name: "model", Kind: KindModel,
		Label: "Model",
		Help:  "Codex's model.",
	},
	{
		Harness: HarnessCodex, Name: "effort", Kind: KindEnum,
		Options: options(HarnessCodex, "effort"),
		Label:   "Effort",
		Help:    "Codex's reasoning effort.",
	},
}

// Definitions returns the registry, in the order the UI shows it.
func Definitions() []Definition {
	out := make([]Definition, len(definitions))
	for i, d := range definitions {
		d.Options = slices.Clone(d.Options)
		out[i] = d
	}
	return out
}

// Harnesses lists every harness with at least one setting, sorted.
func Harnesses() []string {
	var out []string
	for _, d := range definitions {
		if !slices.Contains(out, d.Harness) {
			out = append(out, d.Harness)
		}
	}
	sort.Strings(out)
	return out
}

func lookup(harness, name string) (Definition, bool) {
	for _, d := range definitions {
		if d.Harness == harness && d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}

var (
	ErrUnknownSetting = errors.New("harness config: unknown setting")
	ErrInvalidValue   = errors.New("harness config: invalid value")
	// ErrNotConfigOwner is a conductor's override changed by anyone but its owner.
	ErrNotConfigOwner = errors.New("harness config: not the owner of this agent's config")
	ErrAgentNotFound  = errors.New("harness config: agent not found")
)

// Pattern is the regular expression a KindModel or KindProvider value must
// match, for the UI to check as it is typed; empty for an enum. The server
// checks again on save, so this is a convenience, not the boundary.
func (d Definition) Pattern() string {
	switch d.Kind {
	case KindModel:
		return modelPattern.String()
	case KindProvider:
		return providerPattern.String()
	default:
		return ""
	}
}

// Valid reports whether value is acceptable for this setting.
func (d Definition) Valid(value string) bool {
	switch d.Kind {
	case KindModel:
		return modelPattern.MatchString(value)
	case KindProvider:
		return providerPattern.MatchString(value)
	case KindEnum:
		return slices.Contains(d.Options, value)
	default:
		return false
	}
}

// Values is one scope's settings: harness, then setting name, then value.
// A setting that is absent is unset.
type Values map[string]map[string]string

// Validate refuses anything but known settings with valid values. The
// error names the setting but never echoes the value: it is the caller's
// own input, but a log line is not the place for it.
func (v Values) Validate() error {
	for harness, settings := range v {
		for name, value := range settings {
			def, ok := lookup(harness, name)
			if !ok {
				return fmt.Errorf("%w: %s.%s", ErrUnknownSetting, harness, name)
			}
			if !def.Valid(value) {
				return fmt.Errorf("%w for %s.%s", ErrInvalidValue, harness, name)
			}
		}
	}
	return nil
}

// Known returns a copy holding only the known, valid settings, with an
// empty map for every harness so a reader never meets a missing section.
// What is stored was validated on write; this is the check on the way out,
// for a row written before a setting was dropped or narrowed.
func (v Values) Known() Values {
	out := Values{}
	for _, harness := range Harnesses() {
		out[harness] = map[string]string{}
	}
	for harness, settings := range v {
		for name, value := range settings {
			if def, ok := lookup(harness, name); ok && def.Valid(value) {
				out[harness][name] = value
			}
		}
	}
	return out
}

// Overlay returns base with every setting in top laid over it, one setting
// at a time: an override that sets only the model leaves the default's
// effort in place.
func Overlay(base, top Values) Values {
	out := base.Known()
	for harness, settings := range top.Known() {
		for name, value := range settings {
			out[harness][name] = value
		}
	}
	return out
}

// Scope is one saved set of settings: the tenant's defaults (AgentID nil),
// or one agent's override.
type Scope struct {
	AgentID  *uuid.UUID
	Values   Values
	Revision int64
	// UpdatedBy is the person who last saved it.
	UpdatedBy *uuid.UUID
	// UpdatedByDeleted says UpdatedBy is nil because that person's account
	// was deleted.
	UpdatedByDeleted bool
	UpdatedAt        time.Time
}

// Effective is what one agent runs with: the defaults with its override
// on top, and the revision that names that combination.
type Effective struct {
	// Revision is the later of the defaults' and the override's; zero when
	// neither was ever saved. A session started on an earlier revision is
	// running a config other than the current one.
	Revision int64
	Values   Values
	// Harness is the harness the agent's variant runs, "" for an agent in
	// none, which runs whichever harness it is started with.
	Harness string
	// Variant is the version the agent runs on, nil for none.
	Variant *VariantMembership
	// OverrideInactive is whether the agent has a saved override that
	// resolution is ignoring because it is in a variant. The override stays
	// saved and applies again once the agent leaves.
	OverrideInactive bool
}

// VariantMembership is the variant version an agent runs on, as far as
// resolving its config and its briefing needs. The variant package owns the
// versions; this is only what it hands over, so no settings logic is copied.
type VariantMembership struct {
	VersionID uuid.UUID
	Name      string
	Version   int
	Harness   string
	// Settings are the harness's own, name to value.
	Settings map[string]string
	// FleetInstructionsVersionID is nil for explicitly none.
	FleetInstructionsVersionID *uuid.UUID
	// Revision is the tenant's config revision at which the agent last
	// changed variant. Versions never change, so it is what tells a session
	// started before the change from one started after it.
	Revision int64
}

// Tenant is every saved scope of one tenant, and which agents run on a
// variant.
type Tenant struct {
	Defaults  *Scope
	Overrides map[uuid.UUID]*Scope
	Variants  map[uuid.UUID]*VariantMembership
}

// EffectiveFor is what agentID runs with. An agent in a variant runs the
// variant's settings alone: not the tenant's defaults and not its own
// override, which stays saved. Otherwise it lays the agent's override, if
// any, over the defaults.
func (t *Tenant) EffectiveFor(agentID uuid.UUID) Effective {
	if m := t.Variants[agentID]; m != nil {
		return Effective{
			Revision:         m.Revision,
			Values:           Values{m.Harness: m.Settings}.Known(),
			Harness:          m.Harness,
			Variant:          m,
			OverrideInactive: t.Overrides[agentID] != nil,
		}
	}
	var eff Effective
	var base, top Values
	if t.Defaults != nil {
		base = t.Defaults.Values
		eff.Revision = t.Defaults.Revision
	}
	if o := t.Overrides[agentID]; o != nil {
		top = o.Values
		eff.Revision = max(eff.Revision, o.Revision)
	}
	eff.Values = Overlay(base, top)
	return eff
}
