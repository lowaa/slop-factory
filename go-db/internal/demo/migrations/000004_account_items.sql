-- +goose Up
-- account_items is a tenant-scoped child of accounts, demonstrating the
-- composite (tenant_id, parent_id) foreign key pattern backed by the
-- UNIQUE(tenant_id, id) on the parent.
CREATE TABLE account_items (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    account_id uuid NOT NULL,
    description text NOT NULL,
    amount_cents bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id)
);

CREATE INDEX account_items_tenant_id_idx ON account_items (tenant_id, id);
CREATE INDEX account_items_tenant_account_idx ON account_items (tenant_id, account_id);

SELECT go_db_apply_tenant_rls('account_items');

-- +goose Down
DROP TABLE IF EXISTS account_items;
