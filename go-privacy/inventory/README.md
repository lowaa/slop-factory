# go-privacy/inventory

The data inventory (go-privacy/README.md §9): one YAML file per
app, kept in the app's server module (`<app>/server/privacy/inventory.yaml`),
recording what personal data it holds, where, why, for how long, who else
receives it, and which cookies and browser storage keys it sets. It is the
GDPR Art. 30 record and the cookie registry, as code, so CI can tell when the
app has outgrown it.

It describes software and decides nothing legal. A value that needs a legal
answer (a legal basis, a transfer mechanism, a retention period nobody has
chosen) is `TODO-LEGAL`; a fact that couldn't be confirmed from the code or
config is `unknown`. Neither is ever guessed. `Inventory.Placeholders()` lists
the `TODO-LEGAL` values still left.

| File | Holds |
|---|---|
| `inventory.go` | The schema, as Go types. They are the schema: `Load` refuses unknown keys, so there is no second JSON Schema to drift from them |
| `load.go`, `validate.go` | `Load` (strict YAML, then `Validate`): required fields, references that resolve, nothing listed twice, a retention class with a period names a mechanism |
| `checks.go` | `CheckTables`, `CheckConfigKeys` and `CheckEnforcedPeriods` (the periods an app's code enforces must be the inventory's), the CI checks below; `ParsePeriod` |
| `fragments.go` | `Fragments`, the Markdown generator |
| `inventorytest/` | `Run`, the suite an app runs; one implementation, not a copy per app |

## What the file holds

`purposes`, `data_categories` (flagged `special_category: health` where it
applies, each with its stores, purposes, legal basis, retention class and
processors), `tables`, `processors` and `internal_services`, the cookie and
browser `storage` registry, and `retention_classes` (the period in force
today, and the mechanism enforcing it). Read `go-privacy/inventory/testdata/synthetic.yaml`
for a small synthetic example of every field.

## The CI checks

An app adds `privacy/privacy_test.go` that runs
`inventorytest.Run` against its migrated database and config source:

- **The file validates** against the schema above.
- **Every table is in the inventory.** The tables come from the migrated
  database, not parsed SQL, so a table made by any migration is seen. A new
  table with no entry fails the build; so does an entry for a table that no
  longer exists. A table with no personal data says `no_personal_data: true`
  rather than being left out.
- **Every service the config can call is accounted for.** The env var names
  are read out of the app's config key table (`ConfigKeysFromSource`). A key
  that can call out (by name: `_URL`, `_HOST`, `_ENDPOINT`, `_API_KEY`,
  `_CLIENT_ID`, `_PROVIDER`, `_PROVIDERS`) must be listed under a processor's
  `config_keys`, or under `internal_services` for a service the operator runs.
  A listed key the config no longer has also fails, so entries can't go stale.
  A service configured under a name the pattern misses needs the pattern
  widened (`externalKeyPattern`); that is the check's known limit.

### Adding a table, a provider or a cookie

- **A migration adds a table:** add it under `tables` with the data categories
  it holds (and add a category if none fits).
- **The config gains a provider or endpoint:** add a `processors` entry (or
  `internal_services`), with `config_keys`, `data_shared`, `location: unknown`
  and `transfer_mechanism: TODO-LEGAL` until someone knows.
- **The app sets a cookie or storage key:** add it under `storage`.

### The runtime check (not built here)

Each app's e2e test should assert that every cookie and storage
key present after the main flows is in `storage`, and that none of a
non-necessary purpose exists without its consent. It needs only
`inventory.Load(privacy.InventoryYAML())` and the `Storage` list; nothing in
this package needs to change for it.

## The generator

`NewFragments(inv).Fragment(name)` returns Markdown for a legal document:

| Name | Section |
|---|---|
| `cookie_table` | The cookie and storage table of the cookie policy |
| `sub_processors` | The processor and sub-processor list |
| `retention` | The privacy policy's retention section |
| `data_flow` | The DPIA's data-flow section |

`Fragments` has the method `legaldocs.Fragments` asks for
(`Fragment(name string) (string, bool)`), so it is passed as
`legaldocs.LoaderConfig.Fragments` and fills the `{{inventory:...}}`
directives. legaldocs knows all four names; `data_flow` is for the DPIA,
and no built-in template includes it.

`TODO-LEGAL` renders as "To be confirmed" and `unknown` as "Unknown", as §10
says, so a document can read as finished while the inventory has placeholders.
`Fragments.Checked(name)` (and `Unresolved(name)`) lists, per fragment, the values that rendered
that way (and a retention period marked `placeholder` as "(provisional)"); it
is what legaldocs' `reviewed: true` guard uses (`legaldocs.CompletenessReporter`),
so the guard sees only what a document actually shows. `Placeholders()` is
the wider list, including legal bases no fragment renders.

Output is deterministic and golden-tested (`testdata/*.golden.md`; regenerate
with `go test ./inventory -update` and read the diff). Cell text comes from a
file people edit and ends up on a published page, so pipes and newlines in it
are neutralised.
