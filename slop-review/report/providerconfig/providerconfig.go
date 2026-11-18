// Package providerconfig is which provider, model and reasoning effort
// slop-review reviews with, as a tenant sets it centrally on
// the report server and every reporting machine fetches it.
//
// It is a trust boundary: whoever controls this config decides where every
// reviewed diff, and every file a reviewer reads, is sent. So it can only
// choose among the providers compiled in here -- by name -- and a model and
// effort for one. It never carries a base URL, an API-key variable name or a
// key: those stay in the reviewer's own provider profiles and each machine's
// environment. The server and the reviewer both validate with this package,
// so neither accepts a provider the other doesn't know.
package providerconfig

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/report"
)

// Provider is what the rest of the system may know about a compiled-in
// provider: nothing about how it is reached.
type Provider struct {
	// DefaultModel is what it reviews with when no model is chosen.
	DefaultModel string
	// ReasoningEffort says whether it takes an effort as a parameter.
	// OpenRouter selects effort by model slug instead
	// (openai/gpt-5.6-luna-pro).
	ReasoningEffort bool
	// Efforts narrows the efforts it accepts; nil means all of
	// ReasoningEfforts.
	Efforts []string
}

// Providers are the providers compiled into slop-review, by name.
var Providers = map[string]Provider{
	"chatgpt":    {DefaultModel: "gpt-5.6-luna", ReasoningEffort: true},
	"openrouter": {DefaultModel: "openai/gpt-5.6-luna"},
	"openai":     {DefaultModel: "gpt-5.6-luna"},
	// codex and claude run the vendor's own CLI under the machine's login,
	// so (unlike the others) the CLI reads the repository itself.
	"codex":  {DefaultModel: "gpt-5.6-luna", ReasoningEffort: true},
	"claude": {DefaultModel: "claude-sonnet-5-5", ReasoningEffort: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
}

// AcceptsEffort reports whether p takes effort as a parameter.
func (p *Provider) AcceptsEffort(effort string) bool {
	if !p.ReasoningEffort || !slices.Contains(ReasoningEfforts, effort) {
		return false
	}
	return p.Efforts == nil || slices.Contains(p.Efforts, effort)
}

// DefaultReasoningEffort is what a review runs at when no effort is chosen,
// on a provider that accepts it: slop-review reviews a change in one
// large-context pass, which is only as good as its reasoning. A provider
// that takes no effort parameter keeps its model's default.
const DefaultReasoningEffort = "xhigh"

// EffortOrDefault is effort, or, when that is empty, DefaultReasoningEffort
// if p accepts it and "" (the model's default) if not.
func (p *Provider) EffortOrDefault(effort string) string {
	if effort == "" && p.AcceptsEffort(DefaultReasoningEffort) {
		return DefaultReasoningEffort
	}
	return effort
}

// ProviderNames lists Providers' names, sorted.
func ProviderNames() []string {
	return slices.Sorted(maps.Keys(Providers))
}

// ReasoningEfforts are the efforts a provider that takes one accepts.
var ReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// TriggerClasses are the failure classes a fallback may be taken on: the
// provider's own failures. config and other are local mistakes that another
// provider would not fix.
var TriggerClasses = []report.FailureClass{
	report.FailureUsageLimit, report.FailureCredits, report.FailureAuth, report.FailureBackend,
}

// DefaultTrigger is what a fallback is taken on unless more is chosen:
// falling back on auth hides a lost login behind a working review, and on
// backend spends money on what is often a passing blip.
var DefaultTrigger = []report.FailureClass{report.FailureUsageLimit}

// MaxModelBytes bounds a model name.
const MaxModelBytes = 128

// modelPattern is a model slug: letters, digits and the punctuation real
// slugs use (openai/gpt-5.6-luna-pro, deepseek/deepseek-v4-pro-0813,
// vendor/model:free). No spaces, quotes or anything a URL or shell would
// treat specially. A model only ever travels in a request body, never a
// URL, but "//" and ".." are refused anyway: no real slug has them, and a
// model that looks like a URL or a path is a sign of something else.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// Choice is one provider and how to use it.
type Choice struct {
	Provider string `json:"provider" doc:"a provider compiled into slop-review"`
	// Model empty means the provider's DefaultModel.
	Model string `json:"model" doc:"the model slug; empty for the provider's default"`
	// ReasoningEffort empty means DefaultReasoningEffort where the provider
	// accepts it, else the model's own default (see EffortOrDefault).
	ReasoningEffort string `json:"reasoning_effort" doc:"only for providers that take one; empty for xhigh, or the model's default on a provider that does not take xhigh"`
}

// Settings is what a person chooses: everything but the bookkeeping.
type Settings struct {
	Primary Choice `json:"primary"`
	// Fallback, if set, is used when Primary fails with one of FallbackOn.
	Fallback   *Choice               `json:"fallback,omitempty"`
	FallbackOn []report.FailureClass `json:"fallback_on,omitempty"`
}

// Config is a tenant's settings as served to a reviewer.
type Config struct {
	Settings
	// Revision counts the tenant's saves, from 1. A run reports the
	// revision it ran under.
	Revision int64 `json:"revision"`
}

// ModelOrDefault is c's model, or its provider's default.
func (c *Choice) ModelOrDefault() string {
	if c.Model != "" {
		return c.Model
	}
	return Providers[c.Provider].DefaultModel
}

// Validate reports every way c is not a choice among the compiled-in
// providers, joined. name prefixes each problem.
func (c *Choice) Validate(name string) error {
	var errs []error
	p, ok := Providers[c.Provider]
	if !ok {
		errs = append(errs, fmt.Errorf("%s.provider %q is not one of %v", name, c.Provider, ProviderNames()))
	}
	if c.Model != "" {
		if len(c.Model) > MaxModelBytes {
			errs = append(errs, fmt.Errorf("%s.model is longer than %d bytes", name, MaxModelBytes))
		} else if !modelPattern.MatchString(c.Model) || strings.Contains(c.Model, "//") || strings.Contains(c.Model, "..") {
			errs = append(errs, fmt.Errorf("%s.model %q is not a model slug (letters, digits and . _ : / -)", name, c.Model))
		}
	}
	if c.ReasoningEffort != "" {
		switch {
		case ok && !p.ReasoningEffort:
			errs = append(errs, fmt.Errorf("%s.reasoning_effort can't be set for %q: it selects effort by model slug", name, c.Provider))
		case !slices.Contains(ReasoningEfforts, c.ReasoningEffort):
			errs = append(errs, fmt.Errorf("%s.reasoning_effort %q is not one of %v", name, c.ReasoningEffort, ReasoningEfforts))
		case ok && !p.AcceptsEffort(c.ReasoningEffort):
			errs = append(errs, fmt.Errorf("%s.reasoning_effort %q is not accepted by %q (one of %v)", name, c.ReasoningEffort, c.Provider, p.Efforts))
		}
	}
	return errors.Join(errs...)
}

// Validate reports every way s is not a usable config, joined.
func (s *Settings) Validate() error {
	errs := []error{s.Primary.Validate("primary")}
	if s.Fallback == nil {
		if len(s.FallbackOn) > 0 {
			errs = append(errs, errors.New("fallback_on must be empty without a fallback"))
		}
		return errors.Join(errs...)
	}
	errs = append(errs, s.Fallback.Validate("fallback"))
	// The same provider again would fail the same way: a usage limit or a
	// lost login is the provider's, whatever the model.
	if s.Fallback.Provider == s.Primary.Provider {
		errs = append(errs, fmt.Errorf("fallback.provider must differ from the primary's (%q)", s.Primary.Provider))
	}
	if len(s.FallbackOn) == 0 {
		errs = append(errs, fmt.Errorf("fallback_on must name at least one of %v", TriggerClasses))
	}
	seen := map[report.FailureClass]bool{}
	for _, c := range s.FallbackOn {
		if !slices.Contains(TriggerClasses, c) {
			errs = append(errs, fmt.Errorf("fallback_on: %q is not one of %v", c, TriggerClasses))
		}
		if seen[c] {
			errs = append(errs, fmt.Errorf("fallback_on: %q is repeated", c))
		}
		seen[c] = true
	}
	return errors.Join(errs...)
}

// Validate reports every way c is not a usable config, joined.
func (c *Config) Validate() error {
	var errs []error
	if c.Revision < 1 {
		errs = append(errs, fmt.Errorf("revision %d must be at least 1", c.Revision))
	}
	errs = append(errs, c.Settings.Validate())
	return errors.Join(errs...)
}

// FallsBackOn reports whether a failure of class takes the fallback.
func (s *Settings) FallsBackOn(class report.FailureClass) bool {
	return s.Fallback != nil && slices.Contains(s.FallbackOn, class)
}

// Path is where a reviewer fetches its tenant's config, relative to the
// server's base URL, with its ingest token.
const Path = "/api/v1/provider-config"
