-- +goose Up
-- A tenant's code host credentials are no longer one row. App-level data (the
-- key and the webhook) is stored once per GitHub App, because GitHub signs
-- every installation's deliveries with the App's one secret: a copy per
-- installation would go stale when one of them rotated it. A credential is one
-- installation of an App plus the binding that says which repos it serves.
--
-- Each existing row becomes one App, keeping its id (the sealed key and
-- webhook secret are bound to it), and one credential bound to the host-wide
-- 'github.com'. No binding is inferred and GitHub is not asked: the one
-- credential is tried for every repo, as before.
ALTER TABLE codehost_credentials RENAME TO codehost_credentials_before_apps;
ALTER INDEX codehost_credentials_pkey RENAME TO codehost_credentials_before_apps_pkey;
ALTER INDEX codehost_credentials_id_key RENAME TO codehost_credentials_before_apps_id_key;

CREATE TABLE codehost_apps (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    provider text NOT NULL,
    app_id bigint NOT NULL,
    app_slug text,
    private_key_enc bytea NOT NULL,
    key_id text NOT NULL,
    key_fingerprint text NOT NULL,
    created_by uuid,
    created_by_deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    verified_at timestamp with time zone,
    webhook_secret_enc bytea,
    webhook_secret_key_id text,
    webhook_url text,
    webhook_applied_at timestamp with time zone,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (id),
    UNIQUE (tenant_id, app_id),
    CONSTRAINT codehost_apps_provider_check CHECK (provider = 'github_app'),
    CONSTRAINT codehost_apps_app_id_check CHECK (app_id > 0),
    CONSTRAINT codehost_apps_key_fingerprint_check CHECK (length(key_fingerprint) = 64),
    CONSTRAINT codehost_apps_key_id_check CHECK (length(key_id) = 16),
    CONSTRAINT codehost_apps_created_by_deleted_is_null CHECK (NOT created_by_deleted OR created_by IS NULL),
    CONSTRAINT codehost_apps_webhook_secret_complete CHECK ((webhook_secret_enc IS NULL) = (webhook_secret_key_id IS NULL)),
    CONSTRAINT codehost_apps_webhook_secret_key_id_check CHECK (length(webhook_secret_key_id) = 16),
    FOREIGN KEY (created_by) REFERENCES global_users (id) ON DELETE SET NULL
);

CREATE TRIGGER codehost_apps_mark_deleted_actors BEFORE UPDATE OF created_by ON codehost_apps
    FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');

SELECT go_db_apply_tenant_rls('codehost_apps');

-- installation_id is null while the credential is pending: the App is created
-- but not installed, or the install awaits an org owner's approval. The
-- binding is a host-qualified, lowercased path: 'github.com',
-- 'github.com/acme' or 'github.com/acme/app'.
CREATE TABLE codehost_credentials (
    tenant_id uuid DEFAULT current_tenant() NOT NULL,
    id uuid NOT NULL,
    app_ref uuid NOT NULL,
    installation_id bigint,
    binding text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, binding),
    UNIQUE (tenant_id, installation_id),
    CONSTRAINT codehost_credentials_installation_id_check CHECK (installation_id IS NULL OR installation_id > 0),
    CONSTRAINT codehost_credentials_binding_check CHECK (binding = lower(binding) AND binding ~ '^[^/[:space:]]+(/[^/[:space:]]+){0,2}$'),
    FOREIGN KEY (tenant_id, app_ref) REFERENCES codehost_apps (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX codehost_credentials_by_app ON codehost_credentials (tenant_id, app_ref);

SELECT go_db_apply_tenant_rls('codehost_credentials');

-- One row per installation that has a webhook secret to verify against, so a
-- delivery's installation id, read before any tenant is known, finds its
-- tenant and the App whose secret signed it.
DELETE FROM global_github_app_installations;
ALTER TABLE global_github_app_installations DROP CONSTRAINT global_github_app_installations_pkey;
ALTER TABLE global_github_app_installations ADD COLUMN app_ref uuid NOT NULL;
ALTER TABLE global_github_app_installations ADD CONSTRAINT global_github_app_installations_pkey PRIMARY KEY (credential_id);

-- +goose StatementBegin
CREATE FUNCTION sync_github_app_credential() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    secret bytea;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        DELETE FROM global_github_app_installations WHERE credential_id = OLD.id;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF NEW.installation_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT webhook_secret_enc INTO secret FROM codehost_apps WHERE tenant_id = NEW.tenant_id AND id = NEW.app_ref;
    IF secret IS NULL THEN
        RETURN NEW;
    END IF;
    INSERT INTO global_github_app_installations (tenant, installation_id, credential_id, app_ref, webhook_secret_enc)
    VALUES (NEW.tenant_id, NEW.installation_id, NEW.id, NEW.app_ref, secret);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION sync_github_app_secret() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    DELETE FROM global_github_app_installations WHERE app_ref = NEW.id;
    IF NEW.webhook_secret_enc IS NOT NULL THEN
        INSERT INTO global_github_app_installations (tenant, installation_id, credential_id, app_ref, webhook_secret_enc)
        SELECT c.tenant_id, c.installation_id, c.id, c.app_ref, NEW.webhook_secret_enc
          FROM codehost_credentials c
         WHERE c.tenant_id = NEW.tenant_id AND c.app_ref = NEW.id AND c.installation_id IS NOT NULL;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sync_github_app_credential AFTER INSERT OR DELETE OR UPDATE ON codehost_credentials
    FOR EACH ROW EXECUTE FUNCTION sync_github_app_credential();
CREATE TRIGGER sync_github_app_secret AFTER UPDATE OF webhook_secret_enc ON codehost_apps
    FOR EACH ROW EXECUTE FUNCTION sync_github_app_secret();

-- +goose StatementBegin
SELECT go_db_with_rls_lifted(
    ARRAY['codehost_credentials_before_apps', 'codehost_apps', 'codehost_credentials'],
    $body$
    INSERT INTO codehost_apps (tenant_id, id, provider, app_id, app_slug, private_key_enc, key_id, key_fingerprint,
                               created_by, created_by_deleted, created_at, updated_at, verified_at,
                               webhook_secret_enc, webhook_secret_key_id, webhook_url, webhook_applied_at)
    SELECT tenant_id, id, provider, app_id, app_slug, private_key_enc, key_id, key_fingerprint,
           created_by, created_by_deleted, created_at, updated_at, verified_at,
           webhook_secret_enc, webhook_secret_key_id, webhook_url, webhook_applied_at
      FROM codehost_credentials_before_apps;
    INSERT INTO codehost_credentials (tenant_id, id, app_ref, installation_id, binding, created_at, updated_at)
    SELECT tenant_id, gen_random_uuid(), id, installation_id, 'github.com', created_at, updated_at
      FROM codehost_credentials_before_apps;
    $body$);
-- +goose StatementEnd

DROP TABLE codehost_credentials_before_apps;
DROP FUNCTION sync_github_app_installation();

-- +goose Down
DROP TRIGGER sync_github_app_credential ON codehost_credentials;
DROP TRIGGER sync_github_app_secret ON codehost_apps;

CREATE TABLE codehost_credentials_after_apps (
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
    CONSTRAINT codehost_credentials_webhook_secret_key_id_check CHECK ((length(webhook_secret_key_id) = 16)),
    CONSTRAINT codehost_credentials_pkey_after_apps PRIMARY KEY (tenant_id),
    CONSTRAINT codehost_credentials_id_key_after_apps UNIQUE (id),
    CONSTRAINT codehost_credentials_created_by_fkey FOREIGN KEY (created_by) REFERENCES global_users(id) ON DELETE SET NULL
);

-- One credential per tenant is all the old shape holds: the earliest, with
-- the rest of the tenant's installations lost.
-- +goose StatementBegin
SELECT go_db_with_rls_lifted(
    ARRAY['codehost_apps', 'codehost_credentials', 'codehost_credentials_after_apps'],
    $body$
    INSERT INTO codehost_credentials_after_apps (tenant_id, id, provider, app_id, installation_id, private_key_enc, key_id,
                                                 key_fingerprint, created_by, created_by_deleted, created_at, updated_at,
                                                 verified_at, app_slug, webhook_secret_enc, webhook_secret_key_id,
                                                 webhook_url, webhook_applied_at)
    SELECT DISTINCT ON (a.tenant_id) a.tenant_id, a.id, a.provider, a.app_id, c.installation_id, a.private_key_enc, a.key_id,
           a.key_fingerprint, a.created_by, a.created_by_deleted, a.created_at, a.updated_at,
           a.verified_at, a.app_slug, a.webhook_secret_enc, a.webhook_secret_key_id,
           a.webhook_url, a.webhook_applied_at
      FROM codehost_apps a LEFT JOIN codehost_credentials c ON c.tenant_id = a.tenant_id AND c.app_ref = a.id
     ORDER BY a.tenant_id, c.created_at NULLS LAST, a.created_at;
    $body$);
-- +goose StatementEnd

DELETE FROM global_github_app_installations;
DROP TABLE codehost_credentials;
DROP TABLE codehost_apps;
ALTER TABLE codehost_credentials_after_apps RENAME TO codehost_credentials;
ALTER TABLE codehost_credentials RENAME CONSTRAINT codehost_credentials_pkey_after_apps TO codehost_credentials_pkey;
ALTER TABLE codehost_credentials RENAME CONSTRAINT codehost_credentials_id_key_after_apps TO codehost_credentials_id_key;
CREATE TRIGGER codehost_credentials_mark_deleted_actors BEFORE UPDATE OF created_by ON codehost_credentials
    FOR EACH ROW EXECUTE FUNCTION mark_deleted_actors('created_by');
SELECT go_db_apply_tenant_rls('codehost_credentials');

ALTER TABLE global_github_app_installations DROP CONSTRAINT global_github_app_installations_pkey;
ALTER TABLE global_github_app_installations DROP COLUMN app_ref;
ALTER TABLE global_github_app_installations ADD CONSTRAINT global_github_app_installations_pkey PRIMARY KEY (tenant);

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

CREATE TRIGGER sync_github_app_installation AFTER INSERT OR DELETE OR UPDATE ON codehost_credentials
    FOR EACH ROW EXECUTE FUNCTION sync_github_app_installation();

-- Rebuild the lookup the trigger keeps, from the rows just restored.
-- +goose StatementBegin
SELECT go_db_with_rls_lifted(
    ARRAY['codehost_credentials'],
    $body$
    INSERT INTO global_github_app_installations (tenant, installation_id, credential_id, webhook_secret_enc)
    SELECT tenant_id, installation_id, id, webhook_secret_enc FROM codehost_credentials
     WHERE installation_id IS NOT NULL AND webhook_secret_enc IS NOT NULL;
    $body$);
-- +goose StatementEnd

DROP FUNCTION sync_github_app_credential();
DROP FUNCTION sync_github_app_secret();
