// Package config is the single place slop-review reads CLI flags and env
// vars; every other package receives a fully-parsed Config rather than
// calling os.Getenv/flag itself.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"
)

// ProviderKind says how slop-review talks to a provider, which decides
// what a profile needs to be usable: an API key for an OpenAI-compatible
// endpoint, nothing here for one whose credentials come from elsewhere.
type ProviderKind string

const (
	// ProviderKindOpenAICompatible is a chat-completions endpoint reached
	// with an API key from the environment.
	ProviderKindOpenAICompatible ProviderKind = "openai-compatible"
	// ProviderKindChatGPT is OpenAI's Responses API reached with a Sign in
	// with ChatGPT token -- a subscription, not an API key -- which pi
	// supplies (see internal/piauth). For workstations and agent VMs only:
	// no subscription credential belongs on a CI agent.
	ProviderKindChatGPT ProviderKind = "chatgpt"
	// ProviderKindCodex hands each session to `codex exec`, which runs its
	// own agent loop under the machine's Codex login, held read-only and
	// offline by its sandbox. For workstations and agent VMs only, like
	// chatgpt.
	ProviderKindCodex ProviderKind = "codex"
	// ProviderKindClaude hands each session to `claude -p
	// --dangerously-skip-permissions`, held read-only and offline by Claude
	// Code's sandbox, under the machine's Claude Code login. Local
	// machines only, like codex.
	ProviderKindClaude ProviderKind = "claude"
)

// providerProfile is how a provider named in providerconfig.Providers is
// reached. Only this process knows it: a central config names a provider,
// and never where it is or which key it takes (see package providerconfig).
// API keys are deliberately env-var-only (see Load) -- never a CLI flag, so
// they can't land in shell history or a process listing.
type providerProfile struct {
	kind      ProviderKind
	baseURL   string
	apiKeyEnv string
	// fixedBaseURL refuses --base-url. The chatgpt provider's bearer token
	// is a subscription credential for OpenAI alone: a base URL left in
	// the environment for an OpenRouter gateway must not redirect it
	// anywhere else.
	fixedBaseURL bool
	// billing says whether a review's cost here is money spent (metered)
	// or covered by a plan (subscription), as reported with its usage.
	billing llmusage.BillingMode
	// reportsCost says the endpoint reports what each request cost.
	reportsCost bool
	// readsFilesItself means the model reads the repository with its own
	// tools rather than slop-review's, so the secret-shaped file denylist
	// can't cover what it opens (see settleSecrets).
	readsFilesItself bool
	// deniesSecretReads means such a provider is itself kept from the
	// checkout's secret-shaped files and the usual credential stores
	// (claude: see internal/claudecli). codex's sandbox can't deny reads.
	deniesSecretReads bool
}

// providerProfiles has one entry per providerconfig.Providers name, and no
// others (pinned by a test): the provider's model and effort rules live
// there, shared with the report server.
var providerProfiles = map[string]providerProfile{
	"chatgpt": {
		kind:         ProviderKindChatGPT,
		baseURL:      "https://api.openai.com/v1",
		fixedBaseURL: true,
		billing:      llmusage.BillingSubscription,
	},
	"codex": {
		kind:             ProviderKindCodex,
		fixedBaseURL:     true,
		billing:          llmusage.BillingSubscription,
		readsFilesItself: true,
	},
	"claude": {
		kind:              ProviderKindClaude,
		fixedBaseURL:      true,
		billing:           llmusage.BillingSubscription,
		readsFilesItself:  true,
		deniesSecretReads: true,
	},
	"openrouter": {
		kind:        ProviderKindOpenAICompatible,
		baseURL:     "https://openrouter.ai/api/v1",
		apiKeyEnv:   "OPENROUTER_API_KEY",
		billing:     llmusage.BillingMetered,
		reportsCost: true,
	},
	"openai": {
		kind:      ProviderKindOpenAICompatible,
		baseURL:   "https://api.openai.com/v1",
		apiKeyEnv: "OPENAI_API_KEY",
		billing:   llmusage.BillingMetered,
	},
}

// Route is one provider resolved to everything needed to review with it.
type Route struct {
	// Provider selects the profile above; BaseURL/APIKey are resolved from
	// it (BaseURL overridable via --base-url, e.g. to point at a
	// self-hosted OpenAI-compatible gateway).
	Provider     string
	ProviderKind ProviderKind
	BaseURL      string
	APIKey       string
	// Model defaults to the provider's own (see providerconfig.Providers).
	Model string
	// ReasoningEffort is sent by providers that take it as a parameter.
	// Unchosen, it is providerconfig.DefaultReasoningEffort where the
	// provider accepts that, and empty (the model's default) where not.
	ReasoningEffort string
	// BillingMode and ReportsCost come from the provider's profile: how
	// its usage is billed, and whether it reports what a request cost.
	BillingMode llmusage.BillingMode
	ReportsCost bool
}

// ProviderSource says where a run's provider settings came from.
type ProviderSource string

const (
	// SourceLocal: flags, env and defaults, with no central config.
	SourceLocal ProviderSource = "local"
	// SourceFlags: --provider/--model/--reasoning-effort were given, which
	// makes the run a local one-off and the central config is not applied.
	SourceFlags ProviderSource = "flags"
	// SourceCentral: the tenant's config, as the server just served it.
	SourceCentral ProviderSource = "central"
	// SourceSaved: the tenant's config as last fetched, the server being
	// unreachable.
	SourceSaved ProviderSource = "saved"
)

// Central is the tenant's central provider config, when there is one, and
// where it came from.
type Central struct {
	Config *providerconfig.Config
	// Source is SourceCentral or SourceSaved.
	Source ProviderSource
	// Notes are for stderr: why a saved copy or the local settings are
	// being used instead.
	Notes []string
}

// CentralSource looks up the tenant's central provider config for a run
// that reports to reportURL. A nil Config means there is none to apply.
type CentralSource interface {
	Lookup(ctx context.Context, reportURL string, tokens reporter.TokenSource, cacheDir string) *Central
}

type Config struct {
	// Route is the primary provider: the one every session starts on.
	Route
	// Fallback, when the central config or the local
	// SLOP_REVIEW_FALLBACK_* settings name one usable on this machine, is
	// where a session goes when the primary fails with one of FallbackOn.
	Fallback   *Route
	FallbackOn []report.FailureClass
	// ProviderSource is where Route and Fallback came from, and
	// ProviderConfigRevision the central config's revision when that was
	// the central config (0 otherwise).
	ProviderSource         ProviderSource
	ProviderConfigRevision int64
	// ProviderConfigCacheDir is where the central config's last fetched
	// copy is kept, for when the server can't be reached.
	ProviderConfigCacheDir string
	// Notes are things the person running slop-review should know about
	// how it was set up -- a stale config, a fallback unusable here --
	// written to stderr, never into the review.
	Notes []string
	// Warnings go into the review itself: what the person reading it must
	// know about how it was produced.
	Warnings []string

	// Persona is a filename stem under PersonaDir (e.g. "standard" ->
	// PersonaDir/standard.md), a comma-separated list of them, or "all".
	Persona    string
	PersonaDir string

	// RepoRoot is the git repository being reviewed.
	RepoRoot string
	// FromCommitHash, if set, reviews from this commit onwards instead of
	// the current branch's merge-base with main. Required when on main.
	FromCommitHash string
	// Intent is the text of the file named by --intent/SLOP_REVIEW_INTENT:
	// what the change is meant to do, typically its PR description. Empty
	// when none was given. Capped at MaxIntentBytes (see IntentTruncated).
	Intent          string
	IntentTruncated bool

	// MaxTurns bounds the review session's tool-call loop so a model that
	// keeps requesting context can't run away indefinitely.
	MaxTurns int
	// ToolOutputCapBytes truncates any single tool result to this size
	// before it's added to the conversation, so one large file/log read
	// can't blow out the context on every subsequent turn.
	ToolOutputCapBytes int
	// MaxDiffBytes bounds how much diff one review session's prompt
	// carries inline. Files past it are listed for the session to fetch
	// with get_diff instead -- never cut off mid-file.
	MaxDiffBytes int
	// ChunkDiffBytes is the size a large change is split into for the
	// personas that allow splitting (see persona.Chunking).
	ChunkDiffBytes int
	// ContextBudgetBytes bounds a session's resent conversation; past it,
	// the oldest tool results are elided.
	ContextBudgetBytes int
	// Parallelism caps concurrent review sessions.
	Parallelism int
	// SeamPass runs a cross-chunk session after a split review.
	SeamPass bool
	// AllowSecrets lets read_file return files matching the secret-shaped
	// denylist (see internal/toolset) instead of refusing them outright;
	// still recorded as a warning in the final review either way.
	AllowSecrets bool
	// RunChecks lets the codex and claude providers build and run tests to
	// check a claim: the repository stays read-only and the network stays
	// off, but the build caches and a scratch temp dir become writable.
	// Off by default; the providers that use slop-review's own tools
	// ignore it.
	RunChecks bool

	// ReportURL, when set, is where the finished run is posted (see
	// report/reporter and internal/runreport). Unset means today's
	// behaviour exactly: nothing is sent anywhere.
	ReportURL string
	// ReportRemote names which git remote identifies the reviewed repo (see
	// gitctx.ScopeOptions.RemoteName and report.RepoRemote) -- "origin" by
	// default.
	ReportRemote string
	// ReportRepo, if set, overrides the remote URL a report is filed under
	// outright, instead of whatever ReportRemote resolves to -- for a repo
	// whose configured remote isn't the shared identity (a personal fork,
	// say).
	ReportRepo string
	// ReportToken authenticates the post. From the environment or the
	// .env, never a flag, so it can't land in shell history or a process
	// listing.
	ReportToken string
	// ReportTokenCommand, when set, is run for the token before each
	// request, for a token that expires; an explicit ReportToken
	// beats it (see ReportTokenSource). It holds no secret, so it has a flag.
	ReportTokenCommand string
	// ReportSpoolDir holds a run that failed to post, for retry on a later
	// invocation. Defaults to a per-user cache directory, resolved once
	// ReportURL is known to be set.
	ReportSpoolDir string
	// OverviewCacheDir holds overview-stage results (see
	// persona.StageOverview), reused across runs over the same diff.
	// Defaults to a per-user cache directory.
	OverviewCacheDir string
	// ToolVersion identifies the slop-review build itself -- review.sh
	// passes its own git SHA (see report.ToolVersion). Reporting still
	// happens without it, just with an empty tool version value.
	ToolVersion string
	// ToolVersionDirty says whether the slop-review checkout that produced
	// ToolVersion had uncommitted changes -- review.sh computes this itself
	// (git status on its own script dir), since it, not the reviewed
	// repository, is what ToolVersion identifies.
	ToolVersionDirty bool
	// SourceDir is the reviewer's own source tree, hashed into the run's
	// content fingerprint (see package fingerprint). review.sh passes its
	// own directory; without it the run is reported without a source
	// fingerprint, since the reviewed repository is not the reviewer.
	SourceDir string

	// BenchCase/BenchVariant/BenchRun tag a run as one sample of a
	// slop-review-bench sweep (see bench.ExecReviewer); a posted report is
	// Kind bench, rather than Kind review, only when all three are set.
	BenchCase    string
	BenchVariant string
	BenchRun     int
}

type flagSpec struct {
	flag string
	key  string // viper key; also the env var name (see Load)
	def  string
}

// The defaults review a change in one pass: the standard persona over the
// whole diff in a single session (chunk and inline-diff sizes far above a
// typical change, a context budget twice that), with 40 turns and
// providerconfig.DefaultReasoningEffort: the configuration chosen by
// benchmark (fluffy-journey's self-review/reviewer/bench, variant
// single40) over four persona sessions on package-sized chunks. A change
// over a megabyte is still split, and its seams get their pass.
var flagSpecs = []flagSpec{
	{"provider", "SLOP_REVIEW_PROVIDER", "claude"},
	{"base-url", "SLOP_REVIEW_BASE_URL", ""},
	{"model", "SLOP_REVIEW_MODEL", ""},
	{"reasoning-effort", "SLOP_REVIEW_REASONING_EFFORT", ""},
	{"persona", "SLOP_REVIEW_PERSONA", "standard"},
	{"persona-dir", "SLOP_REVIEW_PERSONA_DIR", "personas"},
	{"repo-root", "SLOP_REVIEW_REPO_ROOT", "."},
	{"from-commit-hash", "SLOP_REVIEW_FROM_COMMIT_HASH", ""},
	{"intent", "SLOP_REVIEW_INTENT", ""},
	{"max-turns", "SLOP_REVIEW_MAX_TURNS", "40"},
	{"tool-output-cap-bytes", "SLOP_REVIEW_TOOL_OUTPUT_CAP_BYTES", "8000"},
	{"max-diff-bytes", "SLOP_REVIEW_MAX_DIFF_BYTES", "1000000"},
	{"chunk-diff-bytes", "SLOP_REVIEW_CHUNK_DIFF_BYTES", "1000000"},
	{"context-budget-bytes", "SLOP_REVIEW_CONTEXT_BUDGET_BYTES", "2000000"},
	{"parallelism", "SLOP_REVIEW_PARALLELISM", "4"},
	{"seam-pass", "SLOP_REVIEW_SEAM_PASS", "true"},
	{"allow-secrets", "SLOP_REVIEW_ALLOW_SECRETS", "false"},
	{"run-checks", "SLOP_REVIEW_RUN_CHECKS", "false"},
	{"report-to", "SLOP_REVIEW_REPORT_URL", ""},
	{"report-remote", "SLOP_REVIEW_REPORT_REMOTE", "origin"},
	{"report-repo", "SLOP_REVIEW_REPORT_REPO", ""},
	{"report-spool-dir", "SLOP_REVIEW_REPORT_SPOOL_DIR", ""},
	{"report-token-command", "SLOP_REVIEW_REPORT_TOKEN_COMMAND", ""},
	{"overview-cache-dir", "SLOP_REVIEW_OVERVIEW_CACHE_DIR", ""},
	{"source-dir", "SLOP_REVIEW_SOURCE_DIR", ""},
	{"tool-version", "SLOP_REVIEW_TOOL_VERSION", ""},
	{"tool-version-dirty", "SLOP_REVIEW_TOOL_VERSION_DIRTY", "false"},
	{"bench-case", "SLOP_REVIEW_BENCH_CASE", ""},
	{"bench-variant", "SLOP_REVIEW_BENCH_VARIANT", ""},
	{"bench-run", "SLOP_REVIEW_BENCH_RUN", "0"},
	{"provider-config-cache-dir", "SLOP_REVIEW_PROVIDER_CONFIG_CACHE_DIR", ""},
	{"fallback-provider", "SLOP_REVIEW_FALLBACK_PROVIDER", ""},
	{"fallback-model", "SLOP_REVIEW_FALLBACK_MODEL", ""},
	{"fallback-reasoning-effort", "SLOP_REVIEW_FALLBACK_REASONING_EFFORT", ""},
	{"fallback-on", "SLOP_REVIEW_FALLBACK_ON", ""},
}

// providerFlags are the flags that choose the reviewer. Any of them on the
// command line makes the run a local one-off (SourceFlags): a flag's model
// laid over the central config's provider could pair a model with a
// provider that doesn't serve it.
var providerFlags = append([]string{"provider", "model", "reasoning-effort"}, fallbackFlags...)

// fallbackFlags choose the fallback. A one-off run has one only if they are
// on its command line too: the SLOP_REVIEW_FALLBACK_* settings were chosen
// to back up SLOP_REVIEW_PROVIDER, not whatever --provider names.
var fallbackFlags = []string{"fallback-provider", "fallback-model", "fallback-reasoning-effort", "fallback-on"}

// envFileFlag names the .env file read below the environment (see DotEnv).
// A flag only, never a setting itself, so the file can't point elsewhere.
const envFileFlag = "env-file"

// boolFlags are the flagSpecs that may be given bare: "--run-checks" means
// true. A value then has to follow "=", so LoadWith refuses a stray
// argument rather than let "--allow-secrets false" read as true.
var boolFlags = map[string]bool{"run-checks": true, "allow-secrets": true}

// reportTokenEnv is where the ingest token comes from. Deliberately absent
// from flagSpecs/providerProfiles' apiKeyEnv pattern: it must never be
// bindable as a flag (see Config.ReportToken), so Load reads it with
// DotEnv.Getenv (the environment, then the .env) rather than through
// viper's flag/env precedence.
const reportTokenEnv = "SLOP_REVIEW_REPORT_TOKEN"

// ReportTokenSource is where the report token comes from: the fixed
// SLOP_REVIEW_REPORT_TOKEN, or a command run for each request (see
// reporter.NewCommandToken) when the token has to be refreshed. Nil, with no
// error, means reporting has no credentials. Both set is not an error: an
// explicit fixed token wins (see TokenCommandIgnoredNote), because a
// workspace sets the command for every run and a human's own token must not
// break the review.
func (cfg *Config) ReportTokenSource() (reporter.TokenSource, error) {
	return ReportTokenSource(cfg.ReportToken, cfg.ReportTokenCommand)
}

// TokenCommandIgnoredNote is the warning for a run with both a fixed token
// and a token command, or "" when the command isn't being ignored.
func TokenCommandIgnoredNote(token, command string) string {
	if token == "" || strings.TrimSpace(command) == "" {
		return ""
	}
	return "SLOP_REVIEW_REPORT_TOKEN_COMMAND is ignored because SLOP_REVIEW_REPORT_TOKEN is set: an explicit token takes precedence over the command"
}

// ReportTokenSource is Config.ReportTokenSource for settings read outside
// Load, such as the label command's.
func ReportTokenSource(token, command string) (reporter.TokenSource, error) {
	// A blank command is no command: failing the whole run over it would
	// make a reporting setting a review failure.
	command = strings.TrimSpace(command)
	switch {
	case token != "":
		return reporter.StaticToken(token), nil
	case command != "":
		src, err := reporter.NewCommandToken(command)
		if err != nil {
			return nil, fmt.Errorf("config: SLOP_REVIEW_REPORT_TOKEN_COMMAND: %w", err)
		}
		return src, nil
	}
	return nil, nil
}

// Load reads configuration from CLI flags (highest precedence), then env
// vars, then the --env-file .env, then defaults. args is the program's
// argument list excluding argv[0] (i.e. os.Args[1:]).
//
// On an error past flag parsing, Load still returns the Config as far as it
// got: every setting read, the reporting ones resolved, the rest possibly
// not validated. It is for reporting the failure (see
// cmd/slop-review's failed runs) and nothing else.
func Load(args []string) (*Config, error) {
	return LoadWith(context.Background(), args, nil)
}

// LoadWith is Load, with the central provider config looked up from
// central (nil: never) and applied over the local settings, as
// ProviderSource describes. A central config beats the environment; the
// command line beats both.
func LoadWith(ctx context.Context, args []string, central CentralSource) (*Config, error) {
	l, err := readLayers(args)
	if err != nil {
		return nil, err
	}
	v := l.v

	cfg := &Config{
		Route: Route{
			Provider:        v.GetString("SLOP_REVIEW_PROVIDER"),
			BaseURL:         v.GetString("SLOP_REVIEW_BASE_URL"),
			Model:           v.GetString("SLOP_REVIEW_MODEL"),
			ReasoningEffort: v.GetString("SLOP_REVIEW_REASONING_EFFORT"),
		},
		Persona:            v.GetString("SLOP_REVIEW_PERSONA"),
		PersonaDir:         v.GetString("SLOP_REVIEW_PERSONA_DIR"),
		RepoRoot:           v.GetString("SLOP_REVIEW_REPO_ROOT"),
		FromCommitHash:     v.GetString("SLOP_REVIEW_FROM_COMMIT_HASH"),
		ReportURL:          v.GetString("SLOP_REVIEW_REPORT_URL"),
		ReportRemote:       v.GetString("SLOP_REVIEW_REPORT_REMOTE"),
		ReportRepo:         v.GetString("SLOP_REVIEW_REPORT_REPO"),
		ReportToken:        l.dotEnv.Getenv(reportTokenEnv),
		ReportTokenCommand: v.GetString("SLOP_REVIEW_REPORT_TOKEN_COMMAND"),
		ReportSpoolDir:     v.GetString("SLOP_REVIEW_REPORT_SPOOL_DIR"),
		ToolVersion:        v.GetString("SLOP_REVIEW_TOOL_VERSION"),
		SourceDir:          v.GetString("SLOP_REVIEW_SOURCE_DIR"),

		OverviewCacheDir: v.GetString("SLOP_REVIEW_OVERVIEW_CACHE_DIR"),
		BenchCase:        v.GetString("SLOP_REVIEW_BENCH_CASE"),
		BenchVariant:     v.GetString("SLOP_REVIEW_BENCH_VARIANT"),
	}

	// The spool directory only needs to exist when there's somewhere to
	// report to; resolving it unconditionally would fail runs that never
	// touch it (e.g. no HOME set in a minimal container) for no reason.
	// Resolved first, so a run failing on any later setting can still be
	// reported.
	if cfg.ReportURL != "" && cfg.ReportSpoolDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("config: resolve default report spool dir: %w", err)
		}
		cfg.ReportSpoolDir = filepath.Join(dir, "slop-review", "spool")
	}

	if keys := l.dotEnv.usedKeys(); len(keys) > 0 {
		cfg.Notes = append(cfg.Notes, fmt.Sprintf("settings from %s: %s", l.envFile, strings.Join(keys, ", ")))
	}

	cfg.ProviderSource = SourceLocal
	if slices.ContainsFunc(providerFlags, l.fs.Changed) {
		cfg.ProviderSource = SourceFlags
	}
	cfg.ProviderConfigCacheDir = v.GetString("SLOP_REVIEW_PROVIDER_CONFIG_CACHE_DIR")

	if err := cfg.resolve(v); err != nil {
		return cfg, err
	}
	if err := cfg.resolveProviders(ctx, central, l.localFallback()); err != nil {
		return cfg, err
	}
	cfg.settleSecrets()
	return cfg, nil
}

// layers are where a run's settings come from, highest first: the flags,
// the environment, the .env file (viper's config layer), the defaults.
type layers struct {
	v       *viper.Viper
	fs      *pflag.FlagSet
	dotEnv  DotEnv
	envFile string
}

func readLayers(args []string) (*layers, error) {
	l := &layers{v: viper.New(), fs: pflag.NewFlagSet("slop-review", pflag.ContinueOnError)}
	for _, s := range flagSpecs {
		l.fs.String(s.flag, "", fmt.Sprintf("overrides %s", s.key))
		if boolFlags[s.flag] {
			l.fs.Lookup(s.flag).NoOptDefVal = "true"
		}
	}
	envFile := l.fs.String(envFileFlag, "", "a .env file whose SLOP_REVIEW_* settings apply below the environment's")
	if err := l.fs.Parse(args); err != nil {
		return nil, fmt.Errorf("config: parse flags: %w", err)
	}
	if l.fs.NArg() > 0 {
		return nil, fmt.Errorf("config: unexpected argument %q: slop-review takes only flags, and a bare flag's value goes after \"=\" (--run-checks=false)", l.fs.Arg(0))
	}

	l.envFile = *envFile
	var err error
	if l.dotEnv, err = ReadDotEnv(l.envFile); err != nil {
		return nil, err
	}
	// An intent belongs to one change, and a relative path in the file
	// would be taken from review.sh's directory, not the caller's.
	if _, ok := l.dotEnv["SLOP_REVIEW_INTENT"]; ok {
		return nil, fmt.Errorf("config: %s: SLOP_REVIEW_INTENT can't be set in a .env: it names one change's intent; give --intent", l.envFile)
	}
	fileSettings := make(map[string]any, len(l.dotEnv))
	for k, val := range l.dotEnv {
		fileSettings[k] = val
	}
	if err := l.v.MergeConfigMap(fileSettings); err != nil {
		return nil, fmt.Errorf("config: %s: %w", l.envFile, err)
	}

	for _, s := range flagSpecs {
		if err := l.v.BindEnv(s.key, s.key); err != nil {
			return nil, fmt.Errorf("config: bind env %s: %w", s.key, err)
		}
		if err := l.v.BindPFlag(s.key, l.fs.Lookup(s.flag)); err != nil {
			return nil, fmt.Errorf("config: bind flag %s: %w", s.flag, err)
		}
		if s.def != "" {
			l.v.SetDefault(s.key, s.def)
		}
	}
	return l, nil
}

// localFallback reads the fallback the local settings name. One on the
// command line is taken from the command line alone: merged key by key with
// the environment's, --fallback-provider could pair with a
// SLOP_REVIEW_FALLBACK_MODEL its provider doesn't serve.
func (l *layers) localFallback() *localFallback {
	f := &localFallback{flagged: slices.ContainsFunc(fallbackFlags, l.fs.Changed)}
	get := l.v.GetString
	if f.flagged {
		get = func(key string) string {
			i := slices.IndexFunc(flagSpecs, func(s flagSpec) bool { return s.key == key })
			return l.fs.Lookup(flagSpecs[i].flag).Value.String()
		}
	}
	f.choice = providerconfig.Choice{
		Provider:        get("SLOP_REVIEW_FALLBACK_PROVIDER"),
		Model:           get("SLOP_REVIEW_FALLBACK_MODEL"),
		ReasoningEffort: get("SLOP_REVIEW_FALLBACK_REASONING_EFFORT"),
	}
	f.on = get("SLOP_REVIEW_FALLBACK_ON")
	return f
}

// resolve parses and checks everything Load read as a plain string.
func (cfg *Config) resolve(v *viper.Viper) error {
	benchRun, err := strconv.Atoi(v.GetString("SLOP_REVIEW_BENCH_RUN"))
	if err != nil {
		return fmt.Errorf("config: SLOP_REVIEW_BENCH_RUN must be an integer: %w", err)
	}
	if benchRun < 0 {
		return fmt.Errorf("config: SLOP_REVIEW_BENCH_RUN must not be negative, got %d", benchRun)
	}
	cfg.BenchRun = benchRun

	if err := cfg.loadIntent(v.GetString("SLOP_REVIEW_INTENT")); err != nil {
		return err
	}

	for key, dst := range map[string]*int{
		"SLOP_REVIEW_MAX_TURNS":             &cfg.MaxTurns,
		"SLOP_REVIEW_TOOL_OUTPUT_CAP_BYTES": &cfg.ToolOutputCapBytes,
		"SLOP_REVIEW_MAX_DIFF_BYTES":        &cfg.MaxDiffBytes,
		"SLOP_REVIEW_CHUNK_DIFF_BYTES":      &cfg.ChunkDiffBytes,
		"SLOP_REVIEW_CONTEXT_BUDGET_BYTES":  &cfg.ContextBudgetBytes,
		"SLOP_REVIEW_PARALLELISM":           &cfg.Parallelism,
	} {
		n, err := strconv.Atoi(v.GetString(key))
		if err != nil {
			return fmt.Errorf("config: %s must be an integer: %w", key, err)
		}
		if n <= 0 {
			return fmt.Errorf("config: %s must be positive, got %d", key, n)
		}
		*dst = n
	}

	// Elision never touches the review input itself (see session.elide),
	// so a context budget that can't hold the inline diff is one no
	// session could ever stay within.
	if cfg.ContextBudgetBytes <= cfg.MaxDiffBytes {
		return fmt.Errorf("config: SLOP_REVIEW_CONTEXT_BUDGET_BYTES (%d) must be larger than SLOP_REVIEW_MAX_DIFF_BYTES (%d)", cfg.ContextBudgetBytes, cfg.MaxDiffBytes)
	}

	for key, dst := range map[string]*bool{
		"SLOP_REVIEW_ALLOW_SECRETS":      &cfg.AllowSecrets,
		"SLOP_REVIEW_RUN_CHECKS":         &cfg.RunChecks,
		"SLOP_REVIEW_SEAM_PASS":          &cfg.SeamPass,
		"SLOP_REVIEW_TOOL_VERSION_DIRTY": &cfg.ToolVersionDirty,
	} {
		b, err := strconv.ParseBool(v.GetString(key))
		if err != nil {
			return fmt.Errorf("config: %s must be a bool: %w", key, err)
		}
		*dst = b
	}
	return nil
}

// settleSecrets notes, once the routes are known, what the secret
// denylist can't cover. codex and claude read the repository with their own
// tools, so a secret-shaped file is withheld from everything slop-review
// sends them (the diff, a fallback's sessions) but not from what they open
// themselves. That is said in the review (for a fallback, only when it ran:
// see cmd/slop-review), and AllowSecrets stays what was asked for: setting
// it would also stop slop-review redacting the diff it sends any route, and
// stores.
func (cfg *Config) settleSecrets() {
	if ReadsFilesItself(cfg.Provider) {
		cfg.Warnings = append(cfg.Warnings, SecretsWarning(cfg.Provider))
	}
}

// ReadsFilesItself reports whether provider reads the repository with its
// own tools, out of reach of the secret denylist.
func ReadsFilesItself(provider string) bool {
	return providerProfiles[provider].readsFilesItself
}

// SecretsMayBeQuoted reports whether the run's findings could quote a
// secret: --allow-secrets showed them to the model, or a route reads the
// checkout itself with nothing keeping it off secret files. The report
// says so as allow_secrets, which the report server refuses to store. A
// fallback counts whether or not it runs: which one will is not known yet
// when this is decided.
func (cfg *Config) SecretsMayBeQuoted() bool {
	unguarded := func(provider string) bool {
		p := providerProfiles[provider]
		return p.readsFilesItself && !p.deniesSecretReads
	}
	return cfg.AllowSecrets || unguarded(cfg.Provider) || (cfg.Fallback != nil && unguarded(cfg.Fallback.Provider))
}

// SecretsWarning is what a review that ran on such a provider says.
func SecretsWarning(provider string) string {
	return fmt.Sprintf("provider %q reads the repository with its own tools, so secret-shaped files (.env, keys, ...) could not be withheld from it", provider)
}

// MaxIntentBytes caps the intent text: it is carried in every session's
// prompt, and a description longer than this is padding the reviewer
// can't act on.
const MaxIntentBytes = 32 * 1024

// loadIntent reads the intent file, if one was named. A relative path is
// relative to this process's working directory; review.sh, which changes
// directory, resolves the caller's relative paths before getting here.
func (cfg *Config) loadIntent(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read SLOP_REVIEW_INTENT file: %w", err)
	}
	if len(data) > MaxIntentBytes {
		cut := MaxIntentBytes
		// Don't leave half a rune at the cut.
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		data = data[:cut]
		cfg.IntentTruncated = true
	}
	cfg.Intent = strings.TrimSpace(string(data))
	if cfg.IntentTruncated {
		cfg.Notes = append(cfg.Notes, fmt.Sprintf("the intent file is over %d bytes; only the start is given to the reviewer", MaxIntentBytes))
	}
	return nil
}

// localFallback is the fallback the local settings name: the command
// line's, or else the environment's over the .env file's.
type localFallback struct {
	choice providerconfig.Choice
	// on is SLOP_REVIEW_FALLBACK_ON, comma-separated; empty is
	// providerconfig.DefaultTrigger.
	on string
	// flagged says a fallback flag was on the command line.
	flagged bool
}

func (f *localFallback) named() bool {
	return f.choice != providerconfig.Choice{} || strings.TrimSpace(f.on) != ""
}

// settings is the fallback as the central config would carry it, over
// primary, so that one validation, resolution and run-time path serves
// both. Only primary's provider is carried: its model and effort were
// already checked as local settings, and the provider is all a fallback's
// rules depend on.
func (f *localFallback) settings(primary *Route) (*providerconfig.Settings, error) {
	if f.choice.Provider == "" {
		return nil, errors.New("config: SLOP_REVIEW_FALLBACK_MODEL, _REASONING_EFFORT and _ON need SLOP_REVIEW_FALLBACK_PROVIDER")
	}
	fallback := f.choice
	s := &providerconfig.Settings{Primary: providerconfig.Choice{Provider: primary.Provider}, Fallback: &fallback}
	for class := range strings.SplitSeq(f.on, ",") {
		if class = strings.TrimSpace(class); class != "" {
			s.FallbackOn = append(s.FallbackOn, report.FailureClass(class))
		}
	}
	if len(s.FallbackOn) == 0 {
		s.FallbackOn = slices.Clone(providerconfig.DefaultTrigger)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("config: SLOP_REVIEW_FALLBACK_*: %w", err)
	}
	return s, nil
}

// resolveProviders settles the primary and fallback routes: from the
// central config when there is one and the command line didn't choose,
// otherwise from the local settings as read. The local fallback backs up
// the local primary only: a central config brings its own fallback (or
// none), and a one-off run gets one only from its own command line.
func (cfg *Config) resolveProviders(ctx context.Context, central CentralSource, fallback *localFallback) error {
	tokens, err := cfg.ReportTokenSource()
	if err != nil {
		return err
	}
	if note := TokenCommandIgnoredNote(cfg.ReportToken, cfg.ReportTokenCommand); note != "" {
		cfg.Notes = append(cfg.Notes, note)
	}
	local := cfg.Route
	if cfg.ProviderSource == SourceLocal && central != nil && cfg.ReportURL != "" && tokens != nil {
		c, err := cfg.lookUpCentral(ctx, central, tokens)
		if err != nil {
			return err
		}
		if c != nil {
			if fallback.named() {
				cfg.Notes = append(cfg.Notes, "the central provider config applies, so SLOP_REVIEW_FALLBACK_* is not used")
			}
			return cfg.applyCentral(c, &local)
		}
	}
	route, err := resolveRoute(&local, &local, "SLOP_REVIEW_")
	if err != nil {
		return err
	}
	cfg.Route = *route
	return cfg.applyLocalFallback(fallback, &local)
}

// lookUpCentral is the central config to apply, or nil for none.
func (cfg *Config) lookUpCentral(ctx context.Context, central CentralSource, tokens reporter.TokenSource) (*Central, error) {
	if cfg.ProviderConfigCacheDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("config: resolve default provider config cache dir: %w", err)
		}
		cfg.ProviderConfigCacheDir = filepath.Join(dir, "slop-review", "provider-config")
	}
	c := central.Lookup(ctx, cfg.ReportURL, tokens, cfg.ProviderConfigCacheDir)
	if c == nil {
		return nil, nil
	}
	cfg.Notes = append(cfg.Notes, c.Notes...)
	if c.Config == nil {
		return nil, nil
	}
	return c, nil
}

// applyLocalFallback sets the local fallback over the local primary,
// unless this is a one-off run whose command line named none.
func (cfg *Config) applyLocalFallback(fallback *localFallback, local *Route) error {
	if !fallback.named() {
		return nil
	}
	if cfg.ProviderSource == SourceFlags && !fallback.flagged {
		cfg.Notes = append(cfg.Notes, "--provider/--model/--reasoning-effort make this run a one-off without the SLOP_REVIEW_FALLBACK_* fallback; give --fallback-provider for one")
		return nil
	}
	s, err := fallback.settings(&cfg.Route)
	if err != nil {
		return err
	}
	cfg.applyFallback(s, local)
	return nil
}

// applyCentral sets the routes from the tenant's central config. The local
// SLOP_REVIEW_BASE_URL still applies, but only to the provider it was set
// for (SLOP_REVIEW_PROVIDER): a gateway configured for one provider must
// never be handed another provider's key.
func (cfg *Config) applyCentral(c *Central, local *Route) error {
	pc := c.Config
	cfg.ProviderSource, cfg.ProviderConfigRevision = c.Source, pc.Revision
	primary, err := resolveRoute(routeFor(&pc.Primary), local, "")
	if err != nil {
		return fmt.Errorf("central provider config (revision %d), primary: %w", pc.Revision, err)
	}
	cfg.Route = *primary
	cfg.applyFallback(&pc.Settings, local)
	return nil
}

// applyFallback sets s's fallback, if it names one. A fallback this machine
// can't use (no key for it, say) is left out rather than failing a run the
// primary may well handle.
func (cfg *Config) applyFallback(s *providerconfig.Settings, local *Route) {
	if s.Fallback == nil {
		return
	}
	fallback, err := resolveRoute(routeFor(s.Fallback), local, "")
	if err != nil {
		cfg.Notes = append(cfg.Notes, fmt.Sprintf("the fallback provider %q can't be used on this machine, so this run has none: %v", s.Fallback.Provider, err))
		return
	}
	cfg.Fallback, cfg.FallbackOn = fallback, s.FallbackOn
}

func routeFor(c *providerconfig.Choice) *Route {
	return &Route{Provider: c.Provider, Model: c.Model, ReasoningEffort: c.ReasoningEffort}
}

// resolveRoute fills in everything that depends on which provider want
// names, and rejects settings that provider can't honour. local is what
// the machine itself set, for its base URL; envPrefix names the settings
// in errors when they came from SLOP_REVIEW_* (a central config is already
// validated, so all it can lack here is a key).
func resolveRoute(want, local *Route, envPrefix string) (*Route, error) {
	r := &Route{Provider: want.Provider, Model: want.Model, ReasoningEffort: want.ReasoningEffort}
	profile, ok := providerProfiles[r.Provider]
	spec, known := providerconfig.Providers[r.Provider]
	if !ok || !known {
		return nil, fmt.Errorf("config: %sPROVIDER must be one of %v, got %q", envPrefix, providerconfig.ProviderNames(), r.Provider)
	}
	r.ProviderKind, r.BillingMode, r.ReportsCost = profile.kind, profile.billing, profile.reportsCost
	if r.Model == "" {
		r.Model = spec.DefaultModel
	}

	if r.ReasoningEffort != "" {
		if !spec.ReasoningEffort {
			return nil, fmt.Errorf("config: %sREASONING_EFFORT is not supported by provider %q; choose effort with the model slug instead (e.g. %s-pro)", envPrefix, r.Provider, spec.DefaultModel)
		}
		if !slices.Contains(providerconfig.ReasoningEfforts, r.ReasoningEffort) {
			return nil, fmt.Errorf("config: %sREASONING_EFFORT must be one of %v, got %q", envPrefix, providerconfig.ReasoningEfforts, r.ReasoningEffort)
		}
		if !spec.AcceptsEffort(r.ReasoningEffort) {
			return nil, fmt.Errorf("config: %sREASONING_EFFORT %q is not accepted by provider %q (one of %v)", envPrefix, r.ReasoningEffort, r.Provider, spec.Efforts)
		}
	}
	// After the checks: only a chosen effort is refused. The default is
	// left out where the provider doesn't take it, so `--provider=openrouter`
	// alone still works.
	r.ReasoningEffort = spec.EffortOrDefault(r.ReasoningEffort)

	if r.Provider == local.Provider {
		r.BaseURL = local.BaseURL
	}
	if r.BaseURL != "" && profile.fixedBaseURL && profile.baseURL == "" {
		return nil, fmt.Errorf("config: SLOP_REVIEW_BASE_URL can't be set for provider %q: it runs its own CLI and has no endpoint to redirect", r.Provider)
	}
	if r.BaseURL != "" && profile.fixedBaseURL && r.BaseURL != profile.baseURL {
		return nil, fmt.Errorf("config: SLOP_REVIEW_BASE_URL can't be set for provider %q: its token is only ever sent to %s", r.Provider, profile.baseURL)
	}
	if r.BaseURL == "" {
		r.BaseURL = profile.baseURL
	}
	if profile.apiKeyEnv != "" {
		r.APIKey = os.Getenv(profile.apiKeyEnv)
		if r.APIKey == "" {
			return nil, fmt.Errorf("config: %s is required for provider %q", profile.apiKeyEnv, r.Provider)
		}
	}
	return r, nil
}
