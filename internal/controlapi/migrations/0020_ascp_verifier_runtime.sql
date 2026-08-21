CREATE SEQUENCE IF NOT EXISTS ascp_verdict_nonce_seq AS bigint MINVALUE 1 START WITH 1 NO CYCLE;

CREATE TABLE IF NOT EXISTS ascp_verdict_decisions (
    call_id text PRIMARY KEY CHECK (call_id ~ '^0x[0-9a-f]{64}$' AND call_id <> ('0x' || repeat('0',64))),
    chain_id text NOT NULL CHECK (chain_id ~ '^[1-9][0-9]{0,77}$'),
    input_fingerprint text NOT NULL CHECK (input_fingerprint ~ '^0x[0-9a-f]{64}$'),
    verdict_nonce numeric(78,0) NOT NULL UNIQUE CHECK (verdict_nonce > 0),
    attestation_hash text NOT NULL UNIQUE CHECK (attestation_hash ~ '^0x[0-9a-f]{64}$'),
    decision_json jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS ascp_verifier_key_observations (
    chain_id text NOT NULL CHECK (chain_id ~ '^[1-9][0-9]{0,77}$'),
    escrow_contract text NOT NULL CHECK (escrow_contract ~ '^0x[0-9a-f]{40}$'),
    verifier_address text NOT NULL CHECK (verifier_address ~ '^0x[0-9a-f]{40}$'),
    verifier_epoch bigint NOT NULL CHECK (verifier_epoch > 0),
    finalized_block bigint NOT NULL CHECK (finalized_block > 0),
    finalized_log_index bigint NOT NULL CHECK (finalized_log_index >= 0),
    active boolean NOT NULL,
    evidence_digest text NOT NULL CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    observed_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, escrow_contract, verifier_address, verifier_epoch, finalized_block, finalized_log_index)
);

CREATE INDEX IF NOT EXISTS ascp_verifier_key_signer_latest_idx
ON ascp_verifier_key_observations (chain_id, escrow_contract, verifier_address, finalized_block DESC, finalized_log_index DESC);

CREATE TABLE IF NOT EXISTS ascp_verifier_intake_replays (
    key_id text NOT NULL CHECK (key_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$'),
    request_nonce text NOT NULL CHECK (request_nonce ~ '^[A-Za-z0-9_-]{22,128}$'),
    body_digest text NOT NULL CHECK (body_digest ~ '^[0-9a-f]{64}$'),
    signed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    PRIMARY KEY (key_id, request_nonce)
);

CREATE INDEX IF NOT EXISTS ascp_verifier_intake_replays_received_idx
ON ascp_verifier_intake_replays (received_at);

CREATE OR REPLACE FUNCTION reject_ascp_verifier_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'ASCP verifier evidence is append-only';
END;
$$;

CREATE OR REPLACE FUNCTION reject_ascp_verifier_replay_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND OLD.received_at < statement_timestamp() - interval '24 hours' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'ASCP verifier replay state is immutable during retention';
END;
$$;

CREATE OR REPLACE FUNCTION prune_ascp_verifier_intake_replays()
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    deleted_rows bigint;
BEGIN
    DELETE FROM public.ascp_verifier_intake_replays
    WHERE received_at < statement_timestamp() - interval '24 hours';
    GET DIAGNOSTICS deleted_rows = ROW_COUNT;
    RETURN deleted_rows;
END;
$$;

REVOKE ALL ON FUNCTION prune_ascp_verifier_intake_replays() FROM PUBLIC;

DROP TRIGGER IF EXISTS ascp_verdict_decisions_immutable ON ascp_verdict_decisions;
CREATE TRIGGER ascp_verdict_decisions_immutable
BEFORE UPDATE OR DELETE ON ascp_verdict_decisions
FOR EACH ROW EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_verdict_decisions_no_truncate ON ascp_verdict_decisions;
CREATE TRIGGER ascp_verdict_decisions_no_truncate
BEFORE TRUNCATE ON ascp_verdict_decisions
FOR EACH STATEMENT EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_verifier_key_observations_immutable ON ascp_verifier_key_observations;
CREATE TRIGGER ascp_verifier_key_observations_immutable
BEFORE UPDATE OR DELETE ON ascp_verifier_key_observations
FOR EACH ROW EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_verifier_key_observations_no_truncate ON ascp_verifier_key_observations;
CREATE TRIGGER ascp_verifier_key_observations_no_truncate
BEFORE TRUNCATE ON ascp_verifier_key_observations
FOR EACH STATEMENT EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_verifier_intake_replays_immutable ON ascp_verifier_intake_replays;
CREATE TRIGGER ascp_verifier_intake_replays_immutable
BEFORE UPDATE OR DELETE ON ascp_verifier_intake_replays
FOR EACH ROW EXECUTE FUNCTION reject_ascp_verifier_replay_mutation();

DROP TRIGGER IF EXISTS ascp_verifier_intake_replays_no_truncate ON ascp_verifier_intake_replays;
CREATE TRIGGER ascp_verifier_intake_replays_no_truncate
BEFORE TRUNCATE ON ascp_verifier_intake_replays
FOR EACH STATEMENT EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();
