package workspace

import (
	"fmt"
	"strings"
)

// Script renders a workspace as a shell script that builds it.
//
// It is how a workspace is set up: the machine running an agent has a
// checkout of some other repo, and no slop-haus source, Go toolchain or
// binary. They get a file, read it, and run it in their repo.
//
// The contents come from Files, as the update script's do, so a workspace
// set up and a workspace updated end up the same by construction. That is
// the reason this lives here rather than in the handler that serves it.
//
// The agent is already registered by the time this is called, but it has
// no credential yet: it is authorized through OAuth's device flow, which
// this script walks the human through after writing everything else. A
// leaked copy of this script is therefore just a script -- there is
// nothing in it worth stealing.
func Script(p Params) (string, error) {
	files, err := scriptFiles(p)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	// The name is shell-quoted everywhere it appears. Go's %q escapes
	// quotes and backslashes but not $, so a name of "$(touch /tmp/x)"
	// inside a double-quoted echo runs as a command substitution -- which
	// an earlier version of this did, and a test using a quote-based
	// payload did not catch.
	fmt.Fprintf(&b, `#!/usr/bin/env bash
# slop-haus workspace setup for agent %s.
#
# Run this in the directory that holds the agent's repos (not in a repo
# itself): they are cloned into subfolders. It writes
# the MCP config, both hooks, the working agreement and the token helper,
# then signs this workspace in over OAuth.
#
# This file carries no credential: there is no static token to embed any
# more, only a device-flow sign-in this script walks you through next.
set -euo pipefail

workspace=$(pwd)
echo "Configuring $workspace for agent" %s

`, ShellQuote(p.AgentName), ShellQuote(p.AgentName))

	writeFiles(&b, p, files, setupWrites)
	writeRepoSync(&b, p)

	fmt.Fprintf(&b, `echo
echo "Signing in as agent" %s "..."
signed_in=1
# The server this script was made for, named outright: token.sh otherwise
# prefers an SLOP_HAUS_URL already in the shell, which could be some other
# slop-haus altogether.
SLOP_HAUS_URL=%s "$workspace"/%s login %s || signed_in=0

cat <<'SLOPHAUS_EOF'

Done. Workers run under the supervisor, which starts a run per ticket in
this checkout: add it as a slot (slop-haus slot add). To serve your own
sessions from here, start .slop-haus/run_remote_control.sh instead.
SLOPHAUS_EOF

if [ "$signed_in" != 1 ]; then
  cat <<'SLOPHAUS_EOF'

Sign-in did not finish. Run this yourself whenever you're ready, before
starting the agent here:

  .slop-haus/token.sh login
SLOPHAUS_EOF
fi

`, ShellQuote(p.AgentName), ShellQuote(strings.TrimSuffix(p.Server, "/")), TokenScript, ShellQuote(p.AgentName))

	b.WriteString(`cat <<'SLOPHAUS_EOF'

Both harnesses connect to slop-haus with this same sign-in -- Claude Code
through .mcp.json, pi through its extension, each asking
.slop-haus/token.sh for it -- so there is no separate MCP login.

Starting .slop-haus/run_remote_control.sh updates these files first, so restarting is all it takes to pick up later
changes. .slop-haus/update.sh does the same by hand.
SLOPHAUS_EOF
`)

	return b.String(), nil
}

// UpdateScript renders the script .slop-haus/update.sh fetches and
// runs: the same files as setup, applied to a workspace that already
// exists.
//
// What it may touch follows each file's Ownership -- managed files are
// rewritten, seeded ones only created if they have gone missing. None of
// them is a credential any more, so unlike the old, token-carrying setup
// script this one never did, running it twice is the same as running it
// once, and an older workspace's dead SLOP_HAUS_TOKEN line is simply
// overwritten away the next time EnvFile is rewritten.
func UpdateScript(p Params) (string, error) {
	files, err := scriptFiles(p)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	// The second line is what update.sh checks before running
	// anything, so an error page served with a 200 is refused rather than
	// executed. Keep it exactly as is.
	fmt.Fprintf(&b, `#!/usr/bin/env bash
# slop-haus workspace update
#
# For agent %s. Rewrites the files slop-haus manages in this workspace and
# leaves this workspace's OAuth credentials, and anything the workspace
# owns, alone. Run from the workspace root -- .slop-haus/update.sh
# does that.
set -euo pipefail

workspace=$(pwd)
# Without this file, setup was never run here, and writing hooks into a
# directory that was never set up would give it a Stop hook with nothing
# behind it -- not yet signed in, and failing open on every turn. This
# checks for EnvFile rather than the OAuth credentials themselves: signing
# in is a separate, sometimes-delayed step (see Script), and a workspace
# that finished setup but hasn't approved the device code yet is still a
# workspace, just one update can't do anything useful for until it has a
# token -- which the fetched files below don't change either way.
if [ ! -f %s ]; then
  echo "slop-haus: $workspace is not a slop-haus workspace (no %s) -- run setup there instead" >&2
  exit 1
fi
echo "Updating $workspace for agent" %s

`, ShellQuote(p.AgentName), ShellQuote(EnvFile), EnvFile, ShellQuote(p.AgentName))

	writeFiles(&b, p, files, updateWrites)
	writeRetiredFiles(&b)
	writeRepoSync(&b, p)

	b.WriteString(`cat <<'SLOPHAUS_EOF'

Updated. An agent already running here keeps the files it started with:
Claude Code only notices a new .claude/agents directory when it starts,
and pi loads its extension and briefing only when it starts.
SLOPHAUS_EOF
`)
	return b.String(), nil
}

// scriptFiles is Files for a script that runs somewhere this process
// cannot see.
func scriptFiles(p Params) ([]File, error) {
	// A newline in the name would end the comment it appears in and start
	// a line of shell, and could also close a heredoc early. Everything
	// else about a name is handled by quoting; this one cannot be, so it is
	// refused rather than mangled. The server takes any non-empty name
	// (agent.ErrEmptyName), so this is not a theoretical input.
	if strings.ContainsAny(p.AgentName, "\r\n") {
		return nil, fmt.Errorf("agent name contains a line break, which cannot be rendered into a script safely")
	}
	// The scripts' absolute paths are only known when the script runs,
	// since it configures whatever directory it is run in.
	p.HookCommand = HookCommandPlaceholder
	p.SessionStartHookCommand = SessionStartHookCommandPlaceholder
	p.UsageHookCommand = UsageHookCommandPlaceholder
	p.UserPromptHookCommand = UserPromptHookCommandPlaceholder
	p.TokenCommand = TokenCommandPlaceholder
	return Files(p)
}

// write is what a script does with one file.
type write int

const (
	skip write = iota
	overwrite
	createIfMissing
)

func setupWrites(File) write { return overwrite }

func updateWrites(f File) write {
	switch f.Ownership {
	case Managed:
		return overwrite
	case Seeded:
		return createIfMissing
	default:
		return skip
	}
}

func writeFiles(b *strings.Builder, p Params, files []File, policy func(File) write) {
	// Every path is checked before anything is written, so a refusal leaves
	// the workspace as it found it.
	//
	// A symlink such as `.claude -> ~/.config/...` would have every write
	// below land outside the workspace, and the updater runs this
	// unattended, again and again.
	fmt.Fprintf(b, `# Refuse to write through a symlink anywhere on a path we are about to
# write -- a link in a checkout would put these files wherever it points.
refuse_links() {
  local prefix="" part
  local -a parts
  IFS=/ read -r -a parts <<< "$1"
  for part in "${parts[@]}"; do
    prefix="${prefix:+$prefix/}$part"
    if [ -L "$prefix" ]; then
      echo "slop-haus: $workspace/$prefix is a symlink -- refusing to write through it" >&2
      exit 1
    fi
  done
}
`)
	for _, file := range files {
		if policy(file) != skip {
			fmt.Fprintf(b, "refuse_links %s\n", ShellQuote(file.Path))
		}
	}
	writeCreateSeeded(b)
	writeScriptPaths(b)
	writeFileBodies(b, files, policy)
}

// writeRetiredFiles removes the files RetiredFiles names. A link on the way
// to one is refused, as for any write, so the removal cannot reach outside
// the workspace.
func writeRetiredFiles(b *strings.Builder) {
	for _, path := range RetiredFiles {
		fmt.Fprintf(b, "refuse_links %s\nrm -f %s\n", ShellQuote(path), ShellQuote(path))
	}
	b.WriteString("\n")
}

func writeScriptPaths(b *strings.Builder) {
	// settings.json and .mcp.json name scripts by absolute path, which is
	// only knowable now, in the directory this is being run in.
	fmt.Fprintf(b, "hook=\"$workspace/%s\"\n", HookScript)
	fmt.Fprintf(b, "session_start_hook=\"$workspace/%s\"\n", SessionStartHookScript)
	fmt.Fprintf(b, "usage_hook=\"$workspace/%s\"\n", UsageHookScript)
	fmt.Fprintf(b, "user_prompt_hook=\"$workspace/%s\"\n", UserPromptHookScript)
	fmt.Fprintf(b, "token_script=\"$workspace/%s\"\n\n", TokenScript)
}

func writeFileBodies(b *strings.Builder, files []File, policy func(File) write) {
	for _, file := range files {
		action := policy(file)
		if action == skip {
			continue
		}
		// The root is not a repo, so these are ours. If it is one anyway (a
		// misplaced checkout, reported as a layout mismatch), its own
		// AGENTS.md and CLAUDE.md are left alone rather than overwritten.
		rootFile := file.Path == AgentsMD || file.Path == ClaudeMD
		if rootFile {
			b.WriteString("if [ ! -e .git ]; then\n")
		}
		indent := ""
		if action == createIfMissing {
			fmt.Fprintf(b, "if [ ! -e %s ]; then\n", ShellQuote(file.Path))
			indent = "  "
		}
		path := ShellQuote(file.Path)
		if dir := parentOf(file.Path); dir != "" {
			fmt.Fprintf(b, "%smkdir -p %s\n", indent, ShellQuote(dir))
		}
		// Written beside the target and renamed over it, so an interrupted
		// run leaves the old file rather than a truncated one -- a half-written
		// hook fails open on every Stop, and half a settings.json is no
		// settings at all. mktemp creates the file itself, so nothing planted
		// at a predictable temporary name gets written through either.
		fmt.Fprintf(b, "%stmp=$(mktemp %s)\n", indent, ShellQuote(file.Path+".XXXXXX"))
		if len(file.Body) > 0 {
			// A quoted heredoc, so nothing inside is expanded: these files
			// legitimately contain shell-looking placeholders that get
			// substituted below, not by the shell reading this heredoc. Its
			// terminator must start the line, so it is not indented.
			tag := heredocTag(file.Body)
			fmt.Fprintf(b, "%scat > \"$tmp\" <<'%s'\n%s%s\n", indent, tag, ensureNewline(string(file.Body)), tag)
		}
		if file.Path == Settings || file.Path == MCPConfig {
			// Bash's ${var//search/replace} rather than sed, because
			// settings.json contains $ of its own and sed's replacement
			// text treats \, & and its own delimiter specially -- a
			// workspace path containing any of them (a `|`, say) would
			// have sed misparse the substitution or write a corrupted
			// command. Bash's replacement is a plain literal string, so
			// nothing about $hook, $session_start_hook, $usage_hook or
			// $token_script needs escaping.
			fmt.Fprintf(b, "%scontent=$(cat \"$tmp\")\n", indent)
			fmt.Fprintf(b, "%scontent=${content//%s/$hook}\n", indent, HookCommandPlaceholder)
			fmt.Fprintf(b, "%scontent=${content//%s/$session_start_hook}\n", indent, SessionStartHookCommandPlaceholder)
			fmt.Fprintf(b, "%scontent=${content//%s/$usage_hook}\n", indent, UsageHookCommandPlaceholder)
			fmt.Fprintf(b, "%scontent=${content//%s/$user_prompt_hook}\n", indent, UserPromptHookCommandPlaceholder)
			fmt.Fprintf(b, "%scontent=${content//%s/$token_script}\n", indent, TokenCommandPlaceholder)
			fmt.Fprintf(b, "%sprintf '%%s\\n' \"$content\" > \"$tmp\"\n", indent)
		}
		fmt.Fprintf(b, "%schmod %o \"$tmp\"\n", indent, file.Mode)
		if action == createIfMissing {
			fmt.Fprintf(b, "%screate_seeded \"$tmp\" %s\n", indent, path)
			b.WriteString("fi\n")
		} else {
			fmt.Fprintf(b, "%smv -f \"$tmp\" %s\n", indent, path)
		}
		if rootFile {
			b.WriteString("fi\n")
		}
		b.WriteString("\n")
	}
}

// writeCreateSeeded defines create_seeded <tmp> <path>, which puts a seeded
// file's template in place only if nothing is at <path> by then.
//
// A seeded file is the workspace's once it exists, and something else --
// Claude Code saving an "always allow", a human editing run.env -- may
// create it between the -e check and here. So ln, not mv -f: link(2)
// refuses an existing target, where a rename would silently replace it.
//
// ln treats a directory target (or a link to one) as "link inside it",
// which -T would prevent, but that is GNU-only and workspaces include
// macs. Hence the check afterwards that <path> is now our file, and the
// clean-up of anything linked inside a directory that appeared instead.
func writeCreateSeeded(b *strings.Builder) {
	b.WriteString(`create_seeded() {
  local tmp="$1" path="$2" stray
  # Failing is expected when the file has appeared; what counts is the
  # check below, not ln's status (and set -e must not see it).
  ln "$tmp" "$path" 2> /dev/null || true
  if [ -L "$path" ] || [ ! "$tmp" -ef "$path" ]; then
    stray="$path/$(basename "$tmp")"
    if [ -d "$path" ] && [ "$tmp" -ef "$stray" ]; then
      rm -f "$stray"
    fi
    if [ ! -e "$path" ] && [ ! -L "$path" ]; then
      rm -f "$tmp"
      echo "slop-haus: could not create $workspace/$path" >&2
      exit 1
    fi
  fi
  rm -f "$tmp"
}

`)
}

// writeRepoSync has RepoSyncer clone the manifest's missing repos. Every
// value is shell-quoted, which is what keeps a hostile path or remote an
// argument rather than shell; RepoSyncer then validates them itself.
//
// A failure never stops the script: a repo that cannot be cloned (no access
// from this machine, say) must not leave a workspace without its hooks.
func writeRepoSync(b *strings.Builder, p Params) {
	args := make([]string, 0, 2*len(p.Repos))
	for _, r := range p.Repos {
		args = append(args, ShellQuote(r.Path), ShellQuote(r.Remote))
	}
	fmt.Fprintf(b, `# Clones what the manifest lists and this workspace lacks -- never touching
# anything that already exists. SLOP_HAUS_CLONE=no opts out for good.
"$workspace"/%s -- %s || echo "slop-haus: not every manifest repo could be set up -- see above; the rest of the workspace is done" >&2

`, RepoSyncer, strings.Join(args, " "))
}

func parentOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}

// heredocTag is the terminator for a file's heredoc: the usual one unless a
// line of the body is already exactly it, which would end the heredoc early
// and run whatever followed as shell. Only text an owner writes (fleet
// instructions) can carry such a line, but the check holds for any body.
func heredocTag(body []byte) string {
	tag := "SLOPHAUS_EOF"
	for {
		clash := false
		for _, line := range strings.Split(string(body), "\n") {
			if line == tag {
				clash = true
				break
			}
		}
		if !clash {
			return tag
		}
		tag += "_"
	}
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
