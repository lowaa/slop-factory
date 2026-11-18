-- +goose Up
-- An agent's request that the supervisor running it review a head of the
-- attempt it holds (the review_request tool). One per attempt: a new request
-- replaces the last. Whether it is still open is read from review_runs, not
-- stored here: it closes once the runs a supervisor posted for the attempt
-- and head since the request cover every persona, so posting the runs is all
-- that closes it, and nothing can leave the two disagreeing.
CREATE TABLE review_requests (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    agent_id uuid NOT NULL,
    head_sha text NOT NULL,
    -- What the change is meant to do (its PR description), which the
    -- reviewers are given.
    intent text NOT NULL,
    requested_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (tenant_id, ticket_id, attempt_number),
    CONSTRAINT review_requests_head_sha_check CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    CONSTRAINT review_requests_intent_check CHECK (intent <> '' AND octet_length(intent) <= 65536),
    FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts (tenant_id, ticket_id, number) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX review_requests_by_agent ON review_requests (tenant_id, agent_id);

SELECT go_db_apply_tenant_rls('review_requests');

-- +goose Down
DROP TABLE IF EXISTS review_requests;
