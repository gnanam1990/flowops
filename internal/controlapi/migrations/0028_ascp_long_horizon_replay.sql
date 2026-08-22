CREATE TABLE IF NOT EXISTS ascp_financial_tombstones (
    organization_id text NOT NULL REFERENCES organizations(id),
    actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 128),
    endpoint text NOT NULL CHECK (endpoint = 'ascp.intent.create'),
    logical_operation text NOT NULL CHECK (logical_operation = 'CREATE_INTENT'),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    canonical_request_hash text NOT NULL CHECK (canonical_request_hash ~ '^[0-9a-f]{64}$'),
    operation_id text NOT NULL UNIQUE CHECK (operation_id ~ '^0x[0-9a-f]{64}$'),
    quote_hash text NOT NULL CHECK (quote_hash ~ '^0x[0-9a-f]{64}$'),
    purchase_spec_hash text NOT NULL CHECK (purchase_spec_hash ~ '^0x[0-9a-f]{64}$'),
    quote_nonce text NOT NULL CONSTRAINT ascp_financial_tombstones_quote_nonce_unique UNIQUE
        CHECK (quote_nonce ~ '^0x[0-9a-f]{64}$'),
    directory_version bigint NOT NULL CHECK (directory_version > 0),
    directory_contract text NOT NULL CHECK (directory_contract ~ '^0x[0-9a-f]{40}$'),
    seller_signer text NOT NULL CHECK (seller_signer ~ '^0x[0-9a-f]{40}$'),
    adaptation_grant_id text,
    terminal_outcome_hash text CHECK (terminal_outcome_hash IS NULL OR terminal_outcome_hash ~ '^0x[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, actor_id, endpoint, logical_operation, idempotency_key)
);

INSERT INTO ascp_financial_tombstones
    (organization_id, actor_id, endpoint, logical_operation, idempotency_key,
     canonical_request_hash, operation_id, quote_hash, purchase_spec_hash, quote_nonce,
     directory_version, directory_contract, seller_signer, adaptation_grant_id, created_at)
SELECT organization_id, actor_id, endpoint, 'CREATE_INTENT', idempotency_key,
       canonical_input_hash, operation_id, quote_hash, purchase_spec_hash, quote_nonce,
       directory_version, directory_contract, seller_signer, adaptation_grant_id, created_at
FROM ascp_intents
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION flowops_require_intent_tombstone()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    tombstone ascp_financial_tombstones%ROWTYPE;
    scope_value text := to_jsonb(NEW)->>'idempotency_key';
BEGIN
    -- Expand/contract compatibility: an older runtime can still insert the
    -- permanent record while a fleet rolls forward to explicit tombstone-first
    -- transactions.
    INSERT INTO ascp_financial_tombstones
        (organization_id, actor_id, endpoint, logical_operation, idempotency_key,
         canonical_request_hash, operation_id, quote_hash, purchase_spec_hash, quote_nonce,
         directory_version, directory_contract, seller_signer, adaptation_grant_id, created_at)
    VALUES
        (NEW.organization_id, NEW.actor_id, NEW.endpoint, 'CREATE_INTENT', scope_value,
         NEW.canonical_input_hash, NEW.operation_id, NEW.quote_hash, NEW.purchase_spec_hash, NEW.quote_nonce,
         NEW.directory_version, NEW.directory_contract, NEW.seller_signer, NEW.adaptation_grant_id, NEW.created_at)
    ON CONFLICT (organization_id, actor_id, endpoint, logical_operation, idempotency_key) DO NOTHING;

    SELECT * INTO tombstone
    FROM ascp_financial_tombstones
    WHERE organization_id = NEW.organization_id
      AND actor_id = NEW.actor_id
      AND endpoint = NEW.endpoint
      AND logical_operation = 'CREATE_INTENT'
      AND idempotency_key IS NOT DISTINCT FROM scope_value;

    IF tombstone.canonical_request_hash <> NEW.canonical_input_hash OR
       tombstone.quote_hash <> NEW.quote_hash OR tombstone.purchase_spec_hash <> NEW.purchase_spec_hash OR
       tombstone.quote_nonce <> NEW.quote_nonce OR tombstone.directory_version <> NEW.directory_version OR
       tombstone.directory_contract <> NEW.directory_contract OR tombstone.seller_signer <> NEW.seller_signer OR
       tombstone.adaptation_grant_id IS DISTINCT FROM NEW.adaptation_grant_id THEN
        RAISE EXCEPTION 'matching permanent financial tombstone is required'
            USING ERRCODE = '23514';
    END IF;
    IF tombstone.operation_id <> NEW.operation_id AND NOT EXISTS (
        SELECT 1 FROM ascp_intents i
        WHERE i.operation_id = tombstone.operation_id
          AND i.organization_id = NEW.organization_id
          AND i.actor_id = NEW.actor_id
          AND i.endpoint = NEW.endpoint
          AND i.idempotency_key IS NOT DISTINCT FROM scope_value
          AND i.canonical_input_hash = NEW.canonical_input_hash
    ) THEN
        RAISE EXCEPTION 'permanent tombstone forbids a replacement operation'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_intents_require_tombstone ON ascp_intents;
CREATE TRIGGER ascp_intents_require_tombstone
BEFORE INSERT ON ascp_intents
FOR EACH ROW EXECUTE FUNCTION flowops_require_intent_tombstone();

DROP TRIGGER IF EXISTS ascp_financial_tombstones_immutable ON ascp_financial_tombstones;
CREATE TRIGGER ascp_financial_tombstones_immutable
BEFORE UPDATE OR DELETE ON ascp_financial_tombstones
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_financial_tombstones_no_truncate ON ascp_financial_tombstones;
CREATE TRIGGER ascp_financial_tombstones_no_truncate
BEFORE TRUNCATE ON ascp_financial_tombstones
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE TABLE IF NOT EXISTS ascp_seller_results (
    seller_id text NOT NULL CHECK (length(seller_id) BETWEEN 1 AND 128),
    call_id text NOT NULL CHECK (call_id ~ '^0x[0-9a-f]{64}$'),
    request_hash text NOT NULL CHECK (request_hash ~ '^0x[0-9a-f]{64}$'),
    resource_operation_key text NOT NULL CHECK (length(resource_operation_key) BETWEEN 1 AND 128),
    state text NOT NULL CHECK (state IN ('STARTED_UNKNOWN','COMPLETED')),
    response_status integer CHECK (response_status BETWEEN 100 AND 599),
    response_headers jsonb,
    response_body bytea,
    content_digest text CHECK (content_digest IS NULL OR content_digest ~ '^0x[0-9a-f]{64}$'),
    side_effect_status text CHECK (side_effect_status IS NULL OR side_effect_status = 'COMPLETED'),
    settle_by timestamptz NOT NULL,
    retain_until timestamptz NOT NULL CHECK (retain_until = settle_by + interval '9600 hours'),
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (seller_id, call_id),
    CONSTRAINT ascp_seller_results_resource_operation_key_unique UNIQUE (seller_id, resource_operation_key),
    CHECK (
      (state = 'STARTED_UNKNOWN' AND response_status IS NULL AND response_headers IS NULL AND response_body IS NULL AND content_digest IS NULL AND side_effect_status IS NULL AND completed_at IS NULL) OR
      (state = 'COMPLETED' AND response_status IS NOT NULL AND response_headers IS NOT NULL AND response_body IS NOT NULL AND content_digest IS NOT NULL AND side_effect_status = 'COMPLETED' AND completed_at IS NOT NULL)
    )
);

CREATE OR REPLACE FUNCTION flowops_guard_seller_result_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'seller results cannot be deleted during durable retention' USING ERRCODE = '55000';
    END IF;
    IF OLD.seller_id <> NEW.seller_id OR OLD.call_id <> NEW.call_id OR
       OLD.request_hash <> NEW.request_hash OR OLD.resource_operation_key <> NEW.resource_operation_key OR
       OLD.settle_by <> NEW.settle_by OR OLD.retain_until <> NEW.retain_until OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'seller result binding is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.state = 'STARTED_UNKNOWN' AND NEW.state = 'COMPLETED' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'invalid seller result transition' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_seller_results_transition_guard ON ascp_seller_results;
CREATE TRIGGER ascp_seller_results_transition_guard
BEFORE UPDATE OR DELETE ON ascp_seller_results
FOR EACH ROW EXECUTE FUNCTION flowops_guard_seller_result_transition();

DROP TRIGGER IF EXISTS ascp_seller_results_no_truncate ON ascp_seller_results;
CREATE TRIGGER ascp_seller_results_no_truncate
BEFORE TRUNCATE ON ascp_seller_results
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
