CREATE TABLE IF NOT EXISTS ascp_intents (
    operation_id text PRIMARY KEY CHECK (operation_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES organizations(id),
    actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 128),
    endpoint text NOT NULL CHECK (endpoint = 'ascp.intent.create'),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    canonical_input_hash text NOT NULL CHECK (canonical_input_hash ~ '^[0-9a-f]{64}$'),
    quote_hash text NOT NULL CHECK (quote_hash ~ '^0x[0-9a-f]{64}$'),
    purchase_spec_hash text NOT NULL CHECK (purchase_spec_hash ~ '^0x[0-9a-f]{64}$'),
    quote_nonce text NOT NULL CONSTRAINT ascp_intents_quote_nonce_unique UNIQUE CHECK (quote_nonce ~ '^0x[0-9a-f]{64}$'),
    directory_version bigint NOT NULL CHECK (directory_version > 0),
    directory_contract text NOT NULL CHECK (directory_contract ~ '^0x[0-9a-f]{40}$'),
    seller_signer text NOT NULL CHECK (seller_signer ~ '^0x[0-9a-f]{40}$'),
    quote_json jsonb NOT NULL,
    purchase_spec_json jsonb NOT NULL,
    request_body bytea NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (organization_id, actor_id, endpoint, idempotency_key)
);

CREATE INDEX IF NOT EXISTS ascp_intents_org_created_idx
ON ascp_intents (organization_id, created_at DESC);
