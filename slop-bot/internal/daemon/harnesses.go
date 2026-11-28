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

// builders are the harness implementations this build has, by harness and
// driver.
var builders = map[[2]string]func(logger *slog.Logger, clk clock.Clock) harness.AIHarness{
	{config.ClaudeCode, claudecode.Driver}: func(l *slog.Logger, c clock.Clock) harness.AIHarness {
		return &claudecode.Harness{Logger: l, Clock: c}
	},
	{config.Codex, codex.Driver}: func(l *slog.Logger, c clock.Clock) harness.AIHarness {
		return &codex.Harness{Logger: l, Clock: c, Version: version.String()}
	},
	{config.Pi, pi.Driver}: func(l *slog.Logger, c clock.Clock) harness.AIHarness {
		return &pi.Harness{Logger: l, Clock: c}
	},
}

// Implemented reports whether this build can run the harness under the
// driver: the config accepts drivers still to come, and doctor says so.
func Implemented(name, driver string) bool {
	_, ok := builders[[2]string{name, driver}]
	return ok
}

// configuredHarnesses builds the implementation for each harness in the
// config, under its configured driver. A harness whose driver this build
// doesn't have yet is left out: tickets that ask for it are misconfigured.
func configuredHarnesses(cfg *config.Config, logger *slog.Logger, clk clock.Clock) Harnesses {
	out := Harnesses{}
	for name, hc := range cfg.Harnesses {
		if build, ok := builders[[2]string{name, hc.Driver}]; ok {
			out[name] = build(logger, clk)
		}
	}
	return out
}
