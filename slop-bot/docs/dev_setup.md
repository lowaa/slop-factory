# Prerequisites and developer setup

What a machine needs to run slop-bot, and to build and test it. The install
lines below are the ones `slop-bot doctor` prints for a missing tool (from
`internal/doctor`), so the two agree; `doctor` is the authority when they
drift. Run `slop-bot doctor` to see what your machine lacks: it changes
nothing and spends no tokens.

## Prerequisites

Required to run slop-bot:

| Tool | Needed for | Linux (as `doctor` prints) | macOS (as `doctor` prints) |
| --- | --- | --- | --- |
| git 2.36 or later | every agent runs in a git worktree (`git worktree list --porcelain -z`) | `sudo apt install git` (or your distribution's package manager); on an old distribution, upgrade git, for example from the git-core PPA | `brew install git`, and put Homebrew's `bin` before `/usr/bin` on `PATH` |
| a service manager | the installed service | systemd user manager. Log in through a normal session (not `su`); on a headless box also `loginctl enable-linger $USER` | launchd, in your GUI login session |
| a writable runtime dir | the control socket (at most 103 bytes) | `XDG_RUNTIME_DIR`, normally `/run/user/<uid>` | `XDG_RUNTIME_DIR`, else `$TMPDIR`; export `XDG_RUNTIME_DIR` to a short directory if the socket path is too long |

At least one harness, installed, on `PATH`, and signed in (`doctor` checks
each configured harness: binary, tested version, signed in, model):

| Harness | Tested versions | Linux | macOS |
| --- | --- | --- | --- |
| `claude` (Claude Code) | 2.1.332 up to 2.2 | `curl -fsSL https://claude.ai/install.sh \| bash` | `curl -fsSL https://claude.ai/install.sh \| bash` (or `brew install --cask claude-code`) |
| `codex` | 0.172.0 up to 0.173 | `npm install -g @openai/codex` | `brew install --cask codex` (or `npm install -g @openai/codex`) |
| `pi` | 1.4.0 up to 1.5 | install pi (see its own install docs) and put `pi` on `PATH` | the same |
| `agy` | 1.5.18 up to 1.6 | install the Antigravity CLI, then `agy install` to put it on `PATH` | the same |

Needed only in some setups:

| Tool | Needed for | Linux | macOS |
| --- | --- | --- | --- |
| tmux 3.4 or later | a harness configured with the `tui` driver (`agy`); `doctor` skips the check otherwise | `sudo apt install tmux` (or your distribution's package manager) | `brew install tmux` |
| any binary in `doctor.require` | your workflow's tools, for example `gh` | `sudo apt install <tool>`, or drop it from `doctor.require` | `brew install <tool>`, or drop it from `doctor.require` |

Providing an API key: `api_key_opt_in` lets a harness's key through to
agents, and the service doesn't read your shell profile. When `doctor` says
so, import the variable into the service: on Linux
`systemctl --user import-environment <NAME>` and a
`~/.config/environment.d/*.conf` line for later logins, on macOS
`launchctl setenv <NAME> ...`, then re-run `slop-bot install`.

## Developer setup

Building and testing also needs Go and the tools in the next sections. For
what each script runs, see [development](development.md).

### Tools installed during development

How each tool was installed while slop-bot was being built.

| Tool | Version | License | Why | How it was installed |
| --- | --- | --- | --- | --- |
| shellcheck | 0.11.0 | GPL-3.0 (dev-only tool, never shipped; already approved in `dev-tools/lint/README.md`) | the repo-wide lint (`dev-tools/lint/shell.sh`) | release tarball from github.com/koalaman/shellcheck, binary copied to `~/.local/bin/shellcheck` |
| shellspec | 0.28.1 | MIT | shell script tests (the repo's convention for new shell tests) | `shellspec-dist.tar.gz` from github.com/shellspec/shellspec releases, unpacked to `~/.local/lib/shellspec`, symlinked as `~/.local/bin/shellspec` |
| tmux | 3.4 | ISC | the TUI driver (agy) and its tests | the user installed it with the OS package manager |
| gocognit (library) | 1.3.0 | MIT | cognitive complexity in `quality.sh` (`dev-tools/quality`) | a Go module dependency, `go get` |
| gocyclo (library) | 0.6.0 | BSD-3-Clause | cyclomatic complexity in `quality.sh` | a Go module dependency, `go get` |
| golang.org/x/tools/cover | x/tools 0.52.0 | BSD-3-Clause | reading cover profiles in `quality.sh` | a Go module dependency, `go get` |
| golang.org/x/mod | 0.43.0 | BSD-3-Clause | reading `go.mod`'s module path in `quality.sh` | a Go module dependency, `go get` |

### Already present on the dev host

Go 1.27.4, git 2.55 (slop-bot needs 2.36 or later: `git worktree list --porcelain -z`;
the tests set `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM`, from 2.32), docker, gh, bats, shfmt, govulncheck, osv-scanner, gitleaks,
trivy, and the four harness CLIs (`claude`, `codex`, `pi`, `agy`).
