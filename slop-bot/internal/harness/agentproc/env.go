package agentproc

import (
	"slices"
	"sort"
	"strings"
)

// apiKeyVars are the variables that switch a harness from the user's
// subscription login to per-token API billing. `claude -p` silently prefers
// ANTHROPIC_API_KEY when it is set, so these never reach an agent unless the
// harness is opted in.
var apiKeyVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"OPENAI_API_KEY", "CODEX_API_KEY",
	"GEMINI_API_KEY", "GOOGLE_API_KEY",
}

// leakPrefixes are variables a parent harness sets for its own children.
// An agent launched from inside a Claude Code session (tests, manual runs)
// would otherwise think it is that session's child.
var leakPrefixes = []string{"CLAUDE_CODE_", "CLAUDECODE"}

// EnvOptions shapes an agent's environment.
type EnvOptions struct {
	// Base is the daemon's environment, as os.Environ gives it.
	Base []string
	// APIKeyOptIn lets apiKeyVars through.
	APIKeyOptIn bool
	// Drop are further variables to remove, e.g. another agent's token.
	Drop []string
	// Set overrides or adds variables.
	Set map[string]string
}

// AgentEnv builds an agent's environment from the daemon's.
//
// It removes known-bad variables rather than allowing a fixed list: an agent
// works with the user's own toolchains (go, node, nvm, gh, ssh-agent), whose
// variables can't be enumerated in advance, and the user's configuration is
// the point (minimal setup). The daemon's own environment is the one the
// installer wrote into the service unit.
func AgentEnv(opts *EnvOptions) []string {
	drop := func(name string) bool {
		if slices.Contains(opts.Drop, name) {
			return true
		}
		if !opts.APIKeyOptIn && slices.Contains(apiKeyVars, name) {
			return true
		}
		for _, p := range leakPrefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		_, overridden := opts.Set[name]
		return overridden
	}
	out := make([]string, 0, len(opts.Base)+len(opts.Set))
	for _, kv := range opts.Base {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || drop(name) {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(opts.Set))
	for k := range opts.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+opts.Set[k])
	}
	return out
}
