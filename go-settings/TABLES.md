# The settings tables

An app creates its own tables in its own migrations; this module ships no
migrations. `pgstore` is written against exactly these shapes, and
`pgstore/tables_test.go` fails if the SQL below drifts from the copies the
module's tests run on (`pgstore/testdata/migrations`).

All three share the same row layout: an append-only typed log, one row per
change, the newest row per `setting_name` wins. "Newest" is the highest `id`
in the two tenant tables. In the global table it is the latest `created_at`,
then the highest `id` (an existing table's index order that has not been
migrated); `created_at` is the transaction's start, so two writes in one
transaction tie on it. Rows are never pruned. The `CHECK`s keep exactly the value column the
`setting_type` names set.

The tables are separate rather than one table with a nullable tenant and
user, because each scope has a different owner, deletion rule and access
rule, and the database enforces each one: a shared table would make those
rules conditional, and a missed condition would be a leak between scopes.

What the SQL below needs from the app's schema:

- `global_users (id uuid)` for the user a row belongs to or was changed by;
- `current_tenant()`, returning the tenant the transaction is scoped to, and
  `go_db_apply_tenant_rls(table)`, go-db's tenant row-level-security helper;
- for a membership rule, a memberships table with `tenant`, `user_id` and
  `created_at` columns (`pgstore.Membership`).

## global

One user's settings in every tenant. No tenant, no row-level security: it is
read by the signed-in user's id. Deleting the user deletes the rows.

<!-- sql:global -->
```sql
CREATE TABLE global_user_settings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES global_users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    setting_name text NOT NULL,
    setting_type text NOT NULL CHECK (setting_type IN ('str', 'int', 'bool', 'float', 'jsonb')),
    str_value text,
    int_value bigint,
    bool_value boolean,
    float_value double precision,
    jsonb_value jsonb,
    CHECK (num_nonnulls(str_value, int_value, bool_value, float_value, jsonb_value) = 1),
    CHECK ((setting_type = 'str') = (str_value IS NOT NULL)),
    CHECK ((setting_type = 'int') = (int_value IS NOT NULL)),
    CHECK ((setting_type = 'bool') = (bool_value IS NOT NULL)),
    CHECK ((setting_type = 'float') = (float_value IS NOT NULL)),
    CHECK ((setting_type = 'jsonb') = (jsonb_value IS NOT NULL))
);

CREATE INDEX global_user_settings_latest
    ON global_user_settings (user_id, setting_name, created_at DESC, id DESC);
```

## user within a tenant

One user's settings in a single tenant. Tenant row-level security, plus a
second, restrictive policy so a query that forgets its user filter still sees
only the caller's rows: the app sets `app.current_user_id` for the
transaction, and unset it admits nothing. The policy's `USING` doubles as the
insert check, so a row can only be written as the transaction's user. Deleting
the user deletes the rows.

<!-- sql:user_tenant -->
```sql
CREATE TABLE user_tenant_settings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL DEFAULT current_tenant(),
    user_id uuid NOT NULL REFERENCES global_users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    setting_name text NOT NULL,
    setting_type text NOT NULL CHECK (setting_type IN ('str', 'int', 'bool', 'float', 'jsonb')),
    str_value text,
    int_value bigint,
    bool_value boolean,
    float_value double precision,
    jsonb_value jsonb,
    CHECK (num_nonnulls(str_value, int_value, bool_value, float_value, jsonb_value) = 1),
    CHECK ((setting_type = 'str') = (str_value IS NOT NULL)),
    CHECK ((setting_type = 'int') = (int_value IS NOT NULL)),
    CHECK ((setting_type = 'bool') = (bool_value IS NOT NULL)),
    CHECK ((setting_type = 'float') = (float_value IS NOT NULL)),
    CHECK ((setting_type = 'jsonb') = (jsonb_value IS NOT NULL))
);
SELECT go_db_apply_tenant_rls('user_tenant_settings');

CREATE POLICY user_isolation ON user_tenant_settings AS RESTRICTIVE
    USING (user_id = NULLIF(current_setting('app.current_user_id', true), '')::uuid);

CREATE INDEX user_tenant_settings_latest
    ON user_tenant_settings (tenant_id, user_id, setting_name, id DESC);
```

## tenant

The settings of a whole tenant. Tenant row-level security. `changed_by` is the
person who made the change, set to NULL if their account is removed, and
`changed_by_deleted` says that is why it is NULL (the app's account-removal
routine sets it). A row with both unset predates history being kept, or was
not made by a person.

<!-- sql:tenant -->
```sql
CREATE TABLE tenant_settings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL DEFAULT current_tenant(),
    created_at timestamptz NOT NULL DEFAULT now(),
    setting_name text NOT NULL,
    setting_type text NOT NULL CHECK (setting_type IN ('str', 'int', 'bool', 'float', 'jsonb')),
    str_value text,
    int_value bigint,
    bool_value boolean,
    float_value double precision,
    jsonb_value jsonb,
    changed_by uuid REFERENCES global_users (id) ON DELETE SET NULL,
    changed_by_deleted boolean NOT NULL DEFAULT false,
    CHECK (num_nonnulls(str_value, int_value, bool_value, float_value, jsonb_value) = 1),
    CHECK ((setting_type = 'str') = (str_value IS NOT NULL)),
    CHECK ((setting_type = 'int') = (int_value IS NOT NULL)),
    CHECK ((setting_type = 'bool') = (bool_value IS NOT NULL)),
    CHECK ((setting_type = 'float') = (float_value IS NOT NULL)),
    CHECK ((setting_type = 'jsonb') = (jsonb_value IS NOT NULL)),
    CHECK (NOT changed_by_deleted OR changed_by IS NULL)
);
SELECT go_db_apply_tenant_rls('tenant_settings');

CREATE INDEX tenant_settings_latest ON tenant_settings (tenant_id, setting_name, id DESC);
```
