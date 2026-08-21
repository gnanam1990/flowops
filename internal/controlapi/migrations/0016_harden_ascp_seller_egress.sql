DROP INDEX IF EXISTS ascp_seller_jobs_finalize_idx;
CREATE INDEX ascp_seller_jobs_finalize_idx
ON ascp_seller_jobs (eligible_after,created_at,job_id)
WHERE state = 'RESPONSE_STORED';

CREATE OR REPLACE FUNCTION flowops_validate_seller_operation_binding()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE operation_row ascp_payment_operations%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.state IS DISTINCT FROM 'SENDING' THEN
        RETURN NEW;
    END IF;
    SELECT * INTO operation_row FROM ascp_payment_operations WHERE operation_id = NEW.operation_id FOR SHARE;
    IF operation_row.operation_id IS NULL OR operation_row.state IS DISTINCT FROM 'LOCKED_FINALIZED' OR
       operation_row.organization_id IS DISTINCT FROM NEW.organization_id OR operation_row.chain_id IS DISTINCT FROM NEW.chain_id OR
       NEW.offer_json#>>'{accepted,network}' IS DISTINCT FROM ('eip155:' || NEW.chain_id::text) OR
       operation_row.call_id IS DISTINCT FROM NEW.job_id OR operation_row.call_id IS DISTINCT FROM NEW.binding_json->>'callId' OR
       operation_row.commitment_hash IS DISTINCT FROM NEW.binding_json->>'commitmentHash' OR
       operation_row.escrow_contract IS DISTINCT FROM NEW.binding_json->>'escrowContract' OR
       operation_row.asset IS DISTINCT FROM NEW.offer_json#>>'{accepted,asset}' OR
       operation_row.pay_to IS DISTINCT FROM NEW.offer_json#>>'{accepted,payTo}' OR
       operation_row.amount_base_units IS DISTINCT FROM NEW.offer_json#>>'{accepted,amount}' OR
       operation_row.locked_transaction_hash IS DISTINCT FROM NEW.locked_transaction_hash OR operation_row.buyer IS DISTINCT FROM NEW.payer OR
       extract(epoch FROM operation_row.settle_by)::bigint <= NEW.deliver_by THEN
        RAISE EXCEPTION 'seller job does not bind an executable payment operation' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_seller_jobs_validate_operation ON ascp_seller_jobs;
CREATE TRIGGER ascp_seller_jobs_validate_operation BEFORE INSERT OR UPDATE OF state,attempt_count ON ascp_seller_jobs
FOR EACH ROW EXECUTE FUNCTION flowops_validate_seller_operation_binding();
