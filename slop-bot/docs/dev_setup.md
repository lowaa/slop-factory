# Developer setup for slop-bot

What a machine needs to build and test slop-bot, and how each tool was
installed while slop-bot was being built. The install docs grow from this.

## Tools installed during development

| Tool | Version | License | Why | How it was installed |
| --- | --- | --- | --- | --- |
| shellcheck | 0.11.0 | GPL-3.0 (dev-only tool, never shipped; already approved in `dev-tools/lint/README.md`) | the repo-wide lint (`dev-tools/lint/shell.sh`) | release tarball from github.com/koalaman/shellcheck, binary copied to `~/.local/bin/shellcheck` |
| shellspec | 0.28.1 | MIT | shell script tests (the repo's convention for new shell tests) | `shellspec-dist.tar.gz` from github.com/shellspec/shellspec releases, unpacked to `~/.local/lib/shellspec`, symlinked as `~/.local/bin/shellspec` |
| tmux | 3.4 | ISC | the TUI driver (agy) and its tests | the user installed it with the OS package manager |
| gocognit (library) | 1.3.0 | MIT | cognitive complexity in `quality.sh` (`dev-tools/quality`) | a Go module dependency, `go get` |
| gocyclo (library) | 0.6.0 | BSD-3-Clause | cyclomatic complexity in `quality.sh` | a Go module dependency, `go get` |
| golang.org/x/tools/cover | x/tools 0.52.0 | BSD-3-Clause | reading cover profiles in `quality.sh` | a Go module dependency, `go get` |
| golang.org/x/mod | 0.43.0 | BSD-3-Clause | reading `go.mod`'s module path in `quality.sh` | a Go module dependency, `go get` |

## Already present on the dev host

Go 1.27.4, git 2.55 (slop-bot needs 2.36 or later: `git worktree list --porcelain -z`;
the tests set `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM`, from 2.32), docker, gh, bats, shfmt, govulncheck, osv-scanner, gitleaks,
trivy, and the four harness CLIs (`claude`, `codex`, `pi`, `agy`).
