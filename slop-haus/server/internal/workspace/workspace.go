// Package workspace describes what a slop-haus agent workspace contains.
//
// The server hands out two scripts that write it -- setup, for a new
// workspace, and the update .slop-haus/update.sh fetches -- and this
// is the one description of what they write, so the two cannot drift.
package workspace

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Where each file goes, relative to the workspace root. The .slop-haus
// directory holds everything of ours that is not a config file Claude Code
// insists on finding by name, so a workspace can be de-slopped by deleting
// one directory and two lines.
const (
	EnvFile = ".slop-haus.env"
	// GitignoreAll keeps everything under .slop-haus/ out of git, credentials
	// included, if the workspace root is a repo despite the layout rules.
	GitignoreAll           = ".slop-haus/.gitignore"
	HookScript             = ".slop-haus/hook-stop.sh"
	SessionStartHookScript = ".slop-haus/hook-session-start.sh"
	UsageHookScript        = ".slop-haus/hook-usage.sh"
	UserPromptHookScript   = ".slop-haus/hook-user-prompt.sh"
	UsageReporter          = ".slop-haus/usage-report.py"
	SessionDescriber       = ".slop-haus/describe-session.py"
	TokenModule            = ".slop-haus/slop_haus_token.py"
	PiExtension            = ".slop-haus/pi/slop-haus.ts"
	HeadlessBriefing       = ".slop-haus/headless-briefing.md"
	TokenScript            = ".slop-haus/token.sh"
	Updater                = ".slop-haus/update.sh"
	RepoSyncer             = ".slop-haus/sync-repos.sh"
	AppInstaller           = ".slop-haus/install-app.sh"
	RemoteControlLauncher  = ".slop-haus/run_remote_control.sh"
	LauncherEnv            = ".slop-haus/run.env"
	ServerURL              = ".slop-haus/server-url"
	Settings               = ".claude/settings.json"
	LocalSettings          = ".claude/settings.local.json"
	MCPConfig              = ".mcp.json"
	ClaudeMD               = "CLAUDE.md"
)

// ToolCallTimeout is the longest a slop-haus tool call may run: the MCP
// client timeout this workspace's config sets, and the write deadline the
// server gives an MCP request. No tool blocks any more; the slowest are the
// ones that call the code host (change_open, change_merge, a claim making
// the attempt's branch), a few requests of at most 15s each. Two minutes
// covers that several times over, and matches the supervisor's MCP timeout.
//
// Set explicitly because Claude Code's own default for an http MCP server
// is about a minute.
const ToolCallTimeout = 2 * time.Minute

// stopHookTimeout is how long Claude Code gives the Stop hook, in seconds:
// token.sh's lock wait (up to 30s) plus hook-stop.sh riding out a server
// restart (about 45s), with room to spare. Past it the hook is killed and
// the agent stops, the same as failing open.
//
// The usage hook that runs beside it on Stop is async, so it is not part of
// this budget: Claude Code neither waits for it nor reads its answer.
const stopHookTimeout = 90

// userPromptHookTimeout is the prompt hook's own limit, in seconds: token.sh's
// lock wait (up to 30s) and a 5s request, with room to spare. It runs async,
// so this bounds a stray process, never a prompt.
const userPromptHookTimeout = 45

// usageHookTimeout is the usage hook's own limit, in seconds: its wait for
// the report before it to finish (up to 60s), usage-report.py's wait for
// the transcript to settle (up to 5s) and its 20s budget for reading it,
// token.sh's lock wait (up to 30s) and a 10s request, with room to spare.
// It runs async, so this bounds a stray process, never a stop.
const usageHookTimeout = 150

// RetiredFiles are the stubs older workspaces were given, which an update
// deletes rather than leaves for someone to run. An old checkout calling
// one now gets "No such file or directory" from the shell.
//
// Remove the briefing stubs on or after 1 Jan 2027, once no workspace can
// still carry them; the rest can go when they are no longer in any workspace.
var RetiredFiles = []string{
	".slop-haus/briefing.md",
	".slop-haus/pi/briefing.md",
	".slop-haus/run_worker.sh",
	".slop-haus/run.sh",
	".slop-haus/update-worker.sh",
	".slop-haus/harness-config.py",
}

// HookCommandPlaceholder, SessionStartHookCommandPlaceholder,
// UsageHookCommandPlaceholder and TokenCommandPlaceholder stand in for a
// script's absolute path in a generated settings.json or .mcp.json, for
// callers that only learn it at run time -- the shell script does, since it
// configures whatever directory it is run in. A placeholder rather than an
// unquoted heredoc, because a predictable string is trivial to substitute
// exactly once with sed, and nothing else in settings.json happens to
// collide with any of them.
const (
	HookCommandPlaceholder             = "__SLOP_HAUS_HOOK__"
	SessionStartHookCommandPlaceholder = "__SLOP_HAUS_SESSION_START_HOOK__"
	UsageHookCommandPlaceholder        = "__SLOP_HAUS_USAGE_HOOK__"
	UserPromptHookCommandPlaceholder   = "__SLOP_HAUS_USER_PROMPT_HOOK__"
	TokenCommandPlaceholder            = "__SLOP_HAUS_TOKEN_SCRIPT__"
)

//go:embed assets/sh/hook-stop.sh
var hookScript []byte

//go:embed assets/sh/hook-session-start.sh
var sessionStartHookScript []byte

//go:embed assets/sh/hook-user-prompt.sh
var userPromptHookScript []byte

//go:embed assets/sh/hook-usage.sh
var usageHookScript []byte

//go:embed assets/py/usage-report.py
var usageReporter []byte

//go:embed assets/py/slop_haus_token.py
var tokenModule []byte

//go:embed assets/sh/token.sh
var tokenScript []byte

//go:embed assets/sh/update.sh
var updaterScript []byte

//go:embed assets/sh/sync-repos.sh
var repoSyncerScript []byte

//go:embed assets/sh/install-app.sh
var appInstallerScript []byte

//go:embed assets/sh/run_remote_control.sh
var remoteControlLauncherScript []byte

//go:embed assets/sh/run.env
var launcherEnv []byte

//go:embed assets/md/worker-rules.md
var workerRules string

//go:embed assets/md/splitting.md
var splittingProtocol string

//go:embed assets/py/describe-session.py
var sessionDescriber []byte

//go:embed assets/ts/pi-extension.ts
var piExtension []byte

// Hook is the Stop hook, byte-for-byte the script in this package.
func Hook() []byte { return hookScript }

// Params is everything a workspace is built from.
type Params struct {
	// AgentName appears in the briefing, which is why the briefing cannot
	// live in a shared CLAUDE.md: three checkouts of one repo need three.
	AgentName string
	// Server is the base URL agents talk to. One URL for MCP, the hooks
	// and the API -- see frontend/nginx.conf.template, which proxies all
	// three to make that true.
	Server string
	// HookCommand is the absolute path to the installed Stop hook. Callers
	// that do not know it yet pass HookCommandPlaceholder and substitute
	// later.
	HookCommand string
	// SessionStartHookCommand is the same, for the SessionStart hook.
	SessionStartHookCommand string
	// UserPromptHookCommand is the same, for the UserPromptSubmit hook.
	UserPromptHookCommand string
	// UsageHookCommand is the same, for the usage hook.
	UsageHookCommand string
	// TokenCommand is the same, for token.sh, which .mcp.json's
	// headersHelper runs.
	TokenCommand string
	// Repos is the tenant's workspace manifest, which the scripts hand to
	// RepoSyncer to clone whatever is missing. Empty while the tenant has
	// declared none, when nothing is cloned.
	Repos []Repo
	// Fleet is the tenant's default fleet instructions, rendered into the
	// briefings. Nil while the tenant has none,
	// when those files are exactly what they were before there were any.
	Fleet *FleetInstructions
	// ChangeTools is whether the tenant has a code host credential, so
	// slop-haus opens and merges a ticket's change (change_open,
	// change_checks, change_merge) and the briefings say to use those. False
	// leaves the landing rules as the repo's own CLAUDE.md flow.
	ChangeTools bool
	// Role is whose session reads the workspace; it picks the protocol in a
	// nested workspace's root AGENTS.md. Left unset it is a headless worker's.
	Role Role
}

// Repo is one manifest entry: a path under the workspace root and the repo
// (host/owner/repo) that belongs there.
type Repo struct {
	Path   string
	Remote string
}

// Ownership says who a file belongs to once setup has written it, which is
// what an update may do to it.
type Ownership int

const (
	// Managed files are slop-haus's. An update rewrites them wholesale, so
	// a change here reaches every workspace.
	Managed Ownership = iota
	// Seeded files are written once and then belong to the workspace.
	// Claude Code records the human's "always allow" answers in
	// settings.local.json, and an update that rewrote it would quietly take
	// every one of those away.
	Seeded
)

// File is one file to write into the workspace.
type File struct {
	Path string
	Body []byte
	// Mode matters for the two files with +x, since Claude Code and the
	// shell run them directly.
	Mode      uint32
	Ownership Ownership
}

// Files returns the workspace's contents, in the order they should be
// written. CLAUDE.md is not among them: it is appended to rather than
// written, since the repo may own it. Nor is .slop-haus/update.lock, which
// update.sh creates for itself, nor .slop-haus/usage.lock, which the
// usage hook does, nor .slop-haus/harness-config.json, the harness config
// the Remote Control host saves each time it fetches one, nor .slop-haus/credentials.json or
// its lock file -- token.sh writes those at run time, this workspace's
// OAuth grant lives in them, and neither setup nor update has anything to
// put there. Leaving them off this list is what keeps an update from ever
// touching them: writeFiles only ever acts on what is here.
func Files(p Params) ([]File, error) {
	settings, err := encode(settingsFor(p))
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	local, err := encode(localSettings())
	if err != nil {
		return nil, fmt.Errorf("local settings: %w", err)
	}
	mcp, err := encode(mcpConfig(p.Server, p.TokenCommand))
	if err != nil {
		return nil, fmt.Errorf("mcp config: %w", err)
	}

	docs := Documents(p.AgentName, p.Fleet, p.ChangeTools)
	files := []File{
		// No secret in this one any more -- an agent has no static token,
		// only an OAuth grant behind token.sh -- so it is safe to rewrite
		// wholesale on every update, which is exactly what migrates an
		// older workspace's dead SLOP_HAUS_TOKEN line away.
		{Path: EnvFile, Body: []byte(envFile(p.Server, p.AgentName)), Mode: 0o644, Ownership: Managed},
		{Path: HookScript, Body: hookScript, Mode: 0o755, Ownership: Managed},
		{Path: SessionStartHookScript, Body: sessionStartHookScript, Mode: 0o755, Ownership: Managed},
		{Path: UsageHookScript, Body: usageHookScript, Mode: 0o755, Ownership: Managed},
		{Path: UserPromptHookScript, Body: userPromptHookScript, Mode: 0o755, Ownership: Managed},
		// Run as `python3 -I <path>` by the usage hook, never directly.
		{Path: UsageReporter, Body: usageReporter, Mode: 0o644, Ownership: Managed},
		// Run as `python3 <path>` by both hooks, never directly.
		{Path: SessionDescriber, Body: sessionDescriber, Mode: 0o644, Ownership: Managed},
		// pi's whole slop-haus integration -- tools, session binding, the
		// keep-working loop -- which the Remote Control host loads with -e. Managed like
		// the hooks it drives, and for the same reason: it runs with this
		// workspace's credentials in reach.
		{Path: PiExtension, Body: piExtension, Mode: 0o644, Ownership: Managed},
		// Delivered ahead of its use: nothing runs it yet.
		{Path: TokenModule, Body: tokenModule, Mode: 0o644, Ownership: Managed},
		{Path: TokenScript, Body: tokenScript, Mode: 0o755, Ownership: Managed},
		{Path: Updater, Body: updaterScript, Mode: 0o755, Ownership: Managed},
		{Path: RepoSyncer, Body: repoSyncerScript, Mode: 0o755, Ownership: Managed},
		// Fetches the supervisor package a Remote Control host runs from; see
		// RemoteControlLauncher.
		{Path: AppInstaller, Body: appInstallerScript, Mode: 0o755, Ownership: Managed},
		// A Remote Control's whole job: starting the host that serves its
		// owner's sessions. The
		// host itself is the supervisor package's, which update.sh installs
		// into .slop-haus/app. Every workspace carries the launcher, since
		// which kind of agent it is lives on the server, not on this machine.
		// Managed like the hooks, for the same reason: it runs with this
		// workspace's credentials in reach.
		{Path: RemoteControlLauncher, Body: remoteControlLauncherScript, Mode: 0o755, Ownership: Managed},
		// Seeded: it is where a workspace says which of its own secrets
		// the agent gets, and an update must never take that away.
		{Path: LauncherEnv, Body: launcherEnv, Mode: 0o600, Ownership: Seeded},
		// Where the updater asks for updates, and where token.sh asks for a
		// server to authenticate against.
		{Path: ServerURL, Body: []byte(strings.TrimSuffix(p.Server, "/") + "\n"), Mode: 0o644, Ownership: Managed},
		{Path: GitignoreAll, Body: []byte("*\n"), Mode: 0o644, Ownership: Managed},
		// For the supervisor, which passes it as a headless run's system prompt.
		{Path: HeadlessBriefing, Body: []byte(docs.Headless.Text()), Mode: 0o644, Ownership: Managed},
		{Path: Settings, Body: settings, Mode: 0o644, Ownership: Managed},
		{Path: LocalSettings, Body: local, Mode: 0o644, Ownership: Seeded},
		{Path: MCPConfig, Body: mcp, Mode: 0o644, Ownership: Managed},
	}
	// The root is not a repo, so these are ours outright.
	files = append(files,
		File{Path: AgentsMD, Body: []byte(RootAgentsText(p, docs)), Mode: 0o644, Ownership: Managed},
		File{Path: ClaudeMD, Body: []byte(RootClaudeMDText()), Mode: 0o644, Ownership: Managed},
	)
	// Last, so an update cut short leaves the old hash: the workspace is
	// reported behind until the whole bundle has been written.
	files = append(files, File{Path: BundleHashFile, Body: []byte(BundleHash() + "\n"), Mode: 0o644, Ownership: Managed})
	return files, nil
}

func encode(v any) ([]byte, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// envFile carries nothing secret any more: an agent's credential is
// .slop-haus/credentials.json, which token.sh manages. This is only ever a
// convenience for a human running `set -a && . ./.slop-haus.env && set +a`
// before starting claude, so its values are shell-quoted the way anything
// meant to be sourced has to be.
func envFile(server, agentName string) string {
	return fmt.Sprintf("# slop-haus workspace config. No credential lives here -- see\n"+
		"# .slop-haus/credentials.json, which .slop-haus/token.sh manages.\n"+
		"SLOP_HAUS_URL=%s\nSLOP_HAUS_AGENT=%s\n",
		ShellQuote(strings.TrimSuffix(server, "/")), ShellQuote(agentName))
}

// ShellQuote wraps a value for the shell Claude Code runs a command hook
// through. The hook lives inside the workspace, so its path is the
// human's: "~/Documents/My Repo/.slop-haus/hook-stop.sh" is two words
// unquoted, and the hook then never runs at all. That failure is silent
// and fails open, which looks exactly like an agent with nothing to do.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HookPathIn is where the Stop hook lands for a given workspace root.
func HookPathIn(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(HookScript))
}

// SessionStartHookPathIn is the same, for the SessionStart hook.
func SessionStartHookPathIn(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(SessionStartHookScript))
}

// UserPromptHookPathIn is the same, for the UserPromptSubmit hook.
func UserPromptHookPathIn(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(UserPromptHookScript))
}

// UsageHookPathIn is the same, for the usage hook.
func UsageHookPathIn(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(UsageHookScript))
}

// TokenPathIn is the same, for token.sh.
func TokenPathIn(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(TokenScript))
}

func settingsFor(p Params) map[string]any {
	server := strings.TrimSuffix(p.Server, "/")
	// The hooks are all command hooks now, and identically shaped: none
	// carries a credential of its own any more. An http hook's headers can
	// only ever hold a fixed string, and this workspace has no such string
	// -- the access token comes from token.sh, refreshed on demand -- so
	// the only way to still have one is to run something first. That is
	// also why Stop had to be a command hook already: an http hook's
	// "block" is surfaced and then ignored, and only a command hook
	// exiting 2 actually sends Claude Code back to work. SessionStart
	// moving to match is a side effect of losing the static token, not a
	// change to what it does -- see the script's own comment for exactly
	// what could and couldn't be preserved.
	//
	// Neither needs allowedEnvVars any more: the scripts read the
	// workspace's own files on disk rather than an environment variable.
	commandHook := func(command string, timeout int) map[string]any {
		return map[string]any{
			"type":    "command",
			"command": fmt.Sprintf("%s -server=%s", ShellQuote(command), ShellQuote(server)),
			"timeout": timeout,
		}
	}
	// The usage hook is async: Claude Code starts it and carries on, so it
	// can neither delay a stop nor change the Stop hook's decision -- an
	// async hook's exit code and output are not read. A hook in its own
	// group, too, so it never shares a matcher with the decision.
	usageHook := commandHook(p.UsageHookCommand, usageHookTimeout)
	usageHook["async"] = true
	// Async for the same reason, and one that matters more: this one runs
	// on every prompt a human types, and its answer is never read.
	userPromptHook := commandHook(p.UserPromptHookCommand, userPromptHookTimeout)
	userPromptHook["async"] = true
	return map[string]any{
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{"hooks": []any{userPromptHook}},
			},
			"SessionStart": []any{
				map[string]any{"hooks": []any{commandHook(p.SessionStartHookCommand, 10)}},
			},
			"Stop": []any{
				map[string]any{"hooks": []any{commandHook(p.HookCommand, stopHookTimeout)}},
				map[string]any{"hooks": []any{usageHook}},
			},
			// Every subagent's, nested ones included: each has a transcript
			// of its own that no other report counts.
			"SubagentStop": []any{
				map[string]any{"hooks": []any{usageHook}},
			},
		},
	}
}

// localSettings pre-approves slop-haus's own tools.
//
// Without it an agent is asked to approve each tool the first time it
// reaches for it, which in a headless or unattended run means it simply
// cannot work: the very first checkin comes back "you haven't granted it
// yet".
//
// It goes in .claude/settings.local.json, not settings.json, because allow
// rules in the shared file wait for the workspace-trust step while the
// local file's apply immediately as long as it stays untracked. The server
// segment of the rule has to be glob-free; the tool half may be a glob.
func localSettings() map[string]any {
	return map[string]any{
		"permissions": map[string]any{
			"allow": []string{"mcp__slop-haus__*"},
		},
	}
}

// mcpConfig goes in .mcp.json, not .claude/settings.json: the latter has
// no mcpServers key and ignores one without complaint, so the tools simply
// never appear.
//
// The credential comes from headersHelper, never a fixed header: Claude
// Code runs it on every connection and again after a 401, so it hands over
// whatever token.sh has just refreshed. Leaving it to Claude Code's own MCP
// OAuth instead meant a second sign-in per workspace, on top of the device
// flow setup already did, and one a machine without a browser could only
// finish by copying a failed localhost redirect back into its terminal.
func mcpConfig(server, tokenCommand string) map[string]any {
	server = strings.TrimSuffix(server, "/")
	return map[string]any{
		"mcpServers": map[string]any{
			"slop-haus": map[string]any{
				"type":          "http",
				"url":           server + "/mcp",
				"headersHelper": fmt.Sprintf("%s headers -server=%s", ShellQuote(tokenCommand), ShellQuote(server)),
				// Milliseconds. See ToolCallTimeout.
				"timeout": ToolCallTimeout.Milliseconds(),
			},
		},
	}
}
