DROP TRIGGER IF EXISTS ascp_verifier_key_observations_immutable ON ascp_verifier_key_observations;

ALTER TABLE ascp_verifier_key_observations
ADD COLUMN finalized_log_index bigint;

UPDATE ascp_verifier_key_observations
SET finalized_log_index = 0
WHERE finalized_log_index IS NULL;

ALTER TABLE ascp_verifier_key_observations
ALTER COLUMN finalized_log_index SET NOT NULL;

ALTER TABLE ascp_verifier_key_observations
ADD CONSTRAINT ascp_verifier_key_observations_log_index_check CHECK (finalized_log_index >= 0);

ALTER TABLE ascp_verifier_key_observations
DROP CONSTRAINT ascp_verifier_key_observations_pkey;

ALTER TABLE ascp_verifier_key_observations
ADD PRIMARY KEY (chain_id, escrow_contract, verifier_address, verifier_epoch, finalized_block, finalized_log_index);

DROP INDEX IF EXISTS ascp_verifier_key_latest_idx;
CREATE INDEX ascp_verifier_key_signer_latest_idx
ON ascp_verifier_key_observations (chain_id, escrow_contract, verifier_address, finalized_block DESC, finalized_log_index DESC);

CREATE INDEX ascp_verifier_intake_replays_received_idx
ON ascp_verifier_intake_replays (received_at);

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

CREATE TRIGGER ascp_verifier_key_observations_immutable
BEFORE UPDATE OR DELETE ON ascp_verifier_key_observations
FOR EACH ROW EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_verifier_intake_replays_immutable ON ascp_verifier_intake_replays;
CREATE TRIGGER ascp_verifier_intake_replays_immutable
BEFORE UPDATE OR DELETE ON ascp_verifier_intake_replays
FOR EACH ROW EXECUTE FUNCTION reject_ascp_verifier_replay_mutation();

CREATE TRIGGER ascp_verdict_decisions_no_truncate
BEFORE TRUNCATE ON ascp_verdict_decisions
FOR EACH STATEMENT EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

CREATE TRIGGER ascp_verifier_key_observations_no_truncate
BEFORE TRUNCATE ON ascp_verifier_key_observations
FOR EACH STATEMENT EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();

CREATE TRIGGER ascp_verifier_intake_replays_no_truncate
BEFORE TRUNCATE ON ascp_verifier_intake_replays
FOR EACH STATEMENT EXECUTE FUNCTION reject_ascp_verifier_immutable_mutation();
