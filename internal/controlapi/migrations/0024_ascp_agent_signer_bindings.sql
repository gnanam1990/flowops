CREATE TABLE IF NOT EXISTS ascp_agent_signer_bindings (
    organization_id text NOT NULL,
    agent_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    chain_id bigint NOT NULL CHECK (chain_id > 0),
    signer_key_id text NOT NULL CHECK (signer_key_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    key_epoch bigint NOT NULL CHECK (key_epoch > 0),
    module_address text NOT NULL CHECK (module_address ~ '^0x[0-9a-f]{40}$' AND module_address <> '0x0000000000000000000000000000000000000000'),
    safe_address text NOT NULL CHECK (safe_address ~ '^0x[0-9a-f]{40}$' AND safe_address <> '0x0000000000000000000000000000000000000000'),
    keeper_id text NOT NULL CHECK (keeper_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    updated_by text NOT NULL CHECK (updated_by ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    updated_at timestamptz NOT NULL,
    CHECK (module_address <> safe_address),
    PRIMARY KEY (organization_id, agent_id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id)
);

CREATE TABLE IF NOT EXISTS ascp_agent_signer_binding_history (
    organization_id text NOT NULL,
    agent_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    chain_id bigint NOT NULL CHECK (chain_id > 0),
    signer_key_id text NOT NULL CHECK (signer_key_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    key_epoch bigint NOT NULL CHECK (key_epoch > 0),
    module_address text NOT NULL CHECK (module_address ~ '^0x[0-9a-f]{40}$' AND module_address <> '0x0000000000000000000000000000000000000000'),
    safe_address text NOT NULL CHECK (safe_address ~ '^0x[0-9a-f]{40}$' AND safe_address <> '0x0000000000000000000000000000000000000000'),
    keeper_id text NOT NULL CHECK (keeper_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    changed_by text NOT NULL CHECK (changed_by ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 1024),
    created_at timestamptz NOT NULL,
    CHECK (module_address <> safe_address),
    PRIMARY KEY (organization_id, agent_id, version),
    UNIQUE (organization_id, agent_id, signer_key_id, key_epoch),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id)
);

ALTER TABLE ascp_agent_signer_bindings
    ADD CONSTRAINT ascp_agent_signer_bindings_history_fk
    FOREIGN KEY (organization_id, agent_id, version)
    REFERENCES ascp_agent_signer_binding_history(organization_id, agent_id, version);

CREATE OR REPLACE FUNCTION flowops_validate_current_signer_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM ascp_agent_signer_binding_history history
        WHERE history.organization_id = NEW.organization_id
          AND history.agent_id = NEW.agent_id
          AND history.version = NEW.version
          AND history.chain_id = NEW.chain_id
          AND history.signer_key_id = NEW.signer_key_id
          AND history.key_epoch = NEW.key_epoch
          AND history.module_address = NEW.module_address
          AND history.safe_address = NEW.safe_address
          AND history.keeper_id = NEW.keeper_id
          AND history.changed_by = NEW.updated_by
          AND history.created_at = NEW.updated_at
    ) THEN
        RAISE EXCEPTION 'current signer binding must exactly match immutable history' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_agent_signer_bindings_history_match ON ascp_agent_signer_bindings;
CREATE TRIGGER ascp_agent_signer_bindings_history_match
BEFORE INSERT OR UPDATE ON ascp_agent_signer_bindings
FOR EACH ROW EXECUTE FUNCTION flowops_validate_current_signer_binding();

-- Existing in-flight rows predate the authoritative binding registry and keep
-- version 0 for recovery. Every new application request requires a positive
-- version and is transactionally checked by ActivationStore.
ALTER TABLE ascp_sign_requests
    ADD COLUMN IF NOT EXISTS signer_binding_version bigint NOT NULL DEFAULT 0
    CHECK (signer_binding_version >= 0);

CREATE OR REPLACE FUNCTION flowops_require_signer_binding_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.signer_binding_version <= 0 THEN
        RAISE EXCEPTION 'new signer request requires an authoritative binding version' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_sign_requests_require_binding_version ON ascp_sign_requests;
CREATE TRIGGER ascp_sign_requests_require_binding_version
BEFORE INSERT ON ascp_sign_requests
FOR EACH ROW EXECUTE FUNCTION flowops_require_signer_binding_version();

CREATE TABLE IF NOT EXISTS ascp_agent_signer_binding_changes (
    change_id text PRIMARY KEY CHECK (change_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES organizations(id),
    agent_id text NOT NULL,
    actor_id text NOT NULL CHECK (actor_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    idempotency_key text NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    input_hash text NOT NULL CHECK (input_hash ~ '^0x[0-9a-f]{64}$'),
    resulting_version bigint NOT NULL CHECK (resulting_version > 0),
    created_at timestamptz NOT NULL,
    UNIQUE (organization_id, idempotency_key),
    FOREIGN KEY (organization_id, agent_id, resulting_version)
        REFERENCES ascp_agent_signer_binding_history(organization_id, agent_id, version)
);

CREATE OR REPLACE FUNCTION flowops_reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_agent_signer_binding_history_append_only ON ascp_agent_signer_binding_history;
CREATE TRIGGER ascp_agent_signer_binding_history_append_only
BEFORE UPDATE OR DELETE ON ascp_agent_signer_binding_history
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_agent_signer_binding_changes_append_only ON ascp_agent_signer_binding_changes;
CREATE TRIGGER ascp_agent_signer_binding_changes_append_only
BEFORE UPDATE OR DELETE ON ascp_agent_signer_binding_changes
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE INDEX IF NOT EXISTS ascp_agent_signer_binding_history_actor_idx
ON ascp_agent_signer_binding_history (organization_id, changed_by, created_at DESC);
