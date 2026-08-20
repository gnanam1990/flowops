CREATE TABLE IF NOT EXISTS ascp_execution_authorizations (
    authorization_id text PRIMARY KEY CHECK (authorization_id ~ '^0x[0-9a-f]{64}$'),
    approval_id text REFERENCES ascp_approvals(approval_id),
    auto_decision_ref text,
    intent_id text NOT NULL UNIQUE REFERENCES ascp_intents(operation_id),
    state text NOT NULL CHECK (state IN ('NOT_EVALUATED','VALIDATED_AND_RESERVED','INVALIDATED')),
    execution_snapshot_hash text NOT NULL CHECK (execution_snapshot_hash ~ '^0x[0-9a-f]{64}$'),
    reservation_id text UNIQUE REFERENCES ascp_budget_reservations(reservation_id),
    invalidation_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    evaluated_at timestamptz,
    CHECK ((approval_id IS NULL) <> (auto_decision_ref IS NULL)),
    CHECK ((state = 'NOT_EVALUATED' AND reservation_id IS NULL AND evaluated_at IS NULL) OR
           (state = 'VALIDATED_AND_RESERVED' AND reservation_id IS NOT NULL AND evaluated_at IS NOT NULL AND invalidation_reason = '') OR
           (state = 'INVALIDATED' AND reservation_id IS NULL AND evaluated_at IS NOT NULL AND invalidation_reason <> ''))
);
