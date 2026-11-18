-- +goose Up
CREATE TABLE widgets (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    PRIMARY KEY (id),
    UNIQUE (tenant_id, id)
);

CREATE INDEX widgets_tenant_id_idx ON widgets (tenant_id, id);

ALTER TABLE widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE widgets FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON widgets
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

-- +goose Down
DROP TABLE IF EXISTS widgets;
