-- REFUSED existed in the original state enum before its atomic transition was
-- implemented. Refuse to guess when a legacy row shows evidence that signing
-- progressed or its reservation became live; operators must reconcile that
-- corruption explicitly before retrying this transactional migration.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM ascp_sign_requests request
        JOIN ascp_budget_reservations reservation
          ON reservation.reservation_id = request.reservation_id
        WHERE request.state = 'REFUSED'
          AND (
              request.prepared_handle IS NOT NULL OR
              request.prepared_at IS NOT NULL OR
              request.activated_at IS NOT NULL OR
              request.mirrored_at IS NOT NULL OR
              request.acknowledged_at IS NOT NULL OR
              request.unactivated_proof IS NOT NULL OR
              request.expired_at IS NOT NULL OR
              reservation.state NOT IN ('RESERVED','RELEASED') OR
              NOT EXISTS (
                  SELECT 1 FROM ascp_signer_outbox outbox
                  WHERE outbox.request_id = request.request_id
                    AND outbox.kind = 'SIGN_PREPARE_REQUESTED'
                    AND outbox.state IN ('PENDING','CANCELLED')
              ) OR
              EXISTS (
                  SELECT 1 FROM ascp_signer_outbox outbox
                  WHERE outbox.request_id = request.request_id
                    AND (outbox.kind <> 'SIGN_PREPARE_REQUESTED' OR outbox.state = 'DELIVERED')
              )
          )
    ) THEN
        RAISE EXCEPTION 'legacy REFUSED signer rows contain progressed signing, live-budget evidence, or a missing/invalid prepare outbox; reconcile them before migration'
            USING ERRCODE = '23514';
    END IF;
END;
$$;

-- The remaining legacy rows are unambiguously pre-signature refusals. Bring
-- the reservation, outbox, and request into the same atomic terminal shape
-- used by ActivationStore.Refuse before validating the constraint.
UPDATE ascp_budget_reservations reservation
SET state = 'RELEASED'
FROM ascp_sign_requests request
WHERE request.reservation_id = reservation.reservation_id
  AND request.state = 'REFUSED'
  AND reservation.state = 'RESERVED';

UPDATE ascp_signer_outbox outbox
SET state = 'CANCELLED',
    delivered_at = NULL,
    cancelled_at = COALESCE(outbox.cancelled_at, now()),
    last_error = 'SIGNER_REFUSED'
FROM ascp_sign_requests request
WHERE outbox.request_id = request.request_id
  AND request.state = 'REFUSED'
  AND outbox.kind = 'SIGN_PREPARE_REQUESTED'
  AND outbox.state IN ('PENDING','CANCELLED');

UPDATE ascp_sign_requests
SET prepared_handle = NULL,
    prepared_at = NULL,
    activated_at = NULL,
    mirrored_at = NULL,
    acknowledged_at = NULL,
    primary_mirror_digest = NULL,
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    next_attempt_at = now(),
    last_error = 'SIGNER_REFUSED',
    unactivated_proof = NULL,
    expired_at = NULL
WHERE state = 'REFUSED';

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
