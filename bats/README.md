# bats

The repo's existing shell test suites use [bats-core](https://github.com/bats-core/bats-core),
pinned as a root devDependency alongside bats-support and bats-assert. They
stay; new shell tests use shellspec (`dev-tools/quality/spec/` is the
example, `CLAUDE.md` the rule).

```
npm ci                                        # once, at the repo root
bats/run.sh slop-haus/server              # a directory's suites
bats/run.sh --recursive dev-tools            # everything under a tree
bats/run.sh -f promote slop-haus/server   # only tests matching a name
```

Always go through `bats/run.sh`: it runs the pinned bats and puts the helper
libraries on `BATS_LIB_PATH`, which `bats_load_library` needs. A bare `bats`
or `npx bats` will not find them.

## Writing a suite

- A suite is `<script>.bats`, next to the script it tests.
- Start `setup()` with `bats_load_library repo_test` -- that loads
  bats-support, bats-assert and the helpers in `lib/repo_test/load.bash`.
- Put scratch files in `$BATS_TEST_TMPDIR`. bats makes one per test and
  removes it, so there is no cleanup to write.
- Run the script under test with `run`, then `assert_success` /
  `assert_failure` / `assert_output --partial ...`. `run` merges stderr into
  `$output`.
- Stub anything with a side effect -- docker, terraform, ansible, a
  registry -- rather than guarding it with a flag. `stub_on_path docker`
  records every call to `$BATS_TEST_TMPDIR/docker.calls`;
  `assert_lines_in_order` checks a sequence of calls, not just presence.
- Compare paths canonically (`cd ... && pwd -P`). `$BATS_TEST_DIRNAME`
  keeps whatever spelling the suite was invoked with.
- Do a negative run before trusting a suite: break the script the way a
  real bug would and check the right test fails.

## Gotcha: background processes

bats waits until every process holding its file descriptor 3 has exited. A
test that starts a background process (a stub server, a `timeout` probe) and
leaves it running hangs the whole suite. Start it with `3>&-`, and kill it in
`teardown`.

## Running a component's suites

Like the rest of the repo's tests, bats suites run from the component's
`test.sh` -- by hand, or before a deploy -- not from the pre-commit hooks,
which stay to fast static checks. A bats suite goes in a directory an
existing `test.sh` already covers, or gets a `test.sh` of its own that calls
`bats/run.sh`.
