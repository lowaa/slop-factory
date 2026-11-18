-- +goose Up

-- The whole schema as of version 172, in one file. It replaces migrations
-- 000007-000172, which built it up step by step; their history is not
-- carried here.
--
-- Numbered 172, the version of the last migration it replaces, on purpose.
-- A database already at 172 has that version recorded, so goose counts this
-- as applied and changes nothing. A fresh database applies only this file.
-- A database below 172 must first be brought to 172 by the release that
-- still has the step-by-step migrations; this file cannot upgrade it.
--
-- Two kinds of table, told apart by name and checked by go-db's lint
-- (migrations.LintGlobalTenantInvariants) and by the live schema check:
--
-- * global_ tables hold what has to be read before any tenant is known --
--   who a user is at login, which tenant an agent's token belongs to. No
--   tenant_id, no RLS.
-- * everything else is one tenant's data: a tenant_id column, and row level
--   security so a transaction scoped to one tenant cannot read or write
--   another's rows, whatever the query says.
--
-- Names are unqualified, as in every migration: the lint matches bare table
-- names, and the migration runs with the default search_path.

-- Functions are created before the tables their bodies name. LOCAL: this
-- lasts only for the migration's own transaction.
SET LOCAL check_function_bodies = false;

-- +goose StatementBegin
CREATE FUNCTION agents_archived_revoke_grants() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE global_oauth_grants SET revoked_at = now()
     WHERE agent_id = NEW.id AND tenant = NEW.tenant_id AND revoked_at IS NULL;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_kind_owner_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.kind = OLD.kind AND OLD.owner_user_id IS NOT NULL AND NEW.owner_user_id IS NULL
       AND NOT EXISTS (SELECT 1 FROM global_users WHERE id = OLD.owner_user_id) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'agents.kind and agents.owner_user_id cannot change once registered'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_owner_is_member() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    PERFORM 1 FROM global_tenant_memberships
     WHERE tenant = NEW.tenant_id AND user_id = NEW.owner_user_id
       FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'the owner of a conductor must be a member of its tenant'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_run_by_supervisor_is_supervisor() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM 1 FROM agents WHERE tenant_id = NEW.tenant_id AND id = NEW.run_by_supervisor_id AND kind = 'supervisor';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'the supervisor running a remote control must be a supervisor agent in its tenant'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_steward_minted_by_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'agents.steward_minted_by cannot change once registered'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_steward_minted_by_supervisor() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM 1 FROM agents WHERE tenant_id = NEW.tenant_id AND id = NEW.steward_minted_by
        AND kind = 'supervisor' AND disabled_at IS NULL AND archived_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'a steward must be minted by a live supervisor agent in its tenant'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_supervisor_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'agents.supervisor_agent_id cannot change once registered'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agents_supervisor_is_supervisor() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM 1 FROM agents WHERE tenant_id = NEW.tenant_id AND id = NEW.supervisor_agent_id AND kind = 'supervisor';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'the supervisor of a slot must be a supervisor agent in its tenant'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION attachments_after_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO attachment_delete_queue (tenant_id, storage_key, reason)
        VALUES (OLD.tenant_id, OLD.storage_key, 'deleted')
        ON CONFLICT (tenant_id, storage_key) DO UPDATE
            SET reason = 'deleted', queued_at = now(), attempts = 0, last_error = NULL, swept_at = NULL;
    UPDATE attachment_usage SET bytes = GREATEST(bytes - OLD.size_bytes, 0) WHERE tenant_id = OLD.tenant_id;
    RETURN OLD;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION attachments_after_insert() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    DELETE FROM attachment_reservations WHERE tenant_id = NEW.tenant_id AND storage_key = NEW.storage_key;
    INSERT INTO attachment_usage (tenant_id, bytes) VALUES (NEW.tenant_id, NEW.size_bytes)
        ON CONFLICT (tenant_id) DO UPDATE SET bytes = attachment_usage.bytes + EXCLUDED.bytes;
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION attachments_require_reservation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    -- Locked, so the sweeper's expiry (which skips locked rows) cannot queue the
    -- object for deletion while this insert is still to commit.
    PERFORM 1 FROM attachment_reservations r
        WHERE r.tenant_id = NEW.tenant_id AND r.storage_key = NEW.storage_key FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'attachment % has no reservation for %', NEW.id, NEW.storage_key
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION attempt_status_for_ticket(ticket_status text) RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT CASE ticket_status
        WHEN 'preparing' THEN 'queued'
        WHEN 'parked' THEN 'queued'
        WHEN 'open' THEN 'queued'
        WHEN 'blocked' THEN 'queued'
        WHEN 'in_progress' THEN 'in_progress'
        WHEN 'in_merge_queue' THEN 'in_merge_queue'
        WHEN 'discussion' THEN 'discussion'
        WHEN 'review' THEN 'review'
        WHEN 'done' THEN 'done'
        WHEN 'cancelled' THEN 'closed'
    END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION bump_board_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_tenant uuid;
    changed_row jsonb;
    changed_ticket bigint;
    new_revision bigint;
    keep_revisions CONSTANT bigint := 1000;
    previous text := COALESCE(current_setting('app.current_tenant', true), '');
BEGIN
    IF TG_OP = 'DELETE' THEN
        changed_tenant := OLD.tenant_id;
        changed_row := to_jsonb(OLD);
    ELSE
        changed_tenant := NEW.tenant_id;
        changed_row := to_jsonb(NEW);
    END IF;
    PERFORM set_config('app.current_tenant', changed_tenant::text, true);
    IF current_setting('app.board_revision_bumped', true) IS DISTINCT FROM changed_tenant::text THEN
        INSERT INTO board_revisions (tenant_id, revision) VALUES (changed_tenant, 1)
        ON CONFLICT (tenant_id) DO UPDATE SET revision = board_revisions.revision + 1
        RETURNING revision INTO new_revision;
        PERFORM set_config('app.board_revision_bumped', changed_tenant::text, true);
        DELETE FROM board_changes WHERE tenant_id = changed_tenant AND revision <= new_revision - keep_revisions;
    ELSE
        SELECT revision INTO new_revision FROM board_revisions WHERE tenant_id = changed_tenant;
    END IF;

    IF TG_TABLE_NAME = 'tickets' THEN
        changed_ticket := (changed_row->>'id')::bigint;
    ELSE
        changed_ticket := COALESCE((changed_row->>'ticket_id')::bigint, 0);
    END IF;
    INSERT INTO board_changes (tenant_id, revision, ticket_id) VALUES (changed_tenant, new_revision, changed_ticket)
    ON CONFLICT DO NOTHING;
    IF TG_TABLE_NAME = 'tickets' AND TG_OP <> 'INSERT' THEN
        INSERT INTO board_changes (tenant_id, revision, ticket_id)
        SELECT changed_tenant, new_revision, d.ticket_id FROM ticket_deps d
         WHERE d.tenant_id = changed_tenant AND d.depends_on_ticket_id = changed_ticket
        ON CONFLICT DO NOTHING;
    END IF;
    PERFORM set_config('app.current_tenant', previous, true);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION clear_attempt_without_ticket() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.ticket_id IS NULL THEN
        NEW.attempt_number := NULL;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION create_first_attempt() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO attempts (tenant_id, ticket_id, number, status, promoted_at, created_at, updated_at, closed_at)
    VALUES (NEW.tenant_id, NEW.id, 1, attempt_status_for_ticket(NEW.status), NEW.created_at, NEW.created_at,
            NEW.updated_at, NEW.closed_at);
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION current_tenant() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$
    SELECT NULLIF(current_setting('app.current_tenant', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION deliver_human_comment() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    holder uuid;
    target integer;
BEGIN
    FOR holder, target IN
        SELECT l.agent_id, l.attempt_number FROM leases l
         WHERE l.tenant_id = NEW.tenant_id AND l.ticket_id = NEW.ticket_id
           AND (NEW.attempt_number IS NULL OR l.attempt_number = NEW.attempt_number)
           AND l.released_at IS NULL
           AND (l.expires_at > now()
                OR EXISTS (SELECT 1 FROM locks lk WHERE lk.tenant_id = l.tenant_id AND lk.holder_lease_id = l.id))
         ORDER BY l.attempt_number
         -- A release racing this comment waits for it, so its drop sees the copy.
           FOR SHARE OF l
    LOOP
        CONTINUE WHEN holder = NEW.author_agent_id;
        PERFORM pg_advisory_xact_lock(hashtextextended('agent_messages:' || holder::text, 0));
        CONTINUE WHEN (SELECT count(*) FROM agent_messages
                        WHERE tenant_id = NEW.tenant_id AND agent_id = holder
                          AND delivered_at IS NULL AND dropped_at IS NULL) >= 20;
        INSERT INTO agent_messages (tenant_id, agent_id, kind, author_user_id, via_agent_id, ticket_id, attempt_number, body, created_at)
        VALUES (NEW.tenant_id, holder, 'ticket_comment', COALESCE(NEW.author_user_id, NEW.on_behalf_of_user_id),
                CASE WHEN NEW.on_behalf_of_user_id IS NOT NULL THEN NEW.author_agent_id END,
                NEW.ticket_id, target,
                CASE WHEN char_length(NEW.body) > 4000
                     THEN left(NEW.body, 4000) || E'\n[comment cut short -- read the rest on the ticket]'
                     ELSE NEW.body END,
                NEW.created_at);
    END LOOP;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TABLE agents (
    id uuid NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    disabled_at timestamp with time zone,
    kind text DEFAULT 'worker'::text NOT NULL,
    owner_user_id uuid,
    variant_id uuid,
    variant_revision bigint,
    owner_user_id_deleted boolean DEFAULT false NOT NULL,
    archived_at timestamp with time zone,
    replaces_agent_id uuid,
    supervisor_agent_id uuid,
    slot_cap integer,
    steward_minted_by uuid,
    observed_phase text,
    observed_reason text,
    observed_at timestamp with time zone,
    run_by_supervisor_id uuid,
    CONSTRAINT agents_archived_is_disabled CHECK (((archived_at IS NULL) OR (disabled_at IS NOT NULL))),
    CONSTRAINT agents_kind_check CHECK ((kind = ANY (ARRAY['worker'::text, 'conductor'::text, 'console'::text, 'supervisor'::text, 'steward'::text]))),
    CONSTRAINT agents_observed_is_whole CHECK ((((observed_phase IS NULL) = (observed_at IS NULL)) AND ((observed_phase IS NULL) = (observed_reason IS NULL)))),
    CONSTRAINT agents_observed_only_steward CHECK (((observed_phase IS NULL) OR (kind = 'steward'::text))),
    CONSTRAINT agents_observed_phase_check CHECK ((observed_phase = ANY (ARRAY['provisioning'::text, 'running'::text, 'paused'::text, 'failed'::text]))),
    CONSTRAINT agents_observed_reason_check CHECK ((char_length(observed_reason) <= 500)),
    CONSTRAINT agents_owner_iff_conductor CHECK (((kind = 'conductor'::text) = ((owner_user_id IS NOT NULL) OR owner_user_id_deleted))),
    CONSTRAINT agents_owner_user_id_deleted_is_null CHECK (((NOT owner_user_id_deleted) OR (owner_user_id IS NULL))),
    CONSTRAINT agents_replaces_not_self CHECK (((replaces_agent_id IS NULL) OR (replaces_agent_id <> id))),
    CONSTRAINT agents_run_by_supervisor_is_conductor CHECK (((run_by_supervisor_id IS NULL) OR (kind = 'conductor'::text))),
    CONSTRAINT agents_slot_cap_iff_supervisor CHECK (((kind = 'supervisor'::text) = (slot_cap IS NOT NULL))),
    CONSTRAINT agents_slot_cap_range CHECK (((slot_cap IS NULL) OR ((slot_cap >= 1) AND (slot_cap <= 16)))),
    CONSTRAINT agents_slot_is_worker CHECK (((supervisor_agent_id IS NULL) OR (kind = 'worker'::text))),
    CONSTRAINT agents_steward_iff_minted CHECK (((kind = 'steward'::text) = (steward_minted_by IS NOT NULL))),
    CONSTRAINT agents_variant_revision CHECK (((variant_id IS NULL) = (variant_revision IS NULL)))
);

ALTER TABLE agents FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
CREATE FUNCTION disable_agent(p_agent uuid, p_reason text) RETURNS SETOF agents
    LANGUAGE plpgsql
    AS $$
DECLARE
    disabled agents;
    ended uuid;
    ended_seq bigint;
    work_seq bigint;
BEGIN
    UPDATE agents SET disabled_at = COALESCE(disabled_at, now())
     WHERE id = p_agent
     RETURNING * INTO disabled;
    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE pending_actions SET status = 'voided', outcome = p_reason, decided_at = now()
     WHERE agent_id = p_agent AND status = 'pending' AND expires_at > now();

    UPDATE global_oauth_grants SET revoked_at = now()
     WHERE (disabled.kind IN ('conductor', 'supervisor', 'steward') OR disabled.supervisor_agent_id IS NOT NULL)
       AND agent_id = p_agent AND tenant = disabled.tenant_id AND revoked_at IS NULL;

    IF disabled.kind = 'supervisor' THEN
        PERFORM disable_supervisor_slots(p_agent, 'supervisor disabled');
    END IF;

    SELECT id INTO ended FROM conductor_conversations
     WHERE conductor_id = p_agent AND state IN ('starting', 'ready')
       FOR UPDATE;
    IF FOUND THEN
        UPDATE conductor_conversations
           SET state = 'stopped', next_seq = next_seq + 1, updated_at = now()
         WHERE id = ended
        RETURNING next_seq INTO ended_seq;
        INSERT INTO conductor_conversation_events (conversation_id, seq, kind, body)
        VALUES (ended, ended_seq, 'stopped', p_reason);

        INSERT INTO conductor_queues (conductor_id, next_seq) VALUES (p_agent, 1)
        ON CONFLICT (tenant_id, conductor_id) DO UPDATE SET next_seq = conductor_queues.next_seq + 1
        RETURNING next_seq INTO work_seq;
        INSERT INTO conductor_work (conductor_id, seq, kind, conversation_id)
        VALUES (p_agent, work_seq, 'stop', ended);
    END IF;

    RETURN NEXT disabled;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION disable_supervisor_slots(p_supervisor uuid, p_reason text) RETURNS SETOF agents
    LANGUAGE plpgsql
    AS $$
DECLARE
    slot_id uuid;
    disabled agents;
BEGIN
    PERFORM 1 FROM agents WHERE id = p_supervisor AND kind = 'supervisor' FOR UPDATE;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    FOR slot_id IN
        SELECT id FROM agents WHERE supervisor_agent_id = p_supervisor AND disabled_at IS NULL ORDER BY id
    LOOP
        SELECT * INTO disabled FROM disable_agent(slot_id, p_reason);
        RETURN NEXT disabled;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION drop_messages_of_ended_lease() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE agent_messages SET dropped_at = now(), dropped_reason = 'lease_ended'
     WHERE tenant_id = NEW.tenant_id AND agent_id = NEW.agent_id
       AND ticket_id = NEW.ticket_id AND attempt_number = NEW.attempt_number
       AND delivered_at IS NULL AND dropped_at IS NULL;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION fleet_instruction_versions_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.created_by IS NOT NULL AND NEW.created_by IS NULL
       AND NOT EXISTS (SELECT 1 FROM global_users WHERE id = OLD.created_by)
       AND to_jsonb(NEW) - 'created_by' - 'created_by_deleted' = to_jsonb(OLD) - 'created_by' - 'created_by_deleted' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'fleet instruction versions are immutable' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_consent_events_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'global_consent_events is append-only' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_legal_document_versions_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'global_legal_document_versions is immutable' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_oauth_grants_agent_available() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    previous text := current_setting('app.current_tenant', true);
    unavailable boolean;
BEGIN
    PERFORM set_config('app.current_tenant', NEW.tenant::text, true);
    SELECT disabled_at IS NOT NULL OR archived_at IS NOT NULL INTO unavailable
      FROM agents WHERE tenant_id = NEW.tenant AND id = NEW.agent_id FOR SHARE;
    PERFORM set_config('app.current_tenant', COALESCE(previous, ''), true);
    IF unavailable THEN
        RAISE EXCEPTION 'agent % is disabled or archived and cannot be granted access', NEW.agent_id
            USING ERRCODE = '23A01';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_tenant_memberships_clear_former() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    DELETE FROM global_former_memberships WHERE tenant = NEW.tenant AND user_id = NEW.user_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_tenant_memberships_disable_conductors() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    previous text := COALESCE(current_setting('app.current_tenant', true), '');
BEGIN
    PERFORM set_config('app.current_tenant', OLD.tenant::text, true);

    IF NOT EXISTS (SELECT 1 FROM agents WHERE tenant_id = OLD.tenant AND owner_user_id = OLD.user_id) THEN
        PERFORM set_config('app.current_tenant', previous, true);
        RETURN OLD;
    END IF;
    IF previous NOT IN ('', OLD.tenant::text) THEN
        RAISE EXCEPTION 'a member who owns conductors cannot be removed from inside another tenant''s scope'
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    PERFORM disable_agent(id, 'owner left the tenant')
       FROM agents WHERE tenant_id = OLD.tenant AND owner_user_id = OLD.user_id;

    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_tenant_memberships_record_former() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM global_users WHERE id = OLD.user_id)
       AND EXISTS (SELECT 1 FROM global_tenants WHERE id = OLD.tenant) THEN
        INSERT INTO global_former_memberships (tenant, user_id)
        VALUES (OLD.tenant, OLD.user_id)
        ON CONFLICT (tenant, user_id) DO UPDATE SET left_at = now();
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_tenants_seed_sensitive_words() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM seed_sensitive_words(NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION global_users_clear_unkeyed_actors() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    previous text := COALESCE(current_setting('app.current_tenant', true), '');
    tenant uuid;
BEGIN
    FOR tenant IN SELECT id FROM global_tenants LOOP
        PERFORM set_config('app.current_tenant', tenant::text, true);
        UPDATE ticket_status_changes SET actor_id = NULL, actor_id_deleted = true
         WHERE actor_kind = 'user' AND actor_id = OLD.id;
        UPDATE attempt_status_changes SET actor_id = NULL, actor_id_deleted = true
         WHERE actor_kind = 'user' AND actor_id = OLD.id;
        UPDATE harness_config SET updated_by = NULL, updated_by_deleted = true
         WHERE updated_by = OLD.id;
        DELETE FROM codehost_setup_states WHERE user_id = OLD.id;
    END LOOP;
    PERFORM set_config('app.current_tenant', previous, true);
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION go_db_apply_tenant_rls(table_name text) RETURNS void
    LANGUAGE plpgsql
    AS $_$
BEGIN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format(
        $policy$CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)$policy$,
        table_name
    );
END;
$_$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION go_db_with_rls_lifted(tables text[], body text) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    tbl text;
    rel regclass;
    forced regclass[] := '{}';
BEGIN
    FOREACH tbl IN ARRAY tables LOOP
        rel := to_regclass(tbl);
        IF rel IS NULL THEN
            RAISE EXCEPTION 'go_db_with_rls_lifted: no table %', tbl;
        END IF;
        IF (SELECT relforcerowsecurity FROM pg_class WHERE oid = rel) THEN
            forced := forced || rel;
            EXECUTE format('ALTER TABLE %s NO FORCE ROW LEVEL SECURITY', rel);
        END IF;
    END LOOP;

    EXECUTE body;

    FOREACH rel IN ARRAY forced LOOP
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', rel);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION lease_configs_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'a lease''s recorded configuration is immutable' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION lock_holds_close() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE lock_holds SET released_at = GREATEST(clock_timestamp(), acquired_at)
     WHERE tenant_id = OLD.tenant_id AND name = OLD.name AND released_at IS NULL;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION lock_holds_open() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO lock_holds (tenant_id, name, ticket_id, agent_id, queued_at, acquired_at)
    SELECT NEW.tenant_id, NEW.name, l.ticket_id, NEW.holder_agent_id,
           (SELECT w.queued_at FROM lock_waiters w
             WHERE w.tenant_id = NEW.tenant_id AND w.name = NEW.name AND w.agent_id = NEW.holder_agent_id),
           NEW.acquired_at
      FROM leases l
     WHERE l.tenant_id = NEW.tenant_id AND l.id = NEW.holder_lease_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION mark_deleted_actors() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    col text;
BEGIN
    FOREACH col IN ARRAY TG_ARGV LOOP
        IF to_jsonb(OLD) ->> col IS NOT NULL
           AND to_jsonb(NEW) ->> col IS NULL
           AND NOT EXISTS (SELECT 1 FROM global_users WHERE id = (to_jsonb(OLD) ->> col)::uuid) THEN
            NEW := jsonb_populate_record(NEW, jsonb_build_object(col || '_deleted', true));
        ELSIF to_jsonb(OLD) ->> col IS NULL AND to_jsonb(NEW) ->> col IS NOT NULL
              AND (to_jsonb(OLD) ->> (col || '_deleted'))::boolean THEN
            NEW := jsonb_populate_record(NEW, jsonb_build_object(col || '_deleted', false));
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION next_ticket_id() RETURNS bigint
    LANGUAGE sql
    AS $$
    INSERT INTO ticket_sequences (last_id) VALUES (1)
    ON CONFLICT (tenant_id) DO UPDATE SET last_id = ticket_sequences.last_id + 1
    RETURNING last_id
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION record_attempt_status_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NULL;
    END IF;
    INSERT INTO attempt_status_changes
           (tenant_id, ticket_id, attempt_number, from_status, to_status, priority, changed_at, actor_kind, actor_id, cause)
    SELECT NEW.tenant_id, NEW.ticket_id, NEW.number,
           CASE WHEN TG_OP = 'UPDATE' THEN OLD.status END,
           NEW.status, t.priority, now(),
           COALESCE(NULLIF(current_setting('slophaus.actor_kind', true), ''), 'system'),
           NULLIF(current_setting('slophaus.actor_id', true), '')::uuid,
           NULLIF(current_setting('slophaus.cause', true), '')
      FROM tickets t WHERE t.tenant_id = NEW.tenant_id AND t.id = NEW.ticket_id;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION record_ticket_status_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NULL;
    END IF;
    INSERT INTO ticket_status_changes
           (tenant_id, ticket_id, from_status, to_status, priority, changed_at, actor_kind, actor_id, cause)
    VALUES (NEW.tenant_id, NEW.id,
            CASE WHEN TG_OP = 'UPDATE' THEN OLD.status END,
            NEW.status, NEW.priority, now(),
            COALESCE(NULLIF(current_setting('slophaus.actor_kind', true), ''), 'system'),
            NULLIF(current_setting('slophaus.actor_id', true), '')::uuid,
            NULLIF(current_setting('slophaus.cause', true), ''));
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION seed_sensitive_words(p_tenant uuid) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    previous text := coalesce(current_setting('app.current_tenant', true), '');
    version_id uuid := gen_random_uuid();
BEGIN
    PERFORM set_config('app.current_tenant', p_tenant::text, true);
    IF NOT EXISTS (SELECT 1 FROM fleet_instruction_versions WHERE kind = 'sensitive_words') THEN
        INSERT INTO fleet_instruction_versions (id, kind, name, version, body)
        VALUES (version_id, 'sensitive_words', 'sensitive words', 1, array_to_string(ARRAY[
            'secret', 'secrets', 'token', 'password', 'credential', 'api key', 'private key',
            'prod', 'production', 'real user', 'delete', 'drop', 'truncate', 'purge', 'erase',
            'restore', 'billing', 'cost', 'spend', 'invoice', 'payment'], E'\n'));
        INSERT INTO fleet_instruction_defaults (kind, version_id) VALUES ('sensitive_words', version_id);
    END IF;
    PERFORM set_config('app.current_tenant', previous, true);
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION sync_github_app_installation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM global_github_app_installations WHERE tenant = OLD.tenant_id;
        RETURN OLD;
    END IF;
    IF NEW.installation_id IS NULL OR NEW.webhook_secret_enc IS NULL THEN
        DELETE FROM global_github_app_installations WHERE tenant = NEW.tenant_id;
        RETURN NEW;
    END IF;
    INSERT INTO global_github_app_installations (tenant, installation_id, credential_id, webhook_secret_enc)
    VALUES (NEW.tenant_id, NEW.installation_id, NEW.id, NEW.webhook_secret_enc)
    ON CONFLICT (tenant) DO UPDATE
       SET installation_id = EXCLUDED.installation_id, credential_id = EXCLUDED.credential_id,
           webhook_secret_enc = EXCLUDED.webhook_secret_enc;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION variant_versions_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.created_by IS NOT NULL AND NEW.created_by IS NULL
       AND NOT EXISTS (SELECT 1 FROM global_users WHERE id = OLD.created_by)
       AND to_jsonb(NEW) - 'created_by' - 'created_by_deleted' = to_jsonb(OLD) - 'created_by' - 'created_by_deleted' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'variant versions are immutable' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

CREATE TABLE agent_contexts (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    agent_id uuid NOT NULL,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    attempt_number integer NOT NULL,
    CONSTRAINT agent_contexts_body_check CHECK ((body <> ''::text))
);

ALTER TABLE agent_contexts FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE agent_contexts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE agent_contexts_id_seq OWNED BY agent_contexts.id;

CREATE TABLE agent_messages (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    agent_id uuid NOT NULL,
    kind text NOT NULL,
    author_user_id uuid,
    ticket_id bigint,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    delivered_at timestamp with time zone,
    attempt_number integer,
    via_agent_id uuid,
    author_user_id_deleted boolean DEFAULT false NOT NULL,
    dropped_at timestamp with time zone,
    dropped_reason text,
    CONSTRAINT agent_messages_attempt_with_ticket CHECK (((ticket_id IS NOT NULL) = (attempt_number IS NOT NULL))),
    CONSTRAINT agent_messages_author_required CHECK (((author_user_id IS NOT NULL) OR author_user_id_deleted)),
    CONSTRAINT agent_messages_author_user_id_deleted_is_null CHECK (((NOT author_user_id_deleted) OR (author_user_id IS NULL))),
    CONSTRAINT agent_messages_check CHECK (((kind = 'ticket_comment'::text) = (ticket_id IS NOT NULL))),
    CONSTRAINT agent_messages_delivered_or_dropped CHECK (((delivered_at IS NULL) OR (dropped_at IS NULL))),
    CONSTRAINT agent_messages_dropped_reason CHECK (((dropped_at IS NULL) = (dropped_reason IS NULL))),
    CONSTRAINT agent_messages_kind_check CHECK ((kind = ANY (ARRAY['message'::text, 'ticket_comment'::text])))
);

ALTER TABLE agent_messages FORCE ROW LEVEL SECURITY;

ALTER TABLE agent_messages ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME agent_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE agent_role_members (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    role_id uuid NOT NULL
);

ALTER TABLE agent_role_members FORCE ROW LEVEL SECURITY;

CREATE TABLE agent_roles (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE agent_roles FORCE ROW LEVEL SECURITY;

CREATE TABLE agent_workspace_layouts (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    layout jsonb NOT NULL,
    reported_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_workspace_layouts_layout_check CHECK ((jsonb_typeof(layout) = 'object'::text))
);

ALTER TABLE agent_workspace_layouts FORCE ROW LEVEL SECURITY;

CREATE TABLE attachment_delete_queue (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    storage_key text NOT NULL,
    reason text NOT NULL,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text,
    swept_at timestamp with time zone,
    CONSTRAINT attachment_delete_queue_reason_check CHECK ((reason = ANY (ARRAY['deleted'::text, 'upload_abandoned'::text])))
);

ALTER TABLE attachment_delete_queue FORCE ROW LEVEL SECURITY;

CREATE TABLE attachment_reservations (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    storage_key text NOT NULL,
    size_bytes bigint NOT NULL,
    reserved_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT attachment_reservations_size_bytes_check CHECK ((size_bytes > 0))
);

ALTER TABLE attachment_reservations FORCE ROW LEVEL SECURITY;

CREATE TABLE attachment_usage (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    bytes bigint DEFAULT 0 NOT NULL,
    CONSTRAINT attachment_usage_bytes_check CHECK ((bytes >= 0))
);

ALTER TABLE attachment_usage FORCE ROW LEVEL SECURITY;

CREATE TABLE attachments (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    ticket_id bigint,
    conversation_id uuid,
    uploader_kind text NOT NULL,
    uploader_user_id uuid,
    uploader_user_id_deleted boolean DEFAULT false NOT NULL,
    uploader_agent_id uuid,
    filename text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 bytea NOT NULL,
    storage_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT attachments_check CHECK (((NOT uploader_user_id_deleted) OR (uploader_user_id IS NULL))),
    CONSTRAINT attachments_check1 CHECK (((ticket_id IS NOT NULL) OR (conversation_id IS NOT NULL))),
    CONSTRAINT attachments_check2 CHECK (((uploader_kind = 'user'::text) OR ((uploader_user_id IS NULL) AND (NOT uploader_user_id_deleted)))),
    CONSTRAINT attachments_check3 CHECK (((uploader_kind = 'agent'::text) OR (uploader_agent_id IS NULL))),
    CONSTRAINT attachments_content_type_check CHECK ((content_type = ANY (ARRAY['image/png'::text, 'image/jpeg'::text, 'image/webp'::text, 'image/gif'::text, 'video/mp4'::text, 'video/webm'::text, 'video/quicktime'::text]))),
    CONSTRAINT attachments_filename_check CHECK (((char_length(filename) >= 1) AND (char_length(filename) <= 255))),
    CONSTRAINT attachments_sha256_check CHECK ((octet_length(sha256) = 32)),
    CONSTRAINT attachments_size_bytes_check CHECK ((size_bytes > 0)),
    CONSTRAINT attachments_uploader_kind_check CHECK ((uploader_kind = ANY (ARRAY['user'::text, 'agent'::text])))
);

ALTER TABLE attachments FORCE ROW LEVEL SECURITY;

CREATE TABLE attempt_signals (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    final boolean DEFAULT false NOT NULL,
    computed_at timestamp with time zone DEFAULT now() NOT NULL,
    head_sha text DEFAULT ''::text NOT NULL,
    tests_deleted integer,
    skips_added integer,
    assertions_removed integer,
    out_of_scope_files integer,
    lines_added integer,
    lines_removed integer,
    files_touched integer,
    checks_passed text DEFAULT 'unknown'::text NOT NULL,
    reruns integer,
    regressions integer,
    own_test_failures integer,
    own_tests_flagged integer,
    runs bigint,
    turns bigint,
    wall_clock_seconds bigint,
    discussions bigint,
    scope_extension_denials bigint,
    blocked_handbacks bigint,
    detail jsonb NOT NULL,
    CONSTRAINT attempt_signals_assertions_removed_check CHECK ((assertions_removed >= 0)),
    CONSTRAINT attempt_signals_attempt_number_check CHECK ((attempt_number > 0)),
    CONSTRAINT attempt_signals_blocked_handbacks_check CHECK ((blocked_handbacks >= 0)),
    CONSTRAINT attempt_signals_checks_passed_check CHECK ((checks_passed = ANY (ARRAY['yes'::text, 'no'::text, 'unknown'::text]))),
    CONSTRAINT attempt_signals_discussions_check CHECK ((discussions >= 0)),
    CONSTRAINT attempt_signals_files_touched_check CHECK ((files_touched >= 0)),
    CONSTRAINT attempt_signals_lines_added_check CHECK ((lines_added >= 0)),
    CONSTRAINT attempt_signals_lines_removed_check CHECK ((lines_removed >= 0)),
    CONSTRAINT attempt_signals_out_of_scope_files_check CHECK ((out_of_scope_files >= 0)),
    CONSTRAINT attempt_signals_own_test_failures_check CHECK ((own_test_failures >= 0)),
    CONSTRAINT attempt_signals_own_tests_flagged_check CHECK ((own_tests_flagged >= 0)),
    CONSTRAINT attempt_signals_regressions_check CHECK ((regressions >= 0)),
    CONSTRAINT attempt_signals_reruns_check CHECK ((reruns >= 0)),
    CONSTRAINT attempt_signals_runs_check CHECK ((runs >= 0)),
    CONSTRAINT attempt_signals_scope_extension_denials_check CHECK ((scope_extension_denials >= 0)),
    CONSTRAINT attempt_signals_skips_added_check CHECK ((skips_added >= 0)),
    CONSTRAINT attempt_signals_tests_deleted_check CHECK ((tests_deleted >= 0)),
    CONSTRAINT attempt_signals_turns_check CHECK ((turns >= 0)),
    CONSTRAINT attempt_signals_wall_clock_seconds_check CHECK ((wall_clock_seconds >= 0))
);

ALTER TABLE attempt_signals FORCE ROW LEVEL SECURITY;

CREATE TABLE attempt_status_changes (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    from_status text,
    to_status text NOT NULL,
    priority text NOT NULL,
    changed_at timestamp with time zone DEFAULT now() NOT NULL,
    actor_kind text NOT NULL,
    actor_id uuid,
    cause text,
    actor_id_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT attempt_status_changes_actor_id_deleted_is_null CHECK (((NOT actor_id_deleted) OR (actor_id IS NULL))),
    CONSTRAINT attempt_status_changes_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['agent'::text, 'user'::text, 'system'::text]))),
    CONSTRAINT attempt_status_changes_check CHECK (((actor_kind <> 'system'::text) OR (actor_id IS NULL)))
);

ALTER TABLE attempt_status_changes FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE attempt_status_changes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE attempt_status_changes_id_seq OWNED BY attempt_status_changes.id;

CREATE TABLE attempt_verdicts (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    verdict text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    set_by_user_id uuid,
    set_by_user_id_deleted boolean DEFAULT false NOT NULL,
    set_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT attempt_verdicts_attempt_number_check CHECK ((attempt_number > 0)),
    CONSTRAINT attempt_verdicts_note_check CHECK ((char_length(note) <= 1000)),
    CONSTRAINT attempt_verdicts_set_by_deleted_is_null CHECK (((NOT set_by_user_id_deleted) OR (set_by_user_id IS NULL))),
    CONSTRAINT attempt_verdicts_verdict_check CHECK ((verdict = ANY (ARRAY['would_merge'::text, 'would_merge_with_changes'::text, 'would_not_merge'::text])))
);

ALTER TABLE attempt_verdicts FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE attempt_verdicts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE attempt_verdicts_id_seq OWNED BY attempt_verdicts.id;

CREATE TABLE attempts (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    number integer NOT NULL,
    status text NOT NULL,
    promoted_at timestamp with time zone,
    promoted_by_user_id uuid,
    resume_agent_id uuid,
    affinity_until timestamp with time zone,
    pr_url text,
    approved_at timestamp with time zone,
    approved_by_user_id uuid,
    changes_requested_at timestamp with time zone,
    review_started_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    variant_version_id uuid,
    shadow boolean DEFAULT false NOT NULL,
    started_revision integer,
    discussed_at timestamp with time zone,
    approved_by_user_id_deleted boolean DEFAULT false NOT NULL,
    promoted_by_user_id_deleted boolean DEFAULT false NOT NULL,
    branch_created_at timestamp with time zone,
    harness text,
    harness_session_id text,
    replay boolean DEFAULT false NOT NULL,
    replay_batch_id uuid,
    discussion_round integer DEFAULT 0 NOT NULL,
    CONSTRAINT attempts_approved_by_user_id_deleted_is_null CHECK (((NOT approved_by_user_id_deleted) OR (approved_by_user_id IS NULL))),
    CONSTRAINT attempts_check CHECK (((promoted_at IS NOT NULL) OR (promoted_by_user_id IS NULL))),
    CONSTRAINT attempts_check1 CHECK (((approved_at IS NULL) OR (changes_requested_at IS NULL))),
    CONSTRAINT attempts_discussion_round_check CHECK ((discussion_round >= 0)),
    CONSTRAINT attempts_harness_session_id_check CHECK (((length(harness_session_id) >= 1) AND (length(harness_session_id) <= 200))),
    CONSTRAINT attempts_number_check CHECK ((number > 0)),
    CONSTRAINT attempts_pr_url_check CHECK (((pr_url ~ '^https://'::text) AND (length(pr_url) <= 2048))),
    CONSTRAINT attempts_promoted_by_user_id_deleted_is_null CHECK (((NOT promoted_by_user_id_deleted) OR (promoted_by_user_id IS NULL))),
    CONSTRAINT attempts_session_has_harness CHECK (((harness IS NULL) = (harness_session_id IS NULL))),
    CONSTRAINT attempts_shadow_never_promoted CHECK (((NOT shadow) OR (promoted_at IS NULL))),
    CONSTRAINT attempts_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'in_progress'::text, 'in_merge_queue'::text, 'discussion'::text, 'review'::text, 'closed'::text, 'done'::text])))
);

ALTER TABLE attempts FORCE ROW LEVEL SECURITY;

CREATE TABLE batches (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    base_sha text NOT NULL,
    branch text NOT NULL,
    member_ticket_ids bigint[] NOT NULL,
    state text NOT NULL,
    ci_pipeline text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    member_prefix_shas text[] DEFAULT '{}'::text[] NOT NULL,
    member_head_shas text[] DEFAULT '{}'::text[] NOT NULL,
    change_ref jsonb,
    result text,
    result_reason text,
    CONSTRAINT batches_member_ticket_ids_check CHECK ((cardinality(member_ticket_ids) > 0)),
    CONSTRAINT batches_reason_needs_result CHECK (((result_reason IS NULL) OR (result IS NOT NULL))),
    CONSTRAINT batches_result_check CHECK ((result = ANY (ARRAY['merged'::text, 'red'::text, 'voided'::text, 'split'::text]))),
    CONSTRAINT batches_result_only_when_done CHECK (((result IS NULL) OR (state = 'done'::text))),
    CONSTRAINT batches_state_check CHECK ((state = ANY (ARRAY['building'::text, 'testing'::text, 'merging'::text, 'done'::text])))
);

ALTER TABLE batches FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE batches_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE batches_id_seq OWNED BY batches.id;

CREATE TABLE board_changes (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    revision bigint NOT NULL,
    ticket_id bigint NOT NULL,
    CONSTRAINT board_changes_ticket_id_check CHECK ((ticket_id >= 0))
);

ALTER TABLE board_changes FORCE ROW LEVEL SECURITY;

CREATE TABLE board_revisions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    revision bigint NOT NULL
);

ALTER TABLE board_revisions FORCE ROW LEVEL SECURITY;

CREATE TABLE codehost_credentials (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    provider text NOT NULL,
    app_id bigint NOT NULL,
    installation_id bigint,
    private_key_enc bytea NOT NULL,
    key_id text NOT NULL,
    key_fingerprint text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    verified_at timestamp with time zone,
    app_slug text,
    created_by_deleted boolean DEFAULT false NOT NULL,
    webhook_secret_enc bytea,
    webhook_secret_key_id text,
    webhook_url text,
    webhook_applied_at timestamp with time zone,
    CONSTRAINT codehost_credentials_app_id_check CHECK ((app_id > 0)),
    CONSTRAINT codehost_credentials_created_by_deleted_is_null CHECK (((NOT created_by_deleted) OR (created_by IS NULL))),
    CONSTRAINT codehost_credentials_installation_id_check CHECK (((installation_id IS NULL) OR (installation_id > 0))),
    CONSTRAINT codehost_credentials_key_fingerprint_check CHECK ((length(key_fingerprint) = 64)),
    CONSTRAINT codehost_credentials_key_id_check CHECK ((length(key_id) = 16)),
    CONSTRAINT codehost_credentials_provider_check CHECK ((provider = 'github_app'::text)),
    CONSTRAINT codehost_credentials_webhook_secret_complete CHECK (((webhook_secret_enc IS NULL) = (webhook_secret_key_id IS NULL))),
    CONSTRAINT codehost_credentials_webhook_secret_key_id_check CHECK ((length(webhook_secret_key_id) = 16))
);

ALTER TABLE codehost_credentials FORCE ROW LEVEL SECURITY;

CREATE TABLE codehost_setup_states (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    token_hash bytea NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT codehost_setup_states_kind_check CHECK ((kind = ANY (ARRAY['manifest'::text, 'install'::text]))),
    CONSTRAINT codehost_setup_states_token_hash_check CHECK ((length(token_hash) = 32))
);

ALTER TABLE codehost_setup_states FORCE ROW LEVEL SECURITY;

CREATE TABLE conductor_conversation_events (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    conversation_id uuid NOT NULL,
    seq bigint NOT NULL,
    kind text NOT NULL,
    turn bigint,
    body text DEFAULT ''::text NOT NULL,
    client_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    block text,
    attachment_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    CONSTRAINT conductor_conversation_events_attachment_ids_check CHECK ((cardinality(attachment_ids) <= 8)),
    CONSTRAINT conductor_conversation_events_block_check CHECK ((length(block) <= 128)),
    CONSTRAINT conductor_conversation_events_body_check CHECK ((octet_length(body) <= 65536)),
    CONSTRAINT conductor_conversation_events_kind_check CHECK ((kind = ANY (ARRAY['user'::text, 'assistant'::text, 'turn_end'::text, 'ready'::text, 'failed'::text, 'stopped'::text, 'truncated'::text, 'repo_state'::text, 'interrupt_requested'::text, 'interrupted'::text, 'activity'::text, 'unsupported'::text, 'context_cleared'::text, 'context_compacted'::text])))
);

ALTER TABLE conductor_conversation_events FORCE ROW LEVEL SECURITY;

CREATE TABLE conductor_conversations (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    conductor_id uuid NOT NULL,
    owner_user_id uuid,
    state text NOT NULL,
    failure text,
    next_seq bigint DEFAULT 0 NOT NULL,
    host_acked bigint DEFAULT 0 NOT NULL,
    transcript_bytes bigint DEFAULT 0 NOT NULL,
    truncated boolean DEFAULT false NOT NULL,
    start_client_id uuid,
    start_seq bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    repo_state jsonb,
    owner_user_id_deleted boolean DEFAULT false NOT NULL,
    first_line text,
    title text,
    context_tokens bigint DEFAULT 0 NOT NULL,
    can_compact boolean DEFAULT false NOT NULL,
    CONSTRAINT conductor_conversations_context_tokens_check CHECK ((context_tokens >= 0)),
    CONSTRAINT conductor_conversations_first_line_check CHECK (((char_length(first_line) >= 1) AND (char_length(first_line) <= 120))),
    CONSTRAINT conductor_conversations_owner_required CHECK (((owner_user_id IS NOT NULL) OR owner_user_id_deleted)),
    CONSTRAINT conductor_conversations_owner_user_id_deleted_is_null CHECK (((NOT owner_user_id_deleted) OR (owner_user_id IS NULL))),
    CONSTRAINT conductor_conversations_state_check CHECK ((state = ANY (ARRAY['starting'::text, 'ready'::text, 'failed'::text, 'stopped'::text]))),
    CONSTRAINT conductor_conversations_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 100)))
);

ALTER TABLE conductor_conversations FORCE ROW LEVEL SECURITY;

CREATE TABLE conductor_queues (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    conductor_id uuid NOT NULL,
    next_seq bigint DEFAULT 0 NOT NULL,
    acked_seq bigint DEFAULT 0 NOT NULL,
    host_id uuid,
    host_seen_at timestamp with time zone,
    host_capabilities text[] DEFAULT '{}'::text[] NOT NULL,
    host_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT conductor_queues_check CHECK ((acked_seq <= next_seq))
);

ALTER TABLE conductor_queues FORCE ROW LEVEL SECURITY;

CREATE TABLE conductor_work (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    conductor_id uuid NOT NULL,
    seq bigint NOT NULL,
    kind text NOT NULL,
    conversation_id uuid NOT NULL,
    message_seq bigint,
    resume boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    turn bigint,
    CONSTRAINT conductor_work_check CHECK (((kind = 'message'::text) = (message_seq IS NOT NULL))),
    CONSTRAINT conductor_work_check1 CHECK (((kind = 'interrupt'::text) = (turn IS NOT NULL))),
    CONSTRAINT conductor_work_kind_check CHECK ((kind = ANY (ARRAY['start'::text, 'message'::text, 'stop'::text, 'interrupt'::text, 'clear'::text, 'compact'::text])))
);

ALTER TABLE conductor_work FORCE ROW LEVEL SECURITY;

CREATE TABLE eval_set_batches (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    eval_set_id uuid NOT NULL,
    batch_id uuid NOT NULL,
    version_ids uuid[] NOT NULL,
    runs integer NOT NULL,
    ticket_count integer NOT NULL,
    created_by uuid,
    created_by_deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT eval_set_batches_check CHECK (((NOT created_by_deleted) OR (created_by IS NULL))),
    CONSTRAINT eval_set_batches_runs_check CHECK ((runs >= 1)),
    CONSTRAINT eval_set_batches_ticket_count_check CHECK ((ticket_count >= 0))
);

ALTER TABLE eval_set_batches FORCE ROW LEVEL SECURITY;

CREATE TABLE eval_sets (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    filter jsonb,
    ticket_ids bigint[],
    created_by uuid,
    created_by_deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT eval_sets_check CHECK (((NOT created_by_deleted) OR (created_by IS NULL))),
    CONSTRAINT eval_sets_check1 CHECK (((filter IS NULL) <> (ticket_ids IS NULL))),
    CONSTRAINT eval_sets_description_check CHECK ((length(description) <= 4000)),
    CONSTRAINT eval_sets_filter_check CHECK (((filter IS NULL) OR (jsonb_typeof(filter) = 'object'::text))),
    CONSTRAINT eval_sets_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128)))
);

ALTER TABLE eval_sets FORCE ROW LEVEL SECURITY;

CREATE TABLE fleet_instruction_archives (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    version_id uuid NOT NULL,
    archived_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_by uuid,
    archived_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT fleet_instruction_archives_archived_by_deleted_is_null CHECK (((NOT archived_by_deleted) OR (archived_by IS NULL)))
);

ALTER TABLE fleet_instruction_archives FORCE ROW LEVEL SECURITY;

CREATE TABLE fleet_instruction_defaults (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    version_id uuid NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    updated_by_deleted boolean DEFAULT false NOT NULL,
    kind text DEFAULT 'fleet'::text NOT NULL,
    CONSTRAINT fleet_instruction_defaults_updated_by_deleted_is_null CHECK (((NOT updated_by_deleted) OR (updated_by IS NULL)))
);

ALTER TABLE fleet_instruction_defaults FORCE ROW LEVEL SECURITY;

CREATE TABLE fleet_instruction_versions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    version integer NOT NULL,
    body text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_deleted boolean DEFAULT false NOT NULL,
    kind text DEFAULT 'fleet'::text NOT NULL,
    CONSTRAINT fleet_instruction_versions_body_check CHECK (((octet_length(body) <= 20000) AND ((kind = 'sensitive_words'::text) OR (octet_length(body) >= 1)))),
    CONSTRAINT fleet_instruction_versions_created_by_deleted_is_null CHECK (((NOT created_by_deleted) OR (created_by IS NULL))),
    CONSTRAINT fleet_instruction_versions_kind_check CHECK ((kind = ANY (ARRAY['fleet'::text, 'steward'::text, 'sensitive_words'::text]))),
    CONSTRAINT fleet_instruction_versions_name_check CHECK (((length(name) >= 1) AND (length(name) <= 64))),
    CONSTRAINT fleet_instruction_versions_version_check CHECK ((version >= 1))
);

ALTER TABLE fleet_instruction_versions FORCE ROW LEVEL SECURITY;

CREATE TABLE github_deliveries (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    delivery_id text NOT NULL,
    event text NOT NULL,
    action text DEFAULT ''::text NOT NULL,
    outcome text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    ticket_id bigint,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    attempt_number integer,
    installation_id bigint,
    CONSTRAINT github_deliveries_action_check CHECK ((length(action) <= 100)),
    CONSTRAINT github_deliveries_attempt_with_ticket CHECK (((attempt_number IS NULL) OR (ticket_id IS NOT NULL))),
    CONSTRAINT github_deliveries_delivery_id_check CHECK (((length(delivery_id) >= 1) AND (length(delivery_id) <= 100))),
    CONSTRAINT github_deliveries_detail_check CHECK ((length(detail) <= 1000)),
    CONSTRAINT github_deliveries_event_check CHECK ((length(event) <= 100)),
    CONSTRAINT github_deliveries_outcome_check CHECK ((outcome = ANY (ARRAY['processing'::text, 'applied'::text, 'ignored'::text, 'error'::text])))
);

ALTER TABLE github_deliveries FORCE ROW LEVEL SECURITY;

CREATE TABLE github_event_keys (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    event_key text NOT NULL,
    delivery_id text NOT NULL,
    state text DEFAULT 'claimed'::text NOT NULL,
    claimed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT github_event_keys_event_key_check CHECK (((length(event_key) >= 1) AND (length(event_key) <= 500))),
    CONSTRAINT github_event_keys_state_check CHECK ((state = ANY (ARRAY['claimed'::text, 'applied'::text])))
);

ALTER TABLE github_event_keys FORCE ROW LEVEL SECURITY;

CREATE TABLE global_auth_providers (
    name text NOT NULL,
    issuer text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE global_auth_tokens (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash text NOT NULL,
    purpose text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT global_auth_tokens_purpose_check CHECK ((purpose = ANY (ARRAY['activation'::text, 'password_reset'::text])))
);

CREATE TABLE global_consent_events (
    id bigint NOT NULL,
    user_id uuid NOT NULL,
    purpose text NOT NULL,
    granted boolean NOT NULL,
    document_kind text NOT NULL,
    document_version text NOT NULL,
    source text NOT NULL,
    decided_at timestamp with time zone NOT NULL,
    recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    merged_from_anonymous boolean DEFAULT false NOT NULL,
    merge_token text,
    session_id text,
    CONSTRAINT global_consent_events_check CHECK ((merged_from_anonymous = (merge_token IS NOT NULL))),
    CONSTRAINT global_consent_events_source_check CHECK ((source = ANY (ARRAY['banner'::text, 'settings'::text, 'sign_up'::text, 'prompt'::text, 'gpc'::text, 'migration'::text])))
);

CREATE SEQUENCE global_consent_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE global_consent_events_id_seq OWNED BY global_consent_events.id;

CREATE TABLE global_consent_merge_tokens (
    token_hash text NOT NULL,
    claimed_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE global_former_memberships (
    tenant uuid NOT NULL,
    user_id uuid NOT NULL,
    left_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE global_github_app_installations (
    tenant uuid NOT NULL,
    installation_id bigint NOT NULL,
    credential_id uuid NOT NULL,
    webhook_secret_enc bytea NOT NULL,
    CONSTRAINT global_github_app_installations_installation_id_check CHECK ((installation_id > 0))
);

CREATE TABLE global_harnesses (
    name text NOT NULL,
    CONSTRAINT global_harnesses_name_check CHECK ((name ~ '^[a-z][a-z0-9_-]{0,31}$'::text))
);

CREATE TABLE global_intake_credentials (
    source_id uuid NOT NULL,
    tenant uuid NOT NULL,
    kind text NOT NULL,
    token_hash bytea,
    generation integer DEFAULT 1 NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT global_intake_credentials_check CHECK (((kind = 'bearer'::text) = (token_hash IS NOT NULL))),
    CONSTRAINT global_intake_credentials_generation_check CHECK ((generation >= 1)),
    CONSTRAINT global_intake_credentials_kind_check CHECK ((kind = ANY (ARRAY['bearer'::text, 'hmac'::text])))
);

CREATE TABLE global_invites (
    id uuid NOT NULL,
    token_hash text NOT NULL,
    email text NOT NULL,
    kind text NOT NULL,
    tenant uuid,
    invited_by uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    accepted_at timestamp with time zone,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT global_invites_kind_check CHECK ((kind = ANY (ARRAY['new_tenant'::text, 'join_tenant'::text]))),
    CONSTRAINT global_invites_tenant_matches_kind CHECK (((kind = 'join_tenant'::text) = (tenant IS NOT NULL)))
);

CREATE TABLE global_legal_document_versions (
    kind text NOT NULL,
    version text NOT NULL,
    locale text NOT NULL,
    effective date NOT NULL,
    material boolean NOT NULL,
    reviewed boolean NOT NULL,
    reviewed_by text DEFAULT ''::text NOT NULL,
    reviewed_on date,
    body text NOT NULL,
    content_sha256 text NOT NULL,
    published_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT global_legal_document_versions_check CHECK (((NOT reviewed) OR ((reviewed_by <> ''::text) AND (reviewed_on IS NOT NULL))))
);

CREATE TABLE global_oauth_auth_requests (
    id uuid NOT NULL,
    client_id text NOT NULL,
    redirect_uri text NOT NULL,
    code_challenge text NOT NULL,
    state text DEFAULT ''::text NOT NULL,
    resource text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    decided_at timestamp with time zone
);

CREATE TABLE global_oauth_clients (
    id text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    redirect_uris text[] DEFAULT '{}'::text[] NOT NULL,
    grant_types text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    secret_hash text
);

CREATE TABLE global_oauth_codes (
    code_hash text NOT NULL,
    grant_id uuid NOT NULL,
    redirect_uri text NOT NULL,
    code_challenge text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone
);

CREATE TABLE global_oauth_device_codes (
    id uuid NOT NULL,
    device_code_hash text NOT NULL,
    user_code text NOT NULL,
    client_id text NOT NULL,
    resource text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_polled_at timestamp with time zone,
    grant_id uuid,
    denied_at timestamp with time zone,
    consumed_at timestamp with time zone,
    started_from inet,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE global_oauth_grants (
    id uuid NOT NULL,
    tenant uuid NOT NULL,
    agent_id uuid NOT NULL,
    client_id text NOT NULL,
    approved_by uuid,
    resource text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone,
    scope text DEFAULT 'agent'::text NOT NULL,
    approved_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT global_oauth_grants_approved_by_deleted_is_null CHECK (((NOT approved_by_deleted) OR (approved_by IS NULL))),
    CONSTRAINT global_oauth_grants_scope_check CHECK ((scope = ANY (ARRAY['agent'::text, 'read'::text])))
);

CREATE TABLE global_oauth_tokens (
    token_hash text NOT NULL,
    grant_id uuid NOT NULL,
    kind text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    replay_blob bytea,
    replaced_by text,
    CONSTRAINT global_oauth_tokens_kind_check CHECK ((kind = ANY (ARRAY['access'::text, 'refresh'::text])))
);

CREATE TABLE global_tenant_memberships (
    id uuid NOT NULL,
    tenant uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    role text DEFAULT 'member'::text NOT NULL,
    CONSTRAINT global_tenant_memberships_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'member'::text])))
);

CREATE TABLE global_tenants (
    id uuid NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    colour text,
    archived_at timestamp with time zone,
    archived_by uuid,
    archived_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT global_tenants_archived_by_deleted_means_null CHECK (((NOT archived_by_deleted) OR (archived_by IS NULL))),
    CONSTRAINT global_tenants_archived_together CHECK (((archived_at IS NULL) = ((archived_by IS NULL) AND (NOT archived_by_deleted))))
);

CREATE TABLE global_user_identities (
    provider text NOT NULL,
    subject text NOT NULL,
    user_id uuid NOT NULL,
    login text DEFAULT ''::text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_login_at timestamp with time zone
);

CREATE TABLE global_user_settings (
    id bigint NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    setting_name text NOT NULL,
    setting_type text NOT NULL,
    str_value text,
    int_value bigint,
    bool_value boolean,
    float_value double precision,
    jsonb_value jsonb,
    CONSTRAINT global_user_settings_check CHECK ((num_nonnulls(str_value, int_value, bool_value, float_value, jsonb_value) = 1)),
    CONSTRAINT global_user_settings_check1 CHECK (((setting_type = 'str'::text) = (str_value IS NOT NULL))),
    CONSTRAINT global_user_settings_check2 CHECK (((setting_type = 'int'::text) = (int_value IS NOT NULL))),
    CONSTRAINT global_user_settings_check3 CHECK (((setting_type = 'bool'::text) = (bool_value IS NOT NULL))),
    CONSTRAINT global_user_settings_check4 CHECK (((setting_type = 'float'::text) = (float_value IS NOT NULL))),
    CONSTRAINT global_user_settings_check5 CHECK (((setting_type = 'jsonb'::text) = (jsonb_value IS NOT NULL))),
    CONSTRAINT global_user_settings_setting_type_check CHECK ((setting_type = ANY (ARRAY['str'::text, 'int'::text, 'bool'::text, 'float'::text, 'jsonb'::text])))
);

ALTER TABLE global_user_settings ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME global_user_settings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE global_users (
    id uuid NOT NULL,
    email text NOT NULL,
    password_hash text,
    display_name text DEFAULT ''::text NOT NULL,
    is_active boolean DEFAULT false NOT NULL,
    sessions_valid_after timestamp with time zone DEFAULT now() NOT NULL,
    date_joined timestamp with time zone DEFAULT now() NOT NULL,
    is_admin boolean DEFAULT false NOT NULL
);

CREATE TABLE harness_config (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    revision bigint NOT NULL,
    updated_by uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT harness_config_revision_check CHECK ((revision > 0)),
    CONSTRAINT harness_config_settings_check CHECK ((jsonb_typeof(settings) = 'object'::text)),
    CONSTRAINT harness_config_updated_by_deleted_is_null CHECK (((NOT updated_by_deleted) OR (updated_by IS NULL))),
    CONSTRAINT harness_config_updated_by_required CHECK (((updated_by IS NOT NULL) OR updated_by_deleted))
);

ALTER TABLE harness_config FORCE ROW LEVEL SECURITY;

CREATE TABLE harness_config_revisions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    revision bigint NOT NULL,
    CONSTRAINT harness_config_revisions_revision_check CHECK ((revision > 0))
);

ALTER TABLE harness_config_revisions FORCE ROW LEVEL SECURITY;

CREATE TABLE ignored_merges (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    host text NOT NULL,
    owner text NOT NULL,
    repo text NOT NULL,
    number bigint NOT NULL,
    merge_sha text DEFAULT ''::text NOT NULL,
    merger_login text NOT NULL,
    merger_github_id bigint NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ignored_merges_attempt_number_check CHECK ((attempt_number > 0)),
    CONSTRAINT ignored_merges_host_check CHECK ((host <> ''::text)),
    CONSTRAINT ignored_merges_number_check CHECK ((number > 0)),
    CONSTRAINT ignored_merges_owner_check CHECK ((owner <> ''::text)),
    CONSTRAINT ignored_merges_reason_check CHECK ((reason <> ''::text)),
    CONSTRAINT ignored_merges_repo_check CHECK ((repo <> ''::text))
);

ALTER TABLE ignored_merges FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ignored_merges_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ignored_merges_id_seq OWNED BY ignored_merges.id;

CREATE TABLE intake_deliveries (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    source_id uuid NOT NULL,
    delivery_id text NOT NULL,
    event text DEFAULT ''::text NOT NULL,
    key text DEFAULT ''::text NOT NULL,
    outcome text NOT NULL,
    downgraded boolean DEFAULT false NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    ticket_id bigint,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT intake_deliveries_delivery_id_check CHECK (((length(delivery_id) >= 1) AND (length(delivery_id) <= 200))),
    CONSTRAINT intake_deliveries_detail_check CHECK ((length(detail) <= 1000)),
    CONSTRAINT intake_deliveries_event_check CHECK ((length(event) <= 100)),
    CONSTRAINT intake_deliveries_key_check CHECK ((length(key) <= 200)),
    CONSTRAINT intake_deliveries_outcome_check CHECK ((outcome = ANY (ARRAY['processing'::text, 'applied'::text, 'ignored'::text, 'rejected'::text, 'rate_limited'::text, 'error'::text])))
);

ALTER TABLE intake_deliveries FORCE ROW LEVEL SECURITY;

CREATE TABLE intake_events (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    source_id uuid NOT NULL,
    delivery_id text NOT NULL,
    event text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    link text DEFAULT ''::text NOT NULL,
    context jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT intake_events_context_check CHECK ((jsonb_typeof(context) = 'object'::text)),
    CONSTRAINT intake_events_event_check CHECK ((event = ANY (ARRAY['open'::text, 'update'::text, 'resolve'::text])))
);

ALTER TABLE intake_events FORCE ROW LEVEL SECURITY;

ALTER TABLE intake_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME intake_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE intake_sources (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL,
    adapter text NOT NULL,
    trust text NOT NULL,
    auth_kind text NOT NULL,
    default_status text NOT NULL,
    default_priority text NOT NULL,
    max_priority text NOT NULL,
    role_names text[] DEFAULT '{}'::text[] NOT NULL,
    role_match text DEFAULT 'any'::text NOT NULL,
    paths text[] DEFAULT '{}'::text[] NOT NULL,
    reopen_policy text DEFAULT 'new'::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    last_delivery_at timestamp with time zone,
    created_by_deleted boolean DEFAULT false NOT NULL,
    resolve_closes boolean DEFAULT false NOT NULL,
    CONSTRAINT intake_sources_adapter_check CHECK ((adapter = ANY (ARRAY['native'::text, 'alertmanager'::text]))),
    CONSTRAINT intake_sources_auth_kind_check CHECK ((auth_kind = ANY (ARRAY['bearer'::text, 'hmac'::text]))),
    CONSTRAINT intake_sources_check CHECK (((trust = 'trusted'::text) OR (default_status = 'preparing'::text))),
    CONSTRAINT intake_sources_created_by_deleted_is_null CHECK (((NOT created_by_deleted) OR (created_by IS NULL))),
    CONSTRAINT intake_sources_default_priority_check CHECK ((default_priority = ANY (ARRAY['low'::text, 'normal'::text, 'high'::text, 'urgent'::text]))),
    CONSTRAINT intake_sources_default_status_check CHECK ((default_status = ANY (ARRAY['open'::text, 'preparing'::text]))),
    CONSTRAINT intake_sources_max_priority_check CHECK ((max_priority = ANY (ARRAY['low'::text, 'normal'::text, 'high'::text, 'urgent'::text]))),
    CONSTRAINT intake_sources_name_check CHECK (((length(name) >= 1) AND (length(name) <= 100))),
    CONSTRAINT intake_sources_reopen_policy_check CHECK ((reopen_policy = ANY (ARRAY['new'::text, 'reopen'::text]))),
    CONSTRAINT intake_sources_resolve_closes_trusted CHECK (((NOT resolve_closes) OR (trust = 'trusted'::text))),
    CONSTRAINT intake_sources_role_match_check CHECK ((role_match = ANY (ARRAY['any'::text, 'all'::text]))),
    CONSTRAINT intake_sources_trust_check CHECK ((trust = ANY (ARRAY['trusted'::text, 'untrusted'::text])))
);

ALTER TABLE intake_sources FORCE ROW LEVEL SECURITY;

CREATE TABLE landings (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    change_url text NOT NULL,
    head_sha text NOT NULL,
    back_reason text,
    keeps_place boolean DEFAULT false NOT NULL,
    seq bigint NOT NULL,
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    batch_id bigint,
    batch_position integer,
    queue_state text DEFAULT 'queued'::text NOT NULL,
    CONSTRAINT landings_attempt_number_check CHECK ((attempt_number > 0)),
    CONSTRAINT landings_batch_position_check CHECK ((batch_position > 0)),
    CONSTRAINT landings_place_kept_only_when_back CHECK (((queue_state = 'back_with_agent'::text) OR (NOT keeps_place))),
    CONSTRAINT landings_queue_state_check CHECK ((queue_state = ANY (ARRAY['queued'::text, 'serial_merging_main'::text, 'serial_waiting_checks'::text, 'serial_merging'::text, 'batch_testing'::text, 'batch_merging'::text, 'back_with_agent'::text]))),
    CONSTRAINT landings_reason_iff_back CHECK (((queue_state = 'back_with_agent'::text) = (back_reason IS NOT NULL)))
);

ALTER TABLE landings FORCE ROW LEVEL SECURITY;

ALTER TABLE landings ALTER COLUMN seq ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME landings_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE lease_configs (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    lease_id bigint NOT NULL,
    revision bigint NOT NULL,
    settings jsonb NOT NULL,
    variant_id uuid,
    fleet_instructions_version_id uuid,
    harness text,
    harness_version text,
    model text,
    bundle_hash text,
    CONSTRAINT lease_configs_revision_check CHECK ((revision >= 0)),
    CONSTRAINT lease_configs_settings_check CHECK ((jsonb_typeof(settings) = 'object'::text))
);

ALTER TABLE lease_configs FORCE ROW LEVEL SECURITY;

CREATE TABLE leases (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    agent_id uuid NOT NULL,
    acquired_by_session_id bigint,
    acquired_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    released_at timestamp with time zone,
    release_reason text,
    revoked_to text,
    revocation_delivered_at timestamp with time zone,
    attempt_number integer NOT NULL,
    last_heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT leases_revoked_to_check CHECK ((revoked_to = ANY (ARRAY['preparing'::text, 'open'::text, 'blocked'::text, 'discussion'::text, 'review'::text, 'done'::text, 'cancelled'::text, 'closed'::text])))
);

ALTER TABLE leases FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE leases_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE leases_id_seq OWNED BY leases.id;

CREATE TABLE llm_guidance_usage (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    transcript_key text NOT NULL,
    ticket_id bigint NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    harness text NOT NULL,
    billing_mode text NOT NULL,
    requests bigint NOT NULL,
    input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    cache_read_tokens bigint NOT NULL,
    cache_write_tokens bigint NOT NULL,
    reasoning_tokens bigint NOT NULL,
    reported_cost_microdollars bigint,
    estimated_cost_microdollars bigint,
    price_version text NOT NULL,
    unpriced boolean NOT NULL,
    ended_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    attempt_number integer,
    unreliable boolean DEFAULT false NOT NULL,
    CONSTRAINT llm_guidance_usage_billing_mode_check CHECK ((billing_mode = ANY (ARRAY['metered'::text, 'subscription'::text]))),
    CONSTRAINT llm_guidance_usage_cache_read_tokens_check CHECK ((cache_read_tokens >= 0)),
    CONSTRAINT llm_guidance_usage_cache_write_tokens_check CHECK ((cache_write_tokens >= 0)),
    CONSTRAINT llm_guidance_usage_estimated_cost_microdollars_check CHECK ((estimated_cost_microdollars >= 0)),
    CONSTRAINT llm_guidance_usage_input_tokens_check CHECK ((input_tokens >= 0)),
    CONSTRAINT llm_guidance_usage_output_tokens_check CHECK ((output_tokens >= 0)),
    CONSTRAINT llm_guidance_usage_reasoning_tokens_check CHECK ((reasoning_tokens >= 0)),
    CONSTRAINT llm_guidance_usage_reported_cost_microdollars_check CHECK ((reported_cost_microdollars >= 0)),
    CONSTRAINT llm_guidance_usage_requests_check CHECK ((requests >= 0))
);

ALTER TABLE llm_guidance_usage FORCE ROW LEVEL SECURITY;

CREATE TABLE llm_tool_usage (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    transcript_key text NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    harness text NOT NULL,
    billing_mode text NOT NULL,
    category text NOT NULL,
    tool text NOT NULL,
    command text DEFAULT ''::text NOT NULL,
    calls bigint NOT NULL,
    errors bigint NOT NULL,
    duration_ms bigint NOT NULL,
    result_bytes bigint NOT NULL,
    ended_at timestamp with time zone NOT NULL,
    CONSTRAINT llm_tool_usage_calls_check CHECK ((calls >= 0)),
    CONSTRAINT llm_tool_usage_duration_ms_check CHECK ((duration_ms >= 0)),
    CONSTRAINT llm_tool_usage_errors_check CHECK ((errors >= 0)),
    CONSTRAINT llm_tool_usage_result_bytes_check CHECK ((result_bytes >= 0))
);

ALTER TABLE llm_tool_usage FORCE ROW LEVEL SECURITY;

CREATE TABLE llm_usage (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    transcript_key text NOT NULL,
    harness text NOT NULL,
    session_id text NOT NULL,
    scope_hint text NOT NULL,
    scope text NOT NULL,
    ticket_id bigint,
    provider text NOT NULL,
    model text NOT NULL,
    billing_mode text NOT NULL,
    requests bigint NOT NULL,
    input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    cache_read_tokens bigint NOT NULL,
    cache_write_tokens bigint NOT NULL,
    reasoning_tokens bigint NOT NULL,
    reported_cost_microdollars bigint,
    estimated_cost_microdollars bigint,
    price_version text NOT NULL,
    unpriced boolean NOT NULL,
    started_at timestamp with time zone NOT NULL,
    ended_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    conversation_id uuid,
    attempt_number integer,
    unreliable boolean DEFAULT false NOT NULL,
    CONSTRAINT llm_usage_attempt_with_ticket CHECK (((attempt_number IS NULL) OR (ticket_id IS NOT NULL))),
    CONSTRAINT llm_usage_billing_mode_check CHECK ((billing_mode = ANY (ARRAY['metered'::text, 'subscription'::text]))),
    CONSTRAINT llm_usage_cache_read_tokens_check CHECK ((cache_read_tokens >= 0)),
    CONSTRAINT llm_usage_cache_write_tokens_check CHECK ((cache_write_tokens >= 0)),
    CONSTRAINT llm_usage_check CHECK ((ended_at >= started_at)),
    CONSTRAINT llm_usage_check1 CHECK (((scope = 'ticket'::text) OR (ticket_id IS NULL))),
    CONSTRAINT llm_usage_estimated_cost_microdollars_check CHECK ((estimated_cost_microdollars >= 0)),
    CONSTRAINT llm_usage_input_tokens_check CHECK ((input_tokens >= 0)),
    CONSTRAINT llm_usage_output_tokens_check CHECK ((output_tokens >= 0)),
    CONSTRAINT llm_usage_reasoning_tokens_check CHECK ((reasoning_tokens >= 0)),
    CONSTRAINT llm_usage_reported_cost_microdollars_check CHECK ((reported_cost_microdollars >= 0)),
    CONSTRAINT llm_usage_requests_check CHECK ((requests >= 0)),
    CONSTRAINT llm_usage_scope_check CHECK ((scope = ANY (ARRAY['ticket'::text, 'overhead'::text])))
);

ALTER TABLE llm_usage FORCE ROW LEVEL SECURITY;

CREATE TABLE llm_usage_daily (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    day timestamp with time zone NOT NULL,
    agent_id uuid NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    billing_mode text NOT NULL,
    records bigint DEFAULT 0 NOT NULL,
    unpriced_records bigint DEFAULT 0 NOT NULL,
    requests bigint DEFAULT 0 NOT NULL,
    unpriced_requests bigint DEFAULT 0 NOT NULL,
    input_tokens bigint DEFAULT 0 NOT NULL,
    output_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    reasoning_tokens bigint DEFAULT 0 NOT NULL,
    cost_microdollars bigint,
    unreliable boolean DEFAULT false NOT NULL,
    CONSTRAINT llm_usage_daily_billing_mode_check CHECK ((billing_mode = ANY (ARRAY['metered'::text, 'subscription'::text]))),
    CONSTRAINT llm_usage_daily_day_check CHECK ((day = (date_trunc('day'::text, (day AT TIME ZONE 'UTC'::text)) AT TIME ZONE 'UTC'::text)))
);

ALTER TABLE llm_usage_daily FORCE ROW LEVEL SECURITY;

CREATE TABLE llm_usage_marks (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    transcript_key text NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    harness text NOT NULL,
    billing_mode text NOT NULL,
    requests bigint NOT NULL,
    input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    cache_read_tokens bigint NOT NULL,
    cache_write_tokens bigint NOT NULL,
    reasoning_tokens bigint NOT NULL,
    reported_cost_microdollars bigint,
    estimated_cost_microdollars bigint,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT llm_usage_marks_billing_mode_check CHECK ((billing_mode = ANY (ARRAY['metered'::text, 'subscription'::text]))),
    CONSTRAINT llm_usage_marks_cache_read_tokens_check CHECK ((cache_read_tokens >= 0)),
    CONSTRAINT llm_usage_marks_cache_write_tokens_check CHECK ((cache_write_tokens >= 0)),
    CONSTRAINT llm_usage_marks_estimated_cost_microdollars_check CHECK ((estimated_cost_microdollars >= 0)),
    CONSTRAINT llm_usage_marks_input_tokens_check CHECK ((input_tokens >= 0)),
    CONSTRAINT llm_usage_marks_output_tokens_check CHECK ((output_tokens >= 0)),
    CONSTRAINT llm_usage_marks_reasoning_tokens_check CHECK ((reasoning_tokens >= 0)),
    CONSTRAINT llm_usage_marks_reported_cost_microdollars_check CHECK ((reported_cost_microdollars >= 0)),
    CONSTRAINT llm_usage_marks_requests_check CHECK ((requests >= 0))
);

ALTER TABLE llm_usage_marks FORCE ROW LEVEL SECURITY;

CREATE TABLE lock_holds (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    name text NOT NULL,
    ticket_id bigint NOT NULL,
    agent_id uuid NOT NULL,
    queued_at timestamp with time zone,
    acquired_at timestamp with time zone NOT NULL,
    released_at timestamp with time zone,
    CONSTRAINT lock_holds_check CHECK (((queued_at IS NULL) OR (queued_at <= acquired_at))),
    CONSTRAINT lock_holds_check1 CHECK (((released_at IS NULL) OR (released_at >= acquired_at)))
);

ALTER TABLE lock_holds FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE lock_holds_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE lock_holds_id_seq OWNED BY lock_holds.id;

CREATE TABLE lock_waiters (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    name text NOT NULL,
    seq bigint NOT NULL,
    agent_id uuid NOT NULL,
    lease_id bigint NOT NULL,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    granted_at timestamp with time zone
);

ALTER TABLE lock_waiters FORCE ROW LEVEL SECURITY;

ALTER TABLE lock_waiters ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME lock_waiters_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE locks (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    name text NOT NULL,
    holder_agent_id uuid NOT NULL,
    holder_lease_id bigint NOT NULL,
    acquired_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE locks FORCE ROW LEVEL SECURITY;

CREATE TABLE merge_drift_decisions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    change_id bigint NOT NULL,
    decision text NOT NULL,
    reason text NOT NULL,
    drift_dirs text[] DEFAULT '{}'::text[] NOT NULL,
    head_sha text NOT NULL,
    base_tip_sha text NOT NULL,
    enforced boolean NOT NULL,
    decided_at timestamp with time zone DEFAULT now() NOT NULL,
    ci_head_sha text,
    ci_passed boolean,
    ci_recorded_at timestamp with time zone,
    CONSTRAINT merge_drift_decisions_check CHECK ((((ci_head_sha IS NULL) = (ci_passed IS NULL)) AND ((ci_passed IS NULL) = (ci_recorded_at IS NULL)))),
    CONSTRAINT merge_drift_decisions_ci_head_sha_check CHECK ((ci_head_sha <> ''::text)),
    CONSTRAINT merge_drift_decisions_decision_check CHECK ((decision = ANY (ARRAY['would_accept'::text, 'would_refuse'::text]))),
    CONSTRAINT merge_drift_decisions_head_sha_check CHECK ((head_sha <> ''::text))
);

ALTER TABLE merge_drift_decisions FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE merge_drift_decisions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE merge_drift_decisions_id_seq OWNED BY merge_drift_decisions.id;

CREATE TABLE pending_actions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    ticket_id bigint,
    agent_id uuid NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    base_status text NOT NULL,
    base_revision integer NOT NULL,
    base_pr_url text,
    base_review_started_at timestamp with time zone,
    status text DEFAULT 'pending'::text NOT NULL,
    outcome text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    decided_at timestamp with time zone,
    decided_by_user_id uuid,
    attempt_number integer,
    decided_by_user_id_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT pending_actions_attempt_by_kind CHECK (((kind = ANY (ARRAY['discussion_answer'::text, 'review_decision'::text])) = (attempt_number IS NOT NULL))),
    CONSTRAINT pending_actions_check CHECK (((status = 'pending'::text) = (decided_at IS NULL))),
    CONSTRAINT pending_actions_check1 CHECK ((expires_at > created_at)),
    CONSTRAINT pending_actions_decided_by_user_id_deleted_is_null CHECK (((NOT decided_by_user_id_deleted) OR (decided_by_user_id IS NULL))),
    CONSTRAINT pending_actions_kind_check CHECK ((kind = ANY (ARRAY['discussion_answer'::text, 'ready'::text, 'status'::text, 'review_decision'::text, 'edit'::text, 'manifest_edit'::text, 'close_merged'::text]))),
    CONSTRAINT pending_actions_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT pending_actions_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'withdrawn'::text, 'expired'::text, 'voided'::text, 'stale'::text, 'failed'::text]))),
    CONSTRAINT pending_actions_ticket_by_kind CHECK (((kind = ANY (ARRAY['discussion_answer'::text, 'ready'::text, 'status'::text, 'review_decision'::text, 'edit'::text, 'close_merged'::text])) = (ticket_id IS NOT NULL)))
);

ALTER TABLE pending_actions FORCE ROW LEVEL SECURITY;

CREATE TABLE remote_control_host_leases (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    holder text NOT NULL,
    acquired_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT remote_control_host_leases_holder_check CHECK (((char_length(holder) >= 1) AND (char_length(holder) <= 100)))
);

ALTER TABLE remote_control_host_leases FORCE ROW LEVEL SECURITY;

CREATE TABLE scope_blocks (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    blocking_ticket_id bigint NOT NULL,
    path text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    ended_at timestamp with time zone,
    end_reason text,
    CONSTRAINT scope_blocks_check CHECK (((ended_at IS NULL) = (end_reason IS NULL))),
    CONSTRAINT scope_blocks_end_reason_check CHECK ((end_reason = ANY (ARRAY['claimed'::text, 'cleared'::text])))
);

ALTER TABLE scope_blocks FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE scope_blocks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE scope_blocks_id_seq OWNED BY scope_blocks.id;

CREATE TABLE scope_extension_denials (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    agent_id uuid NOT NULL,
    path text NOT NULL,
    blocking_ticket_id bigint NOT NULL,
    blocking_path text NOT NULL,
    denied_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    resolution text,
    CONSTRAINT scope_extension_denials_check CHECK (((resolved_at IS NULL) = (resolution IS NULL))),
    CONSTRAINT scope_extension_denials_resolution_check CHECK ((resolution = ANY (ARRAY['granted'::text, 'narrowed'::text])))
);

ALTER TABLE scope_extension_denials FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE scope_extension_denials_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE scope_extension_denials_id_seq OWNED BY scope_extension_denials.id;

CREATE TABLE scope_narrowings (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    agent_id uuid NOT NULL,
    removed integer NOT NULL,
    kept integer NOT NULL,
    narrowed_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE scope_narrowings FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE scope_narrowings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE scope_narrowings_id_seq OWNED BY scope_narrowings.id;

CREATE TABLE server_merge_outcomes (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    change_url text NOT NULL,
    head_sha text NOT NULL,
    approver_user_id uuid,
    approver_user_id_deleted boolean DEFAULT false NOT NULL,
    approved_at timestamp with time zone,
    outcome text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    batch_id bigint,
    batch_size integer,
    batch_position integer,
    queue_depth integer,
    enqueued_at timestamp with time zone,
    CONSTRAINT server_merge_outcomes_approver_deleted_is_null CHECK (((NOT approver_user_id_deleted) OR (approver_user_id IS NULL))),
    CONSTRAINT server_merge_outcomes_attempt_number_check CHECK ((attempt_number > 0)),
    CONSTRAINT server_merge_outcomes_batch_position_check CHECK ((batch_position > 0)),
    CONSTRAINT server_merge_outcomes_batch_size_check CHECK ((batch_size > 0)),
    CONSTRAINT server_merge_outcomes_outcome_check CHECK ((outcome = ANY (ARRAY['merged'::text, 'main_moved'::text, 'checks_red'::text, 'checks_pending_timeout'::text, 'head_changed'::text, 'not_approved_for_head'::text, 'lock_timeout'::text, 'merge_refused'::text, 'code_host_error'::text, 'merge_main_conflict'::text, 'already_merged'::text, 'batch_voided'::text, 'batch_interaction'::text, 'checks_never_started'::text, 'ci_event_missing_requeued'::text]))),
    CONSTRAINT server_merge_outcomes_queue_depth_check CHECK ((queue_depth >= 0))
);

ALTER TABLE server_merge_outcomes FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE server_merge_outcomes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE server_merge_outcomes_id_seq OWNED BY server_merge_outcomes.id;

CREATE TABLE sessions (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    claude_session_id text NOT NULL,
    cwd text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    ended_at timestamp with time zone,
    last_progress_at timestamp with time zone DEFAULT now() NOT NULL,
    last_block_at timestamp with time zone,
    stop_block_count integer DEFAULT 0 NOT NULL,
    harness text DEFAULT ''::text NOT NULL,
    harness_version text DEFAULT ''::text NOT NULL,
    provider text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    config_revision bigint,
    stuck_reason text,
    stuck_at timestamp with time zone,
    bundle_hash text DEFAULT ''::text NOT NULL,
    CONSTRAINT sessions_bundle_hash_short CHECK ((char_length(bundle_hash) <= 100)),
    CONSTRAINT sessions_config_revision_check CHECK ((config_revision >= 0)),
    CONSTRAINT sessions_harness_check CHECK ((length(harness) <= 100)),
    CONSTRAINT sessions_harness_version_check CHECK ((length(harness_version) <= 100)),
    CONSTRAINT sessions_labels_check CHECK ((jsonb_typeof(labels) = 'object'::text)),
    CONSTRAINT sessions_model_check CHECK ((length(model) <= 100)),
    CONSTRAINT sessions_provider_check CHECK ((length(provider) <= 100)),
    CONSTRAINT sessions_stuck_together CHECK (((stuck_reason IS NULL) = (stuck_at IS NULL)))
);

ALTER TABLE sessions FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE sessions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE sessions_id_seq OWNED BY sessions.id;

CREATE TABLE steward_assessments (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    ticket_id bigint,
    pending_action_id uuid NOT NULL,
    category text NOT NULL,
    tier text NOT NULL,
    verdict text NOT NULL,
    mode text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    ticket_revision integer,
    head_sha text,
    applied_at timestamp with time zone,
    veto_deadline timestamp with time zone,
    vetoed_at timestamp with time zone,
    vetoed_by uuid,
    vetoed_by_deleted boolean DEFAULT false NOT NULL,
    undone_at timestamp with time zone,
    human_decision text,
    decided_at timestamp with time zone,
    agent_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    voided_at timestamp with time zone,
    void_kind text,
    void_reason text DEFAULT ''::text NOT NULL,
    steward_instructions_version_id uuid,
    model text,
    effort text,
    CONSTRAINT steward_assessments_check CHECK (((human_decision IS NULL) = (decided_at IS NULL))),
    CONSTRAINT steward_assessments_check1 CHECK (((NOT vetoed_by_deleted) OR (vetoed_by IS NULL))),
    CONSTRAINT steward_assessments_evidence_check CHECK ((jsonb_typeof(evidence) = 'array'::text)),
    CONSTRAINT steward_assessments_human_decision_check CHECK ((human_decision = ANY (ARRAY['approved'::text, 'rejected'::text, 'edited'::text, 'withdrawn'::text]))),
    CONSTRAINT steward_assessments_mode_check CHECK ((mode = ANY (ARRAY['shadow'::text, 'live'::text]))),
    CONSTRAINT steward_assessments_tier_check CHECK ((tier = ANY (ARRAY['auto'::text, 'veto'::text, 'human'::text]))),
    CONSTRAINT steward_assessments_verdict_check CHECK ((verdict = ANY (ARRAY['would_auto'::text, 'would_veto'::text, 'needs_human'::text]))),
    CONSTRAINT steward_assessments_void_kind_check CHECK ((void_kind = ANY (ARRAY['checks'::text, 'daily_cap'::text, 'refused'::text, 'not_applicable'::text]))),
    CONSTRAINT steward_assessments_void_or_applied CHECK (((voided_at IS NULL) OR (applied_at IS NULL))),
    CONSTRAINT steward_assessments_void_together CHECK (((voided_at IS NULL) = (void_kind IS NULL)))
);

ALTER TABLE steward_assessments FORCE ROW LEVEL SECURITY;

CREATE TABLE steward_discussion_applies (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    label_id uuid NOT NULL,
    tier text NOT NULL,
    apply_at timestamp with time zone NOT NULL,
    voided_at timestamp with time zone,
    voided_reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT steward_discussion_applies_check CHECK (((voided_at IS NULL) = (voided_reason = ''::text))),
    CONSTRAINT steward_discussion_applies_tier_check CHECK ((tier = ANY (ARRAY['auto'::text, 'veto'::text])))
);

ALTER TABLE steward_discussion_applies FORCE ROW LEVEL SECURITY;

CREATE TABLE steward_discussion_decisions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    label_id uuid NOT NULL,
    action text NOT NULL,
    original_body text DEFAULT ''::text NOT NULL,
    final_body text DEFAULT ''::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    actor text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    posted_at timestamp with time zone DEFAULT now(),
    CONSTRAINT steward_discussion_decisions_action_check CHECK ((action = ANY (ARRAY['approve'::text, 'edit'::text, 'reject'::text]))),
    CONSTRAINT steward_discussion_decisions_check CHECK (((action = 'reject'::text) = (final_body = ''::text))),
    CONSTRAINT steward_discussion_decisions_check1 CHECK (((action <> 'reject'::text) OR (reason <> ''::text)))
);

ALTER TABLE steward_discussion_decisions FORCE ROW LEVEL SECURITY;

CREATE TABLE steward_discussion_labels (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    ticket_id bigint NOT NULL,
    attempt integer NOT NULL,
    round integer NOT NULL,
    label text NOT NULL,
    category text DEFAULT ''::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    reasoning text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    assessment_id uuid,
    ticket_revision integer,
    head_sha text,
    assessed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT steward_discussion_labels_attempt_check CHECK ((attempt >= 1)),
    CONSTRAINT steward_discussion_labels_check CHECK (((label = 'routine'::text) = (category <> ''::text))),
    CONSTRAINT steward_discussion_labels_check1 CHECK (((label = 'not_assessed'::text) = (assessed_at IS NULL))),
    CONSTRAINT steward_discussion_labels_evidence_check CHECK ((jsonb_typeof(evidence) = 'array'::text)),
    CONSTRAINT steward_discussion_labels_label_check CHECK ((label = ANY (ARRAY['routine'::text, 'yours'::text, 'not_assessed'::text, 'pending'::text]))),
    CONSTRAINT steward_discussion_labels_round_check CHECK ((round >= 1))
);

ALTER TABLE steward_discussion_labels FORCE ROW LEVEL SECURITY;

CREATE TABLE steward_drafts (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    ticket_id bigint NOT NULL,
    pending_action_id uuid NOT NULL,
    category text NOT NULL,
    kind text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    would_send_back boolean DEFAULT false NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    ticket_revision integer NOT NULL,
    head_sha text,
    agent_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_at timestamp with time zone,
    applied_by uuid,
    CONSTRAINT steward_drafts_check CHECK (((applied_at IS NULL) = (applied_by IS NULL))),
    CONSTRAINT steward_drafts_check1 CHECK (((NOT would_send_back) OR (applied_at IS NULL))),
    CONSTRAINT steward_drafts_evidence_check CHECK ((jsonb_typeof(evidence) = 'array'::text))
);

ALTER TABLE steward_drafts FORCE ROW LEVEL SECURITY;

CREATE TABLE steward_leases (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    holder_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

ALTER TABLE steward_leases FORCE ROW LEVEL SECURITY;

CREATE TABLE supervisor_desired_agents (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    supervisor_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    harness text NOT NULL,
    state text NOT NULL,
    generation bigint NOT NULL,
    resume_generation bigint DEFAULT 0 NOT NULL,
    observed_generation bigint DEFAULT 0 NOT NULL,
    observed_phase text DEFAULT ''::text NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    retired_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    launch text DEFAULT 'manual'::text NOT NULL,
    observed_harness text DEFAULT ''::text NOT NULL,
    CONSTRAINT supervisor_desired_agents_check CHECK (((observed_generation >= 0) AND (observed_generation <= generation))),
    CONSTRAINT supervisor_desired_agents_check1 CHECK (((retired_at IS NULL) OR (observed_phase = 'retired'::text))),
    CONSTRAINT supervisor_desired_agents_failure_reason_check CHECK ((char_length(failure_reason) <= 500)),
    CONSTRAINT supervisor_desired_agents_generation_check CHECK ((generation >= 1)),
    CONSTRAINT supervisor_desired_agents_harness_check CHECK ((harness ~ '^[a-z][a-z0-9_-]{0,31}$'::text)),
    CONSTRAINT supervisor_desired_agents_kind_check CHECK ((kind = ANY (ARRAY['slot'::text, 'steward'::text, 'remote_control'::text]))),
    CONSTRAINT supervisor_desired_agents_launch_check CHECK ((launch = ANY (ARRAY['supervisor'::text, 'manual'::text]))),
    CONSTRAINT supervisor_desired_agents_name_check CHECK (((name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$'::text) AND (name <> ALL (ARRAY['.'::text, '..'::text])))),
    CONSTRAINT supervisor_desired_agents_observed_phase_check CHECK ((observed_phase = ANY (ARRAY[''::text, 'provisioning'::text, 'running'::text, 'paused'::text, 'failed'::text, 'retired'::text]))),
    CONSTRAINT supervisor_desired_agents_resume_generation_check CHECK ((resume_generation >= 0)),
    CONSTRAINT supervisor_desired_agents_state_check CHECK ((state = ANY (ARRAY['running'::text, 'paused'::text, 'removed'::text])))
);

ALTER TABLE supervisor_desired_agents FORCE ROW LEVEL SECURITY;

CREATE TABLE supervisor_desired_state (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    supervisor_id uuid NOT NULL,
    revision bigint DEFAULT 0 NOT NULL,
    CONSTRAINT supervisor_desired_state_revision_check CHECK ((revision >= 0))
);

ALTER TABLE supervisor_desired_state FORCE ROW LEVEL SECURITY;

CREATE TABLE supervisor_slot_states (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    phase text NOT NULL,
    phase_entered_at timestamp with time zone NOT NULL,
    ticket_id bigint,
    error_class text DEFAULT ''::text NOT NULL,
    error_streak integer DEFAULT 0 NOT NULL,
    reported_at timestamp with time zone DEFAULT now() NOT NULL,
    pause_reason text DEFAULT ''::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    reason_detail text DEFAULT ''::text NOT NULL,
    bundle_hash text DEFAULT ''::text NOT NULL,
    idle boolean DEFAULT false NOT NULL,
    CONSTRAINT supervisor_slot_states_bundle_hash_short CHECK ((char_length(bundle_hash) <= 100)),
    CONSTRAINT supervisor_slot_states_error_class_short CHECK ((length(error_class) <= 64)),
    CONSTRAINT supervisor_slot_states_error_streak_nonneg CHECK ((error_streak >= 0)),
    CONSTRAINT supervisor_slot_states_pause_reason_short CHECK ((char_length(pause_reason) <= 512)),
    CONSTRAINT supervisor_slot_states_reason_detail_short CHECK ((char_length(reason_detail) <= 160)),
    CONSTRAINT supervisor_slot_states_reason_short CHECK ((char_length(reason) <= 64))
)
WITH (fillfactor='90');

ALTER TABLE supervisor_slot_states FORCE ROW LEVEL SECURITY;

CREATE TABLE tenant_settings (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    setting_name text NOT NULL,
    setting_type text NOT NULL,
    str_value text,
    int_value bigint,
    bool_value boolean,
    float_value double precision,
    jsonb_value jsonb,
    changed_by uuid,
    changed_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT tenant_settings_check CHECK ((num_nonnulls(str_value, int_value, bool_value, float_value, jsonb_value) = 1)),
    CONSTRAINT tenant_settings_check1 CHECK (((setting_type = 'str'::text) = (str_value IS NOT NULL))),
    CONSTRAINT tenant_settings_check2 CHECK (((setting_type = 'int'::text) = (int_value IS NOT NULL))),
    CONSTRAINT tenant_settings_check3 CHECK (((setting_type = 'bool'::text) = (bool_value IS NOT NULL))),
    CONSTRAINT tenant_settings_check4 CHECK (((setting_type = 'float'::text) = (float_value IS NOT NULL))),
    CONSTRAINT tenant_settings_check5 CHECK (((setting_type = 'jsonb'::text) = (jsonb_value IS NOT NULL))),
    CONSTRAINT tenant_settings_check6 CHECK (((NOT changed_by_deleted) OR (changed_by IS NULL))),
    CONSTRAINT tenant_settings_setting_type_check CHECK ((setting_type = ANY (ARRAY['str'::text, 'int'::text, 'bool'::text, 'float'::text, 'jsonb'::text])))
);

ALTER TABLE tenant_settings FORCE ROW LEVEL SECURITY;

ALTER TABLE tenant_settings ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME tenant_settings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE ticket_changes (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    host text NOT NULL,
    owner text NOT NULL,
    repo text NOT NULL,
    number bigint NOT NULL,
    head_branch text NOT NULL,
    base_branch text NOT NULL,
    draft boolean NOT NULL,
    opened_by_agent_id uuid NOT NULL,
    opened_at timestamp with time zone DEFAULT now() NOT NULL,
    superseded_at timestamp with time zone,
    merged_at timestamp with time zone,
    merged_by_agent_id uuid,
    merge_sha text,
    reviewed_head_sha text,
    merging_since timestamp with time zone,
    reviewed_diff_fingerprint text,
    reviewed_base_sha text,
    attempt_number integer NOT NULL,
    closed_at timestamp with time zone,
    closed_reason text,
    base_deleted_at timestamp with time zone,
    merged_into_base_at timestamp with time zone,
    CONSTRAINT ticket_changes_base_branch_check CHECK ((base_branch <> ''::text)),
    CONSTRAINT ticket_changes_check CHECK (((merged_at IS NULL) = (merge_sha IS NULL))),
    CONSTRAINT ticket_changes_check1 CHECK (((merged_at IS NULL) = (merged_by_agent_id IS NULL))),
    CONSTRAINT ticket_changes_check2 CHECK (((merged_at IS NULL) OR (superseded_at IS NULL))),
    CONSTRAINT ticket_changes_closed_not_merged CHECK (((closed_at IS NULL) OR (merged_at IS NULL))),
    CONSTRAINT ticket_changes_closed_reason CHECK ((((closed_at IS NULL) = (closed_reason IS NULL)) AND (closed_reason = 'closing_rule'::text))),
    CONSTRAINT ticket_changes_head_branch_check CHECK ((head_branch <> ''::text)),
    CONSTRAINT ticket_changes_host_check CHECK ((host <> ''::text)),
    CONSTRAINT ticket_changes_merged_into_base_superseded CHECK (((merged_into_base_at IS NULL) OR (superseded_at IS NOT NULL))),
    CONSTRAINT ticket_changes_number_check CHECK ((number > 0)),
    CONSTRAINT ticket_changes_owner_check CHECK ((owner <> ''::text)),
    CONSTRAINT ticket_changes_repo_check CHECK ((repo <> ''::text)),
    CONSTRAINT ticket_changes_reviewed_base_sha_check CHECK ((reviewed_base_sha <> ''::text)),
    CONSTRAINT ticket_changes_reviewed_diff_fingerprint_check CHECK ((reviewed_diff_fingerprint <> ''::text)),
    CONSTRAINT ticket_changes_reviewed_diff_needs_head CHECK (((reviewed_diff_fingerprint IS NULL) OR (reviewed_head_sha IS NOT NULL))),
    CONSTRAINT ticket_changes_reviewed_head_sha_check CHECK ((reviewed_head_sha <> ''::text))
);

ALTER TABLE ticket_changes FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ticket_changes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ticket_changes_id_seq OWNED BY ticket_changes.id;

CREATE TABLE ticket_comments (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    author_agent_id uuid,
    author_user_id uuid,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    attempt_number integer,
    on_behalf_of_user_id uuid,
    author_user_id_deleted boolean DEFAULT false NOT NULL,
    on_behalf_of_user_id_deleted boolean DEFAULT false NOT NULL,
    idempotency_key text,
    CONSTRAINT ticket_comments_author_user_id_deleted_is_null CHECK (((NOT author_user_id_deleted) OR (author_user_id IS NULL))),
    CONSTRAINT ticket_comments_check CHECK (((num_nonnulls(author_agent_id, author_user_id) + (author_user_id_deleted)::integer) = 1)),
    CONSTRAINT ticket_comments_on_behalf_by_agent CHECK (((on_behalf_of_user_id IS NULL) OR (author_agent_id IS NOT NULL))),
    CONSTRAINT ticket_comments_on_behalf_of_user_id_deleted_is_null CHECK (((NOT on_behalf_of_user_id_deleted) OR (on_behalf_of_user_id IS NULL)))
);

ALTER TABLE ticket_comments FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ticket_comments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ticket_comments_id_seq OWNED BY ticket_comments.id;

CREATE TABLE ticket_deps (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    depends_on_ticket_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    added_by_agent_id uuid,
    CONSTRAINT ticket_deps_check CHECK ((ticket_id <> depends_on_ticket_id))
);

ALTER TABLE ticket_deps FORCE ROW LEVEL SECURITY;

CREATE TABLE ticket_paths (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    path text NOT NULL,
    source text DEFAULT 'filed'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ticket_paths_source_check CHECK ((source = ANY (ARRAY['filed'::text, 'extended'::text])))
);

ALTER TABLE ticket_paths FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ticket_paths_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ticket_paths_id_seq OWNED BY ticket_paths.id;

CREATE TABLE ticket_required_roles (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    role_id uuid NOT NULL
);

ALTER TABLE ticket_required_roles FORCE ROW LEVEL SECURITY;

CREATE TABLE ticket_revisions (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    ticket_id bigint NOT NULL,
    revision integer NOT NULL,
    editor_agent_id uuid,
    editor_user_id uuid,
    old_title text,
    new_title text,
    old_body text,
    new_body text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    editor_user_id_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT ticket_revisions_check CHECK (((num_nonnulls(editor_agent_id, editor_user_id) + (editor_user_id_deleted)::integer) = 1)),
    CONSTRAINT ticket_revisions_check1 CHECK (((old_title IS NULL) = (new_title IS NULL))),
    CONSTRAINT ticket_revisions_check2 CHECK (((old_body IS NULL) = (new_body IS NULL))),
    CONSTRAINT ticket_revisions_check3 CHECK (((new_title IS NOT NULL) OR (new_body IS NOT NULL))),
    CONSTRAINT ticket_revisions_editor_user_id_deleted_is_null CHECK (((NOT editor_user_id_deleted) OR (editor_user_id IS NULL))),
    CONSTRAINT ticket_revisions_revision_check CHECK ((revision > 0))
);

ALTER TABLE ticket_revisions FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ticket_revisions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ticket_revisions_id_seq OWNED BY ticket_revisions.id;

CREATE TABLE ticket_sequences (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    last_id bigint NOT NULL
);

ALTER TABLE ticket_sequences FORCE ROW LEVEL SECURITY;

CREATE TABLE ticket_snapshots (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    revision integer NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    paths jsonb DEFAULT '[]'::jsonb NOT NULL,
    comments_through_id bigint DEFAULT 0 NOT NULL,
    as_of timestamp with time zone NOT NULL,
    taken_at timestamp with time zone DEFAULT now() NOT NULL,
    source text NOT NULL,
    taken_by_user_id uuid,
    taken_by_user_id_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT ticket_snapshots_revision_check CHECK ((revision >= 0)),
    CONSTRAINT ticket_snapshots_source_check CHECK ((source = ANY (ARRAY['first_claim'::text, 'rebuilt'::text, 'refrozen'::text]))),
    CONSTRAINT ticket_snapshots_taken_by_deleted_is_null CHECK (((NOT taken_by_user_id_deleted) OR (taken_by_user_id IS NULL)))
);

ALTER TABLE ticket_snapshots FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ticket_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ticket_snapshots_id_seq OWNED BY ticket_snapshots.id;

CREATE TABLE ticket_status_changes (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    ticket_id bigint NOT NULL,
    from_status text,
    to_status text NOT NULL,
    priority text NOT NULL,
    changed_at timestamp with time zone DEFAULT now() NOT NULL,
    actor_kind text NOT NULL,
    actor_id uuid,
    cause text,
    actor_id_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT ticket_status_changes_actor_id_deleted_is_null CHECK (((NOT actor_id_deleted) OR (actor_id IS NULL))),
    CONSTRAINT ticket_status_changes_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['agent'::text, 'user'::text, 'system'::text]))),
    CONSTRAINT ticket_status_changes_check CHECK (((actor_kind <> 'system'::text) OR (actor_id IS NULL)))
);

ALTER TABLE ticket_status_changes FORCE ROW LEVEL SECURITY;

CREATE SEQUENCE ticket_status_changes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE ticket_status_changes_id_seq OWNED BY ticket_status_changes.id;

CREATE TABLE tickets (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint DEFAULT next_ticket_id() NOT NULL,
    title text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    priority text DEFAULT 'normal'::text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    created_by_agent_id uuid,
    created_by_user_id uuid,
    needs_discussion boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    revision integer DEFAULT 0 NOT NULL,
    requires_approval boolean DEFAULT false NOT NULL,
    post_reviewed_at timestamp with time zone,
    post_reviewed_by_user_id uuid,
    role_match text DEFAULT 'any'::text NOT NULL,
    intake_source_id uuid,
    intake_key text,
    intake_title text,
    base_commit text,
    next_attempt_number integer DEFAULT 2 NOT NULL,
    blocked_until timestamp with time zone,
    cooldown_releases integer DEFAULT 0 NOT NULL,
    cooldown_since timestamp with time zone,
    created_by_user_id_deleted boolean DEFAULT false NOT NULL,
    post_reviewed_by_user_id_deleted boolean DEFAULT false NOT NULL,
    base_ref text,
    CONSTRAINT tickets_base_commit_format CHECK ((base_commit ~ '^[0-9a-f]{7,64}$'::text)),
    CONSTRAINT tickets_check CHECK ((num_nonnulls(created_by_agent_id, created_by_user_id) <= 1)),
    CONSTRAINT tickets_check1 CHECK (((intake_source_id IS NULL) = (intake_key IS NULL))),
    CONSTRAINT tickets_cooldown_releases_check CHECK ((cooldown_releases >= 0)),
    CONSTRAINT tickets_created_by_user_id_deleted_is_null CHECK (((NOT created_by_user_id_deleted) OR (created_by_user_id IS NULL))),
    CONSTRAINT tickets_intake_key_check CHECK (((intake_key IS NULL) OR ((length(intake_key) >= 1) AND (length(intake_key) <= 200)))),
    CONSTRAINT tickets_intake_title_check CHECK (((intake_title IS NULL) OR (length(intake_title) <= 200))),
    CONSTRAINT tickets_next_attempt_number_check CHECK ((next_attempt_number > 1)),
    CONSTRAINT tickets_post_reviewed_by CHECK (((post_reviewed_at IS NULL) = ((post_reviewed_by_user_id IS NULL) AND (NOT post_reviewed_by_user_id_deleted)))),
    CONSTRAINT tickets_post_reviewed_by_user_id_deleted_is_null CHECK (((NOT post_reviewed_by_user_id_deleted) OR (post_reviewed_by_user_id IS NULL))),
    CONSTRAINT tickets_priority_check CHECK ((priority = ANY (ARRAY['low'::text, 'normal'::text, 'high'::text, 'urgent'::text]))),
    CONSTRAINT tickets_role_match_check CHECK ((role_match = ANY (ARRAY['any'::text, 'all'::text]))),
    CONSTRAINT tickets_status_check CHECK ((status = ANY (ARRAY['preparing'::text, 'parked'::text, 'open'::text, 'in_progress'::text, 'in_merge_queue'::text, 'blocked'::text, 'discussion'::text, 'review'::text, 'done'::text, 'cancelled'::text])))
);

ALTER TABLE tickets FORCE ROW LEVEL SECURITY;

CREATE TABLE usage_declarations (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id bigint NOT NULL,
    agent_id uuid NOT NULL,
    conversation_id uuid,
    ticket_id bigint,
    declared_at timestamp with time zone DEFAULT now() NOT NULL,
    attempt_number integer
);

ALTER TABLE usage_declarations FORCE ROW LEVEL SECURITY;

ALTER TABLE usage_declarations ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME usage_declarations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE user_tenant_settings (
    id bigint NOT NULL,
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    setting_name text NOT NULL,
    setting_type text NOT NULL,
    str_value text,
    int_value bigint,
    bool_value boolean,
    float_value double precision,
    jsonb_value jsonb,
    CONSTRAINT user_tenant_settings_check CHECK ((num_nonnulls(str_value, int_value, bool_value, float_value, jsonb_value) = 1)),
    CONSTRAINT user_tenant_settings_check1 CHECK (((setting_type = 'str'::text) = (str_value IS NOT NULL))),
    CONSTRAINT user_tenant_settings_check2 CHECK (((setting_type = 'int'::text) = (int_value IS NOT NULL))),
    CONSTRAINT user_tenant_settings_check3 CHECK (((setting_type = 'bool'::text) = (bool_value IS NOT NULL))),
    CONSTRAINT user_tenant_settings_check4 CHECK (((setting_type = 'float'::text) = (float_value IS NOT NULL))),
    CONSTRAINT user_tenant_settings_check5 CHECK (((setting_type = 'jsonb'::text) = (jsonb_value IS NOT NULL))),
    CONSTRAINT user_tenant_settings_setting_type_check CHECK ((setting_type = ANY (ARRAY['str'::text, 'int'::text, 'bool'::text, 'float'::text, 'jsonb'::text])))
);

ALTER TABLE user_tenant_settings FORCE ROW LEVEL SECURITY;

ALTER TABLE user_tenant_settings ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME user_tenant_settings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE variant_archives (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    version_id uuid NOT NULL,
    archived_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_by uuid,
    archived_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT variant_archives_archived_by_deleted_is_null CHECK (((NOT archived_by_deleted) OR (archived_by IS NULL)))
);

ALTER TABLE variant_archives FORCE ROW LEVEL SECURITY;

CREATE TABLE variant_versions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    version integer NOT NULL,
    harness text NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    fleet_instructions_version_id uuid,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT variant_versions_created_by_deleted_is_null CHECK (((NOT created_by_deleted) OR (created_by IS NULL))),
    CONSTRAINT variant_versions_name_check CHECK (((length(name) >= 1) AND (length(name) <= 64))),
    CONSTRAINT variant_versions_settings_check CHECK ((jsonb_typeof(settings) = 'object'::text)),
    CONSTRAINT variant_versions_version_check CHECK ((version >= 1))
);

ALTER TABLE variant_versions FORCE ROW LEVEL SECURITY;

CREATE TABLE worker_polls (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    agent_id uuid NOT NULL,
    last_polled_at timestamp with time zone DEFAULT now() NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    protocol integer DEFAULT 0 NOT NULL,
    CONSTRAINT worker_polls_protocol_range CHECK ((protocol >= 0))
)
WITH (fillfactor='90');

ALTER TABLE worker_polls FORCE ROW LEVEL SECURITY;

CREATE TABLE workspace_manifest_revisions (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    revision bigint NOT NULL,
    CONSTRAINT workspace_manifest_revisions_revision_check CHECK ((revision >= 0))
);

ALTER TABLE workspace_manifest_revisions FORCE ROW LEVEL SECURITY;

CREATE TABLE workspace_manifests (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    repos jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    updated_by_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT workspace_manifests_repos_check CHECK (((jsonb_typeof(repos) = 'array'::text) AND (jsonb_array_length(repos) > 0))),
    CONSTRAINT workspace_manifests_updated_by_deleted_is_null CHECK (((NOT updated_by_deleted) OR (updated_by IS NULL)))
);

ALTER TABLE workspace_manifests FORCE ROW LEVEL SECURITY;

ALTER TABLE agent_contexts ALTER COLUMN id SET DEFAULT nextval('agent_contexts_id_seq'::regclass);

ALTER TABLE attempt_status_changes ALTER COLUMN id SET DEFAULT nextval('attempt_status_changes_id_seq'::regclass);

ALTER TABLE attempt_verdicts ALTER COLUMN id SET DEFAULT nextval('attempt_verdicts_id_seq'::regclass);

ALTER TABLE batches ALTER COLUMN id SET DEFAULT nextval('batches_id_seq'::regclass);

ALTER TABLE global_consent_events ALTER COLUMN id SET DEFAULT nextval('global_consent_events_id_seq'::regclass);

ALTER TABLE ignored_merges ALTER COLUMN id SET DEFAULT nextval('ignored_merges_id_seq'::regclass);

ALTER TABLE leases ALTER COLUMN id SET DEFAULT nextval('leases_id_seq'::regclass);

ALTER TABLE lock_holds ALTER COLUMN id SET DEFAULT nextval('lock_holds_id_seq'::regclass);

ALTER TABLE merge_drift_decisions ALTER COLUMN id SET DEFAULT nextval('merge_drift_decisions_id_seq'::regclass);

ALTER TABLE scope_blocks ALTER COLUMN id SET DEFAULT nextval('scope_blocks_id_seq'::regclass);

ALTER TABLE scope_extension_denials ALTER COLUMN id SET DEFAULT nextval('scope_extension_denials_id_seq'::regclass);

ALTER TABLE scope_narrowings ALTER COLUMN id SET DEFAULT nextval('scope_narrowings_id_seq'::regclass);

ALTER TABLE server_merge_outcomes ALTER COLUMN id SET DEFAULT nextval('server_merge_outcomes_id_seq'::regclass);

ALTER TABLE sessions ALTER COLUMN id SET DEFAULT nextval('sessions_id_seq'::regclass);

ALTER TABLE ticket_changes ALTER COLUMN id SET DEFAULT nextval('ticket_changes_id_seq'::regclass);

ALTER TABLE ticket_comments ALTER COLUMN id SET DEFAULT nextval('ticket_comments_id_seq'::regclass);

ALTER TABLE ticket_paths ALTER COLUMN id SET DEFAULT nextval('ticket_paths_id_seq'::regclass);

ALTER TABLE ticket_revisions ALTER COLUMN id SET DEFAULT nextval('ticket_revisions_id_seq'::regclass);

ALTER TABLE ticket_snapshots ALTER COLUMN id SET DEFAULT nextval('ticket_snapshots_id_seq'::regclass);

ALTER TABLE ticket_status_changes ALTER COLUMN id SET DEFAULT nextval('ticket_status_changes_id_seq'::regclass);

ALTER TABLE global_users
    ADD CONSTRAINT global_users_pkey PRIMARY KEY (id);

ALTER TABLE global_users
    ADD CONSTRAINT global_users_email_key UNIQUE (email);

ALTER TABLE global_tenants
    ADD CONSTRAINT global_tenants_pkey PRIMARY KEY (id);

ALTER TABLE global_tenant_memberships
    ADD CONSTRAINT global_tenant_memberships_pkey PRIMARY KEY (id);

ALTER TABLE global_tenant_memberships
    ADD CONSTRAINT global_tenant_memberships_tenant_user_id_key UNIQUE (tenant, user_id);

CREATE INDEX global_tenant_memberships_by_user ON global_tenant_memberships USING btree (user_id);

ALTER TABLE global_auth_tokens
    ADD CONSTRAINT global_auth_tokens_pkey PRIMARY KEY (id);

ALTER TABLE global_auth_tokens
    ADD CONSTRAINT global_auth_tokens_token_hash_key UNIQUE (token_hash);

CREATE INDEX global_auth_tokens_by_user_purpose ON global_auth_tokens USING btree (user_id, purpose);

ALTER TABLE agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);

ALTER TABLE agents
    ADD CONSTRAINT agents_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE agents
    ADD CONSTRAINT agents_tenant_id_name_key UNIQUE (tenant_id, name);

ALTER TABLE ticket_sequences
    ADD CONSTRAINT ticket_sequences_pkey PRIMARY KEY (tenant_id);

ALTER TABLE tickets
    ADD CONSTRAINT tickets_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX tickets_claimable ON tickets USING btree (tenant_id, priority, id) WHERE (status = 'open'::text);

ALTER TABLE ticket_paths
    ADD CONSTRAINT ticket_paths_pkey PRIMARY KEY (id);

ALTER TABLE ticket_paths
    ADD CONSTRAINT ticket_paths_tenant_id_ticket_id_path_key UNIQUE (tenant_id, ticket_id, path);

CREATE INDEX ticket_paths_by_path ON ticket_paths USING btree (tenant_id, path);

ALTER TABLE ticket_deps
    ADD CONSTRAINT ticket_deps_pkey PRIMARY KEY (tenant_id, ticket_id, depends_on_ticket_id);

CREATE INDEX ticket_deps_by_dependency ON ticket_deps USING btree (tenant_id, depends_on_ticket_id);

ALTER TABLE ticket_comments
    ADD CONSTRAINT ticket_comments_pkey PRIMARY KEY (id);

CREATE INDEX ticket_comments_by_ticket ON ticket_comments USING btree (tenant_id, ticket_id, id);

ALTER TABLE agent_contexts
    ADD CONSTRAINT agent_contexts_pkey PRIMARY KEY (id);

CREATE INDEX agent_contexts_by_ticket ON agent_contexts USING btree (tenant_id, ticket_id, id);

ALTER TABLE sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

ALTER TABLE sessions
    ADD CONSTRAINT sessions_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE sessions
    ADD CONSTRAINT sessions_agent_id_claude_session_id_key UNIQUE (agent_id, claude_session_id);

CREATE UNIQUE INDEX sessions_one_current_per_agent ON sessions USING btree (agent_id) WHERE (ended_at IS NULL);

CREATE INDEX sessions_prunable ON sessions USING btree (ended_at) WHERE (ended_at IS NOT NULL);

ALTER TABLE leases
    ADD CONSTRAINT leases_pkey PRIMARY KEY (id);

ALTER TABLE leases
    ADD CONSTRAINT leases_tenant_id_id_key UNIQUE (tenant_id, id);

CREATE UNIQUE INDEX leases_one_active_per_agent ON leases USING btree (agent_id) WHERE (released_at IS NULL);

CREATE INDEX leases_active_by_expiry ON leases USING btree (tenant_id, expires_at) WHERE (released_at IS NULL);

CREATE INDEX leases_prunable ON leases USING btree (released_at) WHERE (released_at IS NOT NULL);

ALTER TABLE locks
    ADD CONSTRAINT locks_pkey PRIMARY KEY (tenant_id, name);

CREATE INDEX locks_by_lease ON locks USING btree (tenant_id, holder_lease_id);

ALTER TABLE global_invites
    ADD CONSTRAINT global_invites_pkey PRIMARY KEY (id);

ALTER TABLE global_invites
    ADD CONSTRAINT global_invites_token_hash_key UNIQUE (token_hash);

CREATE INDEX global_invites_by_tenant ON global_invites USING btree (tenant) WHERE (tenant IS NOT NULL);

CREATE INDEX global_invites_by_inviter ON global_invites USING btree (invited_by);

ALTER TABLE global_oauth_clients
    ADD CONSTRAINT global_oauth_clients_pkey PRIMARY KEY (id);

ALTER TABLE global_oauth_grants
    ADD CONSTRAINT global_oauth_grants_pkey PRIMARY KEY (id);

CREATE INDEX global_oauth_grants_by_agent ON global_oauth_grants USING btree (tenant, agent_id);

ALTER TABLE global_oauth_auth_requests
    ADD CONSTRAINT global_oauth_auth_requests_pkey PRIMARY KEY (id);

ALTER TABLE global_oauth_codes
    ADD CONSTRAINT global_oauth_codes_pkey PRIMARY KEY (code_hash);

ALTER TABLE global_oauth_device_codes
    ADD CONSTRAINT global_oauth_device_codes_pkey PRIMARY KEY (id);

ALTER TABLE global_oauth_device_codes
    ADD CONSTRAINT global_oauth_device_codes_device_code_hash_key UNIQUE (device_code_hash);

ALTER TABLE global_oauth_device_codes
    ADD CONSTRAINT global_oauth_device_codes_user_code_key UNIQUE (user_code);

ALTER TABLE global_oauth_tokens
    ADD CONSTRAINT global_oauth_tokens_pkey PRIMARY KEY (token_hash);

CREATE INDEX global_oauth_tokens_by_grant ON global_oauth_tokens USING btree (grant_id);

ALTER TABLE global_auth_providers
    ADD CONSTRAINT global_auth_providers_pkey PRIMARY KEY (name);

ALTER TABLE global_user_identities
    ADD CONSTRAINT global_user_identities_pkey PRIMARY KEY (provider, subject);

ALTER TABLE global_user_identities
    ADD CONSTRAINT global_user_identities_one_per_provider UNIQUE (user_id, provider);

CREATE INDEX leases_latest_by_agent ON leases USING btree (agent_id, id DESC);

ALTER TABLE ticket_revisions
    ADD CONSTRAINT ticket_revisions_pkey PRIMARY KEY (id);

ALTER TABLE ticket_revisions
    ADD CONSTRAINT ticket_revisions_tenant_id_ticket_id_revision_key UNIQUE (tenant_id, ticket_id, revision);

ALTER TABLE github_deliveries
    ADD CONSTRAINT github_deliveries_pkey PRIMARY KEY (tenant_id, delivery_id);

CREATE INDEX github_deliveries_by_received ON github_deliveries USING btree (tenant_id, received_at DESC);

ALTER TABLE agent_roles
    ADD CONSTRAINT agent_roles_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE agent_roles
    ADD CONSTRAINT agent_roles_tenant_id_name_key UNIQUE (tenant_id, name);

ALTER TABLE agent_role_members
    ADD CONSTRAINT agent_role_members_pkey PRIMARY KEY (tenant_id, agent_id, role_id);

CREATE INDEX agent_role_members_by_role ON agent_role_members USING btree (tenant_id, role_id);

ALTER TABLE ticket_required_roles
    ADD CONSTRAINT ticket_required_roles_pkey PRIMARY KEY (tenant_id, ticket_id, role_id);

CREATE INDEX ticket_required_roles_by_role ON ticket_required_roles USING btree (tenant_id, role_id);

ALTER TABLE harness_config
    ADD CONSTRAINT harness_config_tenant_id_agent_id_key UNIQUE NULLS NOT DISTINCT (tenant_id, agent_id);

ALTER TABLE harness_config_revisions
    ADD CONSTRAINT harness_config_revisions_pkey PRIMARY KEY (tenant_id);

ALTER TABLE llm_usage
    ADD CONSTRAINT llm_usage_pkey PRIMARY KEY (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode);

CREATE INDEX llm_usage_by_ticket ON llm_usage USING btree (tenant_id, ticket_id);

CREATE INDEX llm_usage_prunable ON llm_usage USING btree (updated_at);

ALTER TABLE llm_tool_usage
    ADD CONSTRAINT llm_tool_usage_pkey PRIMARY KEY (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode, tool, command);

ALTER TABLE board_revisions
    ADD CONSTRAINT board_revisions_pkey PRIMARY KEY (tenant_id);

ALTER TABLE lock_waiters
    ADD CONSTRAINT lock_waiters_pkey PRIMARY KEY (tenant_id, name, agent_id);

CREATE INDEX lock_waiters_in_order ON lock_waiters USING btree (tenant_id, name, seq);

CREATE INDEX lock_waiters_by_lease ON lock_waiters USING btree (tenant_id, lease_id);

ALTER TABLE global_consent_events
    ADD CONSTRAINT global_consent_events_pkey PRIMARY KEY (id);

CREATE INDEX global_consent_events_current_idx ON global_consent_events USING btree (user_id, purpose, decided_at DESC, id DESC);

CREATE UNIQUE INDEX global_consent_events_merge_token_idx ON global_consent_events USING btree (merge_token, purpose) WHERE (merge_token IS NOT NULL);

ALTER TABLE global_consent_merge_tokens
    ADD CONSTRAINT global_consent_merge_tokens_pkey PRIMARY KEY (token_hash);

ALTER TABLE global_legal_document_versions
    ADD CONSTRAINT global_legal_document_versions_pkey PRIMARY KEY (kind, version, locale);

CREATE INDEX global_legal_document_versions_effective_idx ON global_legal_document_versions USING btree (kind, locale, effective DESC, version DESC);

ALTER TABLE ticket_status_changes
    ADD CONSTRAINT ticket_status_changes_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX ticket_status_changes_by_ticket ON ticket_status_changes USING btree (tenant_id, ticket_id, id);

CREATE INDEX ticket_status_changes_by_time ON ticket_status_changes USING btree (tenant_id, changed_at);

CREATE INDEX ticket_status_changes_by_status_time ON ticket_status_changes USING btree (tenant_id, to_status, changed_at);

CREATE INDEX tickets_live ON tickets USING btree (tenant_id, status, priority, id) WHERE (status <> ALL (ARRAY['done'::text, 'cancelled'::text]));

CREATE INDEX llm_usage_by_ended ON llm_usage USING btree (tenant_id, ended_at);

CREATE INDEX llm_tool_usage_by_ended ON llm_tool_usage USING btree (tenant_id, ended_at);

ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX pending_actions_by_status ON pending_actions USING btree (tenant_id, status, created_at);

CREATE INDEX pending_actions_by_ticket ON pending_actions USING btree (tenant_id, ticket_id);

CREATE INDEX pending_actions_by_agent ON pending_actions USING btree (tenant_id, agent_id, created_at);

CREATE INDEX pending_actions_by_decided ON pending_actions USING btree (tenant_id, decided_at) WHERE (decided_at IS NOT NULL);

CREATE INDEX pending_actions_by_expiry ON pending_actions USING btree (tenant_id, expires_at) WHERE (status = 'pending'::text);

ALTER TABLE agent_workspace_layouts
    ADD CONSTRAINT agent_workspace_layouts_pkey PRIMARY KEY (tenant_id, agent_id);

ALTER TABLE workspace_manifests
    ADD CONSTRAINT workspace_manifests_pkey PRIMARY KEY (tenant_id);

ALTER TABLE agent_messages
    ADD CONSTRAINT agent_messages_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX agent_messages_recent ON agent_messages USING btree (tenant_id, id DESC);

ALTER TABLE global_intake_credentials
    ADD CONSTRAINT global_intake_credentials_pkey PRIMARY KEY (source_id);

CREATE INDEX global_intake_credentials_by_tenant ON global_intake_credentials USING btree (tenant);

ALTER TABLE intake_sources
    ADD CONSTRAINT intake_sources_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX intake_sources_live_name ON intake_sources USING btree (tenant_id, lower(name)) WHERE (revoked_at IS NULL);

CREATE INDEX tickets_by_intake_key ON tickets USING btree (tenant_id, intake_source_id, intake_key, id DESC) WHERE (intake_source_id IS NOT NULL);

ALTER TABLE intake_events
    ADD CONSTRAINT intake_events_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX intake_events_by_ticket ON intake_events USING btree (tenant_id, ticket_id, id);

CREATE UNIQUE INDEX intake_events_one_per_delivery ON intake_events USING btree (tenant_id, source_id, delivery_id);

ALTER TABLE intake_deliveries
    ADD CONSTRAINT intake_deliveries_pkey PRIMARY KEY (tenant_id, source_id, delivery_id);

CREATE INDEX intake_deliveries_by_received ON intake_deliveries USING btree (tenant_id, received_at DESC);

ALTER TABLE fleet_instruction_versions
    ADD CONSTRAINT fleet_instruction_versions_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE codehost_credentials
    ADD CONSTRAINT codehost_credentials_pkey PRIMARY KEY (tenant_id);

ALTER TABLE codehost_credentials
    ADD CONSTRAINT codehost_credentials_id_key UNIQUE (id);

CREATE INDEX agents_by_owner ON agents USING btree (tenant_id, owner_user_id) WHERE (owner_user_id IS NOT NULL);

ALTER TABLE ticket_changes
    ADD CONSTRAINT ticket_changes_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX ticket_changes_by_change ON ticket_changes USING btree (tenant_id, host, owner, repo, number);

ALTER TABLE conductor_conversations
    ADD CONSTRAINT conductor_conversations_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE conductor_conversations
    ADD CONSTRAINT conductor_conversations_tenant_id_id_conductor_id_key UNIQUE (tenant_id, id, conductor_id);

CREATE UNIQUE INDEX conductor_one_live_conversation ON conductor_conversations USING btree (tenant_id, conductor_id) WHERE (state = ANY (ARRAY['starting'::text, 'ready'::text]));

CREATE UNIQUE INDEX conductor_conversations_by_start_client_id ON conductor_conversations USING btree (tenant_id, conductor_id, start_client_id) WHERE (start_client_id IS NOT NULL);

CREATE INDEX conductor_conversations_by_conductor ON conductor_conversations USING btree (tenant_id, conductor_id, updated_at DESC);

CREATE INDEX conductor_conversations_prunable ON conductor_conversations USING btree (updated_at) WHERE (state = ANY (ARRAY['failed'::text, 'stopped'::text]));

ALTER TABLE conductor_conversation_events
    ADD CONSTRAINT conductor_conversation_events_pkey PRIMARY KEY (tenant_id, conversation_id, seq);

CREATE UNIQUE INDEX conductor_events_by_client_id ON conductor_conversation_events USING btree (tenant_id, conversation_id, client_id) WHERE (client_id IS NOT NULL);

ALTER TABLE conductor_queues
    ADD CONSTRAINT conductor_queues_pkey PRIMARY KEY (tenant_id, conductor_id);

ALTER TABLE conductor_work
    ADD CONSTRAINT conductor_work_pkey PRIMARY KEY (tenant_id, conductor_id, seq);

CREATE INDEX llm_usage_by_conversation ON llm_usage USING btree (tenant_id, conversation_id) WHERE (conversation_id IS NOT NULL);

ALTER TABLE global_user_settings
    ADD CONSTRAINT global_user_settings_pkey PRIMARY KEY (id);

CREATE INDEX global_user_settings_latest ON global_user_settings USING btree (user_id, setting_name, created_at DESC, id DESC);

ALTER TABLE codehost_setup_states
    ADD CONSTRAINT codehost_setup_states_pkey PRIMARY KEY (token_hash);

ALTER TABLE fleet_instruction_archives
    ADD CONSTRAINT fleet_instruction_archives_pkey PRIMARY KEY (tenant_id, version_id);

CREATE INDEX tickets_by_closed ON tickets USING btree (tenant_id, closed_at DESC NULLS LAST, id DESC);

ALTER TABLE attempts
    ADD CONSTRAINT attempts_pkey PRIMARY KEY (tenant_id, ticket_id, number);

CREATE UNIQUE INDEX attempts_one_promoted ON attempts USING btree (tenant_id, ticket_id) WHERE (promoted_at IS NOT NULL);

CREATE INDEX attempts_by_pr_url ON attempts USING btree (tenant_id, lower(pr_url) text_pattern_ops) WHERE (pr_url IS NOT NULL);

ALTER TABLE attempt_status_changes
    ADD CONSTRAINT attempt_status_changes_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX attempt_status_changes_by_attempt ON attempt_status_changes USING btree (tenant_id, ticket_id, attempt_number, id);

ALTER TABLE variant_versions
    ADD CONSTRAINT variant_versions_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE variant_versions
    ADD CONSTRAINT variant_versions_tenant_id_name_version_key UNIQUE (tenant_id, name, version);

ALTER TABLE variant_archives
    ADD CONSTRAINT variant_archives_pkey PRIMARY KEY (tenant_id, version_id);

CREATE INDEX tickets_by_updated ON tickets USING btree (tenant_id, updated_at DESC, id DESC);

CREATE INDEX tickets_by_priority ON tickets USING btree (tenant_id, (
CASE priority
    WHEN 'urgent'::text THEN 0
    WHEN 'high'::text THEN 1
    WHEN 'normal'::text THEN 2
    ELSE 3
END), id DESC);

ALTER TABLE lease_configs
    ADD CONSTRAINT lease_configs_pkey PRIMARY KEY (tenant_id, lease_id);

ALTER TABLE board_changes
    ADD CONSTRAINT board_changes_pkey PRIMARY KEY (tenant_id, revision, ticket_id);

ALTER TABLE usage_declarations
    ADD CONSTRAINT usage_declarations_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX usage_declarations_current ON usage_declarations USING btree (tenant_id, agent_id, conversation_id, id DESC);

ALTER TABLE llm_usage_marks
    ADD CONSTRAINT llm_usage_marks_pkey PRIMARY KEY (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode);

CREATE INDEX llm_usage_marks_prunable ON llm_usage_marks USING btree (updated_at);

CREATE INDEX llm_guidance_usage_by_ticket ON llm_guidance_usage USING btree (tenant_id, ticket_id);

CREATE INDEX llm_guidance_usage_by_ended ON llm_guidance_usage USING btree (tenant_id, ended_at);

CREATE INDEX tickets_cooldown_idx ON tickets USING btree (tenant_id, blocked_until) WHERE ((status = 'blocked'::text) AND (blocked_until IS NOT NULL));

CREATE UNIQUE INDEX leases_one_active_per_attempt ON leases USING btree (tenant_id, ticket_id, attempt_number) WHERE (released_at IS NULL);

CREATE UNIQUE INDEX ticket_changes_live_per_attempt ON ticket_changes USING btree (tenant_id, ticket_id, attempt_number) WHERE ((superseded_at IS NULL) AND (merged_at IS NULL));

CREATE UNIQUE INDEX pending_actions_one_pending_per_attempt ON pending_actions USING btree (tenant_id, ticket_id, attempt_number, kind) NULLS NOT DISTINCT WHERE (status = 'pending'::text);

CREATE UNIQUE INDEX llm_guidance_usage_key ON llm_guidance_usage USING btree (tenant_id, agent_id, transcript_key, ticket_id, COALESCE(attempt_number, 0), provider, model, harness, billing_mode);

CREATE INDEX attempts_by_work_status ON attempts USING btree (tenant_id, status, ticket_id, number) WHERE (status = ANY (ARRAY['in_progress'::text, 'discussion'::text, 'review'::text]));

ALTER TABLE user_tenant_settings
    ADD CONSTRAINT user_tenant_settings_pkey PRIMARY KEY (id);

CREATE INDEX user_tenant_settings_latest ON user_tenant_settings USING btree (tenant_id, user_id, setting_name, id DESC);

ALTER TABLE tenant_settings
    ADD CONSTRAINT tenant_settings_pkey PRIMARY KEY (id);

CREATE INDEX tenant_settings_latest ON tenant_settings USING btree (tenant_id, setting_name, id DESC);

ALTER TABLE global_former_memberships
    ADD CONSTRAINT global_former_memberships_pkey PRIMARY KEY (tenant, user_id);

CREATE INDEX global_former_memberships_by_user ON global_former_memberships USING btree (user_id);

ALTER TABLE global_github_app_installations
    ADD CONSTRAINT global_github_app_installations_pkey PRIMARY KEY (tenant);

CREATE INDEX global_github_app_installations_by_installation ON global_github_app_installations USING btree (installation_id);

ALTER TABLE github_event_keys
    ADD CONSTRAINT github_event_keys_pkey PRIMARY KEY (tenant_id, event_key);

ALTER TABLE scope_blocks
    ADD CONSTRAINT scope_blocks_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX scope_blocks_one_open ON scope_blocks USING btree (tenant_id, ticket_id, blocking_ticket_id, path) WHERE (ended_at IS NULL);

CREATE INDEX scope_blocks_by_start ON scope_blocks USING btree (tenant_id, started_at);

CREATE INDEX scope_blocks_by_ticket ON scope_blocks USING btree (tenant_id, ticket_id) WHERE (ended_at IS NULL);

ALTER TABLE scope_extension_denials
    ADD CONSTRAINT scope_extension_denials_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX scope_extension_denials_by_time ON scope_extension_denials USING btree (tenant_id, denied_at);

CREATE INDEX scope_extension_denials_open ON scope_extension_denials USING btree (tenant_id, ticket_id) WHERE (resolved_at IS NULL);

ALTER TABLE scope_narrowings
    ADD CONSTRAINT scope_narrowings_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX scope_narrowings_by_time ON scope_narrowings USING btree (tenant_id, narrowed_at);

CREATE INDEX conductor_events_user_by_seq ON conductor_conversation_events USING btree (tenant_id, conversation_id, seq) WHERE (kind = 'user'::text);

ALTER TABLE workspace_manifest_revisions
    ADD CONSTRAINT workspace_manifest_revisions_pkey PRIMARY KEY (tenant_id);

ALTER TABLE lock_holds
    ADD CONSTRAINT lock_holds_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX lock_holds_one_open ON lock_holds USING btree (tenant_id, name) WHERE (released_at IS NULL);

CREATE INDEX lock_holds_by_acquired ON lock_holds USING btree (tenant_id, acquired_at);

ALTER TABLE worker_polls
    ADD CONSTRAINT worker_polls_pkey PRIMARY KEY (tenant_id, agent_id);

CREATE UNIQUE INDEX agents_one_replacement ON agents USING btree (tenant_id, replaces_agent_id) WHERE (replaces_agent_id IS NOT NULL);

ALTER TABLE merge_drift_decisions
    ADD CONSTRAINT merge_drift_decisions_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE merge_drift_decisions
    ADD CONSTRAINT merge_drift_decisions_tenant_id_change_id_head_sha_base_tip_key UNIQUE (tenant_id, change_id, head_sha, base_tip_sha);

ALTER TABLE supervisor_slot_states
    ADD CONSTRAINT supervisor_slot_states_pkey PRIMARY KEY (tenant_id, agent_id);

CREATE INDEX merge_drift_decisions_by_decided ON merge_drift_decisions USING btree (tenant_id, decided_at);

ALTER TABLE server_merge_outcomes
    ADD CONSTRAINT server_merge_outcomes_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX server_merge_outcomes_by_created ON server_merge_outcomes USING btree (tenant_id, created_at);

CREATE INDEX agents_by_supervisor ON agents USING btree (tenant_id, supervisor_agent_id) WHERE (supervisor_agent_id IS NOT NULL);

ALTER TABLE steward_assessments
    ADD CONSTRAINT steward_assessments_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX steward_assessments_by_action ON steward_assessments USING btree (tenant_id, pending_action_id);

CREATE INDEX steward_assessments_by_category ON steward_assessments USING btree (tenant_id, category, created_at);

CREATE INDEX llm_usage_daily_by_day ON llm_usage_daily USING btree (day);

CREATE INDEX agent_messages_pending ON agent_messages USING btree (tenant_id, agent_id, id) WHERE ((delivered_at IS NULL) AND (dropped_at IS NULL));

CREATE INDEX llm_usage_unreliable_by_ended ON llm_usage USING btree (tenant_id, ended_at) WHERE unreliable;

CREATE UNIQUE INDEX agents_one_live_steward ON agents USING btree (tenant_id) WHERE ((kind = 'steward'::text) AND (archived_at IS NULL));

ALTER TABLE steward_leases
    ADD CONSTRAINT steward_leases_pkey PRIMARY KEY (tenant_id);

ALTER TABLE llm_usage_daily
    ADD CONSTRAINT llm_usage_daily_pkey PRIMARY KEY (tenant_id, day, agent_id, provider, model, billing_mode, unreliable);

ALTER TABLE supervisor_desired_state
    ADD CONSTRAINT supervisor_desired_state_pkey PRIMARY KEY (tenant_id, supervisor_id);

ALTER TABLE supervisor_desired_agents
    ADD CONSTRAINT supervisor_desired_agents_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX supervisor_desired_agents_live_name ON supervisor_desired_agents USING btree (tenant_id, name) WHERE (retired_at IS NULL);

CREATE INDEX supervisor_desired_agents_supervisor ON supervisor_desired_agents USING btree (tenant_id, supervisor_id) WHERE (retired_at IS NULL);

CREATE INDEX steward_assessments_due ON steward_assessments USING btree (tenant_id, veto_deadline) WHERE ((applied_at IS NULL) AND (voided_at IS NULL) AND (veto_deadline IS NOT NULL));

ALTER TABLE landings
    ADD CONSTRAINT landings_pkey PRIMARY KEY (tenant_id, ticket_id);

ALTER TABLE steward_drafts
    ADD CONSTRAINT steward_drafts_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX steward_drafts_by_action ON steward_drafts USING btree (tenant_id, pending_action_id);

ALTER TABLE fleet_instruction_versions
    ADD CONSTRAINT fleet_instruction_versions_kind_name_version_key UNIQUE (tenant_id, kind, name, version);

ALTER TABLE fleet_instruction_versions
    ADD CONSTRAINT fleet_instruction_versions_kind_id_key UNIQUE (tenant_id, id, kind);

ALTER TABLE fleet_instruction_defaults
    ADD CONSTRAINT fleet_instruction_defaults_pkey PRIMARY KEY (tenant_id, kind);

ALTER TABLE ignored_merges
    ADD CONSTRAINT ignored_merges_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX ignored_merges_by_change ON ignored_merges USING btree (tenant_id, ticket_id, attempt_number, host, owner, repo, number);

ALTER TABLE batches
    ADD CONSTRAINT batches_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE global_harnesses
    ADD CONSTRAINT global_harnesses_pkey PRIMARY KEY (name);

ALTER TABLE ticket_snapshots
    ADD CONSTRAINT ticket_snapshots_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX ticket_snapshots_by_ticket ON ticket_snapshots USING btree (tenant_id, ticket_id, id);

ALTER TABLE remote_control_host_leases
    ADD CONSTRAINT remote_control_host_leases_pkey PRIMARY KEY (tenant_id, agent_id);

CREATE INDEX attempts_replay_batch_idx ON attempts USING btree (replay_batch_id) WHERE (replay_batch_id IS NOT NULL);

ALTER TABLE steward_discussion_labels
    ADD CONSTRAINT steward_discussion_labels_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX steward_discussion_labels_by_round ON steward_discussion_labels USING btree (tenant_id, ticket_id, attempt, round);

CREATE INDEX steward_discussion_labels_by_run ON steward_discussion_labels USING btree (tenant_id, assessed_at) WHERE (assessed_at IS NOT NULL);

ALTER TABLE steward_discussion_decisions
    ADD CONSTRAINT steward_discussion_decisions_pkey PRIMARY KEY (tenant_id, id);

CREATE UNIQUE INDEX steward_discussion_decisions_by_label ON steward_discussion_decisions USING btree (tenant_id, label_id);

ALTER TABLE attempt_signals
    ADD CONSTRAINT attempt_signals_pkey PRIMARY KEY (tenant_id, ticket_id, attempt_number);

ALTER TABLE steward_discussion_applies
    ADD CONSTRAINT steward_discussion_applies_pkey PRIMARY KEY (tenant_id, label_id);

CREATE UNIQUE INDEX ticket_comments_by_idempotency_key ON ticket_comments USING btree (tenant_id, ticket_id, idempotency_key) WHERE (idempotency_key IS NOT NULL);

ALTER TABLE eval_sets
    ADD CONSTRAINT eval_sets_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE eval_sets
    ADD CONSTRAINT eval_sets_tenant_id_name_key UNIQUE (tenant_id, name);

ALTER TABLE eval_set_batches
    ADD CONSTRAINT eval_set_batches_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE eval_set_batches
    ADD CONSTRAINT eval_set_batches_tenant_id_batch_id_key UNIQUE (tenant_id, batch_id);

CREATE INDEX eval_set_batches_by_set ON eval_set_batches USING btree (tenant_id, eval_set_id, created_at DESC);

ALTER TABLE attempt_verdicts
    ADD CONSTRAINT attempt_verdicts_pkey PRIMARY KEY (tenant_id, id);

CREATE INDEX attempt_verdicts_by_attempt ON attempt_verdicts USING btree (tenant_id, ticket_id, attempt_number, id);

ALTER TABLE attachments
    ADD CONSTRAINT attachments_pkey PRIMARY KEY (tenant_id, id);

ALTER TABLE attachments
    ADD CONSTRAINT attachments_tenant_id_storage_key_key UNIQUE (tenant_id, storage_key);

CREATE INDEX attachments_by_ticket ON attachments USING btree (tenant_id, ticket_id, created_at) WHERE (ticket_id IS NOT NULL);

CREATE INDEX attachments_staged ON attachments USING btree (tenant_id, conversation_id) WHERE (ticket_id IS NULL);

ALTER TABLE attachment_reservations
    ADD CONSTRAINT attachment_reservations_pkey PRIMARY KEY (tenant_id, storage_key);

ALTER TABLE attachment_delete_queue
    ADD CONSTRAINT attachment_delete_queue_pkey PRIMARY KEY (tenant_id, storage_key);

CREATE INDEX attachment_delete_queue_pending ON attachment_delete_queue USING btree (tenant_id, queued_at) WHERE (swept_at IS NULL);

ALTER TABLE attachment_usage
    ADD CONSTRAINT attachment_usage_pkey PRIMARY KEY (tenant_id);

CREATE INDEX landings_in_order ON landings USING btree (tenant_id, queue_state, seq);

CREATE TRIGGER agent_messages_mark_deleted_actors BEFORE UPDATE OF author_user_id ON agent_messages FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('author_user_id');

CREATE TRIGGER agents_archived_revoke_grants AFTER INSERT OR UPDATE OF archived_at ON agents FOR EACH ROW WHEN ((new.archived_at IS NOT NULL)) EXECUTE FUNCTION agents_archived_revoke_grants();

CREATE TRIGGER agents_kind_owner_immutable BEFORE UPDATE OF kind, owner_user_id ON agents FOR EACH ROW WHEN (((old.kind IS DISTINCT FROM new.kind) OR (old.owner_user_id IS DISTINCT FROM new.owner_user_id))) EXECUTE FUNCTION agents_kind_owner_immutable();

CREATE TRIGGER agents_mark_deleted_actors BEFORE UPDATE OF owner_user_id ON agents FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('owner_user_id');

CREATE TRIGGER agents_owner_is_member BEFORE INSERT ON agents FOR EACH ROW WHEN ((new.kind = 'conductor'::text)) EXECUTE FUNCTION agents_owner_is_member();

CREATE TRIGGER agents_run_by_supervisor_is_supervisor BEFORE INSERT ON agents FOR EACH ROW WHEN ((new.run_by_supervisor_id IS NOT NULL)) EXECUTE FUNCTION agents_run_by_supervisor_is_supervisor();

CREATE TRIGGER agents_steward_minted_by_immutable BEFORE UPDATE OF steward_minted_by ON agents FOR EACH ROW WHEN ((old.steward_minted_by IS DISTINCT FROM new.steward_minted_by)) EXECUTE FUNCTION agents_steward_minted_by_immutable();

CREATE TRIGGER agents_steward_minted_by_supervisor BEFORE INSERT ON agents FOR EACH ROW WHEN ((new.steward_minted_by IS NOT NULL)) EXECUTE FUNCTION agents_steward_minted_by_supervisor();

CREATE TRIGGER agents_supervisor_immutable BEFORE UPDATE OF supervisor_agent_id ON agents FOR EACH ROW WHEN (((old.supervisor_agent_id IS NOT NULL) AND (old.supervisor_agent_id IS DISTINCT FROM new.supervisor_agent_id))) EXECUTE FUNCTION agents_supervisor_immutable();

CREATE TRIGGER agents_supervisor_is_supervisor BEFORE INSERT ON agents FOR EACH ROW WHEN ((new.supervisor_agent_id IS NOT NULL)) EXECUTE FUNCTION agents_supervisor_is_supervisor();

CREATE TRIGGER agents_supervisor_is_supervisor_on_link BEFORE UPDATE OF supervisor_agent_id ON agents FOR EACH ROW WHEN (((new.supervisor_agent_id IS NOT NULL) AND (old.supervisor_agent_id IS DISTINCT FROM new.supervisor_agent_id))) EXECUTE FUNCTION agents_supervisor_is_supervisor();

CREATE TRIGGER attachments_after_delete AFTER DELETE ON attachments FOR EACH ROW EXECUTE FUNCTION attachments_after_delete();

CREATE TRIGGER attachments_after_insert AFTER INSERT ON attachments FOR EACH ROW EXECUTE FUNCTION attachments_after_insert();

CREATE TRIGGER attachments_mark_deleted_actors BEFORE UPDATE OF uploader_user_id ON attachments FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('uploader_user_id');

CREATE TRIGGER attachments_require_reservation BEFORE INSERT ON attachments FOR EACH ROW EXECUTE FUNCTION attachments_require_reservation();

CREATE TRIGGER attempt_status_change AFTER INSERT OR UPDATE OF status ON attempts FOR EACH ROW EXECUTE FUNCTION record_attempt_status_change();

CREATE TRIGGER attempt_verdicts_mark_deleted_actors BEFORE UPDATE OF set_by_user_id ON attempt_verdicts FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('set_by_user_id');

CREATE TRIGGER attempt_with_ticket BEFORE UPDATE OF ticket_id ON github_deliveries FOR EACH ROW EXECUTE FUNCTION clear_attempt_without_ticket();

CREATE TRIGGER attempt_with_ticket BEFORE UPDATE OF ticket_id ON llm_usage FOR EACH ROW EXECUTE FUNCTION clear_attempt_without_ticket();

CREATE TRIGGER attempts_mark_deleted_actors BEFORE UPDATE OF approved_by_user_id, promoted_by_user_id ON attempts FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('approved_by_user_id', 'promoted_by_user_id');

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON agent_contexts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER UPDATE OF delivered_at, dropped_at ON agent_messages DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON agent_role_members DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON agent_roles DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE OF disabled_at ON agents DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON attempts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON landings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR UPDATE OF released_at ON leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE OF granted_at ON lock_waiters DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON locks DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON pending_actions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON ticket_comments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON ticket_deps DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON ticket_paths DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON ticket_required_roles DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision AFTER INSERT OR DELETE OR UPDATE ON tickets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE CONSTRAINT TRIGGER board_revision_variant AFTER UPDATE OF variant_id ON agents DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION bump_board_revision();

CREATE TRIGGER codehost_credentials_mark_deleted_actors BEFORE UPDATE OF created_by ON codehost_credentials FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

CREATE TRIGGER conductor_conversations_mark_deleted_actors BEFORE UPDATE OF owner_user_id ON conductor_conversations FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('owner_user_id');

CREATE TRIGGER eval_set_batches_mark_deleted_actors BEFORE UPDATE OF created_by ON eval_set_batches FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

CREATE TRIGGER eval_sets_mark_deleted_actors BEFORE UPDATE OF created_by ON eval_sets FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

CREATE TRIGGER fleet_instruction_archives_mark_deleted_actors BEFORE UPDATE OF archived_by ON fleet_instruction_archives FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('archived_by');

CREATE TRIGGER fleet_instruction_defaults_mark_deleted_actors BEFORE UPDATE OF updated_by ON fleet_instruction_defaults FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('updated_by');

CREATE TRIGGER fleet_instruction_versions_immutable BEFORE DELETE OR UPDATE ON fleet_instruction_versions FOR EACH ROW EXECUTE FUNCTION fleet_instruction_versions_immutable();

CREATE TRIGGER fleet_instruction_versions_mark_deleted_actors BEFORE UPDATE OF created_by ON fleet_instruction_versions FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

CREATE TRIGGER global_consent_events_no_change BEFORE DELETE OR UPDATE ON global_consent_events FOR EACH ROW EXECUTE FUNCTION global_consent_events_append_only();

CREATE TRIGGER global_consent_events_no_truncate BEFORE TRUNCATE ON global_consent_events FOR EACH STATEMENT EXECUTE FUNCTION global_consent_events_append_only();

CREATE TRIGGER global_legal_document_versions_no_change BEFORE DELETE OR UPDATE ON global_legal_document_versions FOR EACH ROW EXECUTE FUNCTION global_legal_document_versions_immutable();

CREATE TRIGGER global_legal_document_versions_no_truncate BEFORE TRUNCATE ON global_legal_document_versions FOR EACH STATEMENT EXECUTE FUNCTION global_legal_document_versions_immutable();

CREATE TRIGGER global_oauth_grants_agent_available BEFORE INSERT ON global_oauth_grants FOR EACH ROW EXECUTE FUNCTION global_oauth_grants_agent_available();

CREATE TRIGGER global_oauth_grants_mark_deleted_actors BEFORE UPDATE OF approved_by ON global_oauth_grants FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('approved_by');

CREATE TRIGGER global_tenant_memberships_clear_former BEFORE INSERT ON global_tenant_memberships FOR EACH ROW EXECUTE FUNCTION global_tenant_memberships_clear_former();

CREATE TRIGGER global_tenant_memberships_disable_conductors BEFORE DELETE ON global_tenant_memberships FOR EACH ROW EXECUTE FUNCTION global_tenant_memberships_disable_conductors();

CREATE TRIGGER global_tenant_memberships_record_former BEFORE DELETE ON global_tenant_memberships FOR EACH ROW EXECUTE FUNCTION global_tenant_memberships_record_former();

CREATE TRIGGER global_tenants_mark_deleted_actors BEFORE UPDATE OF archived_by ON global_tenants FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('archived_by');

CREATE TRIGGER global_tenants_seed_sensitive_words AFTER INSERT ON global_tenants FOR EACH ROW EXECUTE FUNCTION global_tenants_seed_sensitive_words();

CREATE TRIGGER global_users_clear_unkeyed_actors BEFORE DELETE ON global_users FOR EACH ROW EXECUTE FUNCTION global_users_clear_unkeyed_actors();

CREATE TRIGGER intake_sources_mark_deleted_actors BEFORE UPDATE OF created_by ON intake_sources FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

CREATE TRIGGER lease_configs_immutable BEFORE UPDATE ON lease_configs FOR EACH ROW EXECUTE FUNCTION lease_configs_immutable();

CREATE TRIGGER leases_drop_messages AFTER UPDATE OF released_at ON leases FOR EACH ROW WHEN (((old.released_at IS NULL) AND (new.released_at IS NOT NULL))) EXECUTE FUNCTION drop_messages_of_ended_lease();

CREATE TRIGGER lock_holds_close AFTER DELETE ON locks FOR EACH ROW EXECUTE FUNCTION lock_holds_close();

CREATE TRIGGER lock_holds_open AFTER INSERT ON locks FOR EACH ROW EXECUTE FUNCTION lock_holds_open();

CREATE TRIGGER pending_actions_mark_deleted_actors BEFORE UPDATE OF decided_by_user_id ON pending_actions FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('decided_by_user_id');

CREATE TRIGGER server_merge_outcomes_mark_deleted_actors BEFORE UPDATE OF approver_user_id ON server_merge_outcomes FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('approver_user_id');

CREATE TRIGGER steward_assessments_mark_deleted_actors BEFORE UPDATE OF vetoed_by ON steward_assessments FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('vetoed_by');

CREATE TRIGGER sync_github_app_installation AFTER INSERT OR DELETE OR UPDATE ON codehost_credentials FOR EACH ROW EXECUTE FUNCTION sync_github_app_installation();

CREATE TRIGGER tenant_settings_mark_deleted_actors BEFORE UPDATE OF changed_by ON tenant_settings FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('changed_by');

CREATE TRIGGER ticket_comments_deliver_human AFTER INSERT ON ticket_comments FOR EACH ROW WHEN (((new.author_user_id IS NOT NULL) OR (new.on_behalf_of_user_id IS NOT NULL))) EXECUTE FUNCTION deliver_human_comment();

CREATE TRIGGER ticket_comments_mark_deleted_actors BEFORE UPDATE OF author_user_id, on_behalf_of_user_id ON ticket_comments FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('author_user_id', 'on_behalf_of_user_id');

CREATE TRIGGER ticket_create_attempt AFTER INSERT ON tickets FOR EACH ROW EXECUTE FUNCTION create_first_attempt();

CREATE TRIGGER ticket_revisions_mark_deleted_actors BEFORE UPDATE OF editor_user_id ON ticket_revisions FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('editor_user_id');

CREATE TRIGGER ticket_snapshots_mark_deleted_actors BEFORE UPDATE OF taken_by_user_id ON ticket_snapshots FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('taken_by_user_id');

CREATE TRIGGER ticket_status_change AFTER INSERT OR UPDATE OF status ON tickets FOR EACH ROW EXECUTE FUNCTION record_ticket_status_change();

CREATE TRIGGER tickets_mark_deleted_actors BEFORE UPDATE OF created_by_user_id, post_reviewed_by_user_id ON tickets FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by_user_id', 'post_reviewed_by_user_id');

CREATE TRIGGER variant_archives_mark_deleted_actors BEFORE UPDATE OF archived_by ON variant_archives FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('archived_by');

CREATE TRIGGER variant_versions_immutable BEFORE DELETE OR UPDATE ON variant_versions FOR EACH ROW EXECUTE FUNCTION variant_versions_immutable();

CREATE TRIGGER variant_versions_mark_deleted_actors BEFORE UPDATE OF created_by ON variant_versions FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

CREATE TRIGGER workspace_manifests_mark_deleted_actors BEFORE UPDATE OF updated_by ON workspace_manifests FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('updated_by');

ALTER TABLE global_tenant_memberships
    ADD CONSTRAINT global_tenant_memberships_tenant_fkey FOREIGN KEY (tenant) REFERENCES global_tenants(id) ON DELETE CASCADE;

ALTER TABLE global_tenant_memberships
    ADD CONSTRAINT global_tenant_memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE global_auth_tokens
    ADD CONSTRAINT global_auth_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_tenant_id_created_by_agent_id_fkey FOREIGN KEY (tenant_id, created_by_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE ticket_paths
    ADD CONSTRAINT ticket_paths_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_deps
    ADD CONSTRAINT ticket_deps_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_deps
    ADD CONSTRAINT ticket_deps_tenant_id_depends_on_ticket_id_fkey FOREIGN KEY (tenant_id, depends_on_ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_comments
    ADD CONSTRAINT ticket_comments_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_comments
    ADD CONSTRAINT ticket_comments_tenant_id_author_agent_id_fkey FOREIGN KEY (tenant_id, author_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE agent_contexts
    ADD CONSTRAINT agent_contexts_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agent_contexts
    ADD CONSTRAINT agent_contexts_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE sessions
    ADD CONSTRAINT sessions_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE leases
    ADD CONSTRAINT leases_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE leases
    ADD CONSTRAINT leases_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE leases
    ADD CONSTRAINT leases_tenant_id_acquired_by_session_id_fkey FOREIGN KEY (tenant_id, acquired_by_session_id) REFERENCES sessions(tenant_id, id) ON DELETE SET NULL (acquired_by_session_id);

ALTER TABLE locks
    ADD CONSTRAINT locks_tenant_id_holder_agent_id_fkey FOREIGN KEY (tenant_id, holder_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE locks
    ADD CONSTRAINT locks_tenant_id_holder_lease_id_fkey FOREIGN KEY (tenant_id, holder_lease_id) REFERENCES leases(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE global_invites
    ADD CONSTRAINT global_invites_tenant_fkey FOREIGN KEY (tenant) REFERENCES global_tenants(id) ON DELETE CASCADE;

ALTER TABLE global_invites
    ADD CONSTRAINT global_invites_invited_by_fkey FOREIGN KEY (invited_by) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_grants
    ADD CONSTRAINT global_oauth_grants_tenant_fkey FOREIGN KEY (tenant) REFERENCES global_tenants(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_grants
    ADD CONSTRAINT global_oauth_grants_client_id_fkey FOREIGN KEY (client_id) REFERENCES global_oauth_clients(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_grants
    ADD CONSTRAINT global_oauth_grants_approved_by_fkey FOREIGN KEY (approved_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE global_oauth_grants
    ADD CONSTRAINT global_oauth_grants_tenant_agent_id_fkey FOREIGN KEY (tenant, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE global_oauth_auth_requests
    ADD CONSTRAINT global_oauth_auth_requests_client_id_fkey FOREIGN KEY (client_id) REFERENCES global_oauth_clients(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_codes
    ADD CONSTRAINT global_oauth_codes_grant_id_fkey FOREIGN KEY (grant_id) REFERENCES global_oauth_grants(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_device_codes
    ADD CONSTRAINT global_oauth_device_codes_client_id_fkey FOREIGN KEY (client_id) REFERENCES global_oauth_clients(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_device_codes
    ADD CONSTRAINT global_oauth_device_codes_grant_id_fkey FOREIGN KEY (grant_id) REFERENCES global_oauth_grants(id) ON DELETE CASCADE;

ALTER TABLE global_oauth_tokens
    ADD CONSTRAINT global_oauth_tokens_grant_id_fkey FOREIGN KEY (grant_id) REFERENCES global_oauth_grants(id) ON DELETE CASCADE;

ALTER TABLE global_user_identities
    ADD CONSTRAINT global_user_identities_provider_fkey FOREIGN KEY (provider) REFERENCES global_auth_providers(name);

ALTER TABLE global_user_identities
    ADD CONSTRAINT global_user_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE ticket_revisions
    ADD CONSTRAINT ticket_revisions_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_revisions
    ADD CONSTRAINT ticket_revisions_tenant_id_editor_agent_id_fkey FOREIGN KEY (tenant_id, editor_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE github_deliveries
    ADD CONSTRAINT github_deliveries_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE SET NULL (ticket_id);

ALTER TABLE ticket_deps
    ADD CONSTRAINT ticket_deps_added_by_agent FOREIGN KEY (tenant_id, added_by_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE agent_role_members
    ADD CONSTRAINT agent_role_members_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agent_role_members
    ADD CONSTRAINT agent_role_members_tenant_id_role_id_fkey FOREIGN KEY (tenant_id, role_id) REFERENCES agent_roles(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_required_roles
    ADD CONSTRAINT ticket_required_roles_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_required_roles
    ADD CONSTRAINT ticket_required_roles_tenant_id_role_id_fkey FOREIGN KEY (tenant_id, role_id) REFERENCES agent_roles(tenant_id, id) ON DELETE RESTRICT;

ALTER TABLE harness_config
    ADD CONSTRAINT harness_config_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE llm_usage
    ADD CONSTRAINT llm_usage_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE SET NULL (ticket_id);

ALTER TABLE llm_tool_usage
    ADD CONSTRAINT llm_tool_usage_tenant_id_agent_id_transcript_key_provider__fkey FOREIGN KEY (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode) REFERENCES llm_usage(tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode) ON DELETE CASCADE;

ALTER TABLE lock_waiters
    ADD CONSTRAINT lock_waiters_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE lock_waiters
    ADD CONSTRAINT lock_waiters_tenant_id_lease_id_fkey FOREIGN KEY (tenant_id, lease_id) REFERENCES leases(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE global_consent_events
    ADD CONSTRAINT global_consent_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE RESTRICT;

ALTER TABLE ticket_status_changes
    ADD CONSTRAINT ticket_status_changes_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agent_workspace_layouts
    ADD CONSTRAINT agent_workspace_layouts_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE workspace_manifests
    ADD CONSTRAINT workspace_manifests_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE agent_messages
    ADD CONSTRAINT agent_messages_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agent_messages
    ADD CONSTRAINT agent_messages_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE global_intake_credentials
    ADD CONSTRAINT global_intake_credentials_tenant_fkey FOREIGN KEY (tenant) REFERENCES global_tenants(id) ON DELETE CASCADE;

ALTER TABLE intake_sources
    ADD CONSTRAINT intake_sources_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_tenant_id_intake_source_id_fkey FOREIGN KEY (tenant_id, intake_source_id) REFERENCES intake_sources(tenant_id, id);

ALTER TABLE intake_events
    ADD CONSTRAINT intake_events_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE intake_events
    ADD CONSTRAINT intake_events_tenant_id_source_id_fkey FOREIGN KEY (tenant_id, source_id) REFERENCES intake_sources(tenant_id, id);

ALTER TABLE intake_deliveries
    ADD CONSTRAINT intake_deliveries_tenant_id_source_id_fkey FOREIGN KEY (tenant_id, source_id) REFERENCES intake_sources(tenant_id, id);

ALTER TABLE intake_deliveries
    ADD CONSTRAINT intake_deliveries_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE SET NULL (ticket_id);

ALTER TABLE fleet_instruction_versions
    ADD CONSTRAINT fleet_instruction_versions_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE fleet_instruction_defaults
    ADD CONSTRAINT fleet_instruction_defaults_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE codehost_credentials
    ADD CONSTRAINT codehost_credentials_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE ticket_changes
    ADD CONSTRAINT ticket_changes_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ticket_changes
    ADD CONSTRAINT ticket_changes_tenant_id_opened_by_agent_id_fkey FOREIGN KEY (tenant_id, opened_by_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE ticket_changes
    ADD CONSTRAINT ticket_changes_tenant_id_merged_by_agent_id_fkey FOREIGN KEY (tenant_id, merged_by_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE conductor_conversations
    ADD CONSTRAINT conductor_conversations_tenant_id_conductor_id_fkey FOREIGN KEY (tenant_id, conductor_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE conductor_conversation_events
    ADD CONSTRAINT conductor_conversation_events_tenant_id_conversation_id_fkey FOREIGN KEY (tenant_id, conversation_id) REFERENCES conductor_conversations(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE conductor_queues
    ADD CONSTRAINT conductor_queues_tenant_id_conductor_id_fkey FOREIGN KEY (tenant_id, conductor_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE conductor_work
    ADD CONSTRAINT conductor_work_tenant_id_conductor_id_fkey FOREIGN KEY (tenant_id, conductor_id) REFERENCES conductor_queues(tenant_id, conductor_id) ON DELETE CASCADE;

ALTER TABLE conductor_work
    ADD CONSTRAINT conductor_work_tenant_id_conversation_id_conductor_id_fkey FOREIGN KEY (tenant_id, conversation_id, conductor_id) REFERENCES conductor_conversations(tenant_id, id, conductor_id) ON DELETE CASCADE;

ALTER TABLE conductor_work
    ADD CONSTRAINT conductor_work_tenant_id_conversation_id_message_seq_fkey FOREIGN KEY (tenant_id, conversation_id, message_seq) REFERENCES conductor_conversation_events(tenant_id, conversation_id, seq) ON DELETE CASCADE;

ALTER TABLE llm_usage
    ADD CONSTRAINT llm_usage_tenant_id_conversation_id_fkey FOREIGN KEY (tenant_id, conversation_id) REFERENCES conductor_conversations(tenant_id, id) ON DELETE SET NULL (conversation_id);

ALTER TABLE global_user_settings
    ADD CONSTRAINT global_user_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE fleet_instruction_archives
    ADD CONSTRAINT fleet_instruction_archives_archived_by_fkey FOREIGN KEY (archived_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE fleet_instruction_archives
    ADD CONSTRAINT fleet_instruction_archives_tenant_id_version_id_fkey FOREIGN KEY (tenant_id, version_id) REFERENCES fleet_instruction_versions(tenant_id, id);

ALTER TABLE attempts
    ADD CONSTRAINT attempts_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE attempts
    ADD CONSTRAINT attempts_tenant_id_resume_agent_id_fkey FOREIGN KEY (tenant_id, resume_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE attempt_status_changes
    ADD CONSTRAINT attempt_status_changes_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE leases
    ADD CONSTRAINT leases_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE agent_contexts
    ADD CONSTRAINT agent_contexts_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE ticket_changes
    ADD CONSTRAINT ticket_changes_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE ticket_comments
    ADD CONSTRAINT ticket_comments_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE agent_messages
    ADD CONSTRAINT agent_messages_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE github_deliveries
    ADD CONSTRAINT github_deliveries_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE SET NULL (ticket_id, attempt_number);

ALTER TABLE llm_usage
    ADD CONSTRAINT llm_usage_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE SET NULL (ticket_id, attempt_number);

ALTER TABLE variant_versions
    ADD CONSTRAINT variant_versions_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE variant_versions
    ADD CONSTRAINT variant_versions_tenant_id_fleet_instructions_version_id_fkey FOREIGN KEY (tenant_id, fleet_instructions_version_id) REFERENCES fleet_instruction_versions(tenant_id, id);

ALTER TABLE variant_archives
    ADD CONSTRAINT variant_archives_archived_by_fkey FOREIGN KEY (archived_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE variant_archives
    ADD CONSTRAINT variant_archives_tenant_id_version_id_fkey FOREIGN KEY (tenant_id, version_id) REFERENCES variant_versions(tenant_id, id);

ALTER TABLE agents
    ADD CONSTRAINT agents_variant_fk FOREIGN KEY (tenant_id, variant_id) REFERENCES variant_versions(tenant_id, id);

ALTER TABLE lease_configs
    ADD CONSTRAINT lease_configs_tenant_id_lease_id_fkey FOREIGN KEY (tenant_id, lease_id) REFERENCES leases(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE lease_configs
    ADD CONSTRAINT lease_configs_tenant_id_variant_id_fkey FOREIGN KEY (tenant_id, variant_id) REFERENCES variant_versions(tenant_id, id);

ALTER TABLE lease_configs
    ADD CONSTRAINT lease_configs_tenant_id_fleet_instructions_version_id_fkey FOREIGN KEY (tenant_id, fleet_instructions_version_id) REFERENCES fleet_instruction_versions(tenant_id, id);

ALTER TABLE usage_declarations
    ADD CONSTRAINT usage_declarations_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE SET NULL (ticket_id);

ALTER TABLE usage_declarations
    ADD CONSTRAINT usage_declarations_tenant_id_conversation_id_fkey FOREIGN KEY (tenant_id, conversation_id) REFERENCES conductor_conversations(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE llm_guidance_usage
    ADD CONSTRAINT llm_guidance_usage_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE attempts
    ADD CONSTRAINT attempts_variant_fk FOREIGN KEY (tenant_id, variant_version_id) REFERENCES variant_versions(tenant_id, id);

ALTER TABLE agent_messages
    ADD CONSTRAINT agent_messages_tenant_id_via_agent_id_fkey FOREIGN KEY (tenant_id, via_agent_id) REFERENCES agents(tenant_id, id) ON DELETE SET NULL (via_agent_id);

ALTER TABLE usage_declarations
    ADD CONSTRAINT usage_declarations_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE SET NULL (attempt_number);

ALTER TABLE llm_guidance_usage
    ADD CONSTRAINT llm_guidance_usage_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE SET NULL (attempt_number);

ALTER TABLE user_tenant_settings
    ADD CONSTRAINT user_tenant_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE tenant_settings
    ADD CONSTRAINT tenant_settings_changed_by_fkey FOREIGN KEY (changed_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE global_former_memberships
    ADD CONSTRAINT global_former_memberships_tenant_fkey FOREIGN KEY (tenant) REFERENCES global_tenants(id) ON DELETE CASCADE;

ALTER TABLE global_former_memberships
    ADD CONSTRAINT global_former_memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES global_users(id) ON DELETE CASCADE;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE ticket_comments
    ADD CONSTRAINT ticket_comments_author_user_id_fkey FOREIGN KEY (author_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE ticket_revisions
    ADD CONSTRAINT ticket_revisions_editor_user_id_fkey FOREIGN KEY (editor_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_post_reviewed_by_user_id_fkey FOREIGN KEY (post_reviewed_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_decided_by_user_id_fkey FOREIGN KEY (decided_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE agent_messages
    ADD CONSTRAINT agent_messages_author_user_id_fkey FOREIGN KEY (author_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE agents
    ADD CONSTRAINT agents_owner_user FOREIGN KEY (owner_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE conductor_conversations
    ADD CONSTRAINT conductor_conversations_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE attempts
    ADD CONSTRAINT attempts_promoted_by_user_id_fkey FOREIGN KEY (promoted_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE attempts
    ADD CONSTRAINT attempts_approved_by_user_id_fkey FOREIGN KEY (approved_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE ticket_comments
    ADD CONSTRAINT ticket_comments_on_behalf_of_user_id_fkey FOREIGN KEY (on_behalf_of_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE global_github_app_installations
    ADD CONSTRAINT global_github_app_installations_tenant_fkey FOREIGN KEY (tenant) REFERENCES global_tenants(id) ON DELETE CASCADE;

ALTER TABLE scope_blocks
    ADD CONSTRAINT scope_blocks_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE scope_blocks
    ADD CONSTRAINT scope_blocks_tenant_id_blocking_ticket_id_fkey FOREIGN KEY (tenant_id, blocking_ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE scope_extension_denials
    ADD CONSTRAINT scope_extension_denials_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE scope_extension_denials
    ADD CONSTRAINT scope_extension_denials_tenant_id_blocking_ticket_id_fkey FOREIGN KEY (tenant_id, blocking_ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE scope_narrowings
    ADD CONSTRAINT scope_narrowings_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE lock_holds
    ADD CONSTRAINT lock_holds_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE worker_polls
    ADD CONSTRAINT worker_polls_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agents
    ADD CONSTRAINT agents_replaces_fk FOREIGN KEY (tenant_id, replaces_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE merge_drift_decisions
    ADD CONSTRAINT merge_drift_decisions_change_id_fkey FOREIGN KEY (change_id) REFERENCES ticket_changes(id) ON DELETE CASCADE;

ALTER TABLE merge_drift_decisions
    ADD CONSTRAINT merge_drift_decisions_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE supervisor_slot_states
    ADD CONSTRAINT supervisor_slot_states_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE server_merge_outcomes
    ADD CONSTRAINT server_merge_outcomes_approver_user_id_fkey FOREIGN KEY (approver_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE server_merge_outcomes
    ADD CONSTRAINT server_merge_outcomes_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE agents
    ADD CONSTRAINT agents_supervisor_fk FOREIGN KEY (tenant_id, supervisor_agent_id) REFERENCES agents(tenant_id, id);

ALTER TABLE global_tenants
    ADD CONSTRAINT global_tenants_archived_by_fkey FOREIGN KEY (archived_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE steward_assessments
    ADD CONSTRAINT steward_assessments_vetoed_by_fkey FOREIGN KEY (vetoed_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE steward_assessments
    ADD CONSTRAINT steward_assessments_tenant_id_pending_action_id_fkey FOREIGN KEY (tenant_id, pending_action_id) REFERENCES pending_actions(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agents
    ADD CONSTRAINT agents_steward_minted_by_fk FOREIGN KEY (tenant_id, steward_minted_by) REFERENCES agents(tenant_id, id);

ALTER TABLE steward_leases
    ADD CONSTRAINT steward_leases_tenant_id_holder_id_fkey FOREIGN KEY (tenant_id, holder_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE supervisor_desired_state
    ADD CONSTRAINT supervisor_desired_state_tenant_id_supervisor_id_fkey FOREIGN KEY (tenant_id, supervisor_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE supervisor_desired_agents
    ADD CONSTRAINT supervisor_desired_agents_tenant_id_supervisor_id_fkey FOREIGN KEY (tenant_id, supervisor_id) REFERENCES supervisor_desired_state(tenant_id, supervisor_id) ON DELETE CASCADE;

ALTER TABLE landings
    ADD CONSTRAINT landings_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE landings
    ADD CONSTRAINT landings_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE steward_drafts
    ADD CONSTRAINT steward_drafts_tenant_id_pending_action_id_fkey FOREIGN KEY (tenant_id, pending_action_id) REFERENCES pending_actions(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE fleet_instruction_defaults
    ADD CONSTRAINT fleet_instruction_defaults_tenant_id_version_id_kind_fkey FOREIGN KEY (tenant_id, version_id, kind) REFERENCES fleet_instruction_versions(tenant_id, id, kind);

ALTER TABLE ignored_merges
    ADD CONSTRAINT ignored_merges_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE landings
    ADD CONSTRAINT landings_batch_fk FOREIGN KEY (tenant_id, batch_id) REFERENCES batches(tenant_id, id) ON DELETE SET NULL (batch_id);

ALTER TABLE variant_versions
    ADD CONSTRAINT variant_versions_harness_fkey FOREIGN KEY (harness) REFERENCES global_harnesses(name);

ALTER TABLE attempts
    ADD CONSTRAINT attempts_harness_fkey FOREIGN KEY (harness) REFERENCES global_harnesses(name);

ALTER TABLE ticket_snapshots
    ADD CONSTRAINT ticket_snapshots_taken_by_user_id_fkey FOREIGN KEY (taken_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE ticket_snapshots
    ADD CONSTRAINT ticket_snapshots_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE remote_control_host_leases
    ADD CONSTRAINT remote_control_host_leases_tenant_id_agent_id_fkey FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE steward_discussion_labels
    ADD CONSTRAINT steward_discussion_labels_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE steward_discussion_decisions
    ADD CONSTRAINT steward_discussion_decisions_tenant_id_label_id_fkey FOREIGN KEY (tenant_id, label_id) REFERENCES steward_discussion_labels(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE attempt_signals
    ADD CONSTRAINT attempt_signals_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE steward_discussion_applies
    ADD CONSTRAINT steward_discussion_applies_tenant_id_label_id_fkey FOREIGN KEY (tenant_id, label_id) REFERENCES steward_discussion_labels(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE agents
    ADD CONSTRAINT agents_run_by_supervisor_fk FOREIGN KEY (tenant_id, run_by_supervisor_id) REFERENCES agents(tenant_id, id);

ALTER TABLE eval_sets
    ADD CONSTRAINT eval_sets_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE eval_set_batches
    ADD CONSTRAINT eval_set_batches_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE eval_set_batches
    ADD CONSTRAINT eval_set_batches_tenant_id_eval_set_id_fkey FOREIGN KEY (tenant_id, eval_set_id) REFERENCES eval_sets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE attempt_verdicts
    ADD CONSTRAINT attempt_verdicts_set_by_user_id_fkey FOREIGN KEY (set_by_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE attempt_verdicts
    ADD CONSTRAINT attempt_verdicts_tenant_id_ticket_id_attempt_number_fkey FOREIGN KEY (tenant_id, ticket_id, attempt_number) REFERENCES attempts(tenant_id, ticket_id, number) ON DELETE CASCADE;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_uploader_user_id_fkey FOREIGN KEY (uploader_user_id) REFERENCES global_users(id) ON DELETE SET NULL;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_tenant_id_ticket_id_fkey FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_tenant_id_uploader_agent_id_fkey FOREIGN KEY (tenant_id, uploader_agent_id) REFERENCES agents(tenant_id, id) ON DELETE SET NULL (uploader_agent_id);

ALTER TABLE agent_contexts ENABLE ROW LEVEL SECURITY;

ALTER TABLE agent_messages ENABLE ROW LEVEL SECURITY;

ALTER TABLE agent_role_members ENABLE ROW LEVEL SECURITY;

ALTER TABLE agent_roles ENABLE ROW LEVEL SECURITY;

ALTER TABLE agent_workspace_layouts ENABLE ROW LEVEL SECURITY;

ALTER TABLE agents ENABLE ROW LEVEL SECURITY;

ALTER TABLE attachment_delete_queue ENABLE ROW LEVEL SECURITY;

ALTER TABLE attachment_reservations ENABLE ROW LEVEL SECURITY;

ALTER TABLE attachment_usage ENABLE ROW LEVEL SECURITY;

ALTER TABLE attachments ENABLE ROW LEVEL SECURITY;

ALTER TABLE attempt_signals ENABLE ROW LEVEL SECURITY;

ALTER TABLE attempt_status_changes ENABLE ROW LEVEL SECURITY;

ALTER TABLE attempt_verdicts ENABLE ROW LEVEL SECURITY;

ALTER TABLE attempts ENABLE ROW LEVEL SECURITY;

ALTER TABLE batches ENABLE ROW LEVEL SECURITY;

ALTER TABLE board_changes ENABLE ROW LEVEL SECURITY;

ALTER TABLE board_revisions ENABLE ROW LEVEL SECURITY;

ALTER TABLE codehost_credentials ENABLE ROW LEVEL SECURITY;

ALTER TABLE codehost_setup_states ENABLE ROW LEVEL SECURITY;

ALTER TABLE conductor_conversation_events ENABLE ROW LEVEL SECURITY;

ALTER TABLE conductor_conversations ENABLE ROW LEVEL SECURITY;

ALTER TABLE conductor_queues ENABLE ROW LEVEL SECURITY;

ALTER TABLE conductor_work ENABLE ROW LEVEL SECURITY;

ALTER TABLE eval_set_batches ENABLE ROW LEVEL SECURITY;

ALTER TABLE eval_sets ENABLE ROW LEVEL SECURITY;

ALTER TABLE fleet_instruction_archives ENABLE ROW LEVEL SECURITY;

ALTER TABLE fleet_instruction_defaults ENABLE ROW LEVEL SECURITY;

ALTER TABLE fleet_instruction_versions ENABLE ROW LEVEL SECURITY;

ALTER TABLE github_deliveries ENABLE ROW LEVEL SECURITY;

ALTER TABLE github_event_keys ENABLE ROW LEVEL SECURITY;

ALTER TABLE harness_config ENABLE ROW LEVEL SECURITY;

ALTER TABLE harness_config_revisions ENABLE ROW LEVEL SECURITY;

ALTER TABLE ignored_merges ENABLE ROW LEVEL SECURITY;

ALTER TABLE intake_deliveries ENABLE ROW LEVEL SECURITY;

ALTER TABLE intake_events ENABLE ROW LEVEL SECURITY;

ALTER TABLE intake_sources ENABLE ROW LEVEL SECURITY;

ALTER TABLE landings ENABLE ROW LEVEL SECURITY;

ALTER TABLE lease_configs ENABLE ROW LEVEL SECURITY;

ALTER TABLE leases ENABLE ROW LEVEL SECURITY;

ALTER TABLE llm_guidance_usage ENABLE ROW LEVEL SECURITY;

ALTER TABLE llm_tool_usage ENABLE ROW LEVEL SECURITY;

ALTER TABLE llm_usage ENABLE ROW LEVEL SECURITY;

ALTER TABLE llm_usage_daily ENABLE ROW LEVEL SECURITY;

ALTER TABLE llm_usage_marks ENABLE ROW LEVEL SECURITY;

ALTER TABLE lock_holds ENABLE ROW LEVEL SECURITY;

ALTER TABLE lock_waiters ENABLE ROW LEVEL SECURITY;

ALTER TABLE locks ENABLE ROW LEVEL SECURITY;

ALTER TABLE merge_drift_decisions ENABLE ROW LEVEL SECURITY;

ALTER TABLE pending_actions ENABLE ROW LEVEL SECURITY;

ALTER TABLE remote_control_host_leases ENABLE ROW LEVEL SECURITY;

ALTER TABLE scope_blocks ENABLE ROW LEVEL SECURITY;

ALTER TABLE scope_extension_denials ENABLE ROW LEVEL SECURITY;

ALTER TABLE scope_narrowings ENABLE ROW LEVEL SECURITY;

ALTER TABLE server_merge_outcomes ENABLE ROW LEVEL SECURITY;

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;

ALTER TABLE steward_assessments ENABLE ROW LEVEL SECURITY;

ALTER TABLE steward_discussion_applies ENABLE ROW LEVEL SECURITY;

ALTER TABLE steward_discussion_decisions ENABLE ROW LEVEL SECURITY;

ALTER TABLE steward_discussion_labels ENABLE ROW LEVEL SECURITY;

ALTER TABLE steward_drafts ENABLE ROW LEVEL SECURITY;

ALTER TABLE steward_leases ENABLE ROW LEVEL SECURITY;

ALTER TABLE supervisor_desired_agents ENABLE ROW LEVEL SECURITY;

ALTER TABLE supervisor_desired_state ENABLE ROW LEVEL SECURITY;

ALTER TABLE supervisor_slot_states ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON agent_contexts USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON agent_messages USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON agent_role_members USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON agent_roles USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON agent_workspace_layouts USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON agents USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attachment_delete_queue USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attachment_reservations USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attachment_usage USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attachments USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attempt_signals USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attempt_status_changes USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attempt_verdicts USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON attempts USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON batches USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON board_changes USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON board_revisions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON codehost_credentials USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON codehost_setup_states USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON conductor_conversation_events USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON conductor_conversations USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON conductor_queues USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON conductor_work USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON eval_set_batches USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON eval_sets USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON fleet_instruction_archives USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON fleet_instruction_defaults USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON fleet_instruction_versions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON github_deliveries USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON github_event_keys USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON harness_config USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON harness_config_revisions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ignored_merges USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON intake_deliveries USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON intake_events USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON intake_sources USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON landings USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON lease_configs USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON leases USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON llm_guidance_usage USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON llm_tool_usage USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON llm_usage USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON llm_usage_daily USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON llm_usage_marks USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON lock_holds USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON lock_waiters USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON locks USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON merge_drift_decisions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON pending_actions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON remote_control_host_leases USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON scope_blocks USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON scope_extension_denials USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON scope_narrowings USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON server_merge_outcomes USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON sessions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON steward_assessments USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON steward_discussion_applies USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON steward_discussion_decisions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON steward_discussion_labels USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON steward_drafts USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON steward_leases USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON supervisor_desired_agents USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON supervisor_desired_state USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON supervisor_slot_states USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON tenant_settings USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_changes USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_comments USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_deps USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_paths USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_required_roles USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_revisions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_sequences USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_snapshots USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON ticket_status_changes USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON tickets USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON usage_declarations USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON user_tenant_settings USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON variant_archives USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON variant_versions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON worker_polls USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON workspace_manifest_revisions USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

CREATE POLICY tenant_isolation ON workspace_manifests USING ((tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid));

ALTER TABLE tenant_settings ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_changes ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_comments ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_deps ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_paths ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_required_roles ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_revisions ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_sequences ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_snapshots ENABLE ROW LEVEL SECURITY;

ALTER TABLE ticket_status_changes ENABLE ROW LEVEL SECURITY;

ALTER TABLE tickets ENABLE ROW LEVEL SECURITY;

ALTER TABLE usage_declarations ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_isolation ON user_tenant_settings AS RESTRICTIVE USING ((user_id = (NULLIF(current_setting('app.current_user_id'::text, true), ''::text))::uuid));

ALTER TABLE user_tenant_settings ENABLE ROW LEVEL SECURITY;

ALTER TABLE variant_archives ENABLE ROW LEVEL SECURITY;

ALTER TABLE variant_versions ENABLE ROW LEVEL SECURITY;

ALTER TABLE worker_polls ENABLE ROW LEVEL SECURITY;

ALTER TABLE workspace_manifest_revisions ENABLE ROW LEVEL SECURITY;

ALTER TABLE workspace_manifests ENABLE ROW LEVEL SECURITY;

-- The harnesses slop-haus knows at this version; the server syncs the
-- registry at startup as well.
INSERT INTO global_harnesses (name) VALUES ('claude'), ('pi');

-- The workspace's own client: its hooks and scripts use the device flow,
-- so it is known in advance rather than registered by each workspace.
INSERT INTO global_oauth_clients (id, name, grant_types)
VALUES ('slop-haus-workspace', 'slop-haus workspace',
        ARRAY['urn:ietf:params:oauth:grant-type:device_code', 'refresh_token']);

-- +goose Down

-- Rolling back the baseline would mean dropping every table. Refused, so a
-- stray `goose down` on a live database fails instead of emptying it.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'the schema baseline (000172) cannot be rolled back';
END
$$;
-- +goose StatementEnd
