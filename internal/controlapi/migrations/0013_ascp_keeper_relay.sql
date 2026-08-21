CREATE TABLE IF NOT EXISTS ascp_keeper_jobs (
    job_id text PRIMARY KEY CHECK (job_id ~ '^0x[0-9a-f]{64}$' AND job_id <> ('0x' || repeat('0', 64))),
    operation_id text NOT NULL CHECK (operation_id ~ '^0x[0-9a-f]{64}$' AND operation_id <> ('0x' || repeat('0', 64))),
    organization_id text NOT NULL REFERENCES organizations(id),
    action text NOT NULL CHECK (action IN ('LOCK','RELEASE','REFUND','CLAIM_EXPIRED','ADMIN')),
    chain_id bigint NOT NULL CHECK (chain_id IN (8453,84532)),
    keeper_id text NOT NULL CHECK (keeper_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    gas_payer text NOT NULL CHECK (gas_payer ~ '^0x[0-9a-f]{40}$' AND gas_payer <> ('0x' || repeat('0', 40))),
    target text NOT NULL CHECK (target ~ '^0x[0-9a-f]{40}$' AND target <> ('0x' || repeat('0', 40))),
    value_wei text NOT NULL CHECK (value_wei = '0'),
    canonical_payload bytea NOT NULL CHECK (octet_length(canonical_payload) BETWEEN 1 AND 262144),
    canonical_payload_hash text NOT NULL CHECK (canonical_payload_hash ~ '^0x[0-9a-f]{64}$' AND canonical_payload_hash <> ('0x' || repeat('0',64))),
    authorization_digest text CHECK (authorization_digest IS NULL OR (authorization_digest ~ '^0x[0-9a-f]{64}$' AND authorization_digest <> ('0x' || repeat('0',64)))),
    signer_handle text CHECK (signer_handle IS NULL OR (octet_length(signer_handle) BETWEEN 16 AND 512 AND signer_handle !~ '[[:cntrl:]]')),
    signer_address text CHECK (signer_address IS NULL OR (signer_address ~ '^0x[0-9a-f]{40}$' AND signer_address <> ('0x' || repeat('0',40)))),
    valid_after timestamptz,
    valid_before timestamptz,
    eligible_after timestamptz NOT NULL,
    eligibility_evidence_digest text CHECK (eligibility_evidence_digest IS NULL OR (eligibility_evidence_digest ~ '^0x[0-9a-f]{64}$' AND eligibility_evidence_digest <> ('0x' || repeat('0',64)))),
    eligibility_observed_at timestamptz,
    leadership_epoch bigint CHECK (leadership_epoch IS NULL OR leadership_epoch > 0),
    state text NOT NULL CHECK (state IN (
        'QUEUED','PREPARED','BROADCASTING','SUBMITTED','CONFIRMED','FINALIZED',
        'REVERTED','REORGED','TIMED_OUT','AMBIGUOUS','DEAD_LETTER'
    )),
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 4),
    current_attempt integer CHECK (current_attempt IS NULL OR current_attempt BETWEEN 1 AND 4),
    nonce numeric(20,0) CHECK (nonce IS NULL OR nonce BETWEEN 0 AND 18446744073709551615),
    last_error text CHECK (last_error IS NULL OR octet_length(last_error) <= 2048),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (operation_id, action),
    UNIQUE (gas_payer, nonce),
    CHECK ((lease_owner IS NULL) = (lease_token IS NULL) AND (lease_token IS NULL) = (lease_expires_at IS NULL)),
    CHECK ((attempt_count = 0) = (current_attempt IS NULL) AND (current_attempt IS NULL OR nonce IS NOT NULL)),
    CHECK (
        (action = 'CLAIM_EXPIRED' AND authorization_digest IS NULL AND signer_handle IS NULL AND
         signer_address IS NULL AND valid_after IS NULL AND valid_before IS NULL AND leadership_epoch IS NULL AND
         eligibility_evidence_digest IS NOT NULL AND eligibility_observed_at IS NOT NULL)
        OR
        (action <> 'CLAIM_EXPIRED' AND authorization_digest IS NOT NULL AND signer_handle IS NOT NULL AND
         signer_address IS NOT NULL AND valid_after IS NOT NULL AND valid_before IS NOT NULL AND
         valid_before > valid_after AND valid_before <= valid_after + interval '10 minutes' AND
         eligible_after >= valid_after AND eligible_after < valid_before AND leadership_epoch IS NOT NULL AND
         eligibility_evidence_digest IS NULL AND eligibility_observed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS ascp_keeper_jobs_claim_idx
ON ascp_keeper_jobs (eligible_after, created_at, job_id)
WHERE state IN ('QUEUED','PREPARED','BROADCASTING');

CREATE INDEX IF NOT EXISTS ascp_keeper_jobs_recovery_idx
ON ascp_keeper_jobs (state, updated_at)
WHERE state IN ('SUBMITTED','CONFIRMED','REVERTED','REORGED','TIMED_OUT','AMBIGUOUS','DEAD_LETTER');

CREATE TABLE IF NOT EXISTS ascp_keeper_nonce_sequences (
    chain_id bigint NOT NULL CHECK (chain_id IN (8453,84532)),
    gas_payer text NOT NULL CHECK (gas_payer ~ '^0x[0-9a-f]{40}$' AND gas_payer <> ('0x' || repeat('0',40))),
    next_nonce numeric(20,0) NOT NULL CHECK (next_nonce BETWEEN 0 AND 18446744073709551615),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, gas_payer)
);

CREATE TABLE IF NOT EXISTS ascp_keeper_tx_attempts (
    job_id text NOT NULL REFERENCES ascp_keeper_jobs(job_id),
    attempt_number integer NOT NULL CHECK (attempt_number BETWEEN 1 AND 4),
    nonce numeric(20,0) NOT NULL CHECK (nonce BETWEEN 0 AND 18446744073709551615),
    gas_payer text NOT NULL CHECK (gas_payer ~ '^0x[0-9a-f]{40}$' AND gas_payer <> ('0x' || repeat('0',40))),
    max_fee_per_gas_wei text NOT NULL CHECK (max_fee_per_gas_wei ~ '^[1-9][0-9]{0,77}$'),
    max_priority_fee_per_gas_wei text NOT NULL CHECK (max_priority_fee_per_gas_wei ~ '^(0|[1-9][0-9]{0,77})$'),
    transaction_hash text NOT NULL CHECK (transaction_hash ~ '^0x[0-9a-f]{64}$' AND transaction_hash <> ('0x' || repeat('0',64))),
    sealed_raw_transaction bytea NOT NULL CHECK (octet_length(sealed_raw_transaction) BETWEEN 1 AND 2097152),
    sealing_key_id text NOT NULL CHECK (sealing_key_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    state text NOT NULL CHECK (state IN (
        'PREPARED','BROADCASTING','SUBMITTED','CONFIRMED','REPLACED','AMBIGUOUS','REJECTED','REVERTED','REORGED','FINALIZED'
    )),
    prepared_at timestamptz NOT NULL,
    broadcast_at timestamptz,
    last_error text CHECK (last_error IS NULL OR octet_length(last_error) <= 2048),
    evidence_digest text CHECK (evidence_digest IS NULL OR (evidence_digest ~ '^0x[0-9a-f]{64}$' AND evidence_digest <> ('0x' || repeat('0',64)))),
    observed_at timestamptz,
    PRIMARY KEY (job_id, attempt_number),
    UNIQUE (transaction_hash),
    CHECK ((state = 'PREPARED') = (broadcast_at IS NULL)),
    CHECK (max_priority_fee_per_gas_wei::numeric <= max_fee_per_gas_wei::numeric),
    CHECK ((evidence_digest IS NULL) = (observed_at IS NULL)),
    CHECK (state IN ('PREPARED','BROADCASTING','SUBMITTED','REPLACED','AMBIGUOUS','REJECTED') OR evidence_digest IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS ascp_keeper_tx_attempts_nonce_idx
ON ascp_keeper_tx_attempts (gas_payer, nonce, attempt_number DESC);

-- Every attempt carries the keeper EOA explicitly. This trigger prevents a
-- caller from laundering a different sender into the reconciled gas account.
CREATE OR REPLACE FUNCTION flowops_validate_keeper_gas_payer()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE expected_payer text;
DECLARE expected_nonce numeric;
BEGIN
    SELECT gas_payer, nonce INTO expected_payer, expected_nonce FROM ascp_keeper_jobs WHERE job_id = NEW.job_id;
    IF expected_payer IS NULL OR expected_payer <> NEW.gas_payer OR expected_nonce IS NULL OR expected_nonce <> NEW.nonce THEN
        RAISE EXCEPTION 'keeper attempt sender or nonce does not match durable job' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_keeper_attempt_gas_payer ON ascp_keeper_tx_attempts;
CREATE TRIGGER ascp_keeper_attempt_gas_payer
BEFORE INSERT OR UPDATE ON ascp_keeper_tx_attempts
FOR EACH ROW EXECUTE FUNCTION flowops_validate_keeper_gas_payer();
