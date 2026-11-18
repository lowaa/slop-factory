# go-privacy — privacy model and package design

A decision document for the GDPR/CCPA compliance machinery shared by
apps such as slop-haus: consent, telemetry tiers, versioned legal documents,
data subject requests and the data inventory. It covers a Go module,
`go-privacy`, and a JS workspace package, `@slop-factory/privacy`.

This is a **design document**. The `consent` package (see
[§14](#14-the-consent-package-as-built)) and `legaldocs` (see
[§16](#16-the-legaldocs-package-as-built)) are built; the other packages are not yet. It
records what was decided with the human, makes the choices that were left
open, and sets the contracts the tickets that follow it build to.

> **Nothing in this document is legal advice.** It describes software
> machinery: places where legally reviewed text and decisions go. Articles
> and statutes are cited to explain why a mechanism exists, not as a
> statement of what the law requires of this project. Every legal text,
> legal basis, retention period and threshold below is a placeholder until a
> qualified person has reviewed it.

**Contents**

1. [Decided with the human](#1-decided-with-the-human)
2. [Package layout and boundaries](#2-package-layout-and-boundaries)
3. [How each app embeds the packages](#3-how-each-app-embeds-the-packages)
4. [The purpose registry](#4-the-purpose-registry)
5. [Consent storage](#5-consent-storage)
6. [When a re-prompt fires](#6-when-a-re-prompt-fires)
7. [Global Privacy Control](#7-global-privacy-control)
8. [Enforcement points](#8-enforcement-points)
9. [The data inventory format](#9-the-data-inventory-format)
10. [Legal documents and placeholder conventions](#10-legal-documents-and-placeholder-conventions)
11. [Data subject requests](#11-data-subject-requests)
12. [Adding a vendor: checklist](#12-adding-a-vendor-checklist)
13. [Choices confirmed by the human](#13-choices-confirmed-by-the-human)
14. [The consent package, as built](#14-the-consent-package-as-built)
15. [The retention package, as built](#15-the-retention-package-as-built)
16. [The legaldocs package, as built](#16-the-legaldocs-package-as-built)
17. [The dsr package, as built](#17-the-dsr-package-as-built)

---

## 1. Decided with the human

These were settled before this document was written. They are recorded
here as given, and the rest of the design builds on them.

### Opt-in everywhere

One worldwide model, with no geo-detection. Analytics, interaction
recording and marketing are off until the user consents. This meets the
EU's cookie rules and GDPR, and also covers CCPA/CPRA's opt-out regime
(off-by-default is stricter than opt-out).

### Telemetry tiers

| Tier | On when | What it is |
|---|---|---|
| **Operational** | Always; no toggle | Server logs and traces, server error traces, **frontend crash and error reports**, and failed-request logs |
| **Aggregate feature usage** | Always | Server-side counters per operation per day, with **no user or tenant id** |
| **Analytics** | With `analytics` consent | Named UI events from an agreed list, and time on task (Page Visibility) |
| **Interaction recording** | With `interaction_recording` consent **and** its capability flag | Cursor movement, idle time and similar |
| **Marketing** | With `marketing` consent | No scripts yet. The script gate, CCPA "Do Not Sell or Share" and GPC are built in |

The operational tier is legitimate only under these conditions:

- errors only, with no breadcrumb or event stream;
- no persistent visitor id: the random per-session id (§8) is fine, and a
  signed-in user's id is acceptable;
- attributes limited by an allowlist: route template, error type, message
  and stack, build tag and user agent. **Never** form values, names,
  results or filenames;
- short retention;
- disclosure in the privacy policy.

Aggregate feature-usage counts are disclosed in the privacy policy. They
are collected on the server, so nothing is read from or stored on the
device.

Interaction recording is its own consent purpose, separate from analytics,
because consent must be specific. A server-side capability flag, per
environment and optionally per tenant, also gates it. When the flag is off,
its code isn't loaded and its consent option isn't shown. The human wants
it on during the current invite-only testing phase.

### Necessary storage

Session and refresh cookies, CSRF, OIDC login state, the step-up
`sessionStorage` key and the consent cookie itself are **disclosed, not
consented to**.

### Adopting it

An app adopts the framework as a whole. slop-haus is the first consumer and
sets only necessary storage today.

---

## 2. Package layout and boundaries

### Go: `go-privacy` (module `github.com/lowaa/slop-factory/go-privacy`)

A sibling of `go-auth` and `go-db`, consumed through `go.work` like them.

| Package | Holds | Depends on |
|---|---|---|
| `consent` | The purpose registry type, consent events and effective state, the GPC rule, the anonymous consent cookie, middleware that puts the effective consent on the request context, operation markers for processing purposes, huma routes, and a Postgres `Store` | `legaldocs` (document versions), `go-db/engine`, `go-auth/auth` (user id on the context, `DeriveKey`), huma |
| `consent/consenttest` | `StoreSuite`, the tests every app's consent tables must pass, and an in-memory store | `consent` |
| `legaldocs` | Versioned documents: the loader, front matter, include directives, the published-version snapshot, huma routes | `go-db/engine`, huma |
| `dsr` | The data subject request log: records, due dates, status history | `go-db/engine` |
| `inventory` | The data inventory schema, loader, validator, CI checks, and the generator for legal-document fragments | none outside the standard library and a YAML parser |
| `telemetry` | The policy the otel proxy applies to a browser's OTLP/JSON: rebuilds logs from the records and attributes each tier permits (operational attributes, consented attribute tiers, consented event catalogs), and failed-request spans. No transport, no consent dependency: an app hands it a `Granted` function per tier | standard library |
| `otelproxy` | The proxy's handler shell: per-client rate limit, body limit, forward cap, protobuf or JSON forwarding, huma routes. The app supplies the policies, `Identify` and its authentication marker | `telemetry`, `go-auth/clientip`, `go-auth/ratelimit`, huma |
| `logpolicy` | An `slog.Handler` wrapper that drops, hashes or truncates attributes by per-app rules | standard library |
| `otellog` | A service's log handler: a local copy and an OTLP export, both through `logpolicy`, the exported one tagged with the operational tier | `logpolicy`, the OTel log SDK |
| `retention` | "Delete rows older than N, in bounded batches, per tenant, as the app role" | `go-db` |

The ticket proposed `consent`, `legaldocs` and `dsr`. The other four are
added here because the tickets that wait on this one already put that code
in go-privacy (the inventory, log policy and retention work), or need a
shared home for it (the telemetry filter's proxy wiring). Better
that this document names them than that each ticket invents its own.

**Why one module, and why these boundaries:**

- **A separate module, not part of go-auth.** go-auth is authentication: it
  answers *who is this*. Consent answers *what may we do*, applies to
  anonymous visitors who have no identity, and changes for legal reasons
  rather than security ones. Other go-auth consumers should
  not take on consent tables. go-privacy depends on go-auth (the user on the
  context) and go-db; neither depends on go-privacy.
- **One module with several packages, not a module per package.** All of
  them change together when the privacy model changes, are consumed by the
  same two apps, and share one CI pipeline. Separate modules would multiply
  `go.work`, `Dockerfile.dockerignore` and CI wiring for no
  versioning benefit, since nothing here is released separately.
- **`consent` depends on `legaldocs`, not the other way round.** A consent
  event cites the document version it was given under, and a version bump
  is what drives a re-prompt (§6). Legal documents exist without consent
  (terms, the privacy policy), so `legaldocs` stays free of it.
- **`dsr` is independent.** Self-service export and deletion
  live in the app, because what they touch is the app's schema. `dsr` only
  logs and tracks requests that need a human operator.
- **`inventory` has no runtime dependencies.** It is used by CI checks and
  generators, and by `legaldocs` only through the fragments it generates
  (§10). A server never loads the inventory at startup (§4 says why).
- **`telemetry` holds no OTLP transport.** The rules are a function of a
  payload and a config, so they are tested without HTTP. The HTTP plumbing
  that surrounds them is `otelproxy`, which each app's proxy is
  built on: each keeps only its own policy, its identification of the caller
  and the route's authentication marker.

**Following go-llm-usage's split, with one difference.** go-llm-usage
ships no tables and no SQL: each app implements its `Store`, because each
app adds its own columns. Consent tables have no app-specific columns, and
the subtle part of consent is in the store itself: append-only history, the
latest decision deriving the state, the merge rule, and concurrent
decisions not losing an event. Re-implementing that per app is how proof of
consent gets subtly wrong in one of them. So go-privacy ships the Postgres
`Store` (its SQL), and each app owns the DDL in its own migrations (§3).

### JS: `@slop-factory/privacy` (`js-components/privacy`)

One workspace package with two subpath exports, following
`@slop-factory/logging`'s `exports` map:

| Export | Holds |
|---|---|
| `@slop-factory/privacy/consent` | `ConsentProvider`, `useConsent(purpose)`, the banner, the preferences dialog, the script gate (`loadWhenConsented` / `ConsentScript`), GPC detection, the consent check `@slop-factory/logging` takes, and the processing-consent prompt used at sign-up and before a first extraction |
| `@slop-factory/privacy/legal_docs` | The document page (with the DRAFT banner and version history), and footer links for an app's legal pages |

**Boundaries:**

- **It depends on `@slop-factory/ui`, and on nothing else in
  js-components.** `@slop-factory/logging` takes an injected consent check
  so logging doesn't depend on privacy and privacy doesn't depend on
  logging. The app wires the two together.
- **`@slop-factory/auth` doesn't depend on it either.** A processing-purpose
  consent at sign-up is a component from `privacy/consent` that the
  app slots into the auth pages through `AuthPagesConfig`'s extension points. If
  those points aren't enough, that work adds a slot to `@slop-factory/auth`; it
  doesn't import privacy code into auth.
- **The browser never decides.** The provider shows what the server's
  consent route reports, and sends decisions back to it. The purposes, the
  effective state, GPC's effect and which purposes need a decision are all
  computed on the server (§5). The client mirrors GPC only to explain it in
  the UI (§7).

---

## 3. How each app embeds the packages

### Database: the app's own migrations, through go-db

Both apps keep one migration source each
(`<app>/server/internal/schema/migrations`, e.g.
`slop-haus/server/internal/schema/migrations`), run by
`go-db/migrations.Up` with sequential, gapless numbers, and linted by
`migrations.LintGlobalTenantInvariants`. go-privacy does **not** ship a
second migration source.

- **Recommended:** go-privacy keeps its canonical DDL in each package's own
  test schema (used by its own `dbtest` tests). The app adds the same tables
  in an ordinary numbered migration of its own, scaffolded with go-db's
  `create-migration`. The app's server tests run `consenttest.StoreSuite`
  (and the `legaldocs` and `dsr` equivalents) against the app's migrated
  database, so drift between the app's DDL and go-privacy's SQL fails the
  app's build.
- **Rejected: go-privacy ships its own migrations as a second goose
  source.** `go-db/migrations` takes one `fs.FS` per database and one
  version table. Two sources would need a go-db change for a second version
  table, and they'd interleave two schema timelines that `Down` and the lint
  would have to reason about. It would save one copied file per app, once.

### Global, not tenant, tables

All go-privacy tables are **global** (named `global_…`, no `tenant_id`, no
RLS, per go-db's lint rules):

| Table (proposed) | Why global |
|---|---|
| `global_consent_events` | Consent belongs to a person, not to a tenant. A user who belongs to two tenants gave one consent, and `global_users` is already global. Anonymous visitors have no tenant at all. |
| `global_legal_document_versions` | The published text of each document version is the same for every tenant. |
| `global_consent_merge_tokens` | Records every anonymous-cookie `merge_token` that has been presented (§5), so a cookie is merged once across all accounts. |
| `global_privacy_requests`, `global_privacy_request_events` | A request can come from someone with no account, and is handled by the platform operator, not a tenant. |

`global_consent_events.user_id` references `global_users` **`ON DELETE
RESTRICT`**. Deleting a user whose consent history hasn't been dealt with
then fails loudly, rather than silently cascading away proof of consent.
Account deletion decides explicitly whether that history is deleted
or pseudonymised (the decision it already has to make), and does it before
deleting the user row.

### HTTP: huma routes the app mounts

Each package exposes `RegisterRoutes(api huma.API, h *Handler, ops
*Operations)`. `Operations` carries the app's authn markers, so every route
goes through the app's own policy (`authn.Public()` / `authn.RequireUser()`
in each app's `internal/authn`). That keeps each app's
`DefaultDeny` and public-surface test in charge.

| Method and path | Marker | Notes |
|---|---|---|
| `GET /api/privacy/consent` | optional user (below) | Purposes on offer, the effective state, which purposes need a decision, and whether GPC was honoured |
| `PUT /api/privacy/consent` | optional user | Decisions. Signed in: appended to the record. Anonymous: written to the consent cookie |
| `GET /api/privacy/documents/{kind}` | public | The current version |
| `GET /api/privacy/documents/{kind}/versions` | public | The version history |
| `GET /api/privacy/documents/{kind}/versions/{version}` | public | One exact version |
| `POST /api/privacy/requests` | public, rate-limited | The public DSR entry point, if the `dsr` work chooses a form |

**Optional user, a go-auth addition.** go-auth's `UserMiddleware` resolves a
user only for operations marked `RequireUser` (or the app's own user
markers). A `Public` operation never sees one, even when a valid session
cookie came with the request. The consent routes and the otel proxy both
need "the user if there is one, never refuse". So go-auth/authn gains an
`OptionalUser` marker: resolve the session cookie or bearer token if
present and valid, otherwise carry on anonymous, and run `BrowserGuard`'s
checks on writes exactly as now (the CSRF token when a session cookie
authenticated it, the origin check otherwise). The rejected alternative was
for the consent middleware to verify the session cookie itself, with the
app's `UserVerifier`. That would duplicate the edge's credential and CSRF
logic in a second place. A credential that is present but refused
(expired, revoked, forged) does **not** simply mean anonymous: session refresh
is driven by the 401 a `RequireUser` route answers, and this marker never
answers 401, so a signed-in user with an expired access cookie would look like
a visitor. The request carries on anonymous but is flagged: the response has
`X-Auth-Credential: rejected` and `authn.CredentialRejected(ctx)` is true. The
consent PUT must not file a decision in the anonymous cookie for such a
request, and the browser session client refreshes and retries on the header.
**The rule: an `OptionalUser` write never runs as anonymous on a rejected
credential.** If it did, the decision would be filed against the anonymous
cookie, and again as the user on the client's retry: one choice recorded under
two identities, which is wrong for proof of consent. go-auth's
`authn.RefuseRejectedCredential(api)` is an operation middleware that answers
such a request 403 with code `credential_rejected` (the header still set; a
403 not a 401, as for `reauth_required`, and one fixed body so it doesn't say
whether the token was expired or forged). `RegisterRoutes` puts it on the PUT
itself, so an app can't mount the consent write without it; a test fails if a
consent write route lacks it. Reads are unchanged: a GET with a rejected
credential still answers as anonymous. Any other `OptionalUser` write whose
effect depends on who is asking installs the same middleware beside its marker.
Writes with no verified user get the origin check, as a `Public` write does;
only a verified bearer token is exempt. This marker is its own ticket
(`go-auth/authn`, blocked on another ticket). The consent package needs no
`go-auth` scope. The apps' consent routes, the telemetry filter's proxy
wiring and the otel proxy wiring wait on it. See §13.

**Browser users only.** The consent middleware and banner apply to requests
authenticated by the session cookie, or carrying none. Requests with a
bearer token (slop-haus's MCP and bot clients, any script go-auth's bearer
path authenticates) are never
shown a banner, never refused for lack of a cookie-category consent, and
never have a GPC event recorded. Processing purposes still apply to them:
an app with a processing purpose refuses the routes it governs to a user
without a current grant whatever the credential (§8). slop-haus has no
processing purposes. Note
that the *consent* page in slop-haus's OAuth authorize flow (the `oauth`
package) is about authorising a client. It is unrelated to
`go-privacy/consent`, and the UI copy should keep the two apart.

### Wiring and CI

- Root `go.work` adds `./go-privacy`. Both server `Dockerfile.dockerignore` whitelists add
  `!go-privacy`.
- `go-privacy/test.sh` follows `go-db/test.sh` (dockerized, with
  testcontainers Postgres for the store tests), not go-auth's plain
  `go test`, because the stores need a real database. A
  CI job for it follows go-auth's triggers.
- `package.json` workspaces adds `js-components/privacy`. Each frontend's
  `Dockerfile.dockerignore` whitelists its `package.json` and `src` the way
  it already does for `js-components/logging`. `js-components/native_test.sh`
  covers it.
- Each package takes an `slog.Handler` in its constructor, per the repo's
  convention, and wraps its third-party libraries (the YAML parser, the
  Markdown renderer) behind its own types.

---

## 4. The purpose registry

**One registry** covers cookie and storage categories **and** processing
consents. A purpose is the unit that is consented to, recorded, re-prompted
and enforced, whether what it governs is a cookie or a server-side
operation.

**The app declares its purposes in Go, at startup.** Nothing is hard-coded
in go-privacy.

```go
registry, err := consent.NewRegistry(
	consent.Purpose{ID: "necessary", Kind: consent.CookieCategory, Necessary: true,
		Document: legaldocs.CookiePolicy},
	consent.Purpose{ID: "analytics", Kind: consent.CookieCategory,
		Document: legaldocs.CookiePolicy},
	consent.Purpose{ID: "interaction_recording", Kind: consent.CookieCategory,
		Document: legaldocs.CookiePolicy, Capability: "interaction_recording"},
	consent.Purpose{ID: "marketing", Kind: consent.CookieCategory,
		Document: legaldocs.CookiePolicy, SaleOrShare: true},
	consent.Purpose{ID: "health_data_processing", Kind: consent.Processing,
		Document: legaldocs.HealthDataNotice},
	consent.Purpose{ID: "llm_document_processing", Kind: consent.Processing,
		Document: legaldocs.PrivacyPolicy},
)
```

(Illustrative, not a final API. These are example purposes. slop-haus
declares `necessary`, `analytics` and `marketing`: see
`slop-haus/server/privacy/purposes.go`.)

Storage a purpose governs is declared in the inventory's `storage` registry
(§9) under that purpose's id. For example the random per-session analytics
id (§8) is a `sessionStorage` key declared under `analytics`.

| Field | Meaning |
|---|---|
| `ID` | Stable. It is stored in every event, sent as the telemetry tier (§8), and named in the inventory. Never renamed |
| `Kind` | `cookie_category`: shown in the banner and preferences, and applies to anonymous visitors too. `processing`: shown in its own flow (sign-up, before a first extraction) and in Settings for withdrawal. Only for signed-in users, never stored in the anonymous cookie |
| `Necessary` | Always granted, never toggled, never recorded. Shown in preferences as "always on" |
| `Document` | The `legaldocs` kind it is given under. Its latest *material* version is what a decision must cite to be current (§6) |
| `Capability` | Optional. The name of a capability flag. While the flag is off, the purpose is absent from the routes, can't be granted, and counts as denied |
| `SaleOrShare` | The purpose involves a CCPA "sale" or "sharing". GPC and "Do Not Sell or Share" deny every such purpose (§7) |

Each purpose's display name and description (which are legal copy) come
from the app's legal documents, keyed by purpose id, and carry the same
DRAFT marking (§10).

**Capability flags** are an interface the app implements:
`Enabled(ctx, capability) bool`. They start as env config. The context carries the tenant for a signed-in request,
which is how a per-tenant override can be added later. An anonymous request
gets the environment's value. The frontend learns which purposes exist from
the consent route, not from `staticconfig`, so one source decides both
"offered" and "allowed".

**Why the registry is in Go and not loaded from the inventory.** The
registry is runtime behaviour (capability gates, enforcement), and a bad
entry should fail to compile, not fail at startup in production. The
inventory (§9) also names purposes, so an `inventory` check asserts that the
app's registry and inventory list the same purpose ids. That makes them one
source of truth in effect, with a typed side and a documentation side. The
rejected alternative was loading the registry from the inventory YAML at
startup. That would put the server's startup at the mercy of a documentation
file, and couple a hot path to the generator's schema.

---

## 5. Consent storage

### Signed-in users: an append-only server record

`global_consent_events`, one row per decision:

| Column | Content |
|---|---|
| `id` | `bigserial`; the tie-breaker for ordering |
| `user_id` | `global_users`, `ON DELETE RESTRICT` (§3) |
| `purpose` | The registry id |
| `granted` | true or false |
| `document_kind`, `document_version` | What the user was shown; references `global_legal_document_versions` |
| `source` | `banner`, `settings`, `sign_up`, `prompt` (a processing-consent prompt such as before a first extraction), `gpc`, `migration` |
| `decided_at` | When the decision was made: server time, including for merged anonymous decisions (below) |
| `recorded_at` | When the row was written; differs from `decided_at` only for merged rows |
| `merged_from_anonymous` | true for a decision first made while signed out |
| `merge_token` | For merged rows, the anonymous cookie's token; unique per purpose across all users (below) |
| `session_id` | go-auth's `User.SessionID`, which ties the decision to one interactive sign-in |

This is proof of consent under GDPR Art. 7(1): who, what they were shown,
what they chose, when, and through which surface. Rows are never updated or
deleted while the account exists. A trigger refuses `UPDATE` and `DELETE`
on the table, so append-only is enforced by the database, not by
convention. The only exception is the erasure path the account-deletion work defines (a
`SECURITY DEFINER` function, say). Revoking the grants isn't enough here:
go-db's `migrations.EnsureRole`, which an app's database setup can run at every
start, grants the app role `SELECT, INSERT, UPDATE, DELETE` on all tables
again. The current state per purpose is the row with the latest
`decided_at` (ties broken by `id`).

**Writes are serialized per user, and stale writes are refused.** Every
append for a user (a decision, a merge, a GPC denial) runs in one
transaction that first takes a row lock on the user (`SELECT … FROM
global_users WHERE id = $1 FOR UPDATE`), then checks, then inserts:

- `GET` returns, per purpose, the `id` of its current event. `PUT` sends,
  for each purpose it decides, the id it was based on. If a purpose has a
  newer event than that, the `PUT` is refused with `409 consent_changed`,
  and the client reloads the state and shows it. Without this, a delayed
  request (from a second tab, say) would get a later server timestamp than
  the decision that superseded it, and bring the old choice back.
- Decisions on *different* purposes don't conflict, so two concurrent
  decisions both land and neither is lost.
- The GPC transition (§7) and the merge (below) check the latest event under
  the same lock, so neither writes a duplicate.

**Deliberately not stored:** the IP address and the full user agent. Proof
of consent needs who, what, when and how, not where from, and an IP
alongside a consent for sensitive data is more personal data to protect for no
benefit. If legal review asks for more, it is one column.

### Anonymous visitors: a first-party necessary cookie

- `<app>_consent` (slop-haus: `slop_haus_consent`), following the app's
  existing cookie naming (`slop_haus_session`). It is classed
  necessary: it stores the choice to *refuse* storage, so it can't depend
  on consent. It is listed in the app's inventory.
- **Set only when a visitor decides**, never on first visit. Until then
  nothing is stored, and the banner shows.
- **httpOnly, `SameSite=Lax`, `Secure` when served over https, `Path=/`.**
  It is written only by `PUT /api/privacy/consent`, never by script, and
  read by the server. `ConsentProvider` always asks `GET
  /api/privacy/consent`, signed in or not, so the client has one code path.
  The rejected alternative was a script-readable cookie the client parses,
  which would let any script on the page rewrite consent and would give the
  client a second source of truth.
- **Sealed:** a MAC keyed with `auth.DeriveKey(secret, "consent-cookie")`, in
  the same versioned shape as go-auth's login-state cookie. The server can
  then trust its timestamps, and the otel proxy can enforce consent for
  anonymous visitors too (§8).
- **Payload:** per cookie-category purpose, the choice, the document version
  and `decided_at`, plus a `merge_token` while it holds decisions that can
  be merged (below). The token is random, regenerated at each anonymous
  decision, and used for nothing but making a merge happen once. It is never
  logged or sent anywhere else. Processing purposes are never in the cookie.
- **Ordering.** A cookie is last-write-wins in the browser. If two `PUT`s
  are in flight at once, the older decision's `Set-Cookie` can land last,
  and the server can't stop that, because it keeps no anonymous state to
  compare against. So `ConsentProvider` serializes decisions **across tabs**
  with the Web Locks API (`navigator.locks.request("<app>-consent", …)`,
  which needs no storage). Each `PUT` runs while holding the lock and
  completes, `Set-Cookie` included, before the next tab's starts. One `PUT`
  carries every purpose the user just decided. A `BroadcastChannel` tells
  the other tabs to reload the state. A signed-in user's decisions are
  server rows, protected by the stale-write check above whatever the client
  does.
  The residual risk is accepted: a client that bypasses the lock (a
  non-browser client, an old browser) can only leave *its own* visitor's
  cookie in an older state (which could be a grant the visitor has since
  withdrawn), and the server keeps no anonymous state to compare against by
  design. Signed-in users are not exposed to it.
- **Lifetime: 6 months (placeholder).** After that the visitor is asked
  again. Regulator guidance on re-asking varies (often 6 to 13 months), so
  the value is config, to be confirmed in legal review.

### Merging at sign-in

When a signed-in request reaches `GET` or `PUT /api/privacy/consent` with a
consent cookie carrying a `merge_token`, the server:

1. first records the token in `global_consent_merge_tokens` (unique, across
   all users) in the same transaction, whether or not any decision ends up
   merged. A token that is already recorded is skipped entirely, so a
   cookie that survives a lost response, or whose decisions were all older
   than the record, still can't be merged into a later person's account;
2. appends each of the cookie's decisions whose `decided_at` is **newer**
   than the user's current decision for that purpose, with
   `merged_from_anonymous` set, the original `source`, and the token in a
   `merge_token` column. Older ones are dropped: the record wins when it is
   newer. A unique key on `(merge_token, purpose)`, **across all users**,
   means a cookie's decisions are merged into one account at most once; the
   token table above is what makes that hold when nothing was inserted.
   Two concurrent requests (two tabs, or a second person signing in on the
   same browser before the first response's cookie lands) can't both merge
   it: the loser's token insert conflicts and everything is skipped;
3. rewrites the cookie to the user's effective cookie-category state,
   without a `merge_token`.

Dropping the token is what protects a shared device. Choices the cookie
carries after a sign-in or sign-out were not made by whoever signs in next,
so they are never merged into that person's record. Only decisions made
while signed out since then are.

The merge runs in the consent handlers rather than in a global middleware,
so an ordinary `GET` elsewhere never writes. `ConsentProvider` calls the
consent route as the app loads, so in practice the merge happens straight
after sign-in, whichever sign-in path was used (password, OIDC, invite,
activation). Before the merge, enforcement uses the server record alone,
which fails closed.

### Effective consent

Per request, for each purpose:

`effective = necessary OR (current decision is a grant AND it cites a
document version **at or after** the latest material version AND its capability, if any, is on AND
NOT (purpose.SaleOrShare AND GPC))`

Signed in, "current decision" comes from the record. Anonymous, it comes
from the cookie. Middleware computes it once per request and puts it on the
context (`consent.FromContext(ctx)`) for handlers, the processing-purpose
gate and the otel proxy.

---

## 6. When a re-prompt fires

A purpose is reported as **needs decision** when:

1. there is **no decision** for it. This covers a **new purpose**: adding
   one to the registry asks everyone about that purpose only;
2. the decision cites a document version older than the purpose's
   document's **latest material version**. Versions are ordered by
   `effective` date, then `version`, so a decision citing a later
   non-material version still counts (it is at or after the material one),
   and "needs decision" and "effective" always agree;
3. for anonymous visitors, the consent cookie has **expired** or is gone.

Each `legaldocs` version carries `material: true|false`, **required, with no
default**. Fixing a typo or a broken link is a non-material version: it is
published and cited by new decisions, but doesn't ask everyone again. The
material call belongs to whoever publishes the version (with legal review),
and it is visible in the PR that adds the version file. A version file that
doesn't set `material` is rejected (the `legaldocs` loader errors, and the
`versions.lock` CI check fails), so the publisher always makes the call
explicitly rather than inheriting a default (§10). The rejected alternative, every version
bump re-prompting, trains users to click through the banner.

While a cookie-category purpose needs a decision, it counts as **denied**,
and the banner shows. While a processing purpose needs a decision, the
operations it gates are refused, and the app shows that purpose's own
prompt.

**A capability flag turned back on** doesn't by itself re-prompt: a grant
that still cites the current material version becomes effective again. If
a purpose is switched off for long enough that users may have forgotten,
whoever re-enables it should publish a material cookie-policy version,
which re-prompts. This is a runbook rule, not code.

No periodic re-prompt for signed-in users is built. If legal review asks for
one, it is a fourth condition (decision older than N months) with no schema
change.

---

## 7. Global Privacy Control

**Signal:** the `Sec-GPC: 1` request header (authoritative, read by
`consent` middleware), and `navigator.globalPrivacyControl` in the browser
(read by `ConsentProvider` only to explain it in the UI).

**Mapping (decided):** GPC means every purpose flagged `SaleOrShare` is
denied, and the visitor is opted out of sale/share. Today that is
`marketing`.

**Decision: GPC does not also deny analytics.**

- Analytics here is first-party: the events go to this app's own otel proxy
  and its own collector, and no third party receives them for its own
  purposes. That isn't a CCPA "sale" or "sharing" (sharing is disclosure for
  cross-context behavioural advertising), which is what GPC signals an
  opt-out of.
- Analytics is already off until the user explicitly opts in. A GPC user
  sees it unticked like everyone else. Denying it automatically would only
  stop a GPC user who *chose* to opt in. An explicit, specific choice in this
  app is a clearer statement of intent than a browser-wide setting about
  sale and sharing.
- The flag is what keeps this honest. **If an analytics vendor is ever
  added that uses the data for its own purposes** (an ads-linked analytics
  product, for example), its purpose is declared `SaleOrShare`, and GPC
  denies it from then on. The vendor checklist (§12) makes that a required
  step.
- The rejected alternative, GPC denying analytics too, is simpler to
  explain. But it conflates two different signals, and would need override
  rules for an explicit grant that this design avoids. If the human prefers
  the conservative reading, it is one flag on `analytics` and no other
  change. See §13.

**Behaviour:**

- While `Sec-GPC: 1` is present, `SaleOrShare` purposes are effectively
  denied, whatever is stored.
- **Signed in, GPC is a persistent opt-out.** A `source: gpc` denial event
  is appended for each `SaleOrShare` purpose when GPC changes the user's
  effective state, meaning their latest decision for it isn't already a
  denial. Like any other denial, it is the latest decision from then on. So
  the opt-out stays on the account after the signal goes away (on another
  browser, say) until the user grants again, which they can only do without
  GPC. The signal is treated as an opt-out request from a known person,
  which is how CCPA's regulations describe a signal from a signed-in
  consumer. The rejected alternative was to treat GPC as a per-request
  overlay that leaves the stored grant in force. That would let a grant come
  back on any browser without GPC, which surprises the person in the
  direction that matters.
- One event per change, not one per request, so the record shows that the
  signal was honoured without filling up.
- `PUT` granting a `SaleOrShare` purpose while GPC is present is refused
  with code `gpc_active`. The preferences dialog shows the toggle disabled,
  with "Your browser's Global Privacy Control signal is honoured".
- Anonymous visitors: nothing is recorded, and the cookie isn't rewritten.
  The effect is applied per request.
- The CCPA "Do Not Sell or Share My Personal Information" link records the
  same denial with `source: settings`, so it works without GPC too.

---

## 8. Enforcement points

Every non-operational record and script has two independent gates: one in
the browser, one on the server. Neither trusts the other.

| Point | What it enforces | Where |
|---|---|---|
| **The logging package** | `@slop-factory/logging` exports error-level records (`logger.error`: crashes, unhandled rejections, failed requests, failed sign-ins) as operational, always. Analytics records (`logger.event`) are exported only when the injected consent check returns true. `debug`, `info` and `warn` stay on the console and are never exported, because the operational tier is errors only, with no breadcrumb stream. Every record carries a `privacy.tier` attribute. Attributes pass only through the per-app allowlist; URLs become route templates | `js-components/logging` |
| **The script gate** | `loadWhenConsented(purpose, loader)` / `ConsentScript` is the only sanctioned way to load an optional script (analytics vendors, the interaction recorder, marketing). Nothing loads until the purpose is effective, and withdrawal calls the script's teardown. The interaction recorder is a dynamic import behind it, so with the flag off its code is never fetched | `@slop-factory/privacy/consent` |
| **The otel proxy** | Drops every record whose `privacy.tier` isn't `operational` unless that tier's purpose is effective for the request. A record with no tier, or an unknown one, is dropped (fails closed). Uses `go-privacy/telemetry`'s policy, which rebuilds the OTLP/JSON body | `go-privacy/otelproxy`, mounted by each app |
| **Processing-purpose gate** | Operations that process personal data for a processing purpose, or send a document to the LLM, carry a `consent.Requires("health_data_processing")`-style huma marker, and middleware refuses them without an effective grant. Like go-auth's `DefaultDeny`, a test enumerates every `RequireUser` operation and fails unless it carries either `Requires(...)` or an explicit `consent.NotGated()`, so a new processing route can't be added without a decision | the app's API server |
| **CSP** | `script-src 'self'` and `connect-src 'self'` today, in each app's `frontend/security_headers.conf` (with tests in `frontend/nginx.bats`). Adding a third-party vendor needs its origins added there, and only there. The script gate decides *whether* it loads; CSP bounds *what* can load at all (§12) | `slop-haus/frontend/security_headers.conf` |
| **Server logs** | Operational only. `go-privacy/logpolicy` applies per-app attribute rules (drop, hash, truncate), and call sites stop putting health values, document text, filenames or names into logged errors | each app's server |
| **Aggregate counters** | A label allowlist (operation, outcome class, optional coarse client type), enforced by a test that fails if any other label is registered | each app's server |
| **Retention** | One config of periods per data class, each with its enforcing mechanism | go-privacy `retention`, the deployment's own configuration |

**The proxy checks anonymous visitors too.** The ticket asked for the
server-side check for signed-in users. Because the anonymous consent cookie
is sealed, httpOnly and sent same-origin to `/api/otel`, the proxy can
enforce the same rule for anonymous visitors at no extra cost, so it does.
The proxy is marked `OptionalUser` (§3) instead of `Public`, so a signed-in
user's record is used when there is one. Its other protections (rate limit,
body size, forced `service.name`) are unchanged.

**Server log policy (`logpolicy`).** An `slog.Handler` wrapper each
server puts around its one shared handler, so every component's logger is
covered without touching its call sites. Decisions:

- **IP addresses are truncated in every log record, always:** to /24 (IPv4)
  or /48 (IPv6), for the keys `client_ip`, `remote`, `remote_addr`, `ip` and
  `started_from`. There is no short-lived full-address exception. Rate
  limiting and abuse limiting work on the full address in memory
  (`go-auth/ratelimit`) and never need it in a log; investigating abuse uses
  the prefix plus the signed-in user id. A value in an address key that does
  not parse is replaced outright.
- **Emails are hashed, not dropped:** a keyed HMAC-SHA256 pseudonym
  (`h:<16 hex>`) so two records about one address can be correlated. The key
  is random per process unless the app passes one, so the pseudonym cannot be
  joined across restarts or confirmed by guessing addresses from a leaked log.
  Rule sets: `EmailRules()` (`email`, `to`, `from`).
- **Never logged:** keys `body`, `request_body`, `response_body`, `payload`,
  and credentials (`password`, `token`, `authorization`, `cookie`, ...). The
  defaults cannot be removed by an app, only added to.
- **Errors are redacted at the log boundary:** any `error`-typed attribute,
  and the keys `err`, `error`, `cause` and `reason`, have double-quoted
  substrings replaced with `"[redacted]"`. This code base prints input into
  errors with `%q` (`a measurement type named %q already exists`), so the
  value goes and the message keeps its shape. The error text itself is left
  alone at the call site, because tests match it and the UI shows some of it.
  The cost is that quoted text that was not input is redacted too, and an
  unquoted value in an error is not caught: a new error should quote what it
  prints, and code review should look for ones that don't.
- **Record messages are not rewritten.** They are constants by convention.
- Not built on `go-deidentify`: that finds identifiers in prose for a model
  round trip and rehydrates them; here the wanted behaviour is a fixed
  per-key rule with no detector, and a logging path should not depend on a
  detector's models.
- The dev mail sink (`EMAIL_BACKEND=log`) logs the whole message, link
  included, through an unfiltered handler. It is the local-development
  facility, not an operational log, and each app warns at startup when it is
  selected. Log retention is the `retention` package's.

**Tier attribute.** `privacy.tier` is `operational`, or the id of the
consent purpose the record needs: `analytics`, `interaction_recording`.
Tier and purpose are deliberately the same id, so the proxy needs no mapping
table. The aggregate tier never passes through the proxy (it's counted on
the server), and the marketing tier has no records yet.

**The proxy doesn't take the tier on trust.** `privacy.tier` is set by the
browser, so on its own it only catches the app's own mistakes, not a
mislabelled record. The proxy therefore also checks the operational tier's
conditions on the server. An `operational` record must be error severity,
and may carry only the operational attribute allowlist (route template,
error type, message and stack, build tag, user agent, the random per-session
id, the user id). Any other attribute is stripped, and a record that isn't error
severity is dropped. A record labelled `operational` can then carry nothing
that analytics or interaction recording would. Analytics and
interaction-recording records get their own per-tier attribute allowlists in
the same filter, which never include a user id, tenant id or any form value,
so a record that carries identity is stripped of it on the server too and
the no-identity rule above doesn't rest on the browser package alone. As for the threat: consent
here protects the visitor from the app, not the app from the visitor. A
hand-crafted request can only send its own sender's data, which the rate
limit and body-size cap already bound. What needs a server-side boundary is
the app's own code relabelling data, and this closes that.

**Identity in analytics.** Analytics and interaction-recording records
carry **no user id**, not even under analytics consent, and no tenant id or
anything else that maps back to an account. They carry only a **random
per-session id**: made fresh per browser session, held in memory or
`sessionStorage`, and never persisted across sessions. It is declared in the
purpose and storage registry (§9) **under analytics consent, not as
necessary storage**: it is written to `sessionStorage` only once analytics
is granted, and with no consent it exists only in memory, so the operational
tier (which may carry it, §1) never depends on consented storage. If
interaction recording is granted without analytics, the recorder keeps its
own in-memory id and writes nothing. The reason: with no link back to a
person, erasure needs no deletes in the log stores, and short
per-store retention covers it. The accepted cost is that one user can't be
followed across sessions. Operational error reports may still carry the
signed-in user's id.

**Event tiers.** A consented tier is either an attribute allowlist or
an event catalog. For a catalog (`ConsentedTier.Events`, typed `enum`,
`bool` and capped-step `int` attributes) a record passes only as one of the
tier's events, with a session id of the app's pattern, and is rebuilt from
the catalog alone: the body is the event name, nothing the caller added
survives, and a value off the catalog drops the whole record rather than
trimming it. An `operational` record that names an event (`event.name`, any
spelling) is dropped, so an event can't take the operational tier's no-consent
path; so is a record that gives its tier twice, since no reading of it is
safe to pick. Spans are rebuilt the same way (`SpanPolicy`): only a failed
API request's span, with a route template that is one of the API's own.

**Where each tier's records go.** There are no custom stores. Each tier has
its own **VictoriaLogs instance per environment** (operational, analytics,
interaction recording), each with its own retention and access. **Metrics**
(the aggregate counters) go to **VictoriaMetrics**, in the `h` stack, and
**traces** go to **VictoriaTraces**. **Grafana** dashboards visualise all of
them. The collector routes records by the `privacy.tier` attribute
that `@slop-factory/logging` adds, so the tier decided in the browser is the same one that
picks the store.

**Frontend stack traces.** Source maps are **private**: generated per build
tag, hidden (no `sourceMappingURL` comment) and never served. A decoding
script maps a reported stack back using the map for its build tag.
There is no Sentry or GlitchTip for now.

---

## 9. The data inventory format

One YAML file per app, inside the app's server module:
e.g. `slop-haus/server/privacy/inventory.yaml`, embedded by a small
`privacy.go` in the same directory.

**Why inside the server module, not `<app>/privacy/`** (the path the inventory
and app-wiring tickets used): `go:embed` can't reach outside a module, and the server
needs the generated legal-document fragments (§10) and the legal documents
in its binary. A separate Go module for an app's privacy files would add
`go.work`, `Dockerfile.dockerignore` and CI wiring for a directory of data
files. Inside the server module, the CI checks ("every table in the
migrations is in the inventory") are ordinary Go tests next to the
migrations they read. Those tickets' path scopes need updating (§13).

YAML rather than JSON because humans edit it and comment on it in PRs. It is
validated against a JSON Schema that `go-privacy/inventory` embeds, and
loaded through its typed Go loader.

```yaml
schema_version: 1
app: example

purposes:            # must match the app's Go registry (§4), checked in CI
  - id: analytics
    kind: cookie_category
    legal_basis: TODO-LEGAL      # placeholder until reviewed (§10)

data_categories:
  - id: lab_results
    description: Reports a user uploads and the values read from them
    special_category: health      # GDPR Art. 9; also WA MHMDA consumer health data
    purposes: [health_data_processing]
    legal_basis: TODO-LEGAL
    stores:
      - kind: table               # table | object_store | logs | metrics | backups | browser
        name: reports
      - kind: backups
        name: postgres-backups
    retention:
      class: health_records       # a data class in the retention config
    processors: [openrouter]

tables:              # every table in the app's migrations, or CI fails
  - name: reports
    categories: [lab_results]
  - name: global_action_throttle_events
    categories: [security_events]

processors:          # every external service the config can call, or CI fails
  - id: openrouter
    name: OpenRouter
    role: processor               # processor | sub_processor | controller
    data_shared: [lab_results, uploaded_documents]
    location: unknown             # `unknown` rather than a guess
    transfer_mechanism: TODO-LEGAL
    sub_processors: [unknown]

storage:             # the cookie and storage registry
  - key: example_session
    type: cookie                  # cookie | local_storage | session_storage | indexeddb
    purpose: necessary
    first_party: true
    lifetime: access-token TTL
    set_by: server
  - key: slop-factory-auth:pending-step-up
    type: session_storage
    purpose: necessary
    first_party: true
    lifetime: tab
    set_by: "@slop-factory/auth"
  - key: slop-factory-logging:session-id   # random per-session analytics id (§8)
    type: session_storage
    purpose: analytics            # not necessary: written only once analytics is granted
    first_party: true
    lifetime: session             # never persists across sessions
    set_by: "@slop-factory/logging"

retention_classes:
  - id: health_records
    period: TODO-LEGAL
    mechanism: account deletion
```

(Illustrative. Each app's inventory is filled in from the code as it is, with
`unknown` wherever it isn't certain.)

**What reads it:**

- **CI checks** (`go-privacy/inventory`, run as tests in each server): the
  file validates; every table in the app's migrations is listed; every
  processor the config can call is listed; every purpose matches the Go
  registry; every retention class with a period names a mechanism;
  every storage key's purpose is in the registry.
- **The runtime registry check** (each app's Playwright e2e):
  after the main flows, every cookie and storage key present must be listed
  under `storage`, and none of a non-necessary purpose may exist without its
  consent.
- **The generator** emits Markdown fragments: the cookie table, the
  sub-processor list, the retention section, and the DPIA's data-flow
  section. `legaldocs` includes them (§10), so the privacy policy, cookie
  policy, DPIA and sub-processor list are generated from the
  inventory, or checked against it.

Today's necessary storage, as found in the code:

| Key | Type | App | Set by |
|---|---|---|---|
| `slop_haus_session` | cookie | slop-haus | `slop-haus/server/internal/authn.SessionCookie` |
| `slop_haus_refresh` | cookie | slop-haus | `slop-haus/server/internal/user.RefreshCookie` |
| `slop_haus_login_state` | cookie | slop-haus | `slop-haus/server/internal/user.LoginStateCookie` |
| `slop-factory-auth:pending-step-up` | sessionStorage | slop-haus | `js-components/auth/src/sign_in_methods/pending_step_up.ts` |
| `slop_haus_consent` | cookie | slop-haus (new) | `go-privacy/consent` |

The per-session analytics id (`slop-factory-logging:session-id`) is
deliberately **not** in this table: it is a `sessionStorage` key under
`analytics` consent (§8), listed in the inventory's `storage` registry with
that purpose, so the runtime registry check fails if it is present without
the consent.

The CSRF token isn't stored in the browser: it is an HMAC of the session id
(`go-auth/auth.CSRFTokens`), which the page reads from a same-origin
response and holds in memory. It is listed in the inventory as necessary
processing, not as a storage key.

---

## 10. Legal documents and placeholder conventions

### Source: files in the app's repo, snapshotted to the database

Documents live as Markdown files in the app's server module,
`<app>/server/privacy/legal/<kind>/<version>.md`, embedded at build time.
Text changes go through PRs and are versioned in git (the legal-documents
proposal, adopted). On startup the app publishes each version into
`global_legal_document_versions` with its rendered text and a SHA-256 hash.
That row is what a consent event cites, and it is kept exactly, so a user
can always read precisely what they agreed to.

Each file starts with front matter:

```markdown
---
kind: privacy_policy           # privacy_policy | cookie_policy | terms | health_data_notice
                               # | ccpa_notice_at_collection | an app-declared kind
version: 2026-10-01.1
effective: 2026-10-01
locale: en
material: true                 # REQUIRED, no default: does this version re-prompt? (§6)
reviewed: false                # drives the DRAFT banner
reviewed_by:                   # filled in with reviewed: true
reviewed_on:
---
```

**`material` is required on every version.** There is no default, because
a silent default would let a publisher skip the decision. The `legaldocs`
loader refuses a file whose front matter doesn't set it to `true` or
`false`, and the `versions.lock` CI check runs the same loader over every
version file, so a version that omits it fails the PR rather than shipping.

**A published version is immutable.** What was published is the database
row, kept exactly and served from there. Each app keeps
`privacy/legal/versions.lock` (the `legaldocs.Lock` format), one line per
version: `<kind> <version> <locale> <source sha256> <rendered sha256>`. A test
(`legaldocstest.CheckLock`) fails the PR on:

- **Any version's source changing.** The source is the file as written, front
  matter and Markdown with its `{{inventory:...}}` markers unexpanded. An old
  version is locked on this alone.
- **The version in force changing in what it renders.** The head of each kind
  and locale (the latest by `(effective, version)`, a scheduled one included) is
  also locked on its rendered text. Generated sections come from the
  inventory, so a new storage key or a changed retention period changes what
  the head says without its file changing; that has to be a new version file,
  never a quiet change to the one people see.
- **A locked version disappearing**, and a version with no line (run with
  `UPDATE_VERSIONS_LOCK=1`, which appends lines and never rewrites one).

Older versions are not locked on their render, because it would drift with
every inventory change although nothing that was published changed, and an
ordinary inventory change could not ship. Once a newer version is added the
previous head's rendered line stops being checked, and its source line still is.

Startup never compares a published version with a render either:
`Service.Publish` inserts only the versions the database lacks (§16). Changing
a document, including a change to a generated fragment it includes, therefore
means adding a new version file, never editing an old one.

### Marking unreviewed text

- **Whole document:** `reviewed: false` makes the `legal_docs` page render a
  prominent banner, **"DRAFT – not legally reviewed"**, above the text, and
  the version-history list marks the version as a draft. Every document
  starts this way: a template with section headings and placeholders.
- **Inside the text:** `[[TODO-LEGAL: what is needed]]` marks a gap. The page
  renders it highlighted. A test refuses `reviewed: true` on a file that
  still contains one.
- **Generated sections:** `{{inventory:cookie_table}}`,
  `{{inventory:sub_processors}}`, `{{inventory:retention}}` and
  `{{inventory:data_flow}}` are replaced with the inventory generator's
  fragments at load time. Without a generator, each renders as "*Pending
  inventory.*". `data_flow` is the DPIA's section, and it names internal
  stores, so no built-in template includes it: it is for an app's own
  kind (see §16).
- **Structured fields:** `TODO-LEGAL` is the placeholder value for legal
  bases, transfer mechanisms and retention periods in the inventory, and the
  generator renders it as "To be confirmed".
- **Purpose copy:** the banner and preferences text for each purpose is a
  section of the cookie policy (or the health-data notice), so it is marked
  the same way.

### Where reviewed text gets dropped in

The reviewer's text goes into a **new version file** for the same kind, with
`reviewed: true`, `reviewed_by` and `reviewed_on` set and `material`
decided, and its lock entry added, in one PR. Placeholders and generated
directives that legal review keeps stay as directives, so the cookie table
remains generated. Nothing else changes: the banner disappears because the
newest version is reviewed, and the material flag decides whether users are
asked again.

---

## 11. Data subject requests

Self-service export and deletion belong to each app, because
they walk the app's schema. Both use the inventory's table list to prove
completeness. `go-privacy/dsr` covers what needs a human: a request
log in `global_privacy_requests`, with every status change appended to
`global_privacy_request_events`. Due dates are computed from config
(placeholders: GDPR one month, extendable; CCPA 45 days, extendable).

One note for `dsr`: an app's role model may be per tenant. The person who
handles privacy requests operates the whole platform, not one tenant, so
"admin only" needs a platform-level notion. `dsr` should decide it rather
than gate it on a tenant role.

---

## 12. Adding a vendor: checklist

Any third-party script, pixel or service that receives data:

1. **Purpose:** use an existing purpose, or declare a new one (a new
   purpose re-prompts for itself only, §6). Set `SaleOrShare` if the vendor
   uses the data for its own purposes, or for cross-context behavioural
   advertising. GPC then denies it.
2. **Load it only through the script gate**, with a teardown hook.
3. **Widen the CSP** in `<app>/frontend/security_headers.conf` (`script-src`,
   `connect-src`, and `img-src` or `frame-src` as needed) and update
   `nginx.bats`.
4. **Inventory:** add the processor (location, transfer mechanism,
   sub-processors) and every cookie or storage key it sets. The registry e2e
   fails until you do.
5. **Legal documents:** publish a new cookie-policy (and if needed
   privacy-policy) version, `material: true`. The sub-processor list and
   cookie table regenerate from the inventory.
6. **Processor agreement:** the human's job.

---

## 13. Choices confirmed by the human

These are the choices this document made, with the human's answers.

Questions 1 and 3 to 7 of the first draft were answered by the human and
are recorded in the sections named; the former question 2 (a user id on
analytics events) was already decided and is in §8.

1. **GPC doesn't deny analytics** (§7), and for a signed-in user it is a
   persistent opt-out of sale/share purposes. *Accepted.*
2. **`OptionalUser` in go-auth/authn** (§3) is its own ticket,
   blocked on another ticket. *Accepted.*
3. **App privacy files inside the server module**,
   `<app>/server/privacy/` (§9). *Accepted;* the inventory and app-wiring tickets' path
   scopes change to match.
4. **The anonymous consent cookie's lifetime** is **6 months**, configurable
   and still a placeholder for legal review (§5). *Accepted.*
5. **`material` on legal-document versions** (§6, §10) is required, with no
   default, and CI rejects a version that doesn't set it. *Accepted.*
6. **`ON DELETE RESTRICT` from consent events to `global_users`** (§3) is
   accepted. Account deletion decides deletion versus pseudonymisation explicitly. The
   human expects pseudonymisation, since proof of consent may need to be kept
   (GDPR Art. 17(3)(e)), but the decision belongs to the account-deletion work.

---

## 14. The consent package, as built

`go-privacy/consent` implements §4 to §7. slop-haus wires it in (`slop-haus/server/privacy`). Everything below is what the code
does, and where it differs from the design above.

### Contents

| File | Holds |
|---|---|
| `purpose.go` | `Purpose`, `Registry` (`NewRegistry` validates a declaration), `Capabilities` (an interface; `StaticCapabilities` for env-style config) |
| `documents.go` | `Documents`, `DocVersion`: the whole dependency on legal documents |
| `service.go`, `state.go` | `Service`: `Effective`, `Resolve`, `Decide`, `History`; the effective-consent rule |
| `store.go`, `postgres_store.go` | `Store` and `PostgresStore`, over the tables in `schema/consent.sql` |
| `cookie.go` | `CookieCodec`: the sealed anonymous cookie |
| `handler.go`, `routes.go` | `Handler.Middleware()` and `Handler.RegisterRoutes` (huma) |
| `consenttest/` | `Migrations()` (a test schema), `StaticDocuments`, `StoreSuite` |
| `schema/consent.sql` | The DDL, in goose format, for an app to copy into its own migration |

### Deviations from §2 and §3

- **`consent` does not import `legaldocs`.** `legaldocs` isn't built,
  and the dependency runs the other way round better as a consumer-side
  interface: `Documents.Versions(ctx, kind)` returns each published
  version's id, effective date and `material` flag. `legaldocs` implements it
  when it lands; an app can use a fixed list until then. Only versions whose
  effective date has passed count (a scheduled version neither denies grants
  early nor is cited yet). Versions are ordered
  by `(effective, version)` as in §6, and a decision citing a version the
  Documents don't know is not current (fails closed).
- **No in-memory store.** The rules that matter (append-only, the per-user
  serialization, the merge-token claim) belong to the database, so
  `consenttest` ships `StoreSuite` and the app's tests run against its real
  database. The route tests run against `dbtest` too.
- **No `Requires(...)` operation markers** for processing purposes (§8): they
  belong with the app's route table and a `DefaultDeny`-style test.
  `FromContext(ctx).Granted(purpose)` is what that gate will call.
- **No `global_legal_document_versions` foreign key.** The events cite
  `(document_kind, document_version)` as text until `legaldocs` has that
  table; adding the foreign key is `legaldocs`'s migration.
- **Route markers are the app's.** `Operations{Read, Write}` takes each
  route's authn metadata, and `RegisterRoutes` refuses a nil one. Until
  `OptionalUser` exists an app marks them `authn.Public()`, which
  means a signed-in user's record is only used once the app's user
  middleware has resolved the user some other way. The middleware reads the
  user from the context (`consent.AuthnUser` for go-auth's `authn.User`), so
  it must be installed after the app's authn middleware.
- **A signed-in decision merges the cookie in the same transaction**, if it
  carries an unmerged token (a purpose decided in that request is not merged
  over; a spent token skips the merged events and nothing else), then
  refreshes the cookie to the user's effective
  cookie-category state (no token, nothing unmerged), the same rewrite the
  merge does, so choices survive a sign-out.
- **GPC is recorded for every declared sale/share purpose**, including one
  whose capability flag is off, so the opt-out is still there when the flag
  comes back on.
- **The cookie is rewritten from the record at merge and at each signed-in
  decision** (§5 describes the merge case), so after a sign-out the browser
  keeps the last person's cookie-category choices for the next anonymous
  visitor of that browser. That is §5's design (the cookie is the browser's
  choice, not an identity), but on a shared device it is the previous user's
  choices applying: the app's sign-out can clear the consent cookie if that
  is not wanted.
- **GPC with no decision on record** also appends a `gpc` denial (§7 says "when
  the latest decision isn't already a denial"; none isn't one). That makes
  the opt-out persistent from the first signal.

### Tables

`global_consent_events` and `global_consent_merge_tokens`, both **global**
(no `tenant_id`, no RLS), for the reasons in §3: consent belongs to a person
rather than a tenant, and anonymous visitors have no tenant. Only the
signed-in user's `user_id` links a row to anyone. The events reference
`global_users` `ON DELETE RESTRICT`.

- **Append-only in the database.** A trigger refuses `UPDATE` and `DELETE` of
  a consent event, and `TRUNCATE` (for the table owner). Revoking the grants
  would not hold, because go-db's `EnsureRole` grants them again. Account deletion owns
  the one erasure path (pseudonymisation or deletion), which has to go
  through a migration-owner function; nothing in this package bypasses the
  trigger.
- **Merge tokens are stored hashed** (SHA-256), in both tables; the unique
  `(merge_token, purpose)` index and the token table make a cookie's
  decisions merge into one account at most once, even when none were newer.
- **Writes are serialized per user** with `SELECT ... FOR NO KEY UPDATE` on
  the user row (it conflicts with itself but not with the foreign-key lock
  other inserts take). `Store.Apply` runs a plan under that lock, so a
  decision, a GPC denial and a merge never overlap, and a stale write
  (`based_on` not the purpose's current event id) is refused with
  `consent_changed`. A new event never carries a `decided_at` earlier than
  the purpose's previous one, so a server whose clock runs behind can't bring
  an old choice back.

### Data minimisation and logs

An event holds: user id, purpose, choice, document kind and version, source,
`decided_at`, `recorded_at`, whether it was merged, the (hashed) merge token
and go-auth's session id. **Not stored: IP address, user agent, or anything
else of the request.** The cookie holds the same per decision, plus a random
merge token and an expiry. Logs carry the operation (`METHOD /path`) and an
error class only: never a user id, a cookie, a token or a decision. This
package logs an error's text only if it made it itself (`opError`: the
operation name and a SQLSTATE, because the database's own message quotes row
values); anything else, such as what an app's `Store` or `Documents` returned,
is logged as "unclassified error". HTTP errors say only that consent is
unavailable.

### Anonymous cookie

Sealed as `v1.<payload>.<mac>`: HMAC-SHA256 under a key derived with
`auth.DeriveKey(secret, "consent-cookie")`, compared with `hmac.Equal`
(constant time), nothing parsed before the MAC verifies, unknown fields
refused. `httpOnly`, `SameSite=Lax`, `Path=/`, and **`Secure` set by the app's
config with no default**. The expiry is inside the seal and enforced
server-side (a client can keep a cookie as long as it likes); the lifetime is
6 months unless configured. Each decision made while signed out carries an
`unmerged` flag, and only flagged decisions are merged, so a shared browser's
next person doesn't inherit the last one's choices.

### Requests

`Handler.Middleware()` computes the effective consent once per request and
puts it on the context (`consent.FromContext(ctx)`). It records a signed-in
browser's GPC opt-out (a read when nothing changes, a write only on a change)
but never merges the cookie: that is left to the consent routes. A request
with an `Authorization` header is a script's: no cookie is read and no GPC is
recorded. Only `Sec-GPC: 1` is the signal. If consent can't be worked out the
request carries on with nothing granted and the consent routes answer 500.

Wiring, in the app's server (after the authn middleware). **Until
`OptionalUser` exists, go-auth's `UserMiddleware` doesn't resolve a
user on a `Public` operation, so with `Public` markers the routes serve
anonymous visitors only**; signed-in behaviour (the record, GPC opt-outs, the
processing purposes) needs the optional-user marker, or a marker the app's
middleware resolves a user for without refusing anonymous requests:

```go
svc, _ := consent.NewService(&consent.Config{Registry: reg, Capabilities: caps,
	Documents: docs, Store: consent.NewPostgresStore(eng), Cookie: codec, Logger: h})
ch := consent.NewHandler(&consent.HandlerConfig{Service: svc, User: consent.AuthnUser, Logger: h})
api.UseMiddleware(ch.Middleware())
_ = ch.RegisterRoutes(api, "", &consent.Operations{Read: authn.Public(), Write: authn.Public()})
```

`PUT` refusals are `authn.Problem` bodies with a `code`: `credential_rejected` (403, see §3),
`consent_changed`,
`document_changed` and `gpc_active` (409), `sign_in_required` (403),
`invalid_purpose`, `necessary_purpose`, `duplicate_purpose` (400). A `PUT`
must cite the document version the person was shown, and it is refused if
that is no longer the latest.

### Tests

`go-privacy/test.sh` (what CI runs) builds
`go-privacy/Dockerfile` from the repo root and runs `go vet` and
`go test` with the Docker socket, because the tests start Postgres through
go-db's `dbtest`. Locally, `go test ./...` from `go-privacy/` works with the
workspace and a Docker daemon.

## 15. The retention package, as built

`retention` is the mechanism for a retention class whose data is a database
table: delete the rows whose time column is older than the class's period.
It is a helper an app calls from its own schedule (a scheduled job in
the app's worker); it does not schedule itself and holds no periods.
The periods are the app's config, checked against the inventory (below).

- **Only expired rows.** A `Rule` names a table, a time column and a period;
  it takes no free-form predicate, so nothing but the time can select a row. A row is expired when its time column is
  before `now - period`; a row exactly on the edge is not. A zero or negative
  period is refused (`ErrInvalidRule`), because "zero" would mean "everything",
  and so is a table or column that isn't a plain lower-case identifier.
- **Per scope, as the app role.** `Scopes` says where a batch runs.
  `TenantScopes` runs each batch in a `tenancy.TenantTx` on the app's own
  engine, so row-level security, not the rule's SQL, bounds a batch to its
  tenant. `GlobalScope` is for a `global_*` table. A tenant table run in the
  global scope sees no rows (RLS fails closed) and deletes nothing. One scope's
  failure doesn't stop the others; the errors come back joined.
- **Bounded batches, short locks.** Each batch is its own transaction of at
  most `BatchSize` rows (500 by default), selected `FOR UPDATE SKIP LOCKED`:
  a row another transaction holds (a claim path, a deferred trigger's work) is
  skipped for this run rather than waited for, so the sweep never queues behind
  one or holds locks while it waits. Cancelling the context stops between
  batches.
- **Idempotent and resumable.** What committed stays deleted; a repeat or a
  retry after a crash finds the rest, or nothing.
- **Dry run.** `Config.DryRun` counts the expired rows per scope and deletes
  nothing. A sweeper that deletes existing data on its first run ships with it
  on, and a human turns it off after reading what the dry run logged.
- **Not for rows that point at objects.** Rows that own stored objects are
  deleted through the owner's own path (the object goes after the database
  commit, through the deletion queue the owner keeps). This helper knows
  nothing of objects, so don't point it at such a table.

### Periods and the inventory

An app that deletes on a timer passes the period its code enforces, by class
id, as `inventorytest.Config.EnforcedRetention`; the suite fails when it
differs from the class's period in `inventory.yaml`, when the class isn't
there, or when the inventory says the class is `indefinite`. The inventory's
own check (a class with a duration period names a mechanism) was already
there. Periods set in the deployment's own configuration (log store retention) are
not in Go config and are not checked by this: they are kept equal to the
inventory by hand.

---

## 16. The legaldocs package, as built

`go-privacy/legaldocs` implements §10 and the documents routes of §3.
slop-haus (`slop-haus/server/privacy/legal`) wires it in. The JS side is `@slop-factory/privacy/legal_docs`.

| File | Holds |
|---|---|
| `kinds.go` | The built-in kinds and `Kinds`, which adds an app's own |
| `frontmatter.go`, `loader.go`, `render.go` | `Loader`: reads `<kind>/<version>.md` files, validates the front matter, fills `{{inventory:...}}` sections |
| `version.go` | `Version`, its `ContentHash` (the rendered text and metadata) and `SourceHash` (the file as written) |
| `lock.go` | `Lock`: the `versions.lock` rules of §10 |
| `store.go`, `postgres_store.go` | `Store` and `PostgresStore`, over `schema/legaldocs.sql` |
| `service.go` | `Service`: `Publish`, `Current`, `Get`, `History`, and `Versions` for consent |
| `routes.go` | `Handler.RegisterRoutes` (huma) |
| `template.go`, `templates/` | `Template(kind, version, effective)`: the placeholder every document starts as |
| `legaldocstest/` | `Migrations()`, `StoreSuite` and `CheckLock`, the app's `versions.lock` test |

### Source: files, snapshotted to the database (§10, adopted)

An app embeds its Markdown files, loads them with `Loader.Load` at startup and
passes the result to `Service.Publish`. Files, because text changes are then
reviewed in PRs and versioned in git. The database copy, because the file can
change and what a person agreed to must not: **the routes serve from the
database, never from the files.** `Publish` inserts the versions the
database lacks and leaves every existing row alone: it does not render or
compare a published version against its row. (Comparing would bring back the
inventory problem of §10 at startup, as an old version's generated sections
move with the inventory.) A restart or a second replica publishes nothing; if
another replica wins the insert of a new version first, its row stands.
`Store.Publish` itself still reports `ErrVersionChanged` for different content
under a published key, as its contract. The stored row is untouched, and a
trigger refuses `UPDATE`, `DELETE` and `TRUNCATE` on the table even for the app
role. Changing a document means a new version file, held by `versions.lock`
(§10) in CI.

The hash (`content_sha256`) covers the rendered body and the metadata
(effective date, material, reviewed fields), so changing only `material` is
caught too. The metadata is hashed with the rendered text because a flipped `material`
changes what users are asked. (It is the lock's rendered column, for the
version in force; every version's source hash covers the front matter too.)

`global_legal_document_versions` is **global**, as §3 says: the text is the
same for every tenant, and anonymous visitors have no tenant. Primary key
`(kind, version, locale)`. The app copies `schema.LegalDocs` into its own
migration and runs `legaldocstest.StoreSuite` against its database, as with
the consent tables.

### Scaffolding a document

`legaldocs.Template(kind, version, effective)` returns the file an app drops
into its repo for a built-in kind: front matter with `reviewed: false` and
`material: true`, section headings, and `[[TODO-LEGAL: ...]]` markers. It has
no legal text. The inventory-driven sections (`cookie_table`, `sub_processors`,
`retention`, and `data_flow`, which no built-in template uses) are directives, `*Pending inventory.*` until a `Fragments` is
given to the `Loader`.

The loader refuses: a missing `material`, `effective` or `locale` (no default: a loader's and a service's could differ, filing a version under a locale the routes don't serve), an unknown front
matter key, a kind that isn't declared, a file whose directory or name
disagrees with its front matter, a body over 512 KiB, an unknown directive,
and `reviewed: true` without `reviewed_by` and `reviewed_on`, or with a
`TODO-LEGAL` marker or a pending generated section left in the text.

**Reviewed documents and the inventory's placeholders.** The inventory
generator renders a value nobody has decided as "To be confirmed" (or
"Unknown", or "(provisional)" on a retention period), which the text check
can't tell from real text. So a `Fragments` that implements
`CompletenessReporter` (`inventory.Fragments` does, through `Checked`: the text and
what is unresolved in it from one pass) says per fragment what it
rendered as a placeholder, and `reviewed: true` makes `Loader.Load` fail
naming the document, the section and the values (`terms/v1 ... retention:
retention class logs: period`). The check is by section, so a section used
twice or among others is covered, and it is made on each file as it is loaded,
against the inventory the loader holds at that moment. A `Fragments` that
can't report is refused for `reviewed: true` whenever the document uses a
generated section (fail closed). A `reviewed: false` document loads and shows
the placeholders under its DRAFT banner.

What it does not do: it only sees values a section shows. `legal_basis` is in
`Inventory.Placeholders()` but no fragment renders it, so it doesn't block a
document; the reviewer who writes the lawful-basis text owns that. And the
text is rendered once, when a version is first published: a later inventory
change reaches users only as a new version file, never as a change to a
published one (the lock of §10 is what forces the new file).

### Routes

All three are `GET`, take an optional `?locale=` (default `en`, or the
service's `DefaultLocale`), are marked with whatever authn marker the app
passes in `Operations.Read` (`authn.Public()`; documents are public), and are
cacheable.

| Path | Returns |
|---|---|
| `/api/privacy/documents/{kind}` | The version in force |
| `/api/privacy/documents/{kind}/versions` | The history, newest first, without bodies |
| `/api/privacy/documents/{kind}/versions/{version}` | One exact version |

A version whose effective date hasn't come is not public: not in the history,
and a `404` by id. Versions are ordered by `(effective, version)`, as consent
orders them (§6). The body carries `reviewed`, which the page turns into the
DRAFT banner. The Markdown body is untrusted to render (the JS component
doesn't allow raw HTML or script URLs).

### consent

`legaldocs.Service` is a `consent.Documents` (checked at compile time, and a
test drives a real consent `Service` from it: a material version withdraws an
earlier grant and asks again, a non-material one doesn't). So `legaldocs`
imports `consent` for `DocVersion`, the reverse of §2: Go's interfaces need
the same type on both sides, and `consent` defines the interface.
`Versions` reports the default locale. The events' `document_kind` and
`document_version` stay text with **no foreign key** to this table: a version
is keyed by locale too, which a consent event doesn't carry, and events also
come from a cookie.

### Not done

- `Versions` reads the database (summaries, never bodies) on each call, and
  consent calls it per request and document. Published rows never change, so a
  cache is safe once it matters.

---

## 17. The dsr package, as built

`dsr` is the operator's log of data subject requests: a `Request` (requester,
verified yes/no with the method, type, authorised-agent flag, received and due
dates, status, extension), an append-only audit trail of `Event`s, and a
`Service` holding the rules. It is transport-free: an app decides how requests
are logged and read.

- **Types.** The GDPR rights (`access`, `rectification`, `erasure`,
  `restriction`, `objection`, `portability`), the CCPA ones (`ccpa_know`,
  `ccpa_delete`, `ccpa_correct`, `ccpa_opt_out`, `ccpa_limit`) and `other`.
  A type fixes its regime, and the regime fixes the deadline. `other` takes
  the GDPR clock, the shorter one.
- **Deadlines.** `Deadlines` is configuration; `DefaultDeadlines` is the
  placeholder (GDPR a month, extendable by two; CCPA 45 days, extendable by
  45) for a qualified person to confirm. A month added to the 31st lands on
  the last day of a shorter month. An extension is added to the base period
  before the date is worked out, from the received date, and a request is
  extended once, with a reason.
- **Status.** `received`, `verifying`, `in_progress`, then closed as
  `completed`, `refused` or `withdrawn`. A closed request takes no further
  change except a note. Completing a request that discloses, changes or
  deletes data (access, rectification, erasure, portability, CCPA
  know/delete/correct) needs the requester verified first; refusing does not.
- **Audit.** Every change appends a `global_privacy_request_events` row (who,
  what, from and to status, free text). A trigger refuses UPDATE on it, and
  DELETE of a row whose request still exists, so the trail can't be rewritten
  or trimmed even by the app role; rows go only with their request, which is
  what a retention sweep on `closed_at` relies on.
- **Alerts.** `CheckAlerts` claims the open requests that are overdue or due
  within `DueSoon` (default a week), once per threshold, hands them to the
  caller's notifier and records an event. A failed notify puts the claims
  back, but only the mark the claim left, so a release can't undo a newer
  claim. When the alerts went out and recording them failed, the claims stay
  (releasing would send them twice) and the error says the trail is short.
  A claim is a lease: one whose alert was never recorded as sent, because the
  claimer died, is taken over by a later check once `AlertLease` (30 minutes)
  has passed, so a crash can't silence the only warning of a deadline. That
  makes delivery at-least-once. An extension clears the marks for the new date.
- **Tables are global and unlinked.** A request is addressed to the operator,
  its requester may have no account, and an erasure request has to outlive
  the account. There is no foreign key to `global_users`.
- **Personal data.** A request holds an email and free text. Nothing the
  package logs or returns in an error names a value (`opError`, `invalid`).
- **App wiring.** Copy `schema.DSR` into a numbered migration and run
  `dsrtest.StoreSuite` against the migrated database.
