CREATE TABLE IF NOT EXISTS ascp_bearer_handles (
    handle_id text PRIMARY KEY,
    operation_id text NOT NULL REFERENCES ascp_intents(operation_id),
    payload_hash text NOT NULL CHECK (payload_hash ~ '^0x[0-9a-f]{64}$'),
    digest text NOT NULL CHECK (digest ~ '^0x[0-9a-f]{64}$'),
    nonce text NOT NULL CHECK (nonce ~ '^0x[0-9a-f]{64}$'),
    encrypted_artifact bytea,
    state text NOT NULL CHECK (state IN ('PREPARED','ACTIVE','RELEASED','EXPIRED','TERMINAL')),
    valid_until timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (operation_id, digest, nonce)
);
