ALTER TABLE ascp_sign_requests
    DROP CONSTRAINT IF EXISTS ascp_sign_requests_refusal_shape;
ALTER TABLE ascp_sign_requests
    ADD CONSTRAINT ascp_sign_requests_refusal_shape CHECK (
        state <> 'REFUSED' OR (
            prepared_handle IS NULL AND
            unactivated_proof IS NULL AND
            expired_at IS NULL AND
            lease_owner IS NULL AND
            lease_token IS NULL AND
            lease_expires_at IS NULL AND
            last_error IS NOT NULL AND
            last_error = 'SIGNER_REFUSED'
        )
    );
