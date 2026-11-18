# Tenant-Aware Data Layer — Design Plan

A plan for a Go + PostgreSQL data layer with first-class multi-tenancy,
migration management, a database engine abstraction layer, and a test
harness with two distinct isolation strategies.

This is a **design document**, not a package. No code is implemented yet —
this defines scope, boundaries, and the contracts each piece must honor
before implementation starts.

---

## Goals / non-goals

**Goals**
- Tenant scoping is impossible to bypass by accident — enforced at the DB
  layer (RLS), not just convention.
- Migrations are the single source of schema truth, runnable up/down,
  identical across dev/CI/prod.
- A database engine abstraction sits underneath everything, so the rest of
  the codebase (repositories, tests) is not written directly against a
  specific driver — even though Postgres is the only engine implemented.
- Tests run against a real Postgres, with two distinct isolation
  strategies depending on what's under test.
- Tenant-scoping is the default assumption for tables, but a small,
  explicitly-marked class of **global tables** (reference/lookup data,
  platform config, the tenant registry itself) is supported without
  weakening the guarantee for everything else.
- All pieces compose: migrations create RLS-enabled (or explicitly global)
  tables → the engine layer executes queries → the tenancy layer scopes
  every tenant query → the test harness exercises the same path production
  code uses.

**Non-goals**
- Not building schema-per-tenant or database-per-tenant (a separate,
  heavier architecture).
- Not making tenancy a runtime on/off toggle. Whether a table is
  tenant-scoped or global is a fixed, schema-time decision made once when
  the table is created — never a config flag evaluated at request time.
- Not building a general-purpose multi-database engine layer. Only
  Postgres is implemented. The abstraction exists for isolation and
  testability, not portability to MySQL/SQLite/etc.
- Not building an ORM. This sits underneath `database/sql`/`pgx`,
  compatible with raw SQL or a thin query builder.
- Not solving cross-service tenancy — only this service's DB boundary.

---

## Package layout (proposed)

```
/engine       — database engine abstraction: connection/query interfaces, Postgres implementation
/tenancy      — tenant context, RLS session management, scoped executors (built on /engine)
/migrations   — golang-migrate wrapper + migration files
/dbtest       — test harness: container lifecycle, tx-rollback mode, commit-restore mode
```

Dependency direction is one-way:

```
migrations ─┐
            ├─▶ engine ─▶ tenancy ─▶ dbtest
(no dep)  ──┘
```

`engine` has no dependency on `tenancy` or `migrations`. `tenancy` depends
only on `engine`. `dbtest` depends on all three. `migrations` depends only
on `engine`'s connection type, not on `tenancy`.

---

## 1. Database engine abstraction layer

### Why this exists despite only supporting Postgres

Even with one real backend, the abstraction earns its place for three
reasons:

1. **Testability** — a fake/in-memory implementation of the engine
   interface can back fast unit tests that don't need a real database at
   all (distinct from the `dbtest` harness, which uses a real Postgres for
   integration-level tests).
2. **Isolation from driver churn** — `pgx` and `database/sql` have
   different idioms (native types, pooling, context handling). Pinning the
   rest of the codebase to an interface means a driver-level change (e.g.
   `pgx` v5 → v6, or a switch from `database/sql` to `pgx` native) touches
   one implementation file, not every repository.
3. **Explicit surface area** — defining the interface forces a decision
   about exactly which operations the rest of the system is allowed to
   use (no reaching for driver-specific escape hatches from business
   logic).

This is **not** an invitation to write engine-agnostic SQL. Postgres-specific
SQL (RLS syntax, `ON CONFLICT`, JSONB operators, etc.) is fine and expected
inside the Postgres implementation. The abstraction is about the Go-level
call surface, not about hiding SQL dialect differences.

### Interface shape (conceptual, not final)

Two layers of abstraction, mirroring `database/sql`'s own split:

- **`Engine`** — represents a connected database (pool). Responsible for
  acquiring connections/transactions, running migrations against itself,
  and health checks.
- **`Querier`** — the minimal set of methods needed to run queries:
  `ExecContext`, `QueryContext`, `QueryRowContext`, at minimum. Both a raw
  connection and a transaction satisfy this interface, so repository code
  can be written against `Querier` and not care whether it's inside a
  transaction or not.

```
Engine
  ├─ Connect(ctx) error
  ├─ Close() error
  ├─ BeginTx(ctx) (Tx, error)
  └─ Ping(ctx) error

Querier (satisfied by both a raw connection and a Tx)
  ├─ ExecContext(ctx, query, args...) (Result, error)
  ├─ QueryContext(ctx, query, args...) (Rows, error)
  └─ QueryRowContext(ctx, query, args...) Row

Tx (extends Querier)
  ├─ Commit() error
  └─ Rollback() error
```

### Postgres implementation

- Backed by `pgx` (v5), chosen over plain `database/sql` for native UUID
  support and connection-pool control, as noted in earlier discussion.
- The Postgres implementation is the only place that imports `pgx`
  directly — no other package should need to.
- Owns connection pool configuration (max conns, timeouts, health checks).

### Where `tenancy` sits relative to this layer

`TenantTx` (from the tenancy layer) **wraps** an `engine.Tx`, not a raw
`pgx.Tx`. The tenant-scoping constructor takes an `engine.Tx` obtained from
`Engine.BeginTx`, issues the `SET LOCAL app.current_tenant` call through the
`Querier` interface, and returns a `TenantTx` that also satisfies `Querier`.
This means repository code written against `Querier` doesn't need to know
whether it's talking to a tenant-scoped transaction or not — the type
system enforces scoping at the point where `TenantTx` is constructed, not
by inspecting behavior at every call site.

### Open decisions to make before implementation
- Exact method set on `Querier` — start minimal, add only what's proven
  necessary (avoid mirroring the entire `pgx` interface out of caution).
- Whether `Rows`/`Row` are wrapped types or re-exported driver types —
  wrapping adds boilerplate but keeps the driver fully swappable; re-export
  is pragmatic given only Postgres is planned.
- Whether a fake/in-memory `Engine` implementation is built now or deferred
  until a concrete test that needs it exists (recommend deferring — build
  it when the first real need appears, not speculatively).

---

## 2. Multi-tenancy as a first-class citizen

### Core principle

No code path — application, test, script, admin tool — touches a
tenant-scoped table without going through a tenant-context-carrying
transaction. Enforced by **type signatures** built on the engine
abstraction, not documentation: repository/query functions accept a
`TenantTx` (a `Querier`), never a raw `engine.Tx` or driver-native type.

### Components

**a. Tenant identity resolution (edges of the system)**
- HTTP middleware extracts tenant ID from JWT claim / subdomain / header —
  pick one canonical source per system, document it.
- Reject the request at the edge if no tenant can be resolved (fail
  closed, not open).
- Store resolved tenant ID on `context.Context` via an unexported key type.

**b. `TenantTx` — a wrapper type, not a raw transaction**
- Wraps an `engine.Tx`.
- Constructed only via a function taking `(ctx, engine.Engine, tenantID)`
  that begins a transaction and internally issues
  `SET LOCAL app.current_tenant = $1` before returning.
- Exposes only `Querier` methods — mirrors the underlying tx, but the type
  itself signals "this is scoped."
- No constructor exists that skips the `SET LOCAL` step.

**c. RLS as the enforced boundary, app-layer filters as the fast path**
- Every tenant-scoped table: `ENABLE ROW LEVEL SECURITY` +
  `FORCE ROW LEVEL SECURITY`.
- Policy: `USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`
  — the `true` second argument makes an unset session variable resolve to
  NULL rather than error, so an unset tenant context yields zero visible
  rows (fail-closed default).
- App code still writes `WHERE tenant_id = $1` for query planning/index
  use — RLS is the safety net, not the primary filter.

**d. Cross-tenant relational integrity**
- Composite foreign keys `(tenant_id, parent_id)` on child tables,
  backed by `UNIQUE (tenant_id, id)` on the parent.
- Decide early: enforced from day one, or retrofitted later (retrofitting
  onto existing data is materially harder).

**e. Non-HTTP access paths**
- Background jobs, cron, CLI tools, reporting scripts all go through the
  same `TenantTx` constructor — loop explicitly over tenants, one
  `TenantTx` per iteration, never a bypass.
- Decide now whether a "platform admin / no-tenant" path exists at all. If
  so, it should be a distinctly named type (e.g. `SuperuserTx`), ideally
  backed by a different DB role, never reachable from tenant-facing code.

**f. Global tables — the explicit, marked exception**

Tenant-scoping is the default for every table. A small number of tables are
genuinely global by nature — the tenant registry itself, static reference
data (currencies, timezones, plan catalogs), platform-level configuration —
and trying to force `tenant_id` onto them either means duplicating
identical rows per tenant or leaving the column nullable, which quietly
defeats RLS. These tables are supported as a first-class, explicitly
declared exception, not an ad hoc omission.

- **Naming convention marks the table as global.** Every global table name
  carries a fixed prefix, e.g. `global_<name>` (`global_currencies`,
  `global_timezones`, `global_tenants`). This makes the classification
  visible at every call site — in SQL, in migration filenames, in code
  review — without needing to cross-reference a separate registry file to
  know whether a table is tenant-scoped.
- **No RLS policy exists on global tables.** `ENABLE ROW LEVEL SECURITY` is
  never applied to a `global_*` table — enforced by a migration lint check
  (see below), not just convention.
- **`Querier`, not `TenantTx`, is the correct access type.** Code reading
  or writing a `global_*` table uses a plain `engine.Tx`/`Querier` obtained
  directly from `Engine`, not a tenant-scoped transaction. A repository
  function touching a global table should not require a tenant ID as an
  argument at all — needing one is itself a signal the table was
  misclassified.
- **Joins across the boundary are allowed, scoping is not inherited.**
  A query joining a tenant-scoped table with a `global_*` lookup table
  (e.g. an invoice joined to `global_currencies` for a display symbol) is
  expected and fine. RLS still applies to the tenant-scoped side of the
  join; the global table was never protected in the first place, so there
  is nothing to inherit. This should run through a `TenantTx` (since the
  query touches a tenant-scoped table), which is a normal, allowed pattern.
- **Migration-time enforcement, not runtime enforcement.** A migration
  linter step checks two invariants mechanically at CI time: (1) no
  migration applies `ENABLE ROW LEVEL SECURITY` to a table not prefixed
  `global_`, and (2) no migration creates a `global_`-prefixed table with a
  `tenant_id` column. Both directions of mistake — accidentally scoping a
  global table, or accidentally leaving a business table unscoped — are
  caught before merge rather than discovered later.
- **Referential integrity across the boundary.** Tenant-scoped tables may
  hold plain foreign keys into global tables (e.g. `orders.currency_code
  REFERENCES global_currencies.code`), unlike the composite
  `(tenant_id, id)` foreign keys used between two tenant-scoped tables —
  there's no tenant_id on the global side to compose with.

### Open decisions to make before implementation
- UUID vs integer tenant IDs (recommend UUID — avoids enumeration).
- Tenant ID resolution for websocket/long-lived connections, if any exist.
- Whether a platform-admin path exists, and if so, its isolation from
  tenant-facing code paths.
- Whether the `global_` prefix lint check runs as a standalone script over
  migration files, or as an assertion in the migration-verification tests
  under the test harness (Mode C) — either is workable, pick one rather
  than doing both.

---

## 3. Migration handling (up/down)

### Tooling
golang-migrate, used as both CLI (local dev, manual ops) and library
(programmatic use in the test harness and app startup), driven through the
`engine` layer's connection details rather than a separate ad hoc DSN
string scattered across the codebase.

### Layout
Each migration is its own folder, not a pair of flat files:

```
migrations/
  000001_init/
    init.up.sql
    init.down.sql
  000002_widgets/
    widgets.up.sql
    widgets.down.sql
```

The version prefix lives on the folder, once, rather than being repeated in
both file names inside it — and the folder leaves room to add more
per-migration content later (seed data, notes, a generated diagram) without
cluttering a single flat directory. golang-migrate's `iofs` source driver
has no notion of subdirectories, so `migrations.Up`/`Down`/`Version`
flatten this layout into the form it expects internally
(`migrations/foldered.go`) — callers don't need to know that detail.

Use `go-db create-migration -f <folder> -n <name> [--tenant-scoped]`
(`cmd/go-db`) to scaffold a new migration folder with the next sequential
version number worked out automatically, rather than creating one by hand.

### Conventions
- Every migration creating a tenant-scoped table includes, in the same
  file: table creation, `tenant_id NOT NULL`, indexes with `tenant_id`
  leading, and a single `SELECT go_db_apply_tenant_rls('<table>');` call —
  a helper function (defined once, in each project's first migration) that
  applies `ENABLE`/`FORCE ROW LEVEL SECURITY` and the `tenant_isolation`
  policy, so the three near-identical statements don't need to be
  hand-written per table. `LintGlobalTenantInvariants` treats this call as
  equivalent to writing the three statements out directly.
  Never split "create table" and "add RLS" across separate migrations.
- Every migration creating a **global** table names it with the `global_`
  prefix, contains no `tenant_id` column, and applies no RLS. This is the
  only signal needed to identify it as global — no separate registry file
  to keep in sync.
- Every `up` has a corresponding `down` that is actually tested (see
  harness below), not just written and assumed correct.
- Migration files are the only source of truth for schema. No parallel
  "desired schema" file, no ORM auto-migrate at runtime.
- CI runs a lint pass over migration files enforcing the two global/tenant
  invariants described above (no RLS on `global_*`, no `tenant_id` on
  `global_*`), catching misclassification before merge.

### Responsibilities of the `migrations` package
- Thin wrapper around golang-migrate exposing roughly:
  `Up(ctx, engine) error`, `Down(ctx, engine, steps int) error`,
  `Version(ctx, engine) (version int, dirty bool, error)`.
- Embeds migration files via `embed.FS` so the binary is self-contained.
- Startup path (app boot): auto-run `Up` in dev; explicitly decide the
  prod story — separate deploy step (recommended) vs. run-on-boot — rather
  than defaulting into one silently.

### Down-migration policy
- Every migration PR must include a working `down` file, and CI must prove
  it works (via the migration-verification tests under the test harness),
  not just that it exists syntactically.
- Decide the team's stance on inherently-lossy migrations (e.g. dropped
  columns). Likely answer: `down` recreates structure, doesn't promise
  data restoration, and this is documented per-migration.

---

## 4. Test running utilities

Two distinct modes, needed for two distinct test categories — kept
explicit rather than blurred, because they solve different problems. Both
modes are built on the `engine` abstraction and the `tenancy` layer's
`TenantTx`, not on driver-native types.

### Mode A — Transaction + rollback (fast, per-test isolation)

**Use case:** the majority of unit/integration tests — repository
functions, handler behavior — where speed and zero cross-test pollution
matter most.

**Mechanics**
- One Postgres container (via `testcontainers-go`) per test *binary run*,
  migrated up once via the `migrations` package at `TestMain` time.
- Per test function: open a new `engine.Tx`, wrap it as a `TenantTx` (so
  RLS is exercised, not bypassed, even in tests), run the test against it,
  always roll back — pass or fail, never commit.
- Helper shape: `dbtest.WithTenantTx(t *testing.T, tenantID string) *tenancy.TenantTx`
  — handles begin, `SET LOCAL`, and registers rollback via `t.Cleanup`.

**Why this must go through `TenantTx`, not a raw tx**
If tests bypass the tenant-scoping wrapper "for convenience," they stop
exercising RLS entirely, and a real production bug (missing tenant
context) would never be caught by the suite. Tests use the same scoping
path as production — this is a hard requirement, not an optimization.

### Mode B — Commit + restore to known state

**Use case:** tests that exercise transactional behavior itself (e.g. "does
this handler correctly roll back on error," "do these two writes commit
atomically") — these can't run inside an outer test-transaction, since the
code under test needs to actually commit or roll back its *own*
transaction. Also needed for anything asserting on side effects visible to
other connections, and for migration up/down verification.

**Mechanics** — in order of preference:
1. **Template database** — `CREATE DATABASE test_db TEMPLATE template_db`
   (fast, storage-layer copy). Migrate `template_db` once; clone per test
   suite/package.
2. **Truncate + reseed between tests** — `TRUNCATE ... RESTART IDENTITY CASCADE`
   across tenant tables; faster than full recreation, but must be kept in
   sync with schema changes (a forgotten table silently breaks isolation).
3. Full container recreation per test — correct but slow; reserve for a
   small number of tests.

- Helper shape: `dbtest.WithCommittedDB(t *testing.T) engine.Engine` — sets
  up a clean DB per test via template clone or truncate, registers cleanup
  via `t.Cleanup`, but does not wrap anything in an outer transaction.

### Mode C (implicit) — Migration up/down verification

Uses Mode B's machinery. For every migration pair: run `Up` to that
version, assert expected schema state (via `information_schema`
introspection or a golden schema dump), run `Down`, assert clean reversion,
run `Up` again (idempotency check). Never runs inside a wrapping
transaction, since golang-migrate manages its own transactions internally.

### Cross-cutting test harness responsibilities
- **Container lifecycle**: one `testcontainers-go` Postgres container per
  package (via `TestMain`), shared across both modes within that package.
- **Tenant fixtures**: shared helper for seeding N test tenants + baseline
  rows, usable by both modes.
- **Cross-tenant isolation tests** as their own named category — seed data
  for tenant A, assert tenant B's `TenantTx` returns zero rows, against
  real RLS policies (Mode A is sufficient here).
- **Failure mode coverage**: a test asserting that queries run without
  setting `app.current_tenant` at all return zero rows (proves fail-closed
  default), not just that correct tenant IDs return correct rows.
- **Global-table coverage** as its own named category, testing the inverse
  properties of the tenant tests above: (1) a `global_*` table has no RLS
  policy attached at all (queryable via `pg_policies` introspection), so
  it isn't accidentally scoped; (2) reading a `global_*` table via a plain
  `Querier`/`engine.Tx` — with no tenant context set — succeeds and
  returns rows, proving global tables are reachable without a tenant.
  These two together catch both directions of misclassification at the
  data level, complementing the migration-file lint check.

---

## How the four pieces connect

```
engine package
   ↓ (defines Engine/Querier/Tx — the only DB call surface; Postgres is the sole implementation)
migrations package
   ↓ (uses engine to create tables with RLS baked in)
tenancy package
   ↓ (wraps engine.Tx as TenantTx — the only way to query tenant tables)
dbtest package
   ↓ (Mode A: wraps TenantTx in rollback-only test transactions)
   ↓ (Mode B: provides real committed engines via template-clone/truncate
       for transaction-semantics + migration tests)
```

The thing to protect throughout: there is exactly one call surface
(`engine.Querier`) and exactly one way to get a tenant-scoped instance of it
(`TenantTx`), and production code, background jobs, and tests all use them.
The one deliberate exception — `global_`-prefixed tables, accessed via
plain `Querier` with no tenant context — is marked at the schema level by
naming convention and enforced by migration lint, not left as an implicit
gap. If a second, more-convenient path ever appears outside of that one
named exception — a raw `pgx.Tx` passed around, a tenant table quietly left
unscoped, a test that talks to the driver directly — the isolation
guarantees stop being tested for real, and stop being guaranteed in
production.

---

## Status

Implemented: `engine`, `tenancy`, `migrations`, `dbtest` — the shared
library — plus demo content built on top of it that is deliberately kept
out of the library packages: `internal/demo` (the schema itself:
global_tenants/global_currencies/accounts/account_items) and `cmd/demo-api`
(an HTTP server over that schema), `docker-compose-dev.yaml`, and a
dockerized Playwright `e2e_test` suite (API-testing mode, no browser —
demo-api has no UI). All packages have real-Postgres tests via
testcontainers-go (`go test ./...` or `./test.sh` for the dockerized
version), and the demo-api/e2e stack has been run end-to-end.

`migrations` is schema-agnostic by design: `Up`/`Down`/`Version` and
`LintGlobalTenantInvariants` all take the migration source as an `fs.FS`
parameter rather than embedding one migration set themselves, so the
generic wrapper has no dependency on the demo schema. The `migrations`
package's own tests exercise this against a small library-only fixture
(`migrations/testdata`, a global table + a tenant-scoped table), not
against the demo schema; `internal/demo`'s own tests cover the demo
migrations specifically (schema creation, RLS policies, and its own lint
pass). `tenancy`, `dbtest`, and `cmd/demo-api`'s tests/boot path all pass
`demo.Migrations` in explicitly, since something needs a concrete schema to
exercise end-to-end and re-deriving a second fixture for each would just be
duplication.

Resolved open decisions: UUID tenant IDs; `Row`/`Rows`/`Result` are pgx type
aliases declared in `engine/postgres.go` only; the fake/in-memory `Engine`
impl was deferred (no test has needed one); the global/tenant migration
lint runs as a plain `go test` over the embedded SQL (`migrations.
LintGlobalTenantInvariants`), needing no live DB; no platform-admin/
`SuperuserTx` path was built (nothing currently needs one).

One correction to this doc found during implementation:
`current_setting('app.current_tenant', true)` only returns `NULL` the
*first* time a session reads an unset custom GUC — pooled connections get
reused across many transactions/tenants, and once that GUC has been `SET
LOCAL` at least once and the transaction ends, it reverts to `''` (empty
string), not `NULL`. The RLS policies use
`NULLIF(current_setting(...), '')::uuid` to fold both cases to `NULL`, or
the fail-closed guarantee silently breaks after the first tenant
transaction on a given pooled connection. Also, RLS (even `FORCE`) is
unconditionally bypassed by superusers/`BYPASSRLS` roles — the Postgres
Docker image's default user is a superuser, so tests and `demo-api` connect
through a separate `NOSUPERUSER`/`NOBYPASSRLS` role (`migrations.
EnsureRole`) rather than through the role migrations themselves run as.

--

## Impl notes

- implement with tests
- implement with go.mod
- create a docker-compose-dev.yaml for spinning up a local instance with everything needed to manually verify a simple setup
- create playwright (use python) e2e tests in ./e2e_test -> use overlays for docker-compose-dev.yaml to override anything test specific
  - create ./e2e_test/pyproject.toml to manage dependencies and python version with uv
  - e2e_tests run dockerized, create a Dockerfile and Dockerfile.dockerignore (use whitelist style)
- create ./test.sh to invoke dockerized golang tests
- run local golang tests to verify the implementation as the coding progresses
- run e2e_tests to verify full e2e behaviour when necessary

