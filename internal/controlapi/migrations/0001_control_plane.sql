CREATE TABLE IF NOT EXISTS organizations (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agents (
    organization_id text NOT NULL REFERENCES organizations(id),
    id text NOT NULL,
    customer_id text NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    purpose text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('DRAFT','ACTIVE','PAUSED','QUARANTINED','REVOKED','ARCHIVED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, id)
);

CREATE TABLE IF NOT EXISTS credentials (
    id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES organizations(id),
    principal_id text NOT NULL,
    principal_kind text NOT NULL CHECK (principal_kind IN ('HUMAN','AGENT')),
    role text NOT NULL CHECK (role IN ('OWNER','ADMIN','DEVELOPER','FINANCE','APPROVER','AUDITOR','VIEWER','AGENT')),
    agent_id text,
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
    expires_at timestamptz NOT NULL,
    step_up_until timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((principal_kind = 'AGENT' AND role = 'AGENT' AND agent_id IS NOT NULL) OR
           (principal_kind = 'HUMAN' AND role <> 'AGENT' AND agent_id IS NULL)),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id)
);

CREATE TABLE IF NOT EXISTS policies (
    organization_id text NOT NULL,
    agent_id text NOT NULL,
    version text NOT NULL,
    config jsonb NOT NULL,
    active boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz,
    PRIMARY KEY (organization_id, agent_id, version),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id),
    CHECK ((active AND activated_at IS NOT NULL) OR (NOT active))
);

CREATE UNIQUE INDEX IF NOT EXISTS policies_one_active_per_agent
ON policies (organization_id, agent_id)
WHERE active;

CREATE TABLE IF NOT EXISTS commands (
    id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES organizations(id),
    actor_id text NOT NULL,
    kind text NOT NULL,
    target_id text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL,
    input_digest text NOT NULL,
    state text NOT NULL CHECK (state IN ('PENDING','SUCCEEDED','FAILED')),
    result jsonb,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    UNIQUE (organization_id, kind, idempotency_key)
);

CREATE INDEX IF NOT EXISTS commands_org_created_idx ON commands (organization_id, created_at DESC);

CREATE TABLE IF NOT EXISTS audit_events (
    id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES organizations(id),
    actor_id text NOT NULL,
    kind text NOT NULL,
    target_id text NOT NULL,
    previous jsonb,
    current jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_events_org_created_idx ON audit_events (organization_id, created_at DESC);

CREATE TABLE IF NOT EXISTS control_events (
    sequence bigint PRIMARY KEY CHECK (sequence > 0),
    at_unix bigint NOT NULL CHECK (at_unix > 0),
    kind text NOT NULL,
    request_id text NOT NULL,
    previous_hash text NOT NULL,
    -- The hash chain commits to the exact JSON bytes. jsonb normalization
    -- would change those bytes during replay, so preserve them verbatim.
    payload bytea NOT NULL,
    hash text NOT NULL UNIQUE
);

CREATE OR REPLACE FUNCTION flowops_reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS audit_events_append_only ON audit_events;
CREATE TRIGGER audit_events_append_only
BEFORE UPDATE OR DELETE ON audit_events
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS control_events_append_only ON control_events;
CREATE TRIGGER control_events_append_only
BEFORE UPDATE OR DELETE ON control_events
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
