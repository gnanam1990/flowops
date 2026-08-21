CREATE TABLE IF NOT EXISTS ascp_policy_decisions (
    decision_id text PRIMARY KEY CHECK (decision_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES organizations(id),
    agent_id text NOT NULL,
    operation_id text NOT NULL UNIQUE REFERENCES ascp_intents(operation_id),
    outcome text NOT NULL CHECK (outcome IN ('DENY','REQUIRE_APPROVAL','AUTO_APPROVE')),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 128),
    policy_version text NOT NULL,
    policy_hash text NOT NULL CHECK (policy_hash ~ '^0x[0-9a-f]{64}$'),
    commitment_hash text NOT NULL CHECK (commitment_hash ~ '^0x[0-9a-f]{64}$'),
    commitment_json jsonb NOT NULL,
    review_json jsonb NOT NULL,
    review_snapshot_hash text NOT NULL CHECK (review_snapshot_hash ~ '^0x[0-9a-f]{64}$'),
    approval_id text UNIQUE REFERENCES ascp_approvals(approval_id),
    evaluated_at timestamptz NOT NULL,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id),
    CHECK ((outcome = 'REQUIRE_APPROVAL' AND approval_id IS NOT NULL) OR
           (outcome IN ('DENY','AUTO_APPROVE') AND approval_id IS NULL))
);

CREATE INDEX IF NOT EXISTS ascp_policy_decisions_org_evaluated_idx
ON ascp_policy_decisions (organization_id, evaluated_at DESC);

CREATE INDEX IF NOT EXISTS ascp_policy_decisions_approval_idx
ON ascp_policy_decisions (organization_id, approval_id)
WHERE approval_id IS NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'ascp_execution_authorizations_auto_decision_fk'
          AND conrelid = 'ascp_execution_authorizations'::regclass
    ) THEN
        ALTER TABLE ascp_execution_authorizations
            ADD CONSTRAINT ascp_execution_authorizations_auto_decision_fk
            FOREIGN KEY (auto_decision_ref) REFERENCES ascp_policy_decisions(decision_id)
            NOT VALID;
    END IF;
END;
$$;

ALTER TABLE ascp_execution_authorizations
    VALIDATE CONSTRAINT ascp_execution_authorizations_auto_decision_fk;

CREATE OR REPLACE FUNCTION flowops_reject_policy_decision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ascp_policy_decisions is append-only' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_policy_decisions_append_only ON ascp_policy_decisions;
CREATE TRIGGER ascp_policy_decisions_append_only
BEFORE UPDATE OR DELETE ON ascp_policy_decisions
FOR EACH ROW EXECUTE FUNCTION flowops_reject_policy_decision_mutation();
