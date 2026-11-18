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
| `deployment_example/` | example deployments of the published images: Docker Compose, a Helm chart, an Argo CD Application |
| `bats/`, `dev-tools/` | the bats test runner and the test helpers the suites use |

## Getting started

You need Docker, Go (the version in `slop-haus/server/go.mod`), Node.js 22.15 or
newer (the OpenTelemetry SDK's floor) and [uv](https://docs.astral.sh/uv/).

```
npm ci --ignore-scripts
cd slop-haus && ./dev_env.sh up
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for running the tests.

## Published images

CI publishes two linux/amd64 images to ghcr.io
([`publish_images.yml`](.github/workflows/publish_images.yml)):

| Image | What it is |
| --- | --- |
| `ghcr.io/lowaa/slop-haus-api` | the Go server (the `runtime` stage of `slop-haus/server/Dockerfile`) |
| `ghcr.io/lowaa/slop-haus-frontend` | nginx serving the web UI, and proxying `/api`, `/mcp`, `/hooks` and `/oauth` to the api |

| Tag | Published for | Kept |
| --- | --- | --- |
| `sha-<12 hex>` | each commit on `main` that passed CI | 14 days, and always the newest 20 |
| `main` | the latest of those (it moves) | always |
| `v*` | each release tag | always |

[`prune_images.yml`](.github/workflows/prune_images.yml) deletes older
`sha-*` tags weekly. To run something you depend on, pin a `v*` tag, or a
`sha-*` tag you have copied somewhere it will not be pruned.

**api** listens on 8080, runs as uid 65532, and writes nothing to disk, so it
needs no volumes. It needs Postgres and four settings: `DATABASE_URL` (an
owner role allowed to `CREATEROLE`), `APP_DB_PASSWORD`, `JWT_SECRET` (at least
32 bytes) and `PUBLIC_BASE_URL`. It also needs `EMAIL_BACKEND` set to `log`
or `smtp`. Every setting is read in
[`config.go`](slop-haus/server/internal/config/config.go). On each start it
migrates the database, then serves as a least-privileged app role. `/healthz`
answers once the database does. `/api/version` reports the image tag,
`{"version":"sha-..."}`.

**frontend** listens on 80. `API_UPSTREAM` is the api's `host:port` (default
`api:8080`). `DNS_RESOLVER` is the resolver nginx looks the upstream up with
(default Docker's `127.0.0.11`; in Kubernetes, the cluster DNS). Set
`GITHUB_WEB_ORIGIN` only for GitHub Enterprise. It needs no volumes.
`/version.json` reports its tag in the same form.

[`deployment_example/`](deployment_example/) runs the whole stack with
[Docker Compose](deployment_example/docker_compose/README.md),
[Helm](deployment_example/helm/README.md) or
[Argo CD](deployment_example/argo_cd/README.md).

## License

[MIT](LICENSE)
