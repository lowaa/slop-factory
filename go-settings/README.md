# go-settings

Shared Go module (`github.com/lowaa/slop-factory/go-settings`) for typed settings kept as
an append-only log in Postgres: every change is a new row, the newest row per
name wins, and the rows record when, and for tenant-wide settings by whom,
a setting changed. slop-haus builds its settings on it.

Use it when an app needs named settings of a few simple types (string, int,
bool, float) with defaults, change history, and the isolation rules of
[go-db](../go-db/README.md)'s tenancy. Do not use it for a table of columns
read together, or settings that must be edited in place without history.

## Packages

| Package | What it holds |
|---|---|
| `settings` (module root) | No database. `Value`/`Type`; the `Registry` of known settings, their defaults and a per-setting `Validate` hook ("must be a valid IANA zone"); `Stored`/`Change`; the `Store` interface |
| `pgstore` | On go-db's engine: a `Table` for one explicit table and scope, with the typed scanning and inserting and the latest-per-name and history queries; a `Store` over it |
| `settingstest` | `Run`, the conformance suite every app's tables must pass, run against the app's own tables |

## The three scopes

| Scope | Constructor | Owner | Isolation |
|---|---|---|---|
| global user | `pgstore.GlobalUserTable` | a user, in every tenant | none in the database: the user id is the only guard |
| user within a tenant | `pgstore.UserTenantTable` | a user, in one tenant | tenant row-level security, plus a restrictive per-user policy; optional membership rule |
| tenant | `pgstore.TenantTable` | a whole tenant | tenant row-level security; each row records who changed it |

They are separate tables rather than one table with nullable owners: each
scope has its own owner, deletion rule (user rows go with the account, a
tenant's `changed_by` is set to NULL) and access rule, and the database
enforces each of them. In one table those rules would turn conditional, and
a missed condition would leak between scopes. A `Table` is built for one
table name, checked as a plain identifier, and its queries only ever name
that table.

[TABLES.md](TABLES.md) holds the SQL of each table and what it needs from the
app's schema. The app's own migrations create them; a test fails if the
documented SQL drifts from the copies the module's tests run on.

Like the other `go-*` modules it is consumed through a `go.work`; its
`go.mod` lists only third-party requirements, and go-db resolves through the
workspace.

## What stays in the app

Setting names and defaults, permissions, HTTP handlers, and how people who
have gone are shown.

## Tests

`./test.sh` runs them in a container, against real Postgres started by
go-db's `dbtest` harness, so a Docker daemon is needed. To run an app's
tables through the conformance suite:

```go
settingstest.Run(t, settingstest.Fixture{
    Global: globalTable, UserTenant: userTenantTable, Tenant: tenantTable,
    Open: openEnv, // two tenants, three members, a stranger, and an As that scopes a request
})
```

`pgstore/harness_test.go` is a worked `Open`.
