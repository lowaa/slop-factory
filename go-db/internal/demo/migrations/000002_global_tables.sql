-- +goose Up
-- global_tenants is the tenant registry itself, and global_currencies is
-- static reference data. Both are genuinely global: no tenant_id column,
-- no RLS, reachable via a plain Querier.
CREATE TABLE global_tenants (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE global_currencies (
    code text PRIMARY KEY,
    symbol text NOT NULL
);

INSERT INTO global_currencies (code, symbol) VALUES
    ('USD', '$'),
    ('EUR', '€'),
    ('GBP', '£');

-- +goose Down
DROP TABLE IF EXISTS global_currencies;
DROP TABLE IF EXISTS global_tenants;
