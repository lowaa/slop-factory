-- +goose Up
-- slop-review runs as they were posted (slop-review/report): by a supervisor
-- for the agents it runs, or by an agent for itself. The id is the client's:
-- a re-post of a spooled run finds its row instead of adding one. A run is
-- filed under a ticket's attempt only when, as it was posted, the attempt
-- was held by the posting agent or by an agent the posting supervisor runs;
-- otherwise ticket_id and attempt_number are null.
CREATE TABLE review_runs (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    posted_by_kind text NOT NULL,
    posted_by_id uuid NOT NULL,
    ticket_id bigint,
    attempt_number integer,
    kind text NOT NULL,
    status text NOT NULL,
    branch text NOT NULL,
    head_sha text NOT NULL,
    personas text[] NOT NULL,
    verdict text NOT NULL,
    incomplete boolean NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    received_at timestamptz DEFAULT now() NOT NULL,
    -- The run as posted, less head_author (a person's name and email the
    -- server has no use for).
    report jsonb NOT NULL,
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT review_runs_attempt_check CHECK ((ticket_id IS NULL) = (attempt_number IS NULL)),
    CONSTRAINT review_runs_posted_by_kind_check CHECK (posted_by_kind IN ('agent', 'supervisor')),
    CONSTRAINT review_runs_kind_check CHECK (kind IN ('review', 'bench')),
    CONSTRAINT review_runs_status_check CHECK (status IN ('completed', 'failed')),
    CONSTRAINT review_runs_verdict_check CHECK (verdict IN ('', 'approve', 'approve_with_comments', 'request_changes', 'incomplete')),
    CONSTRAINT review_runs_no_head_author CHECK (NOT (report ? 'head_author')),
    -- A supervisor is an agents row too.
    FOREIGN KEY (tenant_id, posted_by_id) REFERENCES agents (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts (tenant_id, ticket_id, number) ON DELETE CASCADE
);

CREATE INDEX review_runs_by_attempt ON review_runs (tenant_id, ticket_id, attempt_number, finished_at DESC)
    WHERE ticket_id IS NOT NULL;
CREATE INDEX review_runs_by_poster ON review_runs (tenant_id, posted_by_id);

SELECT go_db_apply_tenant_rls('review_runs');

-- The merged findings of a run; also in its report, kept here so a finding
-- can be labelled and counted by id.
CREATE TABLE review_findings (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    run_id uuid NOT NULL,
    position integer NOT NULL,
    file text NOT NULL,
    line integer NOT NULL,
    severity text NOT NULL,
    summary text NOT NULL,
    detail text NOT NULL,
    personas text[] NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, run_id, position),
    CONSTRAINT review_findings_severity_check CHECK (severity IN ('blocker', 'major', 'minor', 'nit')),
    FOREIGN KEY (tenant_id, run_id) REFERENCES review_runs (tenant_id, id) ON DELETE CASCADE
);

SELECT go_db_apply_tenant_rls('review_findings');

-- What an agent said of a finding -- the one holding the run's attempt, or
-- the one that posted the run: append-only, the newest row per finding is
-- its current label.
CREATE TABLE review_finding_labels (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint GENERATED ALWAYS AS IDENTITY NOT NULL,
    finding_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    verdict text NOT NULL,
    reason text NOT NULL,
    fixed_in text,
    labelled_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT review_finding_labels_verdict_check CHECK (verdict IN ('confirmed', 'false_positive', 'fabricated', 'duplicate', 'wont_fix')),
    CONSTRAINT review_finding_labels_fixed_in_check CHECK (fixed_in IS NULL OR verdict = 'confirmed'),
    FOREIGN KEY (tenant_id, finding_id) REFERENCES review_findings (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX review_finding_labels_current ON review_finding_labels (tenant_id, finding_id, id DESC);

SELECT go_db_apply_tenant_rls('review_finding_labels');

-- +goose Down
DROP TABLE IF EXISTS review_finding_labels;
DROP TABLE IF EXISTS review_findings;
DROP TABLE IF EXISTS review_runs;
