# go-llm-usage

Shared Go module (`github.com/lowaa/slop-factory/go-llm-usage`) for tracking LLM usage
-- cost, requests, tokens, and the kind of work (MCP, bash, file edits...)
-- across agents and apps.

Each app stores its own usage in its own database, under its own tenancy.
This module holds the types and the logic, never the database: it ships
no tables, no migrations and no SQL to copy, and needs no network access.
One Grafana view covers every app because they all export the metrics
built here, with the same names and labels.

## Packages

| Package | What it holds |
|---|---|
| `llmusage` (module root) | `Record` (the wire format) and `Validate`; `BillingMode`; `Categorize` for tool categories; `ValidCommand`/`CommandLabel` for bash commands; `Delta`; the Prometheus `Collector`; the ingest `Service` and the `Store` interface it uses |
| `pricing` | The generated price table, model aliases and `Estimate` |
| `llmusagetest` | `StoreSuite`, the tests every app's `Store` must pass, and `MemStore` |
| `cmd/genprices` | Regenerates `pricing/prices_gen.go` |

`Record.Harness` says what ran the model: `claude`, `pi`, `slop-review`, or
`app` -- an app's own server calling a model for one of its features,
which maps no tool names, so its tools are all
`other`.

## Cost: metered vs subscription

Money is integer **micro-dollars** everywhere. A `metered` record's cost is
money spent (OpenRouter). A `subscription` record's cost -- Claude Code on
a Max plan, a ChatGPT-subscription provider -- is an **API-equivalent
estimate**: comparable between pieces of work, not money spent. Dashboards
must never add the two together; every cost series carries `billing_mode`.

## Ingesting a report

Clients report **full totals** for a transcript (or run), which overwrite
earlier reports for the same key -- a hook can fire more than once. The
`Service` turns that into counter increments exactly once:

```go
prices := pricing.Default()
collector, err := llmusage.NewCollector(&llmusage.CollectorOptions{Models: prices})
registry.MustRegister(collector) // the app's own registry
svc, err := llmusage.NewService(&llmusage.Options{
	Store:     store, // the app's own, over its own table
	Collector: collector,
	Pricer:    prices,
	Logger:    logger,
})

pending, err := svc.Ingest(ctx, key, records) // Validate, Categorize, Estimate, Store.Upsert, Delta
if err != nil { ... }
// ...commit the transaction Upsert ran in, if the app's is wider...
svc.Commit(pending) // only now do the counters move
```

`Commit` comes after the commit so a report that rolls back is never
counted, and a retry of it isn't counted twice. If `Upsert` commits on its
own, call `Commit` straight after `Ingest`. Totals that shrank (a
transcript cut short) are logged and add nothing: counters only go up.

An app that already knows whether a report is new (a server that
stores each run once) can skip the Service and call `Collector.Add` with the
record itself; anything else must pass `Delta(stored, new)`.

Known limits, by design: two reports for one key delivered out of order
(an older retry landing after a newer report) can overcount, since the
store keeps whichever arrived last; and a crash between the store's commit
and `Commit` loses that delta from the counters, which restart from zero
on any restart anyway. A later report for the key puts the stored totals
right in both cases.

## Implementing Store

`Store.Upsert(ctx, key, records)` replaces the key's records (and their
tools) and returns what was stored before, **atomically**: two concurrent
reports for one key must not both see the same previous totals. Run
`llmusagetest.StoreSuite` against the real store in the app's dockerized
Postgres tests.

Scaffold the app's migration with go-db's `create-migration`; the layout
below is an illustration, not a migration to copy. App-specific columns
(a ticket, an agent) belong in the app's table, not in this module.

```sql
CREATE TABLE llm_usage (
    tenant_id        uuid   NOT NULL,
    transcript_key   text   NOT NULL,
    provider         text   NOT NULL,
    model            text   NOT NULL,
    harness          text   NOT NULL,
    billing_mode     text   NOT NULL,
    requests         bigint NOT NULL,
    input_tokens     bigint NOT NULL,
    output_tokens    bigint NOT NULL,
    cache_read_tokens  bigint NOT NULL,
    cache_write_tokens bigint NOT NULL,
    reasoning_tokens   bigint NOT NULL,
    reported_cost_microdollars  bigint,
    estimated_cost_microdollars bigint,
    price_version    text   NOT NULL,
    unpriced         boolean NOT NULL,
    started_at       timestamptz NOT NULL,
    ended_at         timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, transcript_key, provider, model, harness, billing_mode)
);

CREATE TABLE llm_tool_usage (
    tenant_id      uuid   NOT NULL,
    transcript_key text   NOT NULL,
    provider       text   NOT NULL,
    model          text   NOT NULL,
    harness        text   NOT NULL,
    billing_mode   text   NOT NULL,
    category       text   NOT NULL,
    tool           text   NOT NULL,
    command        text   NOT NULL DEFAULT '',
    calls          bigint NOT NULL,
    errors         bigint NOT NULL,
    duration_ms    bigint NOT NULL,
    result_bytes   bigint NOT NULL,
    PRIMARY KEY (tenant_id, transcript_key, provider, model, harness, billing_mode, tool, command),
    FOREIGN KEY (tenant_id, transcript_key, provider, model, harness, billing_mode)
        REFERENCES llm_usage ON DELETE CASCADE
);
```

One way to make the upsert atomic, in one transaction: lock the key, read
what's there, replace it.

```sql
-- serialises reports for one key; a key with no rows yet has nothing to
-- lock, so take an advisory lock rather than FOR UPDATE
SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0)); -- tenant, key
SELECT ... FROM llm_usage      WHERE tenant_id = $1 AND transcript_key = $2;  -- previous
SELECT ... FROM llm_tool_usage WHERE tenant_id = $1 AND transcript_key = $2;
DELETE FROM llm_usage WHERE tenant_id = $1 AND transcript_key = $2;           -- cascades to tools
INSERT INTO llm_usage ...;
INSERT INTO llm_tool_usage ...;
```

## Tools and bash commands

Clients send **raw tool names**; the receiver categorises them with
`Categorize`, so the mapping lives here. Categories are `mcp:<server>`,
`bash`, `file_read`, `file_edit`, `search`, `web`, `subagent` and `other`.

A bash call's `command` is the program plus, for a few programs, a
subcommand (`git status`, `go test`). The client extracts it, so full
command lines -- which can hold secrets -- never leave the agent's machine.
`fixtures/bash_commands.json` is the shared test every client extractor
runs (Python, TypeScript) and this module's tests check; its `rules` say
how to extract. Metrics label commands through `CommandLabel`, which maps
anything outside its allowlist to `other`; the database keeps the real
value.

The label fields a client sends (`provider`, `model`, tool names) are checked the
same way: `fixtures/record_labels.json` lists values the server accepts and
refuses, and each client's patterns (the Claude hook, the supervisor, the pi
extension) and this module's validation run it.

## Metrics

| Counter | Labels beyond `provider`, `model`, `harness`, `billing_mode` |
|---|---|
| `llm_requests_total` | |
| `llm_tokens_total` | `type`: `input`, `output`, `cache_read`, `cache_write`, `reasoning` |
| `llm_cost_microdollars_total` | `cost`: `reported`, `estimated` |
| `llm_unpriced_requests_total` | |
| `llm_tool_calls_total`, `llm_tool_errors_total`, `llm_tool_duration_seconds_total`, `llm_tool_result_bytes_total` | `tool_category`, `command` |

Token types are disjoint except `reasoning`, which is part of `output`:
never sum the two. The `app` label comes from the scrape job. Nothing is
labelled by ticket, run, session or agent id.

Reporters choose provider, model and MCP server names, and every new label
value is a series held until restart, so each is bounded: `provider` to
the providers the harnesses use, `model` to the models the price table
knows (so an unpriced model is `other`; the database has its name), and
MCP servers to the first `MaxMCPServerLabels` seen (`mcp:other` after that,
with `slop-haus` always admitted). Anything outside is `other`.

## Prices

`pricing/prices_gen.go` is generated from OpenRouter's
`GET /api/v1/models` and committed, so every price change is a reviewed
diff. To refresh it:

```sh
cd go-llm-usage && GOWORK=off go generate ./pricing
```

Its version is the generation date plus a hash of the prices; records store
it with their estimate, so history doesn't shift when prices change.
Models are found by OpenRouter id or by an explicit alias
(`pricing/aliases.go`: `claude-opus-5-5` -> `anthropic/claude-opus-5.5`). A
model that is neither gets no estimate -- never a guess -- and shows as
`unpriced` on the record and in `llm_unpriced_requests_total`; add an
alias for it.

## Tests

```sh
./test.sh   # go vet + go test
```

## Using it from an app

It's already in the root `go.work`. Add it to the app Dockerfile's
`go work init`, its build context and its whitelist `.dockerignore`.
