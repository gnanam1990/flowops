-- Preserve every reverted payment attempt while permitting one exact retry
-- during a fresh, independently observed asset-recovery window.
ALTER TABLE ascp_payment_attempts
    DROP CONSTRAINT IF EXISTS ascp_payment_attempts_pkey;

ALTER TABLE ascp_payment_attempts
    ADD CONSTRAINT ascp_payment_attempts_pkey
    PRIMARY KEY (operation_id, action, transaction_hash);

DROP INDEX IF EXISTS ascp_payment_attempts_one_terminal_idx;

CREATE UNIQUE INDEX IF NOT EXISTS ascp_payment_attempts_one_active_action_idx
ON ascp_payment_attempts (operation_id, action)
WHERE state IN ('SUBMITTED','CONFIRMED_SAFE','FINALIZED');

CREATE UNIQUE INDEX IF NOT EXISTS ascp_payment_attempts_one_active_terminal_idx
ON ascp_payment_attempts (operation_id)
WHERE action IN ('RELEASE','REFUND')
  AND state IN ('SUBMITTED','CONFIRMED_SAFE','FINALIZED');
