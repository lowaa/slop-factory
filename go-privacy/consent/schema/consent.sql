-- +goose Up
-- The consent record. Global tables: consent belongs to a person, not to a
-- tenant (see go-privacy/README.md).

CREATE TABLE global_consent_events (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES global_users (id) ON DELETE RESTRICT,
    purpose text NOT NULL,
    granted boolean NOT NULL,
    document_kind text NOT NULL,
    document_version text NOT NULL,
    source text NOT NULL CHECK (source IN ('banner', 'settings', 'sign_up', 'prompt', 'gpc', 'migration')),
    decided_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    merged_from_anonymous boolean NOT NULL DEFAULT false,
    merge_token text,
    session_id text,
    CHECK (merged_from_anonymous = (merge_token IS NOT NULL))
);

CREATE INDEX global_consent_events_current_idx
    ON global_consent_events (user_id, purpose, decided_at DESC, id DESC);

-- A cookie's decisions land in one account at most once.
CREATE UNIQUE INDEX global_consent_events_merge_token_idx
    ON global_consent_events (merge_token, purpose) WHERE merge_token IS NOT NULL;

-- Every merge token ever presented, hashed, even when nothing was merged.
CREATE TABLE global_consent_merge_tokens (
    token_hash text PRIMARY KEY,
    claimed_at timestamptz NOT NULL DEFAULT now()
);

-- Append-only is enforced here and not only in code. Revoking UPDATE and
-- DELETE from the app role would not hold: go-db's EnsureRole grants them
-- again at every start.
-- +goose StatementBegin
CREATE FUNCTION global_consent_events_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'global_consent_events is append-only' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER global_consent_events_no_change
    BEFORE UPDATE OR DELETE ON global_consent_events
    FOR EACH ROW EXECUTE FUNCTION global_consent_events_append_only();

CREATE TRIGGER global_consent_events_no_truncate
    BEFORE TRUNCATE ON global_consent_events
    FOR EACH STATEMENT EXECUTE FUNCTION global_consent_events_append_only();

-- +goose Down
DROP TABLE IF EXISTS global_consent_merge_tokens;
DROP TABLE IF EXISTS global_consent_events;
DROP FUNCTION IF EXISTS global_consent_events_append_only();
