CREATE TABLE IF NOT EXISTS ascp_budget_reservations (
    reservation_id text PRIMARY KEY CHECK (reservation_id ~ '^0x[0-9a-f]{64}$'),
    operation_id text NOT NULL UNIQUE REFERENCES ascp_intents(operation_id),
    amount_base_units text NOT NULL CHECK (amount_base_units ~ '^[1-9][0-9]*$'),
    state text NOT NULL CHECK (state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','CONSUMED_ON_RELEASE','RESTORED_ON_REFUND','RELEASED','RELEASED_AFTER_EXPIRY_PROOF','REORGED_BACK')),
    dimensions jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS ascp_budget_reservations_open_idx
ON ascp_budget_reservations (state, expires_at)
WHERE state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED');
