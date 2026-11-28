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

// buildOptions is what every harness implementation is built with.
type buildOptions struct {
	logger *slog.Logger
	clock  clock.Clock
	// preToolHook is `slop-bot hook pre-tool`, the merge guard, for the
	// harnesses whose drivers have a pre-tool hook to run it from.
	preToolHook []string
}

// builders are the harness implementations this build has, by harness and
// driver. Codex gets no merge guard: its hooks don't run under app-server
// (docs/merge_guard.md).
var builders = map[[2]string]func(o *buildOptions) harness.AIHarness{
	{config.ClaudeCode, claudecode.Driver}: func(o *buildOptions) harness.AIHarness {
		return &claudecode.Harness{Logger: o.logger, Clock: o.clock, PreToolHook: o.preToolHook}
	},
	{config.Codex, codex.Driver}: func(o *buildOptions) harness.AIHarness {
		return &codex.Harness{Logger: o.logger, Clock: o.clock, Version: version.String()}
	},
	{config.Pi, pi.Driver}: func(o *buildOptions) harness.AIHarness {
		return &pi.Harness{Logger: o.logger, Clock: o.clock, PreToolHook: o.preToolHook}
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
func configuredHarnesses(cfg *config.Config, o *buildOptions) Harnesses {
	out := Harnesses{}
	for name, hc := range cfg.Harnesses {
		if build, ok := builders[[2]string{name, hc.Driver}]; ok {
			out[name] = build(o)
		}
	}
	return out
}
