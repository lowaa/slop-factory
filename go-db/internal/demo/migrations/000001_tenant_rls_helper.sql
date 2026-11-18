-- +goose Up
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

-- +goose Down
DROP FUNCTION go_db_apply_tenant_rls(text);
