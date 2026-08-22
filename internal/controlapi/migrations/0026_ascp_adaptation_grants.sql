CREATE TABLE IF NOT EXISTS ascp_adaptation_grants (
    grant_id text PRIMARY KEY CHECK (grant_id ~ '^0x[0-9a-f]{64}$'),
    original_intent_id text NOT NULL UNIQUE REFERENCES ascp_intents(operation_id),
    organization_id text NOT NULL REFERENCES organizations(id),
    agent_id text NOT NULL,
    task_id text NOT NULL CHECK (length(task_id) BETWEEN 1 AND 128),
    reason_class text NOT NULL CHECK (reason_class IN ('too_expensive','wrong_seller')),
    allowed_category text NOT NULL CHECK (length(allowed_category) BETWEEN 1 AND 128),
    max_amount_atomic text NOT NULL CHECK (max_amount_atomic ~ '^[1-9][0-9]*$'),
    allowed_seller_set text[] NOT NULL CHECK (cardinality(allowed_seller_set) BETWEEN 1 AND 64),
    remaining_attempts smallint NOT NULL CHECK (remaining_attempts IN (0,1)),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '30 minutes'),
    grant_digest text NOT NULL CHECK (grant_digest ~ '^0x[0-9a-f]{64}$'),
    canonical_request_hash text NOT NULL CHECK (canonical_request_hash ~ '^0x[0-9a-f]{64}$'),
    payload_json jsonb NOT NULL,
    signature text NOT NULL CHECK (signature ~ '^0x[0-9a-f]{130}$'),
    state text NOT NULL CHECK (state IN ('ISSUED','CONSUMED')),
    consumed_operation_id text UNIQUE REFERENCES ascp_intents(operation_id),
    consumed_at timestamptz,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id),
    CHECK (payload_json->>'protocol' = 'ASCP_GRANT_V1' AND
           payload_json->>'grantId' = grant_id AND
           payload_json->>'originalIntentId' = original_intent_id AND
           payload_json->>'organizationId' = organization_id AND
           payload_json->>'agentId' = agent_id AND
           payload_json->>'taskId' = task_id AND
           payload_json->>'allowedCategory' = allowed_category AND
           payload_json->>'maxAmountAtomic' = max_amount_atomic AND
           payload_json->'allowedSellerSet' = to_jsonb(allowed_seller_set) AND
           payload_json->>'remainingAttempts' = '1' AND
           (payload_json->>'issuedAt')::bigint = extract(epoch FROM issued_at)::bigint AND
           (payload_json->>'expiresAt')::bigint = extract(epoch FROM expires_at)::bigint),
    CHECK (consumed_operation_id IS NULL OR consumed_operation_id <> original_intent_id),
    CHECK ((state = 'ISSUED' AND remaining_attempts = 1 AND consumed_operation_id IS NULL AND consumed_at IS NULL) OR
           (state = 'CONSUMED' AND remaining_attempts = 0 AND consumed_operation_id IS NOT NULL AND consumed_at IS NOT NULL))
);

ALTER TABLE ascp_intents
    DROP CONSTRAINT IF EXISTS ascp_intents_operation_scope_unique;
ALTER TABLE ascp_intents
    ADD CONSTRAINT ascp_intents_operation_scope_unique UNIQUE (operation_id, organization_id, actor_id);

ALTER TABLE ascp_adaptation_grants
    DROP CONSTRAINT IF EXISTS ascp_adaptation_grants_grant_scope_unique;
ALTER TABLE ascp_adaptation_grants
    ADD CONSTRAINT ascp_adaptation_grants_grant_scope_unique UNIQUE (grant_id, organization_id, agent_id);

ALTER TABLE ascp_adaptation_grants
    DROP CONSTRAINT IF EXISTS ascp_adaptation_grants_original_scope_fk;
ALTER TABLE ascp_adaptation_grants
    ADD CONSTRAINT ascp_adaptation_grants_original_scope_fk
    FOREIGN KEY (original_intent_id, organization_id, agent_id)
    REFERENCES ascp_intents(operation_id, organization_id, actor_id);

ALTER TABLE ascp_adaptation_grants
    DROP CONSTRAINT IF EXISTS ascp_adaptation_grants_consumed_scope_fk;
ALTER TABLE ascp_adaptation_grants
    ADD CONSTRAINT ascp_adaptation_grants_consumed_scope_fk
    FOREIGN KEY (consumed_operation_id, organization_id, agent_id)
    REFERENCES ascp_intents(operation_id, organization_id, actor_id);

CREATE INDEX IF NOT EXISTS ascp_adaptation_grants_scope_idx
ON ascp_adaptation_grants (organization_id, agent_id, expires_at DESC);

ALTER TABLE ascp_intents
    ADD COLUMN IF NOT EXISTS adaptation_grant_id text,
    ADD COLUMN IF NOT EXISTS adaptation_grant_digest text;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'ascp_intents_adaptation_grant_fk'
          AND conrelid = 'ascp_intents'::regclass
    ) THEN
        ALTER TABLE ascp_intents
            ADD CONSTRAINT ascp_intents_adaptation_grant_fk
            FOREIGN KEY (adaptation_grant_id, organization_id, actor_id)
            REFERENCES ascp_adaptation_grants(grant_id, organization_id, agent_id)
            DEFERRABLE INITIALLY DEFERRED;
    END IF;
END;
$$;

ALTER TABLE ascp_intents
    DROP CONSTRAINT IF EXISTS ascp_intents_adaptation_grant_shape;
ALTER TABLE ascp_intents
    ADD CONSTRAINT ascp_intents_adaptation_grant_shape CHECK (
        (adaptation_grant_id IS NULL AND adaptation_grant_digest IS NULL) OR
        (adaptation_grant_id ~ '^0x[0-9a-f]{64}$' AND adaptation_grant_digest ~ '^0x[0-9a-f]{64}$')
    );

CREATE OR REPLACE FUNCTION flowops_guard_adaptation_grant_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.state = 'ISSUED' AND OLD.remaining_attempts = 1
       AND NEW.state = 'CONSUMED' AND NEW.remaining_attempts = 0
       AND OLD.consumed_operation_id IS NULL AND OLD.consumed_at IS NULL
       AND NEW.consumed_operation_id IS NOT NULL AND NEW.consumed_at IS NOT NULL
       AND OLD.grant_id = NEW.grant_id
       AND OLD.original_intent_id = NEW.original_intent_id
       AND OLD.organization_id = NEW.organization_id
       AND OLD.agent_id = NEW.agent_id
       AND OLD.task_id = NEW.task_id
       AND OLD.reason_class = NEW.reason_class
       AND OLD.allowed_category = NEW.allowed_category
       AND OLD.max_amount_atomic = NEW.max_amount_atomic
       AND OLD.allowed_seller_set = NEW.allowed_seller_set
       AND OLD.issued_at = NEW.issued_at
       AND OLD.expires_at = NEW.expires_at
       AND OLD.grant_digest = NEW.grant_digest
       AND OLD.canonical_request_hash = NEW.canonical_request_hash
       AND OLD.payload_json = NEW.payload_json
       AND OLD.signature = NEW.signature THEN
        IF NOT EXISTS (
            SELECT 1
            FROM ascp_intents i
            WHERE i.operation_id = NEW.consumed_operation_id
              AND i.organization_id = OLD.organization_id
              AND i.actor_id = OLD.agent_id
              AND i.adaptation_grant_id = OLD.grant_id
              AND i.adaptation_grant_digest = OLD.grant_digest
        ) THEN
            RAISE EXCEPTION 'adaptation grant consumer is not bound to this grant' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'adaptation grants allow only ISSUED to CONSUMED' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_adaptation_grants_transition_guard ON ascp_adaptation_grants;
CREATE TRIGGER ascp_adaptation_grants_transition_guard
BEFORE UPDATE OR DELETE ON ascp_adaptation_grants
FOR EACH ROW EXECUTE FUNCTION flowops_guard_adaptation_grant_transition();
