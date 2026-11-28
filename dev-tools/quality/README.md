# Quality

Each project has a `quality.sh` beside its `test.sh` and `scan.sh`. It
measures the project as a whole and ratchets the numbers against committed
baselines in `<project>/quality_baseline/`, so a measure can improve but
never quietly get worse.

lint is the per-finding gate ("this function is too complex", "this code is
dead"), with its own ratchet (`dev-tools/lint/README.md`). `quality.sh` owns
the numbers: coverage, the complexity distribution, and test time. Neither
runs the other's checks.

## Before pushing

```sh
slop-bot/quality.sh                    # check: fails on any regression
slop-bot/quality.sh --prune            # after an improvement: tighten the baselines
slop-bot/quality.sh --report           # the full reports, to quality-reports/slop-bot/
```

| Flag | |
| --- | --- |
| (none) | check: fail on any regression, naming the package or measure and both values |
| `--prune` | tighten each baseline where its measure improved; never loosens, and still fails on a regression |
| `--rebaseline` | rewrite the baselines from the current values. Deliberate: its commit says why |
| `--report` | write the full reports to `${QUALITY_REPORT_DIR:-<repo>/quality-reports}/<project-slug>/`, and print the comparison; never fails |
| `--only=MEASURE[,MEASURE]` | of `coverage`, `complexity`, `testtime` (default all three) |
| `--coverprofile=FILE` | reuse a cover profile rather than running the tests; repeatable, merged. Test time is then skipped |
| `--coverdir=DIR` | merge a `GOCOVERDIR` (an e2e run's, from a `-cover` binary) into the coverage; repeatable |
| `--mutation[=changed\|all]` | mutation testing: not implemented yet (plan task 10.1); exits 2 |

It exits 0 when every measure passes, 1 on a regression, and 2 on a usage
error or a measure that couldn't be taken. A failing test suite fails it
too (1), showing the failed tests' output: measures of a broken suite mean
nothing.

By default it runs the suite once:
`go test -race -count=1 -coverprofile=... -coverpkg=<the module's packages> -json ./...`.
`-coverpkg` covers every package from every package's tests, so a package
without tests of its own still counts, at what other tests reach. The one
run feeds both coverage and test time; `-count=1` because a cached result
replays in milliseconds (it is left out when test time isn't selected).

## The measures and their rules

| Measure | Baseline | Regresses when | Notes |
| --- | --- | --- | --- |
| coverage | `coverage.txt`: `<package> <percent>` lines, sorted, then `total <percent>` | a package or the total falls more than 0.1 points below its baseline; a package not in the baseline is under 80% | statement coverage, generated files (a `// Code generated ... DO NOT EDIT.` header) excluded |
| complexity | `complexity.json` | the mean of cyclomatic or cognitive complexity rises more than 0.1; their p90 or max rises at all, or the count of functions over the threshold does | non-test, non-generated functions; thresholds cyclomatic 15, cognitive 20, as lint's |
| testtime | `testtime.json` | never: report-only | warns when the wall time is over 50% (and over 1s) above the baseline |

**Coverage.** Packages are named relative to the module (`.`,
`./internal/x`). The 0.1-point tolerance absorbs rounding (percentages are
kept to one decimal); it can't accumulate, since the baseline never moves
down. A new package must reach **80%** to land; `--prune` then adds it to the
baseline at its own value. A package that is gone is a stale entry,
`--prune` removes it. Profiles merge by "covered or not", so a `set`-mode
e2e profile and an `atomic` one from `-race` combine. A `GOCOVERDIR` is
converted with `go tool covdata textfmt`.

**Complexity.** Cyclomatic is gocyclo's, cognitive gocognit's (with
recursion), both used as libraries. A function lint excuses with
`gocognit:ignore` still counts here: lint's suppression is about one
function, this is about the whole. The means are kept to two decimals.
The function count and the ten longest functions (by lines) are recorded
in the baseline for the diff to show, but not gated: a module may grow.
Directories named `testdata` or `vendor`, starting `.` or `_`, and nested
modules (which measure themselves) are skipped.

These rules are simple and strict: any rise in a gated number fails, but
for a mean's 0.1. A mean moves when simple functions go as much as when
complex ones arrive: folding a duplicated helper into one raised this
module's means by 0.02, which without the tolerance failed. Like coverage's,
it can't accumulate. When a bigger rise is the right call, rebaseline that
measure alone (`--rebaseline --only=complexity`) and say why in the commit.

**Test time.** The wall time is from the first `go test -json` event to the
last (packages run in parallel, so their times don't add), and the ten
slowest top-level tests are listed (a subtest's time is in its parent's).
Timing is noisy, and the repo's tests may not depend on it, so it never
fails. `--prune` leaves it alone, so one fast run doesn't become the bar;
only `--rebaseline` moves it.

**Missing baselines.** A check or prune of coverage or complexity without a
baseline is an error (2): create it with `--rebaseline`. Test time without
one is a note.

## A new project

Copy `slop-bot/quality.sh`, run `<project>/quality.sh --rebaseline`, and
commit `<project>/quality_baseline/`. In a project's first baseline every
package is accepted as it stands (a rebaseline has no floor); from then on
a new package needs 80%. Add a CI step running it after `test.sh`.

`spec/every_project_spec.sh` checks that every project in
`dev-tools/lint/projects.txt` has an executable `quality.sh` built on
`quality_lib.sh`, unless `pending.txt` lists it. `pending.txt` held every
project when this landed, and only shrinks: a project that gets a
`quality.sh` leaves it, and an entry the branch adds (against the merge-base
with `main`, or `QUALITY_BASE`) fails. CI fetches `main` for that and sets
`QUALITY_REQUIRE_BASE=1`, so the check can't silently skip there.

## Tools

| Tool | Version | License | Used for |
| --- | --- | --- | --- |
| golang.org/x/tools/cover | x/tools 0.52.0 | BSD-3-Clause | reading cover profiles |
| `go tool covdata` | the Go toolchain's | BSD-3-Clause | converting a `GOCOVERDIR` |
| gocyclo | 0.6.0 | BSD-3-Clause | cyclomatic complexity, as a library |
| gocognit | 1.3.0 | MIT | cognitive complexity, as a library (lint runs the same version's CLI) |
| golang.org/x/mod | 0.43.0 | BSD-3-Clause | reading `go.mod`'s module path |
| testify | 1.12.1 | MIT | the Go tests |
| shellspec | 0.28.1 | MIT | the shell tests (`spec/`) |

The Go dependencies are pinned in `go.mod` here; `quality_lib.sh` builds the
CLI with `GOWORK=off`, against this module's own `go.sum`, as lint builds its
tools. shellspec is installed from its release tarball
(`shellspec-dist.tar.gz`, unpacked with `shellspec` on `PATH`); CI's
`.github/actions/setup` does the same, checking its sha256.

## Tests

`dev-tools/quality/test.sh`: lint, `gofmt`, `go vet`, `go test -race`, then
`shellspec` (from this directory). The Go tests cover the CLI end to end on
scratch modules and each measure's parsing and rules; one builds a real
`-cover` binary to read its `GOCOVERDIR`. The specs cover `quality_lib.sh`'s
flags and its runs with `go` and the CLI stubbed, and the every-project
check against the real repo.
