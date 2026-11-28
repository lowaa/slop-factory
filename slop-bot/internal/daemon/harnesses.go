package daemon

import (
	"log/slog"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/claudecode"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/pi"
	"github.com/lowaa/slop-factory/slop-bot/internal/version"
)

// Harnesses maps a configured harness name to its implementation.
type Harnesses map[string]harness.AIHarness

// Harness implements agents.Harnesses.
func (h Harnesses) Harness(name string) (harness.AIHarness, bool) {
	a, ok := h[name]
	return a, ok
}

// configuredHarnesses builds the implementation for each harness in the
// config, under its configured driver. A harness whose driver this build
// doesn't have yet is left out: tickets that ask for it are misconfigured.
func configuredHarnesses(cfg *config.Config, logger *slog.Logger, clk clock.Clock) Harnesses {
	out := Harnesses{}
	for name, hc := range cfg.Harnesses {
		switch {
		case name == config.ClaudeCode && hc.Driver == claudecode.Driver:
			out[name] = &claudecode.Harness{Logger: logger, Clock: clk}
		case name == config.Codex && hc.Driver == codex.Driver:
			out[name] = &codex.Harness{Logger: logger, Clock: clk, Version: version.String()}
		case name == config.Pi && hc.Driver == pi.Driver:
			out[name] = &pi.Harness{Logger: logger, Clock: clk}
		}
	}
	return out
}
