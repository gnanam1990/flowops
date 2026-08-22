CREATE TABLE IF NOT EXISTS ascp_workflow_receipt_ownership (
    chain_id bigint NOT NULL CHECK (chain_id IN (8453, 84532)),
    transaction_hash text NOT NULL CHECK (transaction_hash ~ '^0x[0-9a-f]{64}$'),
    log_index bigint NOT NULL CHECK (log_index >= 0),
    workflow_id text NOT NULL,
    organization_id text NOT NULL,
    completion_digest text NOT NULL CHECK (completion_digest ~ '^0x[0-9a-f]{64}$'),
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, transaction_hash, log_index),
    UNIQUE (workflow_id),
    FOREIGN KEY (workflow_id, organization_id)
        REFERENCES ascp_proposal_workflows(workflow_id, organization_id)
);

CREATE INDEX IF NOT EXISTS ascp_workflow_receipt_ownership_org_idx
ON ascp_workflow_receipt_ownership (organization_id, claimed_at);

-- Backfill any pre-migration chain completions. The primary and workflow
-- uniqueness constraints intentionally make the migration fail closed if
-- historical data already assigned one binding event twice or one workflow to
-- two events.
INSERT INTO ascp_workflow_receipt_ownership
    (chain_id, transaction_hash, log_index, workflow_id, organization_id, completion_digest, claimed_at)
SELECT
    (completion_receipt->>'chainId')::bigint,
    completion_receipt->>'transactionHash',
    (completion_receipt->>'logIndex')::bigint,
    workflow_id,
    organization_id,
    completion_digest,
    completed_at
FROM ascp_proposal_workflows
WHERE state = 'APPROVED'
  AND kind NOT IN ('PRODUCTION_GATE', 'ROLE_ADMIN')
  AND completion_receipt IS NOT NULL
  AND completion_digest IS NOT NULL
  AND completed_at IS NOT NULL;

DROP TRIGGER IF EXISTS ascp_workflow_receipt_ownership_immutable ON ascp_workflow_receipt_ownership;
CREATE TRIGGER ascp_workflow_receipt_ownership_immutable
BEFORE UPDATE OR DELETE ON ascp_workflow_receipt_ownership
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_workflow_receipt_ownership_no_truncate ON ascp_workflow_receipt_ownership;
CREATE TRIGGER ascp_workflow_receipt_ownership_no_truncate
BEFORE TRUNCATE ON ascp_workflow_receipt_ownership
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
