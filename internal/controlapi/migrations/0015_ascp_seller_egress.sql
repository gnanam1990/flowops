CREATE TABLE IF NOT EXISTS ascp_seller_jobs (
    job_id text PRIMARY KEY CHECK (job_id ~ '^0x[0-9a-f]{64}$' AND job_id <> ('0x' || repeat('0',64))),
    operation_id text NOT NULL UNIQUE REFERENCES ascp_payment_operations(operation_id),
    organization_id text NOT NULL REFERENCES organizations(id),
    chain_id bigint NOT NULL CHECK (chain_id IN (8453,84532)),
    leadership_epoch bigint NOT NULL CHECK (leadership_epoch > 0),
    deliver_by bigint NOT NULL CHECK (deliver_by > 0),
    method text NOT NULL CHECK (method ~ '^[A-Z]{1,16}$'),
    request_url text NOT NULL CHECK (octet_length(request_url) BETWEEN 9 AND 2048 AND request_url LIKE 'https://%'),
    headers_json jsonb NOT NULL CHECK (jsonb_typeof(headers_json) = 'object' AND octet_length(headers_json::text) <= 262144),
    request_body bytea NOT NULL CHECK (octet_length(request_body) <= 8388608),
    canonical_spec_json bytea NOT NULL CHECK (octet_length(canonical_spec_json) BETWEEN 1 AND 33554432),
    offer_json jsonb NOT NULL CHECK (jsonb_typeof(offer_json) = 'object' AND octet_length(offer_json::text) <= 4194304),
    payment_json jsonb NOT NULL CHECK (jsonb_typeof(payment_json) = 'object' AND octet_length(payment_json::text) <= 4194304),
    binding_json jsonb NOT NULL CHECK (jsonb_typeof(binding_json) = 'object' AND octet_length(binding_json::text) <= 65536),
    locked_transaction_hash text NOT NULL CHECK (locked_transaction_hash ~ '^0x[0-9a-f]{64}$' AND locked_transaction_hash <> ('0x' || repeat('0',64))),
    payer text NOT NULL CHECK (payer ~ '^0x[0-9a-f]{40}$' AND payer <> ('0x' || repeat('0',40))),
    validated_chain_time bigint NOT NULL CHECK (validated_chain_time > 0 AND validated_chain_time < deliver_by),
    input_hash text NOT NULL CHECK (input_hash ~ '^[0-9a-f]{64}$'),
    state text NOT NULL DEFAULT 'QUEUED' CHECK (state IN ('QUEUED','SENDING','RETRY_WAIT','RESPONSE_STORED','CAPTURED','MISSING','DEAD_LETTER')),
    eligible_after timestamptz NOT NULL,
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 3),
    captured_at bigint CHECK (captured_at IS NULL OR captured_at > 0),
    capture_evidence_digest text CHECK (capture_evidence_digest IS NULL OR capture_evidence_digest ~ '^0x[0-9a-f]{64}$'),
    deadline_evidence_digest text CHECK (deadline_evidence_digest IS NULL OR deadline_evidence_digest ~ '^0x[0-9a-f]{64}$'),
    last_error text CHECK (last_error IS NULL OR octet_length(last_error) BETWEEN 1 AND 256),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((lease_owner IS NULL) = (lease_token IS NULL) AND (lease_token IS NULL) = (lease_expires_at IS NULL)),
    CHECK ((state = 'CAPTURED') = (captured_at IS NOT NULL)),
    CHECK ((captured_at IS NULL) = (capture_evidence_digest IS NULL)),
    CHECK (state <> 'MISSING' OR deadline_evidence_digest IS NOT NULL OR attempt_count > 0)
);

CREATE INDEX IF NOT EXISTS ascp_seller_jobs_dispatch_idx
ON ascp_seller_jobs (eligible_after,created_at,job_id)
WHERE state IN ('QUEUED','SENDING','RETRY_WAIT');

CREATE INDEX IF NOT EXISTS ascp_seller_jobs_finalize_idx
ON ascp_seller_jobs (eligible_after,created_at,job_id)
WHERE state = 'RESPONSE_STORED';

CREATE OR REPLACE FUNCTION flowops_validate_seller_operation_binding()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE operation_row ascp_payment_operations%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.state IS DISTINCT FROM 'SENDING' THEN
        RETURN NEW;
    END IF;
    SELECT * INTO operation_row FROM ascp_payment_operations WHERE operation_id = NEW.operation_id FOR SHARE;
    IF operation_row.operation_id IS NULL OR operation_row.state IS DISTINCT FROM 'LOCKED_FINALIZED' OR
       operation_row.organization_id IS DISTINCT FROM NEW.organization_id OR operation_row.chain_id IS DISTINCT FROM NEW.chain_id OR
       NEW.offer_json#>>'{accepted,network}' IS DISTINCT FROM ('eip155:' || NEW.chain_id::text) OR
       operation_row.call_id IS DISTINCT FROM NEW.job_id OR operation_row.call_id IS DISTINCT FROM NEW.binding_json->>'callId' OR
       operation_row.commitment_hash IS DISTINCT FROM NEW.binding_json->>'commitmentHash' OR
       operation_row.escrow_contract IS DISTINCT FROM NEW.binding_json->>'escrowContract' OR
       operation_row.asset IS DISTINCT FROM NEW.offer_json#>>'{accepted,asset}' OR
       operation_row.pay_to IS DISTINCT FROM NEW.offer_json#>>'{accepted,payTo}' OR
       operation_row.amount_base_units IS DISTINCT FROM NEW.offer_json#>>'{accepted,amount}' OR
       operation_row.locked_transaction_hash IS DISTINCT FROM NEW.locked_transaction_hash OR operation_row.buyer IS DISTINCT FROM NEW.payer OR
       extract(epoch FROM operation_row.settle_by)::bigint <= NEW.deliver_by THEN
        RAISE EXCEPTION 'seller job does not bind an executable payment operation' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_seller_jobs_validate_operation ON ascp_seller_jobs;
CREATE TRIGGER ascp_seller_jobs_validate_operation BEFORE INSERT OR UPDATE OF state,attempt_count ON ascp_seller_jobs
FOR EACH ROW EXECUTE FUNCTION flowops_validate_seller_operation_binding();

CREATE TABLE IF NOT EXISTS ascp_seller_attempts (
    job_id text NOT NULL REFERENCES ascp_seller_jobs(job_id),
    attempt_number integer NOT NULL CHECK (attempt_number BETWEEN 1 AND 3),
    state text NOT NULL CHECK (state IN ('STARTED','AMBIGUOUS','HTTP_RESPONSE')),
    chain_time_before_send bigint NOT NULL CHECK (chain_time_before_send > 0),
    chain_evidence_digest text NOT NULL CHECK (chain_evidence_digest ~ '^0x[0-9a-f]{64}$'),
    chain_observed_at timestamptz NOT NULL,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    result_code text CHECK (result_code IS NULL OR octet_length(result_code) BETWEEN 1 AND 256),
    PRIMARY KEY (job_id,attempt_number),
    CHECK ((state = 'STARTED') = (completed_at IS NULL)),
    CHECK ((completed_at IS NULL) = (result_code IS NULL))
);

CREATE TABLE IF NOT EXISTS ascp_seller_responses (
    job_id text NOT NULL,
    attempt_number integer NOT NULL,
    http_status integer NOT NULL CHECK (http_status BETWEEN 100 AND 599),
    content_type text NOT NULL CHECK (octet_length(content_type) <= 256),
    content_encoding text CHECK (content_encoding IS NULL OR content_encoding = 'identity'),
    payment_response text CHECK (payment_response IS NULL OR octet_length(payment_response) BETWEEN 1 AND 1048576),
    response_body bytea NOT NULL CHECK (octet_length(response_body) <= 16777216),
    content_digest text NOT NULL CHECK (content_digest ~ '^0x[0-9a-f]{64}$'),
    received_at timestamptz NOT NULL,
    PRIMARY KEY (job_id,attempt_number),
    FOREIGN KEY (job_id,attempt_number) REFERENCES ascp_seller_attempts(job_id,attempt_number)
);

CREATE OR REPLACE FUNCTION flowops_validate_seller_job_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.job_id,NEW.operation_id,NEW.organization_id,NEW.chain_id,NEW.leadership_epoch,NEW.deliver_by,
           NEW.method,NEW.request_url,NEW.headers_json,NEW.request_body,NEW.canonical_spec_json,NEW.offer_json,
           NEW.payment_json,NEW.binding_json,NEW.locked_transaction_hash,NEW.payer,NEW.validated_chain_time,NEW.input_hash,NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.job_id,OLD.operation_id,OLD.organization_id,OLD.chain_id,OLD.leadership_epoch,OLD.deliver_by,
           OLD.method,OLD.request_url,OLD.headers_json,OLD.request_body,OLD.canonical_spec_json,OLD.offer_json,
           OLD.payment_json,OLD.binding_json,OLD.locked_transaction_hash,OLD.payer,OLD.validated_chain_time,OLD.input_hash,OLD.created_at) THEN
        RAISE EXCEPTION 'seller job immutable binding changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.state IN ('CAPTURED','MISSING','DEAD_LETTER') AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'seller terminal state cannot reopen' USING ERRCODE = '23514';
    END IF;
    IF OLD.state IN ('CAPTURED','MISSING','DEAD_LETTER') AND
       ROW(NEW.eligible_after,NEW.attempt_count,NEW.captured_at,NEW.capture_evidence_digest,NEW.deadline_evidence_digest,NEW.last_error)
       IS DISTINCT FROM
       ROW(OLD.eligible_after,OLD.attempt_count,OLD.captured_at,OLD.capture_evidence_digest,OLD.deadline_evidence_digest,OLD.last_error) THEN
        RAISE EXCEPTION 'seller terminal evidence cannot change' USING ERRCODE = '23514';
    END IF;
    IF NOT (
        NEW.state = OLD.state OR
        (OLD.state IN ('QUEUED','RETRY_WAIT','SENDING') AND NEW.state IN ('SENDING','RETRY_WAIT','MISSING','DEAD_LETTER')) OR
        (OLD.state = 'SENDING' AND NEW.state = 'RESPONSE_STORED') OR
        (OLD.state = 'RESPONSE_STORED' AND NEW.state = 'CAPTURED')
    ) THEN
        RAISE EXCEPTION 'illegal seller job state transition' USING ERRCODE = '23514';
    END IF;
    IF NEW.attempt_count < OLD.attempt_count OR NEW.attempt_count > OLD.attempt_count + 1 THEN
        RAISE EXCEPTION 'illegal seller attempt count transition' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_seller_jobs_validate_update ON ascp_seller_jobs;
CREATE TRIGGER ascp_seller_jobs_validate_update BEFORE UPDATE ON ascp_seller_jobs
FOR EACH ROW EXECUTE FUNCTION flowops_validate_seller_job_update();

DROP TRIGGER IF EXISTS ascp_seller_responses_append_only ON ascp_seller_responses;
CREATE TRIGGER ascp_seller_responses_append_only BEFORE UPDATE OR DELETE ON ascp_seller_responses
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

CREATE OR REPLACE FUNCTION flowops_validate_seller_attempt_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.job_id,NEW.attempt_number,NEW.chain_time_before_send,NEW.chain_evidence_digest,NEW.chain_observed_at,NEW.started_at)
       IS DISTINCT FROM ROW(OLD.job_id,OLD.attempt_number,OLD.chain_time_before_send,OLD.chain_evidence_digest,OLD.chain_observed_at,OLD.started_at) THEN
        RAISE EXCEPTION 'seller attempt immutable binding changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.state <> 'STARTED' OR NEW.state NOT IN ('AMBIGUOUS','HTTP_RESPONSE') THEN
        RAISE EXCEPTION 'illegal seller attempt transition' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_seller_attempts_validate_update ON ascp_seller_attempts;
CREATE TRIGGER ascp_seller_attempts_validate_update BEFORE UPDATE ON ascp_seller_attempts
FOR EACH ROW EXECUTE FUNCTION flowops_validate_seller_attempt_update();

DROP TRIGGER IF EXISTS ascp_seller_attempts_reject_delete ON ascp_seller_attempts;
CREATE TRIGGER ascp_seller_attempts_reject_delete BEFORE DELETE ON ascp_seller_attempts
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
