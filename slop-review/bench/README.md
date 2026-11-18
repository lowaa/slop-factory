# Bench cases

`bench.sh` scores slop-review configurations against cases: changes from
your repo's history whose bugs are known. A case is
`cases/<name>/case.json`; see `internal/bench` for the schema. Two kinds:

- **Planted:** `patch` applies bugs on top of a real change.
- **Real misses:** no `patch`. `head` is the commit that introduced a bug
  only found after it merged, and `base` is its merge-base. `intent.md` is
  the introducing change's description, without hints. The bench doesn't
  read it; pass it per case as a variant flag, `--intent=<abs path>/intent.md`.

No cases ship with this repo: a case's `base` and `head` must be commits in
the repository being benchmarked.

    ./bench.sh --case=bench/cases/<name>/case.json --runs=2 --variant="single=--persona=standard"
