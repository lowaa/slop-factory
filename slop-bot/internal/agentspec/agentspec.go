// Package agentspec resolves and validates a ticket's agent spec (harness,
// model, effort) against this machine's config, early and loudly: a bad spec
// is caught when the ticket is filed or edited, and again just before an
// agent is started, never as a crashed agent.
package agentspec

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("agent spec")

// efforts are the effort or thinking levels each harness's CLI accepts.
// Codex is missing on purpose: its levels are per model, and slop-bot
// doesn't check them; a bad one surfaces as codex's own error.
var efforts = map[string][]string{
	config.ClaudeCode: {"low", "medium", "high", "xhigh", "max"},
	config.Pi:         {"off", "minimal", "low", "medium", "high", "xhigh", "max"},
	config.Agy:        {"low", "medium", "high", "xhigh", "max"},
}

// Resolve fills a blank harness with the config's default_harness, and a
// blank model with that harness's configured model. A model still blank, and
// a blank effort, stay blank: the harness uses its own default.
func Resolve(spec tickets.AgentSpec, cfg *config.Config) tickets.AgentSpec {
	if spec.Harness == "" {
		spec.Harness = cfg.DefaultHarness
	}
	if h, ok := cfg.Harnesses[spec.Harness]; ok && spec.Model == "" {
		spec.Model = h.Model
	}
	return spec
}

// Validate checks spec, after Resolve, against cfg. Its error says how to
// fix the ticket.
func Validate(spec tickets.AgentSpec, cfg *config.Config) error {
	if _, ok := cfg.Harnesses[spec.Harness]; !ok {
		return fmt.Errorf("%w: harness %q isn't configured on this machine (configured: %s)",
			ErrInvalid, spec.Harness, strings.Join(configured(cfg), ", "))
	}
	if spec.Effort == "" {
		return nil
	}
	if levels, ok := efforts[spec.Harness]; ok && !slices.Contains(levels, spec.Effort) {
		return fmt.Errorf("%w: effort %q isn't one %s accepts (%s)",
			ErrInvalid, spec.Effort, spec.Harness, strings.Join(levels, ", "))
	}
	return nil
}

func configured(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Harnesses))
	for n := range cfg.Harnesses {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
