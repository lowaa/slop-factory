# Mutation testing

Coverage says which lines the tests ran; mutation testing says whether they
would notice if those lines were wrong. The tool changes the code one way at
a time (`<` to `<=`, `+` to `-`, `==` to `!=`), runs the package's tests,
and counts the mutant **killed** when a test fails, **survived** (lived)
when they all pass. A survivor is a line the tests execute but don't check.

It is slow, because the tests run once per mutant, so it runs weekly and on
demand (`.github/workflows/mutation.yml`), never on a pull request.

```sh
dev-tools/mutation/mutation.sh slop-bot                    # check the default packages
dev-tools/mutation/mutation.sh slop-bot --packages=./internal/state
dev-tools/mutation/mutation.sh slop-bot --report           # reports to mutation-reports/slop-bot/
dev-tools/mutation/mutation.sh slop-bot --prune            # after an improvement
dev-tools/mutation/mutation.sh slop-bot --rebaseline       # deliberate: say why in the commit
```

| Flag | |
| --- | --- |
| `<project-dir>` | a Go module (a directory with a `go.mod`); required |
| (none) | check: fail when a package's score is more than 2 points below its baseline, or a new package is under 80% |
| `--prune` | tighten the baseline where a score improved, add new packages at or over 80%, drop packages whose source is gone; never loosens, and still fails on a regression |
| `--rebaseline` | record the current scores of the packages run, whatever they are; entries for packages not run stay. Deliberate: its commit says why |
| `--report` | write the reports to `${MUTATION_REPORT_DIR:-<repo>/mutation-reports}/<project>/` and print the comparison; never fails |
| `--packages=P[,P]` | package patterns relative to the project (`./internal/state`, `./internal/agents/...`), instead of the project's list |

It exits 0 when every score passes, 1 on a regression, and 2 on a usage
error or a run that couldn't be taken (a pattern that matches no package, a
failing test suite, gremlins failing or timing out).

## What it reports

For each package: mutants killed, survived, timed out, not covered, and the
**score**: `(killed + timed out) / (killed + survived + timed out)`. A mutant
that makes the tests hang counts as caught, as in PIT and Stryker. Mutants
in code no test covers ("not covered") are left out of the score: they say
nothing about the tests' strength, and `quality.sh`'s coverage already gates
them. A mutant that doesn't compile (a `+` turned into a `-` on strings)
counts as killed, not as "not viable": gremlins 0.6.0 maps a failed build to
KILLED, so a package with such mutants scores a little high. The
total is reported but not ratcheted.

`--report` (or setting `MUTATION_REPORT_DIR`, as CI does) also writes, per
project: `summary.txt`, `survivors.txt` (every survivor as
`path/file.go:line:column MUTATION_TYPE`, which opens in an editor), and
each package's raw gremlins JSON under `gremlins/`. The survivors are the
output to read: each one names a line and the change the tests didn't
notice.

## The baseline

Per project, in `<project>/mutation_baseline/`:

| File | |
| --- | --- |
| `score.txt` | `<package> <percent>` lines, sorted; written by `--prune` and `--rebaseline` |
| `packages.txt` | the packages a run covers by default, one pattern a line, `#` comments |

Rules, as `quality.sh`'s coverage: a score may not fall more than the
tolerance (**2 points**, since a package has tens of mutants and one flaky
timeout or one new survivor moves its score a point or two) below its
baseline; a new package must reach the **80%** floor; the baseline only
tightens (`--prune`), and `--rebaseline` is a deliberate loosening. A
baseline package that wasn't run is left alone, so a `--packages` run of a
subset is fine. A package with no covered mutants has no score and isn't
ratcheted.

### Which packages

All of a module takes hours, so `packages.txt` names a core subset whose
tests are fast and whose logic is worth protecting. For `slop-bot`:
`internal/tickets`, `internal/tickets/local`, `internal/state` and
`internal/agents`. Add a package to the list and `--rebaseline
--packages=<it>` to start ratcheting it. A package whose tests need
Docker, a network or a long-running daemon makes a poor candidate: each
mutant reruns them.

## Keeping a run bounded

- Gremlins times each mutant's test run out at `MUTATION_TIMEOUT_COEFFICIENT`
  (default **10**) times the package's own test time, so a mutant that makes
  a loop infinite costs seconds. Its default is 3, which on a busy machine
  times out mutants whose tests pass in time: a timeout counts as caught, so
  that inflates the score.
- One package may take `MUTATION_PACKAGE_TIMEOUT` seconds (default **1500**)
  in all; past that the run is an error (2), not a quiet partial result.
  Needs `timeout(1)` (GNU coreutils; `gtimeout` on a Mac with coreutils),
  else the cap is not enforced and the run says so.
- `MUTATION_WORKERS` mutants are tested at once (default `min(4, cores)`):
  each worker copies the module and runs its tests, and contention
  turns into spurious timeouts.
- Each package is its own gremlins run, with `--coverpkg` limited to it.
  Gremlins runs the tests of the package and of its subdirectories for the
  coverage, and then each mutant against the package's own tests only.
- It runs with `GOWORK=off` and `GOFLAGS=-count=1`. Gremlins tests its
  mutants in a copy of the module that has no `go.work`, and counts a failed
  build as killed, so a project that needs the workspace to build would
  score 100% for nothing; without it the run fails at once instead. And a
  cached coverage run replays in milliseconds, which sets every mutant's
  timeout so short that the survivors time out and count as caught.
- A package whose directory is not named for it (`package main` in `cmd/x`)
  can't be scored: gremlins looks the package up by its directory name and
  reports every mutant killed. `mutation.sh` refuses such a package when it
  is named, and skips it, with a warning, when a `...` pattern finds it.

## Choice of tool

Pinned in `go.mod` as a `tool` directive, built with `GOWORK=off` against
this module's own `go.sum`, as `dev-tools/quality` builds its CLI:

| Tool | Version | License | Why |
| --- | --- | --- | --- |
| [gremlins](https://github.com/go-gremlins/gremlins) | 0.6.0 | Apache-2.0 | chosen: JSON result per mutant (type, line, column, status), coverage-guided (only mutants a test reaches are run), a per-mutant timeout coefficient, a worker count, builds and runs on Go 1.27 |
| [avito-tech/go-mutesting](https://github.com/avito-tech/go-mutesting) | untagged (pseudo-version) | MIT | maintained, but no tagged releases to pin, text-only output to scrape, and no coverage guidance: it runs every mutant against its package's tests, covered or not |

Gremlins' last tagged release is December 2025 and its repository was still
being pushed to in mid-2026. It is a 0.x tool: its operators are few
(conditional boundary and negation, arithmetic, increment/decrement, sign
inversion are on by default; the rest are off), which suits a first
ratchet, and the repo wraps it so far as `mutation.sh` and
`internal/gremlins` (which reads its JSON, the one interface used) that a
swap would touch those and nothing else.

Limits to know: it tests each mutant in a copy of the module, so a project
must build from its own directory with `GOWORK=off` (a dependency resolved
only by the root `go.work` fails the run); and it walks the directory it is given, subdirectories too, so
`mutation.sh` keeps only the files directly in each package, which each
subdirectory's own run covers. Tests that live in another package (a shared
conformance suite, as `slop-bot`'s `internal/tickets/conformance`) don't
kill the mutants of the package they exercise: the coverage run sees those
lines as covered, then each mutant is tested against the package's own
tests, and survives. Such a package scores low (`internal/tickets`, 5.5%
on its first run), and its survivors say what its own tests don't check.

## Tests

Install shellspec as `dev-tools/quality/README.md` says. `dev-tools/mutation/test.sh`: lint, `gofmt`, `go vet`, `go test -race`, then
`shellspec`. The Go tests cover the report parsing, the score rules and the
CLI's modes end to end on scratch projects. The specs cover `mutation.sh`'s
arguments, package expansion, and its runs with `go`, gremlins and the CLI
stubbed. CI runs it, and the project's own `quality.sh`, as the
`dev-tools-mutation` job.
