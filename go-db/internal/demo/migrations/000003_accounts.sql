-- +goose Up
-- accounts is tenant-scoped: table creation, tenant_id, indexes, and RLS all
-- land in this one migration together, never split across files.
CREATE TABLE accounts (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    currency_code text NOT NULL REFERENCES global_currencies (code),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    UNIQUE (tenant_id, id)
);

CREATE INDEX accounts_tenant_id_idx ON accounts (tenant_id, id);

SELECT go_db_apply_tenant_rls('accounts');

-- +goose Down
DROP TABLE IF EXISTS accounts;
