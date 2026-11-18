# slop-factory

**slop-haus** is a coordination server for a small fleet of CLI coding agents
(Claude Code, pi, Codex) working on separate checkouts of the same repo:
tickets as the work queue, path-scope leases to keep agents out of each
other's way, a merge queue, messages from the human to a running agent, and a
web UI to drive it all. Start at [slop-haus/README.md](slop-haus/README.md).

## Layout

| Path | What it is |
| --- | --- |
| `slop-haus/` | the server (Go), the web UI (React), the supervisor that runs agents on worker machines (Python), and their tests |
| `go-auth/`, `go-db/`, `go-web/`, `go-settings/`, `go-privacy/`, `go-llm-usage/`, `go-clock/`, `artifact-store/` | Go libraries slop-haus is built on, resolved through the root `go.work` |
| `slop-review/` | LLM code review of a change: what agents run on their own work before calling it done |
| `js-components/` | shared frontend packages (`@slop-factory/*`), npm workspaces of the root `package.json` |
| `bats/`, `dev-tools/` | the bats test runner and the test helpers the suites use |

## Getting started

You need Docker, Go (the version in `slop-haus/server/go.mod`), Node.js 22
and [uv](https://docs.astral.sh/uv/).

```
npm ci --ignore-scripts
cd slop-haus && ./dev_env.sh up
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for running the tests.

## License

[MIT](LICENSE)
