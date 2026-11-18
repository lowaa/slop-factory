-- +goose Up
-- The schema the documented tables expect of an app (TABLES.md), reduced to
-- what the tests need.
CREATE TABLE global_tenants (id uuid PRIMARY KEY, name text NOT NULL DEFAULT '');
CREATE TABLE global_users (id uuid PRIMARY KEY);
CREATE TABLE global_tenant_memberships (
    tenant uuid NOT NULL REFERENCES global_tenants (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES global_users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant, user_id)
);

-- +goose StatementBegin
CREATE FUNCTION current_tenant() RETURNS uuid AS $$
    SELECT NULLIF(current_setting('app.current_tenant', true), '')::uuid
$$ LANGUAGE sql STABLE;
-- +goose StatementEnd

-- +goose StatementBegin
-- go_db_apply_tenant_rls centralizes the ENABLE/FORCE ROW LEVEL SECURITY +
-- CREATE POLICY boilerplate every tenant-scoped table needs, so a migration
-- creating one is a single call instead of three near-identical statements
-- repeated verbatim per table.
--
-- current_setting(..., true) resolves to NULL the first time a session
-- reads app.current_tenant without ever having set it -- but pooled
-- connections get reused across many transactions/tenants, and Postgres's
-- placeholder GUCs revert to '' (empty string), not NULL, once they've been
-- set at least once and that transaction ends. NULLIF folds both "never
-- set" (NULL) and "set earlier on this pooled connection, now cleared" ('')
-- into NULL before the uuid cast, so an unscoped query still sees zero rows
-- (fail closed) in both cases, and the cast doesn't error on an empty
-- string.
--
-- A separate dollar-quote tag ($policy$) is used for the inner format()
-- literal so the policy's own single-quoted string literals don't need to
-- be escaped. It also has to differ from goose's own StatementBegin/End
-- wrapper, which only cares about the semicolons outside these blocks, not
-- what quoting is used inside them.
CREATE OR REPLACE FUNCTION go_db_apply_tenant_rls(table_name text) RETURNS void AS $$
BEGIN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format(
        $policy$CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)$policy$,
        table_name
    );
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd


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

-- +goose Down
DROP TABLE tenant_settings, user_tenant_settings, global_user_settings, global_tenant_memberships, global_users, global_tenants;
DROP FUNCTION go_db_apply_tenant_rls(text);
DROP FUNCTION current_tenant();
