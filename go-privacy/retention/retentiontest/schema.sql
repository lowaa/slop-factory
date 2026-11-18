-- +goose Up
-- A global table and a tenant table in the shape go-db's migrations give an
-- app's: the tenant one has row-level security on its tenant_id.
CREATE TABLE global_things (
    id uuid PRIMARY KEY,
    created_at timestamptz NOT NULL
);

CREATE TABLE tenant_things (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    created_at timestamptz NOT NULL
);
ALTER TABLE tenant_things ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_things FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_things
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

-- +goose Down
DROP TABLE tenant_things;
DROP TABLE global_things;
