# slop-haus

A coordination server for a small fleet of CLI coding agents (Claude Code,
pi) working on separate checkouts of the same repo. Tickets as the work
queue, path-scope leases as the conflict-avoidance mechanism, messages from
the human to a running agent, and a web UI to drive it all.

The server never launches agents. A client-side supervisor on each worker
machine claims tickets and runs one headless session per ticket; consoles and
Remote Control hosts are run by the machine's supervisor, or by a person as the fallback. Either way the agent's MCP
tools call back into slop-haus to take work and hand it back.

## Package map

The lists below come from `go list` and the packages' doc comments.

<!-- package-map:start -->

**Server (`server/`, a Go module in the root go.work beside ../go-db and ../go-auth)**

- `server/cmd/add_user` -- creates an account that is active from the start, with a tenant of its own -- the default account development and the e2e suite sign in as, without an emailed link to follow first
- `server/cmd/api` -- boots slop-haus/server's HTTP API
- `server/cmd/generate_openapi_spec` -- writes slop-haus's real route registration (internal/apiserver) as an OpenAPI 3.1 JSON document, so it can never describe an API surface the server doesn't actually serve
- `server/cmd/seed_scale` -- fills an existing account's tenant with the scale seed (seeddata.SeedScale), for the browser render guard (e2e_playwright_ts/tests/lists_at_scale.spec.ts) and for looking at the lists at size by hand
- `server/cmd/slop-agent` -- drives slop-haus the way a real coding agent will: it connects to the MCP endpoint with an agent token, hands the tool definitions to a live model, and lets the model decide what to call
- `server/cmd/smoke_client` -- makes and revokes the read-only credential the deploy pipeline's smoke test signs in with (see internal/smoke)
- `server/cmd/upgrade_data` -- fills a slop-haus database with rows in every table, across several tenants, and later checks that they survived a migration
- `server/internal/agent` -- the durable identity a lease hangs off: a named agent, which clients are authorized to act as through OAuth (internal/oauth)
- `server/internal/agentmsg` -- how a person steers an agent that is working: what they say is held server-side and handed to the agent on its next call to slop-haus, whichever call that is
- `server/internal/apiserver` -- the single place that describes slop-haus's full API surface: the huma.Config it's served under and the list of operations mounted on it
- `server/internal/attemptsignals` -- computes, stores and serves what an attempt's change and run show about the work: cheating checks, size, CI outcome, own-test classes, cost, time and autonomy
- `server/internal/attemptsignals/diffcheck` -- derives cheating and size signals from a unified diff
- `server/internal/attemptsignals/owntests` -- classifies the test files an attempt added or changed by their results at the base and at the head, flagging tests that pass on the base
- `server/internal/attemptsignals/regressions` -- classifies an attempt's CI results into the signals shown per attempt: did checks pass, how many reruns it took, and whether the failures are regressions or failures in tests the attempt itself wrote
- `server/internal/attemptsignals/usage` -- measures what an attempt cost and how much hand-holding it needed, from records the server already keeps
- `server/internal/audit` -- slop-haus's set of security events, recorded by go-auth/audit
- `server/internal/audit/audittest` -- records slop-haus's audit events for tests to assert on
- `server/internal/authn` -- resolves who a request is from, once, at the edge: an agent, by its bearer token, or a signed-in user, by their session
- `server/internal/change` -- slop-haus acting on a ticket's change (pull request) on the code host for the agent that holds the ticket: opening it, reading its checks and merging it
- `go-clock` (shared module) -- the server's one injectable source of time, so time-dependent behaviour is tested by advancing a Fake rather than waiting
- `server/internal/codehost` -- what slop-haus needs from the place a ticket's change is reviewed and merged -- today GitHub, later others -- behind interfaces, so the ticket rules, the webhook door and the merge gate never name a host
- `server/internal/codehost/codehosttest` -- a second codehost.Host and codehost.Changes, for tests of the layers above the interfaces
- `server/internal/codehost/github` -- the GitHub implementation of codehost.Host
- `server/internal/codehost/githubapp` -- slop-haus's GitHub App: its manifest-flow setup, installation tokens and webhook secret
- `server/internal/conductor` -- the server's half of Remote Control, the owner's own Claude Code session on their machine driven from the browser: the conversations and their transcripts, the work queue a remote control's host long-polls, and the events the host reports back
- `server/internal/config` -- the single place slop-haus/server reads environment variables and CLI flags; every other package receives fully-parsed config rather than calling os.Getenv/flag itself
- `server/internal/contracts` -- loads slop-haus/contracts/*.json, the vocabulary Go, Python, TypeScript and bash share
- `server/internal/db` -- slop-haus's wrapper over go-db: every tenant-scoped query runs in a transaction scoped to the request's tenant, and the few that must run before a tenant is known go through an explicitly global handle
- `server/internal/db/dbtest` -- runs tests against a real Postgres: one container per test binary, migrated once into a template database, then cloned per test
- `server/internal/fleetinstructions` -- the tenant's fleet instructions: named texts an owner writes for the agents, kept as immutable versions, one of which is the default every workspace is given
- `server/internal/githubhook` -- slop-haus's fourth transport, after the board's REST, MCP and Claude Code's hooks: GitHub webhooks, so a person's review or merge on a ticket's PR moves the ticket as the same click on the board would
- `server/internal/harness` -- the registry of harnesses agents run in (Claude Code, pi, Codex)
- `server/internal/harnessconfig` -- the configuration a human hands out to the harnesses agents run in (Claude Code, pi, Codex): which model, how hard to think
- `server/internal/hookkit` -- what slop-haus's webhook doors share: githubhook and intake both take a request from a stranger on a listener of their own, read it with a size limit, record a delivery's outcome and turn away a redelivery of one already handled
- `server/internal/hookserver` -- answers Claude Code's hook callbacks, which is what makes an agent keep working without a human prodding it
- `server/internal/httperr` -- maps a package's sentinel errors onto HTTP answers from one table, so that a refusal is declared once and the table can be tested without going through HTTP
- `server/internal/intake` -- the door external systems -- Alertmanager, a support desk, anything with an event an agent should look into -- open, update and resolve tickets through: POST /hooks/intake/{source_id}
- `server/internal/jobs` -- runs the server's background jobs: each on its own interval, logged and measured the same way, and stopped in order on the way out
- `server/internal/landing` -- lands a finished ticket's change on the server's authority: it takes the head of the tenant's landing queue as the server agent, merges the base into the change, waits for CI and merges through the same gate as an agent's change_merge
- `server/internal/landing/guidance` -- works out what a ticket handed back from a failed landing is told: which landed changes it collided with, and whether the red check is its own bug
- `server/internal/leaseconfig` -- resolves the configuration a granted lease records
- `server/internal/llmusage` -- stores the LLM usage agents report for their transcripts, attributes each transcript to the ticket the agent was working, and counts it in the shared llm_* metrics
- `server/internal/logging` -- the api's wiring of go-privacy/otellog: text to stderr always, and -- when an OTLP endpoint is configured -- the same records exported to the collector that routes them by tier to VictoriaLogs
- `server/internal/manifest` -- a tenant's declaration of what its workspace looks like -- which repo is checked out at which path under the root -- and the rules that follow from it: whether an agent's reported layout matches, and what to propose when the tenant has declared nothing yet
- `server/internal/mcpserver` -- exposes slop-haus's coordination surface to agents as MCP tools
- `server/internal/metrics` -- slop-haus's Prometheus instrumentation: the counters alerting keys on, on a registry of the server's own, served by Handler on a listener that no door in front of the server routes to
- `server/internal/oauth` -- slop-haus's OAuth 2.1 authorization server, which is how agents are credentialed
- `server/internal/oauth/oauthtest` -- test wiring for the agent and OAuth services, which are built from each other
- `server/internal/onboarding` -- reports which setup steps a new tenant still needs before its workers can claim code work
- `server/internal/otelproxy` -- the browser's only path to the log store for the frontend's own error reports (js-components/logging's OtelLogger posts OTLP/JSON to /api/otel/v1/logs)
- `server/internal/pendingaction` -- holds the human-only actions a console agent proposes for a signed-in person to approve
- `server/internal/prune` -- keeps slop-haus's database from growing without bound
- `server/internal/reports` -- answers the Reports tab: lead time, time in each state and throughput, read from ticket_status_changes
- `server/internal/reqtrace` -- ties a request's server logs to the W3C traceparent the browser sent (js-components/logging/src/tracing), so a frontend error's trace id finds the server's logs for the same request
- `server/internal/reviewreport` -- the report server slop-review posts to: the runs a supervisor (or an agent) reports, filed under the attempt they review only when its agent holds it, their findings' labels, and the tenant's provider config
- `server/internal/schema` -- embeds slop-haus's own database migrations, so the module is self-contained -- go-db's migration helpers take the source as an fs.FS rather than embedding one themselves
- `server/internal/schema/seeddata` -- puts rows into every table of a slop-haus database, as the app role across several tenants, and checks that a migration kept them
- `server/internal/secretbox` -- seals secrets for the database under a master key, and keeps secrets out of logs
- `server/internal/secretscan` -- recognises text shaped like a credential, so the server can refuse to store it when an agent wrote it
- `server/internal/servermerge/runner` -- merges an approved ticket's change on the server's own authority, as the tenant's server agent, through the same gate an agent's change_merge passes
- `server/internal/session` -- tracks one `claude` run per agent: the liveness signal and the loop-protection counters the Stop hook decides on
- `server/internal/settings` -- holds the server-wide switches the human flips at runtime
- `server/internal/smoke` -- provisions what the deploy pipeline's smoke test signs in as: a read-only client for an agent in a tenant of its own, holding one ticket to read
- `server/internal/steward` -- decides which of the owner's routine approvals a steward agent may handle for them, and how
- `server/internal/steward/agreement` -- scores, per category, how often the owner decided as the steward would have
- `server/internal/steward/assist` -- the steward's level 2 (Assist) mode: it pre-drafts the decision on a pending approval and leaves it for the owner to apply with one click
- `server/internal/steward/discussionanswer` -- turns a discussion's label into what the owner sees and decides: the templated draft, and approve, edit or reject
- `server/internal/steward/discussionapply` -- lets the steward post a routine discussion label's templated answer itself at level 3 and above, following the category's effective tier (auto at once, veto after the veto window)
- `server/internal/steward/discussionlabel` -- classifies each discussion round, whatever route opened it, as routine (a template category covers it) or the owner's, and stores the label
- `server/internal/steward/discussionpick` -- the steward's discussion_pick category: a worker stopped with a question and recommended one of its options, and the steward answers by accepting exactly that option
- `server/internal/steward/ticketready` -- the steward's ticket_ready category: the steward has read a preparing ticket's brief and proposes opening it
- `server/internal/stewardwire` -- adapts the board's services to the steward's own narrow interfaces, so the steward package does not import them
- `server/internal/supervisorpkg` -- serves the supervisor: the Python package in slop-haus/supervisor that runs headless workers on a person's machine
- `server/internal/supervisorwatch` -- how a quiet supervisor is noticed: the Workers page shows it, and a collector exports the numbers the alert is raised on
- `server/internal/tenantstate` -- answers whether a tenant is archived, for the layers that must refuse work on one
- `server/internal/ticket` -- slop-haus's work queue: the tickets agents claim, the path scopes that keep two agents out of the same files, and the comments that keep decisions visible to everyone
- `server/internal/ticket/approvaltest` -- a ticket.ApprovalHook for tests
- `server/internal/ticketusage` -- prices a ticket's attempts from the LLM usage agents reported for them
- `server/internal/user` -- the human side of authentication: registration, activation, sign-in and password reset
- `server/internal/usersettings` -- holds per-user preferences: an append-only log of typed values
- `server/internal/variant` -- the tenant's variants: named configurations an agent can run on -- a harness, its settings and a fleet-instructions version -- kept as immutable versions
- `server/internal/wakeup` -- answers the supervisor's worker polls: it holds a poll open until the agent (or any of a supervisor's slots) has something to do
- `server/internal/wireschema` -- makes the OpenAPI document say exactly what the server sends, so the frontend can use the types generated from it as they are
- `server/internal/worker` -- the REST surface a headless worker's supervisor runs on: what an agent holds and what became of it, and a long poll for when that changes
- `server/internal/workspace` -- describes what a slop-haus agent workspace contains
- `server/privacy` -- holds slop-haus's data inventory, its consent purposes and its legal documents (go-privacy/README.md, sections 9 and 10)

**Supervisor (`supervisor/`, the Python package that runs headless workers)**

- `supervisor/src/slop_haus_supervisor/fleet` -- The slots and how they are stepped: one worker agent per directory, the scheduler and the policy tables they follow
- `supervisor/src/slop_haus_supervisor/harness` -- The headless harnesses (Claude Code, pi, Codex) a slot runs a ticket in, behind one interface
- `supervisor/src/slop_haus_supervisor/launcher` -- Starting a supervisor's own terminal session under a multiplexer
- `supervisor/src/slop_haus_supervisor/reconcile` -- Converging this host's slots on the board's desired state
- `supervisor/src/slop_haus_supervisor/remote_control` -- The Remote Control host: the owner's own session driven from the browser, with its server wire, repo copies and pi variant
- `supervisor/src/slop_haus_supervisor/service` -- Running the supervisor as a service: serve loop, instance lock, paused state, exit codes and log setup
- `supervisor/src/slop_haus_supervisor/setup` -- The guided `setup`: prerequisite checks, prompts and the host actions they lead to
- `supervisor/src/slop_haus_supervisor/slophaus` -- The supervisor's client of the slop-haus server: REST routes, device-flow login and credentials
- `supervisor/src/slop_haus_supervisor/steward` -- The steward runner: a read-only harness run that handles the owner's routine approvals
- `supervisor/src/slop_haus_supervisor/usage` -- Reading a harness's conversation for its token usage and billing mode, to report to the server
- `supervisor/src/slop_haus_supervisor/workspace` -- A slot's workspace on disk: layout, env files, repos, the briefing and the update script

<!-- package-map:end -->

Beside them:

```
  ../go-llm-usage                  shared LLM usage records: validation, tool categories, pricing, the llm_* metrics and the ingest service
  ../go-settings                   shared append-only typed settings: the value model, registry, Postgres store and conformance suite
  ../slop-review/report            slop-review's report contract: the run, its validation and the provider config the server serves
  ../go-auth                       shared auth: sessions, CSRF guard, default-deny markers, login providers, clientip, ratelimit, the audit recorder
  frontend/                        Vite + React + SCSS; the board, and a separate admin bundle
  harness_e2e/                     real harnesses against a scripted model
  e2e_playwright_ts/               dockerized Playwright, overlaying the dev compose file
  docker-compose-dev.yaml          the local dev stack
  dev_env.sh                       up/down for it
  agent_workspace.sh               a throwaway agent workspace against the local stack
```

Packages are grouped by feature, and in each one the `Service` holds every
rule while the transports stay thin callers of it. The ticket, attempt and landing
state machines are drawn in [server/STATE_MACHINES.md](server/STATE_MACHINES.md).

## Running it locally

```
./dev_env.sh up       # board on :5273, API on :8190, MailHog on :8126
```

`./dev_env.sh up` prints the seeded dev account's email and password (the
`seed` service in `docker-compose-dev.yaml`, an admin; registration is also
open in dev, `REGISTRATION_OPEN=true`). Activation, invite and reset links
land in MailHog. The frontend installs with `npm ci`; its lockfile is
load-bearing (see `frontend/Dockerfile`).

To get an agent into the fleet against it, sign in, then ask the board for
a setup script and run it in the workspace directory. `agent_workspace.sh`
does exactly this, playing the human's part of the device-flow approval
too:

```
./agent_workspace.sh up builder-1      # registers the agent, authorizes it over OAuth, writes its config
./agent_workspace.sh seed 2            # file some sample tickets
```

That workspace is for trying the tools and hooks by hand. Workers on a real fleet
are slots of a **supervisor** (`slop-haus setup`, then `slop-haus start`): a
client program that waits outside the model, claims a ticket, and runs one
headless harness session on it (Claude Code, `pi` or Codex), resuming it after
waits, crashes and outages. See [supervisor/README.md](supervisor/README.md).

The setup script writes the workspace's `.mcp.json` and
`.claude/settings.json` -- the MCP server and the hooks -- and
authorizes it over OAuth's device flow, so it leaves with its own
`.slop-haus/credentials.json` and no static token anywhere. Claude Code's
MCP connection reuses that same credential through `.mcp.json`'s
`headersHelper`, once the workspace trust prompt has been accepted. From
there the agent checks itself in; under a supervisor it is handed a ticket and
keeps going without being prodded.

## Testing

These suites each have their own `test.sh`:

| Suite | What it runs |
|---|---|
| `server/test.sh` | Go tests (their own Postgres via testcontainers), plus the bats and node suites for the scripts the server hands agents |
| `frontend/test.sh` | vitest, the Storybook browser tests, and `nginx.bats` against the real nginx configuration |
| `token/test.sh` | pyright and pytest for `slop_haus_token.py`, on the default Python and on the 3.9 floor |
| `e2e_playwright_ts/test.sh` | Playwright against the whole dev stack |

The Docker-driven ones have two modes. By default (`ATTACH=0`) they are
**fully containerized**: the source is baked into the images and nothing is
mounted from the checkout, so they run wherever a Docker daemon is --
including a CI job whose checkout the Docker daemon can't see to mount. `ATTACH=1` is for iterating: it mounts the source over the baked
copy and drops you into a shell in the container (`go test ./...`,
`npm test`, `npx playwright test`), so edits made outside it are picked up
without a rebuild.

CI (`.github/workflows/`) runs these suites on every pull request; see
[CONTRIBUTING.md](../CONTRIBUTING.md) for which ones and how.

After changing a route or a wire body, run `frontend/generate_api_client.sh`
and commit the result; CI fails while the committed spec and generated types
are stale.
