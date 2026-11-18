# codehosttest

A fake `codehost.Host` and `codehost.Changes` for tests of the layers above
the interfaces. By default it decides nothing: it answers what a test set and
records what it was asked.

## Honest CI (`Fake.CI`)

Set `Fake.CI` and the fake decides when checks exist, as GitHub with this
repository's Woodpecker does. The behaviours, each with its source, are
documented on `CI` in `ci.go`:

| Behaviour | Source |
|---|---|
| Checks exist only for a commit with an open change (draft or not); a bare branch has none | Woodpecker workflows run on `pull_request`, cron and manual only |
| Checks are for the head SHA, never the branch name or merge ref | GitHub commit status / check run APIs |
| A pipeline is queued before it reports; no checks is not a pass (`CI.Manual`, `CI.Complete`) | `codehost.Checks` |
| A pipeline can error before it starts: no checks, then one failed check (`ConfigErrorChecks`) | Woodpecker, on a pipeline config error |
| Closing a change starts no new pipeline and never completes a pending one | Woodpecker `pull_request` trigger |
| `CI.Strict`: opening refuses a missing branch, a head with no commits beyond the base and a second open change; merging a stale head is `ErrHeadMoved` | the contract suite |

Tests that move a change's head by editing `ChecksOf` are seen to when they
next read the change; a pipeline starts at that read, while the change is open.

## Contract

`../contract` runs one suite against this fake (with `CI.Strict`) and, when
`SLOP_HAUS_CONTRACT_GITHUB_REPO` (`owner/name`) and
`SLOP_HAUS_CONTRACT_GITHUB_TOKEN` are set, against the real GitHub adapter on a
scratch repository that has no CI workflows. The real half is skipped until
the scratch repository exists; it is meant for a nightly run, not
every pull request.
