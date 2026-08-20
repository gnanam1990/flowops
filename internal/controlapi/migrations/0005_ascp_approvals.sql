CREATE TABLE IF NOT EXISTS ascp_approvals (
    approval_id text PRIMARY KEY CHECK (approval_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES organizations(id),
    intent_id text NOT NULL REFERENCES ascp_intents(operation_id),
    state text NOT NULL CHECK (state IN ('REQUESTED','APPROVED','REJECTED','EXPIRED','CANCELLED')),
    review_snapshot_hash text NOT NULL CHECK (review_snapshot_hash ~ '^0x[0-9a-f]{64}$'),
    requested_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > requested_at),
    decided_at timestamptz,
    decided_by text,
    cancel_reason text,
    UNIQUE (organization_id, intent_id),
    CHECK ((state = 'REQUESTED' AND decided_at IS NULL AND decided_by IS NULL AND cancel_reason IS NULL) OR
           (state IN ('APPROVED','REJECTED') AND decided_at IS NOT NULL AND decided_by IS NOT NULL AND cancel_reason IS NULL) OR
           (state = 'CANCELLED' AND decided_at IS NOT NULL AND decided_by IS NULL AND cancel_reason IS NOT NULL) OR
           (state = 'EXPIRED' AND decided_by IS NULL AND cancel_reason IS NULL))
);

CREATE INDEX IF NOT EXISTS ascp_approvals_pending_expiry_idx
ON ascp_approvals (organization_id, expires_at)
WHERE state = 'REQUESTED';
