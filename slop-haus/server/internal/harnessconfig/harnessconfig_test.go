package harnessconfig_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
)

func TestValidate_AcceptsKnownSettingsWithValidValues(t *testing.T) {
	for _, v := range []harnessconfig.Values{
		{},
		{"claude": {"model": "sonnet"}},
		{"claude": {"model": "claude-opus-5-5", "subagent_model": "opus[1m]", "effort": "xhigh"}},
		{"pi": {"provider": "openrouter", "model": "qwen/qwen3-coder:free", "thinking": "off"}},
		{"pi": {"model": "A" + strings.Repeat("b", 99)}},
		{"codex": {"model": "gpt-5", "effort": "ultra"}},
	} {
		assert.NoError(t, v.Validate(), "%v", v)
	}
}

// Every value here would reach a harness's environment or command line if
// it were stored, so the patterns are the first wall: nothing that could be
// read as a flag, carry a shell or control character, or grow unbounded.
func TestValidate_RefusesUnknownSettingsAndBadValues(t *testing.T) {
	unknown := []harnessconfig.Values{
		{"claude": {"ANTHROPIC_MODEL": "sonnet"}},
		{"claude": {"path": "/tmp"}},
		{"claude": {"thinking": "high"}},
		{"pi": {"effort": "high"}},
		{"emacs": {"model": "gpt"}},
		{"codex": {"provider": "openai"}},
		{"codex": {"thinking": "high"}},
		{"codex": {"subagent_model": "gpt-5"}},
		{"": {"model": "sonnet"}},
	}
	for _, v := range unknown {
		assert.ErrorIs(t, v.Validate(), harnessconfig.ErrUnknownSetting, "%v", v)
	}

	invalid := []harnessconfig.Values{
		{"claude": {"model": ""}},
		{"claude": {"model": "--help"}},
		{"claude": {"model": "-x"}},
		{"claude": {"model": "sonnet opus"}},
		{"claude": {"model": "sonnet;rm -rf ~"}},
		{"claude": {"model": "$(id)"}},
		{"claude": {"model": "sonnet\n"}},
		{"claude": {"model": "sonnet\x00"}},
		{"claude": {"model": "a" + strings.Repeat("b", 100)}},
		{"claude": {"model": "sönnet"}},
		{"claude": {"effort": "HIGH"}},
		{"claude": {"effort": "extreme"}},
		{"pi": {"provider": "Anthropic"}},
		{"pi": {"provider": "open router"}},
		{"pi": {"provider": "-p"}},
		{"pi": {"thinking": "auto"}},
		{"codex": {"effort": "auto"}},
	}
	for _, v := range invalid {
		assert.ErrorIs(t, v.Validate(), harnessconfig.ErrInvalidValue, "%q", v)
	}
}

func TestKnown_DropsWhatTheRegistryWouldRefuseAndFillsEveryHarness(t *testing.T) {
	got := harnessconfig.Values{
		"claude": {"model": "sonnet", "gone": "x", "effort": "not-an-option"},
		"emacs":  {"model": "gpt"},
	}.Known()
	assert.Equal(t, harnessconfig.Values{"claude": {"model": "sonnet"}, "codex": {}, "pi": {}}, got)
}

func TestOverlay_IsOneSettingAtATime(t *testing.T) {
	base := harnessconfig.Values{"claude": {"model": "sonnet", "effort": "high"}, "pi": {"model": "m1"}}
	top := harnessconfig.Values{"claude": {"model": "opus"}}
	assert.Equal(t, harnessconfig.Values{
		"claude": {"model": "opus", "effort": "high"},
		"codex":  {},
		"pi":     {"model": "m1"},
	}, harnessconfig.Overlay(base, top))
}

// An agent's revision is the later of the two scopes it runs, so changing
// one agent's override marks that agent -- and no other -- as behind.
func TestEffectiveFor_TakesTheLaterRevision(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	tenant := &harnessconfig.Tenant{
		Defaults: &harnessconfig.Scope{Revision: 3, Values: harnessconfig.Values{"claude": {"model": "sonnet"}}},
		Overrides: map[uuid.UUID]*harnessconfig.Scope{
			a: {AgentID: &a, Revision: 5, Values: harnessconfig.Values{"claude": {"effort": "low"}}},
		},
	}

	effA := tenant.EffectiveFor(a)
	assert.Equal(t, int64(5), effA.Revision)
	assert.Equal(t, map[string]string{"model": "sonnet", "effort": "low"}, effA.Values["claude"])

	effB := tenant.EffectiveFor(b)
	assert.Equal(t, int64(3), effB.Revision)
	assert.Equal(t, map[string]string{"model": "sonnet"}, effB.Values["claude"])

	none := (&harnessconfig.Tenant{}).EffectiveFor(a)
	assert.Zero(t, none.Revision)
	assert.Equal(t, harnessconfig.Values{"claude": {}, "codex": {}, "pi": {}}, none.Values)
}

// An agent in a variant runs the variant's settings alone, under the revision
// of its membership; its override is kept but inactive, and counts again once
// the membership is gone.
func TestEffectiveFor_AnAgentInAVariantIgnoresDefaultsAndItsOverride(t *testing.T) {
	a := uuid.New()
	tenant := &harnessconfig.Tenant{
		Defaults: &harnessconfig.Scope{Revision: 3, Values: harnessconfig.Values{"claude": {"model": "sonnet"}}},
		Overrides: map[uuid.UUID]*harnessconfig.Scope{
			a: {AgentID: &a, Revision: 5, Values: harnessconfig.Values{"claude": {"effort": "low"}}},
		},
		Variants: map[uuid.UUID]*harnessconfig.VariantMembership{
			a: {Harness: "pi", Settings: map[string]string{"model": "qwen/qwen3:free"}, Revision: 8},
		},
	}

	eff := tenant.EffectiveFor(a)
	assert.Equal(t, int64(8), eff.Revision)
	assert.Equal(t, "pi", eff.Harness)
	assert.True(t, eff.OverrideInactive)
	assert.Equal(t, harnessconfig.Values{"claude": {}, "codex": {}, "pi": {"model": "qwen/qwen3:free"}}, eff.Values)

	tenant.Variants = nil
	eff = tenant.EffectiveFor(a)
	assert.False(t, eff.OverrideInactive)
	assert.Empty(t, eff.Harness)
	assert.Equal(t, map[string]string{"model": "sonnet", "effort": "low"}, eff.Values["claude"])
}

// The usage server refuses a whole report over a provider its pattern
// rejects, so config must accept exactly what go-llm-usage does.
func TestValidate_ProviderMatchesTheUsageServer(t *testing.T) {
	raw, err := os.ReadFile("../../../../go-llm-usage/fixtures/record_labels.json")
	require.NoError(t, err)
	var fixture struct {
		Provider struct {
			Valid   []string `json:"valid"`
			Invalid []string `json:"invalid"`
		} `json:"provider"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	for _, v := range fixture.Provider.Valid {
		assert.NoError(t, harnessconfig.Values{"pi": {"provider": v}}.Validate(), "%q", v)
	}
	for _, v := range fixture.Provider.Invalid {
		assert.Error(t, harnessconfig.Values{"pi": {"provider": v}}.Validate(), "%q", v)
	}
}

// The contract owns which harnesses and settings exist; the definitions here
// add labels and help. Neither may have one the other lacks.
func TestDefinitionsMatchTheHarnessRegistry(t *testing.T) {
	type key struct{ harness, name, kind string }
	var fromRegistry, fromDefinitions []key
	for _, h := range harness.All() {
		for _, s := range h.Settings {
			fromRegistry = append(fromRegistry, key{h.Name, s.Name, s.Kind})
			if s.Kind == "enum" {
				def, ok := lookupDefinition(h.Name, s.Name)
				require.True(t, ok, "%s.%s", h.Name, s.Name)
				assert.Equal(t, s.Options, def.Options, "%s.%s", h.Name, s.Name)
			}
		}
	}
	for _, d := range harnessconfig.Definitions() {
		fromDefinitions = append(fromDefinitions, key{d.Harness, d.Name, string(d.Kind)})
	}
	assert.ElementsMatch(t, fromRegistry, fromDefinitions)
	assert.ElementsMatch(t, harness.Names(), harnessconfig.Harnesses())
}

func lookupDefinition(h, name string) (harnessconfig.Definition, bool) {
	for _, d := range harnessconfig.Definitions() {
		if d.Harness == h && d.Name == name {
			return d, true
		}
	}
	return harnessconfig.Definition{}, false
}
