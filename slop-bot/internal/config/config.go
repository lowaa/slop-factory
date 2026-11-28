// Package config loads and validates slop-bot-config.yaml. Every other
// package receives the parsed, defaulted Config; none reads the file itself.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Harness names, as tickets and the config spell them.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
	Pi         = "pi"
	Agy        = "agy"
)

// drivers lists the drivers each harness can run under, the first being its
// default. Only drivers that exist are listed, so a config can't name one
// slop-bot can't run.
var drivers = map[string][]string{
	ClaudeCode: {"stream-json"},
	Codex:      {"app-server"},
	Pi:         {"rpc"},
	Agy:        {"tui"},
}

// Config is slop-bot-config.yaml after defaults and validation.
type Config struct {
	// WorkspaceDir is where ticket worktrees are created.
	WorkspaceDir string `yaml:"workspace_dir"`
	// User is who slop-bot runs for: tickets assigned to this id are its
	// agents' to work. Blank means the OS user name.
	User string `yaml:"user"`
	// BriefingPath is the user's own briefing. Empty means none.
	BriefingPath       string                    `yaml:"briefing_path"`
	DefaultHarness     string                    `yaml:"default_harness"`
	AgentsMaySetUrgent *bool                     `yaml:"agents_may_set_urgent"`
	Ticketing          Ticketing                 `yaml:"ticketing"`
	Repos              []Repo                    `yaml:"repos"`
	Concurrency        Concurrency               `yaml:"concurrency"`
	Autonomy           Autonomy                  `yaml:"autonomy"`
	Harnesses          map[string]*HarnessConfig `yaml:"harnesses"`
	Agents             Agents                    `yaml:"agents"`
	Retention          Retention                 `yaml:"retention"`
	Doctor             Doctor                    `yaml:"doctor"`
}

// Doctor is what `slop-bot doctor` checks beyond slop-bot's own needs.
type Doctor struct {
	// Require are binaries the user's workflow needs on PATH (gh, linters):
	// slop-bot doesn't use them, but its agents will.
	Require []string `yaml:"require"`
}

// Ticketing picks the TicketingSystem. It's edited by hand for now.
type Ticketing struct {
	System string `yaml:"system"`
}

// Repo is one entry of this machine's repo lookup.
type Repo struct {
	Name      string `yaml:"name"`
	Path      string `yaml:"path"`
	GitRemote string `yaml:"git_remote"`
}

// Concurrency bounds how many agents run.
type Concurrency struct {
	// MaxActive counts agents in a turn; waiting agents don't count.
	MaxActive int `yaml:"max_active"`
	// MaxLive caps live harness processes, working or waiting.
	MaxLive int  `yaml:"max_live"`
	Urgent  Pool `yaml:"urgent"`
}

// Pool is a separately limited pool.
type Pool struct {
	MaxActive int `yaml:"max_active"`
}

// Autonomy governs whether agent-created tickets are worked without a person.
type Autonomy struct {
	// Enabled is the configured default; `slop-bot autonomy on|off` overrides
	// it at runtime.
	Enabled bool `yaml:"enabled"`
	// TicketsPerAgent multiplies the number of agent slots into the budget of
	// outstanding agent-created tickets.
	TicketsPerAgent int `yaml:"tickets_per_agent"`
}

// HarnessConfig is one harness as this machine runs it.
type HarnessConfig struct {
	Driver string `yaml:"driver"`
	// APIKeyOptIn lets the harness's API key through from the environment.
	// Off by default: `claude -p` silently bills an ANTHROPIC_API_KEY at API
	// rates instead of using the subscription login.
	APIKeyOptIn bool `yaml:"api_key_opt_in"`
	// Permissive runs the harness without permission prompts. Default true:
	// nobody is watching a headless agent's dialogs.
	Permissive *bool `yaml:"permissive"`
	// Model is what agents run on when their ticket names none. Blank
	// leaves it to the harness (pi's adapter then picks a known-good one).
	Model string `yaml:"model"`
	// LimitThreshold is the percentage of the plan's allowance at which no
	// new agent is started on this harness until the window resets. 0, or
	// unset, is DefaultLimitThreshold.
	LimitThreshold float64 `yaml:"limit_threshold"`
}

// DefaultLimitThreshold leaves a little of the allowance for the agents
// already running, and for the user's own interactive use.
const DefaultLimitThreshold = 95

// Agents holds the lifecycle timings.
type Agents struct {
	// Nudges is how many times an idle agent is nudged before it's stalled.
	Nudges int `yaml:"nudges"`
	// StallAfter is how long a working agent may go without an event.
	StallAfter Duration `yaml:"stall_after"`
	// AskReminderAfter is when the user is reminded of an unanswered ask.
	AskReminderAfter Duration `yaml:"ask_reminder_after"`
	// AttachLease is how long an attach outlives its CLI's last heartbeat:
	// across a daemon restart, the attached CLI has this long to reconnect
	// before the agent is handed back.
	AttachLease Duration `yaml:"attach_lease"`
}

// Retention is how long finished records are kept.
type Retention struct {
	Transcripts Duration `yaml:"transcripts"`
	Events      Duration `yaml:"events"`
}

// Duration is a time.Duration written as "15m", "24h".
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %q isn't a duration like 15m or 24h", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Load reads, defaults and validates the config at path. home expands "~".
func Load(path, home string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the user names their own config file
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	// Absolute, so a relative --config still resolves relative entries to
	// absolute paths: worktree paths are compared with git's, which are.
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(raw, dir, home)
}

// Parse is Load on bytes. dir resolves relative paths: the config file's
// directory, not the daemon's cwd.
func Parse(raw []byte, dir, home string) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// A misspelt key silently ignored is a setting that silently doesn't
	// apply.
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyDefaults(home)
	if err := c.resolvePaths(dir, home); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyDefaults(home string) {
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = filepath.Join(home, "slop-bot", "workspace")
	}
	if c.DefaultHarness == "" {
		c.DefaultHarness = ClaudeCode
	}
	if c.AgentsMaySetUrgent == nil {
		c.AgentsMaySetUrgent = ptr(true)
	}
	if c.Ticketing.System == "" {
		c.Ticketing.System = "local"
	}
	c.defaultConcurrency()
	if c.Autonomy.TicketsPerAgent == 0 {
		c.Autonomy.TicketsPerAgent = 10
	}
	c.defaultHarnesses()
	c.defaultDurations()
}

func (c *Config) defaultConcurrency() {
	if c.Concurrency.MaxActive == 0 {
		c.Concurrency.MaxActive = 4
	}
	if c.Concurrency.MaxLive == 0 {
		c.Concurrency.MaxLive = 3 * c.Concurrency.MaxActive
	}
	if c.Concurrency.Urgent.MaxActive == 0 {
		c.Concurrency.Urgent.MaxActive = 1
	}
}

func (c *Config) defaultHarnesses() {
	if len(c.Harnesses) == 0 {
		c.Harnesses = map[string]*HarnessConfig{c.DefaultHarness: {}}
	}
	for name, h := range c.Harnesses {
		if h == nil {
			h = &HarnessConfig{}
			c.Harnesses[name] = h
		}
		if h.Driver == "" && len(drivers[name]) > 0 {
			h.Driver = drivers[name][0]
		}
		if h.Permissive == nil {
			h.Permissive = ptr(true)
		}
		if h.LimitThreshold == 0 {
			h.LimitThreshold = DefaultLimitThreshold
		}
	}
}

func (c *Config) defaultDurations() {
	if c.Agents.Nudges == 0 {
		c.Agents.Nudges = 3
	}
	if c.Agents.StallAfter == 0 {
		c.Agents.StallAfter = Duration(15 * time.Minute)
	}
	if c.Agents.AskReminderAfter == 0 {
		c.Agents.AskReminderAfter = Duration(24 * time.Hour)
	}
	if c.Agents.AttachLease == 0 {
		c.Agents.AttachLease = Duration(30 * time.Second)
	}
	if c.Retention.Transcripts == 0 {
		c.Retention.Transcripts = Duration(30 * 24 * time.Hour)
	}
	if c.Retention.Events == 0 {
		c.Retention.Events = Duration(30 * 24 * time.Hour)
	}
}

func (c *Config) resolvePaths(dir, home string) error {
	var err error
	if c.WorkspaceDir, err = absPath(c.WorkspaceDir, dir, home); err != nil {
		return fmt.Errorf("config: workspace_dir: %w", err)
	}
	if c.BriefingPath != "" {
		if c.BriefingPath, err = absPath(c.BriefingPath, dir, home); err != nil {
			return fmt.Errorf("config: briefing_path: %w", err)
		}
	}
	for i := range c.Repos {
		if c.Repos[i].Path, err = absPath(c.Repos[i].Path, dir, home); err != nil {
			return fmt.Errorf("config: repos[%d].path: %w", i, err)
		}
	}
	return nil
}

func absPath(p, dir, home string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("is empty")
	case p == "~":
		return home, nil
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:]), nil
	case filepath.IsAbs(p):
		return filepath.Clean(p), nil
	default:
		return filepath.Join(dir, p), nil
	}
}

func (c *Config) validate() error {
	v := &validation{}
	c.validateHarnesses(v)
	c.validateRepos(v)
	c.validateLimits(v)
	for i, bin := range c.Doctor.Require {
		if strings.TrimSpace(bin) == "" {
			v.add("doctor.require[%d]: is empty", i)
		}
	}
	return errors.Join(v.errs...)
}

// validation collects every problem, so one load names them all.
type validation struct{ errs []error }

func (v *validation) add(format string, a ...any) {
	v.errs = append(v.errs, fmt.Errorf("config: "+format, a...))
}

func (c *Config) validateHarnesses(v *validation) {
	for name, h := range c.Harnesses {
		known, ok := drivers[name]
		switch {
		case !ok:
			v.add("harnesses.%s: unknown harness (known: claude-code, codex, pi, agy)", name)
		case !slices.Contains(known, h.Driver):
			v.add("harnesses.%s.driver: %q isn't a driver for %s (have: %s)", name, h.Driver, name, strings.Join(known, ", "))
		}
		if h.LimitThreshold <= 0 || h.LimitThreshold > 100 {
			v.add("harnesses.%s.limit_threshold: %v isn't a percentage above 0, up to 100", name, h.LimitThreshold)
		}
	}
	if _, ok := c.Harnesses[c.DefaultHarness]; !ok {
		v.add("default_harness: %q isn't configured under harnesses", c.DefaultHarness)
	}
	if c.Ticketing.System != "local" {
		v.add("ticketing.system: %q isn't available (have: local)", c.Ticketing.System)
	}
}

func (c *Config) validateRepos(v *validation) {
	seen := map[string]bool{}
	for i, r := range c.Repos {
		if r.Name == "" {
			v.add("repos[%d].name: is empty", i)
		} else if err := CheckRepoName(r.Name); err != nil {
			v.add("repos[%d].name: %v", i, err)
		}
		if seen[r.Name] {
			v.add("repos[%d].name: %q is listed twice", i, r.Name)
		}
		seen[r.Name] = true
	}
}

// validateLimits checks the numbers: concurrency, budgets and retention.
func (c *Config) validateLimits(v *validation) {
	if c.Concurrency.MaxActive < 1 || c.Concurrency.Urgent.MaxActive < 1 {
		v.add("concurrency: max_active and urgent.max_active must be at least 1")
	}
	if c.Concurrency.MaxLive < c.Concurrency.MaxActive+c.Concurrency.Urgent.MaxActive {
		v.add("concurrency.max_live: %d is below max_active + urgent.max_active (%d)",
			c.Concurrency.MaxLive, c.Concurrency.MaxActive+c.Concurrency.Urgent.MaxActive)
	}
	if c.Autonomy.TicketsPerAgent < 1 {
		v.add("autonomy.tickets_per_agent: must be at least 1")
	}
	if c.Retention.Transcripts < 0 || c.Retention.Events < 0 {
		v.add("retention: transcripts and events must be positive durations")
	}
	// Heartbeats come every third of the lease; much shorter and a busy
	// daemon would hand back attaches whose CLI is fine.
	if c.Agents.AttachLease.D() < time.Second {
		v.add("agents.attach_lease: %s is below 1s", c.Agents.AttachLease.D())
	}
	if c.Agents.Nudges < 1 {
		v.add("agents.nudges: must be at least 1")
	}
}

// Slots is every agent slot, normal and urgent: what the autonomy budget
// scales with.
func (c *Config) Slots() int { return c.Concurrency.MaxActive + c.Concurrency.Urgent.MaxActive }

// AutonomyBudget is the most outstanding agent-created tickets allowed.
func (c *Config) AutonomyBudget() int { return c.Autonomy.TicketsPerAgent * c.Slots() }

const maxRepoName = 100

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CheckRepoName is the rule for a repo's name. The name becomes part of a
// directory name (a ticket's worktree is t<ticket>-<repo>), so it must be
// safe there; checking it at load shows a bad one at startup, not when a
// ticket first needs the repo.
func CheckRepoName(name string) error {
	if len(name) > maxRepoName || !repoNameRe.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("%q: want a letter or digit first, then letters, digits, '.', '_' or '-', "+
			"no \"..\", at most %d characters", name, maxRepoName)
	}
	return nil
}

// Repo looks a repo up by name.
func (c *Config) Repo(name string) (*Repo, bool) {
	for i := range c.Repos {
		if c.Repos[i].Name == name {
			return &c.Repos[i], true
		}
	}
	return nil, false
}

func ptr[T any](v T) *T { return &v }
