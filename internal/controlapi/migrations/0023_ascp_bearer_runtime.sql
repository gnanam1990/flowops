ALTER TABLE ascp_sign_requests
    ADD COLUMN IF NOT EXISTS lease_owner text,
    ADD COLUMN IF NOT EXISTS lease_token text,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT '-infinity',
    ADD COLUMN IF NOT EXISTS last_error text,
    ADD COLUMN IF NOT EXISTS unactivated_proof jsonb,
    ADD COLUMN IF NOT EXISTS expired_at timestamptz;

ALTER TABLE ascp_sign_requests
    DROP CONSTRAINT IF EXISTS ascp_sign_requests_runtime_lease_shape;
ALTER TABLE ascp_sign_requests
    ADD CONSTRAINT ascp_sign_requests_runtime_lease_shape CHECK (
        (lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL) OR
        (lease_owner ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' AND
         lease_token ~ '^0x[0-9a-f]{64}$' AND lease_expires_at IS NOT NULL)
    );

ALTER TABLE ascp_sign_requests
    DROP CONSTRAINT IF EXISTS ascp_sign_requests_runtime_terminal_shape;
ALTER TABLE ascp_sign_requests
    ADD CONSTRAINT ascp_sign_requests_runtime_terminal_shape CHECK (
        ((state = 'EXPIRED_UNACTIVATED') AND unactivated_proof IS NOT NULL AND expired_at IS NOT NULL AND
         lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL) OR
        ((state <> 'EXPIRED_UNACTIVATED') AND unactivated_proof IS NULL AND expired_at IS NULL)
    );

ALTER TABLE ascp_sign_requests
    DROP CONSTRAINT IF EXISTS ascp_sign_requests_runtime_error_shape;
ALTER TABLE ascp_sign_requests
    ADD CONSTRAINT ascp_sign_requests_runtime_error_shape CHECK (
        last_error IS NULL OR last_error ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
    );

CREATE INDEX IF NOT EXISTS ascp_sign_requests_runtime_claim_idx
ON ascp_sign_requests (next_attempt_at, created_at, request_id)
WHERE state NOT IN ('ACTIVATION_ACKNOWLEDGED','EXPIRED_UNACTIVATED','REFUSED');

ALTER TABLE ascp_signer_outbox
    ADD COLUMN IF NOT EXISTS cancelled_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_error text;

ALTER TABLE ascp_signer_outbox
    DROP CONSTRAINT IF EXISTS ascp_signer_outbox_state_check;
ALTER TABLE ascp_signer_outbox
    ADD CONSTRAINT ascp_signer_outbox_state_check
    CHECK (state IN ('PENDING','DELIVERED','CANCELLED'));

ALTER TABLE ascp_signer_outbox
    DROP CONSTRAINT IF EXISTS ascp_signer_outbox_terminal_shape;
ALTER TABLE ascp_signer_outbox
    ADD CONSTRAINT ascp_signer_outbox_terminal_shape CHECK (
        (state = 'PENDING' AND delivered_at IS NULL AND cancelled_at IS NULL) OR
        (state = 'DELIVERED' AND delivered_at IS NOT NULL AND cancelled_at IS NULL) OR
        (state = 'CANCELLED' AND delivered_at IS NULL AND cancelled_at IS NOT NULL)
    );
