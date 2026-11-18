-- +goose Up
-- The data subject request log. Global tables: a request is addressed to the
-- operator, not to a tenant, and its requester may have no account at all.
-- Nothing references global_users for the same reason: a request, an erasure
-- request above all, has to outlive the account it is about.

CREATE TABLE global_privacy_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    requester_name text NOT NULL DEFAULT '',
    requester_email text NOT NULL,
    request_type text NOT NULL CHECK (request_type IN (
        'access', 'rectification', 'erasure', 'restriction', 'objection', 'portability',
        'ccpa_know', 'ccpa_delete', 'ccpa_correct', 'ccpa_opt_out', 'ccpa_limit', 'other')),
    source text NOT NULL CHECK (source IN ('public_form', 'email', 'other')),
    authorised_agent boolean NOT NULL DEFAULT false,
    details text NOT NULL DEFAULT '',
    verified boolean NOT NULL DEFAULT false,
    verification_method text CHECK (verification_method IN ('email_reply', 'account_login', 'documents', 'other')),
    verified_at timestamptz,
    status text NOT NULL CHECK (status IN ('received', 'verifying', 'in_progress', 'completed', 'refused', 'withdrawn')),
    received_at timestamptz NOT NULL,
    due_at timestamptz NOT NULL,
    extended boolean NOT NULL DEFAULT false,
    extension_reason text NOT NULL DEFAULT '',
    closed_at timestamptz,
    alerted_soon_at timestamptz,
    alerted_overdue_at timestamptz,
    CHECK (verified = (verification_method IS NOT NULL)),
    CHECK ((verification_method IS NULL) = (verified_at IS NULL)),
    CHECK ((status IN ('completed', 'refused', 'withdrawn')) = (closed_at IS NOT NULL)),
    CHECK (due_at >= received_at)
);

-- What the operator's list and the alert check read: the open requests, soonest due first.
CREATE INDEX global_privacy_requests_open_due_idx
    ON global_privacy_requests (due_at) WHERE closed_at IS NULL;

-- A request's audit trail. Rows are never changed, and go only when their
-- request does (retention).
CREATE TABLE global_privacy_request_events (
    id bigserial PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES global_privacy_requests (id) ON DELETE CASCADE,
    at timestamptz NOT NULL DEFAULT now(),
    actor text NOT NULL,
    kind text NOT NULL CHECK (kind IN (
        'received', 'status_changed', 'verified', 'extended', 'note', 'alert_due_soon', 'alert_overdue')),
    from_status text,
    to_status text,
    detail text NOT NULL DEFAULT ''
);

CREATE INDEX global_privacy_request_events_request_idx
    ON global_privacy_request_events (request_id, id);

-- Enforced here and not only in code: go-db's EnsureRole grants the app role
-- UPDATE and DELETE again at every start, so revoking them would not hold. A
-- DELETE is allowed only when the request itself is already gone, which is
-- what ON DELETE CASCADE (retention deleting a request) looks like from here.
-- +goose StatementBegin
CREATE FUNCTION global_privacy_request_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND NOT EXISTS (SELECT 1 FROM global_privacy_requests WHERE id = OLD.request_id) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'global_privacy_request_events is append-only' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER global_privacy_request_events_guard
    BEFORE UPDATE OR DELETE ON global_privacy_request_events
    FOR EACH ROW EXECUTE FUNCTION global_privacy_request_events_guard();

-- +goose Down
DROP TABLE IF EXISTS global_privacy_request_events;
DROP TABLE IF EXISTS global_privacy_requests;
DROP FUNCTION IF EXISTS global_privacy_request_events_guard();
