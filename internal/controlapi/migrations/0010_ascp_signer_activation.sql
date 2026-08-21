-- The control plane may retain only an opaque prepared handle. A signature or
-- encrypted signature belongs to the isolated signer ledger.
ALTER TABLE ascp_bearer_handles
    ADD CONSTRAINT ascp_bearer_handles_no_artifact
    CHECK (encrypted_artifact IS NULL) NOT VALID;

ALTER TABLE ascp_bearer_handles
    VALIDATE CONSTRAINT ascp_bearer_handles_no_artifact;

CREATE TABLE IF NOT EXISTS ascp_sign_requests (
    request_id text PRIMARY KEY CHECK (request_id ~ '^0x[0-9a-f]{64}$'),
    authorization_id text NOT NULL UNIQUE REFERENCES ascp_execution_authorizations(authorization_id),
    operation_id text NOT NULL UNIQUE REFERENCES ascp_intents(operation_id),
    reservation_id text NOT NULL UNIQUE REFERENCES ascp_budget_reservations(reservation_id),
    input_hash text NOT NULL CHECK (input_hash ~ '^0x[0-9a-f]{64}$'),
    action_id text NOT NULL UNIQUE CHECK (action_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    canonical_payload bytea NOT NULL CHECK (octet_length(canonical_payload) BETWEEN 1 AND 262144),
    canonical_payload_hash text NOT NULL CHECK (canonical_payload_hash ~ '^0x[0-9a-f]{64}$'),
    evidence_bundle bytea NOT NULL CHECK (octet_length(evidence_bundle) BETWEEN 1 AND 1048576),
    evidence_bundle_hash text NOT NULL CHECK (evidence_bundle_hash ~ '^0x[0-9a-f]{64}$'),
    digest text NOT NULL UNIQUE CHECK (digest ~ '^0x[0-9a-f]{64}$'),
    nonce text NOT NULL CHECK (nonce ~ '^0x[0-9a-f]{64}$'),
    instrument_type text NOT NULL CHECK (instrument_type IN ('LOCK_AUTHORIZATION')),
    signer_key_id text NOT NULL CHECK (signer_key_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    key_epoch bigint NOT NULL CHECK (key_epoch > 0),
    module_address text NOT NULL CHECK (module_address ~ '^0x[0-9a-f]{40}$'),
    safe_address text NOT NULL CHECK (safe_address ~ '^0x[0-9a-f]{40}$'),
    keeper_id text NOT NULL CHECK (keeper_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    valid_after timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    prepared_handle text UNIQUE,
    state text NOT NULL CHECK (state IN (
        'SIGN_REQUESTED','PREPARED','ACTIVE_PENDING_MIRROR','ACTIVE_MIRRORED',
        'ACTIVATION_ACKNOWLEDGED','EXPIRED_UNACTIVATED','REFUSED'
    )),
    primary_mirror_digest text CHECK (primary_mirror_digest IS NULL OR primary_mirror_digest ~ '^0x[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    prepared_at timestamptz,
    activated_at timestamptz,
    mirrored_at timestamptz,
    acknowledged_at timestamptz,
    CHECK (valid_after < valid_until),
    UNIQUE (signer_key_id, nonce)
);

CREATE INDEX IF NOT EXISTS ascp_sign_requests_recovery_idx
ON ascp_sign_requests (state, created_at)
WHERE state NOT IN ('ACTIVATION_ACKNOWLEDGED','EXPIRED_UNACTIVATED','REFUSED');

CREATE TABLE IF NOT EXISTS ascp_bearer_registry (
    digest text PRIMARY KEY CHECK (digest ~ '^0x[0-9a-f]{64}$'),
    instrument_type text NOT NULL CHECK (instrument_type IN ('LOCK_AUTHORIZATION')),
    signature_ref text NOT NULL UNIQUE,
    nonce text NOT NULL CHECK (nonce ~ '^0x[0-9a-f]{64}$'),
    issued_at timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    signer_key_id text NOT NULL,
    key_epoch bigint NOT NULL CHECK (key_epoch > 0),
    operation_id text NOT NULL UNIQUE REFERENCES ascp_intents(operation_id),
    authorization_id text NOT NULL UNIQUE REFERENCES ascp_execution_authorizations(authorization_id),
    reservation_id text NOT NULL UNIQUE REFERENCES ascp_budget_reservations(reservation_id),
    module_address text NOT NULL CHECK (module_address ~ '^0x[0-9a-f]{40}$'),
    safe_address text NOT NULL CHECK (safe_address ~ '^0x[0-9a-f]{40}$'),
    outcome text NOT NULL CHECK (outcome IN ('LIVE','CONSUMED','EXPIRED_UNUSED','INVALIDATED_ONCHAIN')),
    primary_mirror_digest text CHECK (primary_mirror_digest IS NULL OR primary_mirror_digest ~ '^0x[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CHECK (issued_at < valid_until)
);

CREATE TABLE IF NOT EXISTS ascp_signer_outbox (
    event_id text PRIMARY KEY CHECK (event_id ~ '^0x[0-9a-f]{64}$'),
    request_id text NOT NULL REFERENCES ascp_sign_requests(request_id),
    operation_id text NOT NULL REFERENCES ascp_intents(operation_id),
    kind text NOT NULL CHECK (kind IN (
        'SIGN_PREPARE_REQUESTED','ACTIVATION_MIRROR_REQUESTED',
        'ACTIVATION_ACK_REQUESTED','SECONDARY_MIRROR_REQUESTED'
    )),
    payload jsonb NOT NULL,
    state text NOT NULL CHECK (state IN ('PENDING','DELIVERED')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at timestamptz NOT NULL,
    delivered_at timestamptz,
    UNIQUE (request_id, kind)
);

CREATE INDEX IF NOT EXISTS ascp_signer_outbox_pending_idx
ON ascp_signer_outbox (created_at, event_id)
WHERE state='PENDING';
