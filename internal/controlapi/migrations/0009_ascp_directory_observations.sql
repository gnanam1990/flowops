-- JSONB normalization cannot preserve the canonical PurchaseSpec bytes needed
-- to reproduce purchaseSpecHash. New intake writes this bytea column; legacy
-- rows remain NULL and fail execution revalidation closed.
ALTER TABLE ascp_intents
ADD COLUMN IF NOT EXISTS purchase_spec_bytes bytea;

CREATE TABLE IF NOT EXISTS ascp_budget_reservation_dimensions (
    reservation_id text NOT NULL REFERENCES ascp_budget_reservations(reservation_id),
    dimension_id text NOT NULL CHECK (length(dimension_id) BETWEEN 1 AND 256),
    limit_base_units text NOT NULL CHECK (limit_base_units ~ '^[1-9][0-9]*$'),
    refundable boolean NOT NULL,
    PRIMARY KEY (reservation_id, dimension_id)
);

CREATE INDEX IF NOT EXISTS ascp_budget_reservation_dimensions_lookup_idx
ON ascp_budget_reservation_dimensions (dimension_id, reservation_id);

-- Existing rows were produced from the Go Dimension shape. Fail the migration
-- rather than silently omitting malformed live financial state.
INSERT INTO ascp_budget_reservation_dimensions
    (reservation_id, dimension_id, limit_base_units, refundable)
SELECT r.reservation_id,
       COALESCE(d.value->>'ID', d.value->>'id'),
       COALESCE(d.value->>'Limit', d.value->>'limit'),
       COALESCE(d.value->>'Refundable', d.value->>'refundable')::boolean
FROM ascp_budget_reservations r
CROSS JOIN LATERAL jsonb_array_elements(r.dimensions) AS d(value)
ON CONFLICT (reservation_id, dimension_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS ascp_directory_snapshots (
    observation_digest text PRIMARY KEY CHECK (observation_digest ~ '^0x[0-9a-f]{64}$'),
    chain_id bigint NOT NULL CHECK (chain_id IN (8453, 84532)),
    directory_contract text NOT NULL CHECK (directory_contract ~ '^0x[0-9a-f]{40}$'),
    directory_version bigint NOT NULL CHECK (directory_version > 0),
    directory_root text NOT NULL CHECK (directory_root ~ '^0x[0-9a-f]{64}$'),
    finalized_block_number bigint NOT NULL CHECK (finalized_block_number > 0),
    finalized_block_hash text NOT NULL CHECK (finalized_block_hash ~ '^0x[0-9a-f]{64}$'),
    providers jsonb NOT NULL,
    observed_at timestamptz NOT NULL,
    UNIQUE (chain_id, directory_contract, finalized_block_number)
);

CREATE TABLE IF NOT EXISTS ascp_directory_quote_evidence (
    observation_digest text NOT NULL REFERENCES ascp_directory_snapshots(observation_digest),
    seller_id text NOT NULL CHECK (seller_id ~ '^0x[0-9a-f]{64}$'),
    resource_id text NOT NULL CHECK (resource_id ~ '^0x[0-9a-f]{64}$'),
    quote_signing_key text NOT NULL CHECK (quote_signing_key ~ '^0x[0-9a-f]{40}$'),
    key_epoch bigint NOT NULL CHECK (key_epoch > 0),
    payout_address text NOT NULL CHECK (payout_address ~ '^0x[0-9a-f]{40}$'),
    ack_authority text NOT NULL CHECK (ack_authority ~ '^0x[0-9a-f]{40}$'),
    amount_base_units text NOT NULL CHECK (amount_base_units ~ '^[1-9][0-9]*$'),
    verification_spec_hash text NOT NULL CHECK (verification_spec_hash ~ '^0x[0-9a-f]{64}$'),
    declared_work_time bigint NOT NULL CHECK (declared_work_time > 0),
    verification_budget_seconds bigint NOT NULL CHECK (verification_budget_seconds > 0),
    active boolean NOT NULL,
    quote_key_revoked boolean NOT NULL,
    PRIMARY KEY (observation_digest, seller_id, resource_id)
);

CREATE TABLE IF NOT EXISTS ascp_directory_heads (
    chain_id bigint NOT NULL CHECK (chain_id IN (8453, 84532)),
    directory_contract text NOT NULL CHECK (directory_contract ~ '^0x[0-9a-f]{40}$'),
    observation_digest text NOT NULL REFERENCES ascp_directory_snapshots(observation_digest),
    directory_version bigint NOT NULL CHECK (directory_version > 0),
    finalized_block_number bigint NOT NULL CHECK (finalized_block_number > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, directory_contract)
);
