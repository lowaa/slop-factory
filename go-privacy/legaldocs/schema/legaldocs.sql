-- +goose Up
-- The published text of each legal document version. A global table: the
-- text is the same for every tenant (see go-privacy/README.md).

CREATE TABLE global_legal_document_versions (
    kind text NOT NULL,
    version text NOT NULL,
    locale text NOT NULL,
    effective date NOT NULL,
    material boolean NOT NULL,
    reviewed boolean NOT NULL,
    reviewed_by text NOT NULL DEFAULT '',
    reviewed_on date,
    body text NOT NULL,
    -- Over the metadata and the body, so a republish that differs in any
    -- field is caught by comparing one value.
    content_sha256 text NOT NULL,
    published_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, version, locale),
    CHECK (NOT reviewed OR (reviewed_by <> '' AND reviewed_on IS NOT NULL))
);

CREATE INDEX global_legal_document_versions_effective_idx
    ON global_legal_document_versions (kind, locale, effective DESC, version DESC);

-- A published version is kept exactly as it was, so what a person agreed to
-- can always be read. Revoking UPDATE and DELETE from the app role would not
-- hold: go-db's EnsureRole grants them again at every start.
-- +goose StatementBegin
CREATE FUNCTION global_legal_document_versions_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'global_legal_document_versions is immutable' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER global_legal_document_versions_no_change
    BEFORE UPDATE OR DELETE ON global_legal_document_versions
    FOR EACH ROW EXECUTE FUNCTION global_legal_document_versions_immutable();

CREATE TRIGGER global_legal_document_versions_no_truncate
    BEFORE TRUNCATE ON global_legal_document_versions
    FOR EACH STATEMENT EXECUTE FUNCTION global_legal_document_versions_immutable();

-- +goose Down
DROP TABLE IF EXISTS global_legal_document_versions;
DROP FUNCTION IF EXISTS global_legal_document_versions_immutable();
