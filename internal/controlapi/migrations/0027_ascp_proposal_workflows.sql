ALTER TABLE credentials ADD COLUMN IF NOT EXISTS step_up_at timestamptz;

ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_role_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_role_check CHECK (
    role IN ('OWNER','ADMIN','DEVELOPER','FINANCE','APPROVER','AUDITOR','VIEWER','AGENT',
             'ORG_ADMIN','SELLER_ADMIN','SIGNER_OPERATOR','INCIDENT_RESPONDER')
);

CREATE TABLE IF NOT EXISTS ascp_proposal_workflows (
    workflow_id text PRIMARY KEY CHECK (workflow_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES organizations(id),
    kind text NOT NULL CHECK (kind IN ('PAYOUT_CHANGE','SIGNER_CAPS','VERIFIER_GOVERNANCE','PRODUCTION_GATE','BREAK_GLASS','ROLE_ADMIN','MODULE_GOVERNANCE','DIRECTORY_CANCEL')),
    payload_hash text NOT NULL CHECK (payload_hash ~ '^0x[0-9a-f]{64}$'),
    proposed_by text NOT NULL,
    proposer_role text NOT NULL CHECK (proposer_role IN ('ORG_ADMIN','SELLER_ADMIN','SIGNER_OPERATOR')),
    proposer_step_up_at timestamptz NOT NULL,
    proposer_step_up_until timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('PROPOSED','APPROVED_PENDING_CHAIN','APPROVED','CANCELLED','EXPIRED')),
    approved_by text,
    approver_role text CHECK (approver_role IN ('ORG_ADMIN','SELLER_ADMIN','INCIDENT_RESPONDER')),
    approver_step_up_at timestamptz,
    approver_step_up_until timestamptz,
    cancelled_by text,
    proposed_at timestamptz NOT NULL,
    approved_at timestamptz,
    cancelled_at timestamptz,
    expired_at timestamptz,
    expires_at timestamptz NOT NULL CHECK (expires_at = proposed_at + interval '24 hours'),
    completion_receipt jsonb,
    completion_digest text CHECK (completion_digest ~ '^0x[0-9a-f]{64}$'),
    completed_at timestamptz,
    UNIQUE (workflow_id, organization_id),
    CHECK (proposer_step_up_at <= proposed_at AND proposed_at - proposer_step_up_at <= interval '5 minutes' AND proposer_step_up_until > proposed_at),
    CHECK (approved_by IS NULL OR approved_by <> proposed_by),
    CHECK (
      (state = 'PROPOSED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR
      (state = 'APPROVED_PENDING_CHAIN' AND approved_by IS NOT NULL AND approver_role IS NOT NULL AND approver_step_up_at <= approved_at AND approved_at - approver_step_up_at <= interval '5 minutes' AND approver_step_up_until > approved_at AND approved_at IS NOT NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR
      (state = 'APPROVED' AND approved_by IS NOT NULL AND approver_role IS NOT NULL AND approver_step_up_at <= approved_at AND approved_at - approver_step_up_at <= interval '5 minutes' AND approver_step_up_until > approved_at AND approved_at IS NOT NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND ((kind IN ('PRODUCTION_GATE','ROLE_ADMIN') AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR (kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND completion_receipt IS NOT NULL AND completion_digest IS NOT NULL AND completed_at IS NOT NULL))) OR
      (state = 'CANCELLED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NOT NULL AND cancelled_at IS NOT NULL AND expired_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR
      (state = 'EXPIRED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NOT NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS ascp_proposal_workflows_org_state_idx
ON ascp_proposal_workflows (organization_id, state, expires_at);

CREATE TABLE IF NOT EXISTS ascp_workflow_actions (
    organization_id text NOT NULL REFERENCES organizations(id),
    actor_id text NOT NULL,
    action text NOT NULL CHECK (action IN ('CREATE','APPROVE','CANCEL','COMPLETE','EXPIRE')),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    input_hash text NOT NULL CHECK (input_hash ~ '^0x[0-9a-f]{64}$'),
    workflow_id text NOT NULL,
    result_state text NOT NULL CHECK (result_state IN ('PROPOSED','APPROVED_PENDING_CHAIN','APPROVED','CANCELLED','EXPIRED')),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, actor_id, action, idempotency_key),
    FOREIGN KEY (workflow_id, organization_id) REFERENCES ascp_proposal_workflows(workflow_id, organization_id)
);

CREATE TABLE IF NOT EXISTS ascp_workflow_events (
    event_id text PRIMARY KEY CHECK (event_id ~ '^0x[0-9a-f]{64}$'),
    workflow_id text NOT NULL,
    organization_id text NOT NULL,
    actor_id text NOT NULL,
    event_kind text NOT NULL CHECK (event_kind IN ('PROPOSED','APPROVED_PENDING_CHAIN','APPROVED','CANCELLED','EXPIRED')),
    event_json jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (workflow_id, organization_id) REFERENCES ascp_proposal_workflows(workflow_id, organization_id)
);

CREATE TABLE IF NOT EXISTS ascp_workflow_outbox (
    outbox_id text PRIMARY KEY CHECK (outbox_id ~ '^0x[0-9a-f]{64}$'),
    workflow_id text NOT NULL,
    organization_id text NOT NULL,
    topic text NOT NULL CHECK (topic = 'ascp.workflow.changed'),
    payload_json jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (workflow_id, organization_id) REFERENCES ascp_proposal_workflows(workflow_id, organization_id)
);

CREATE OR REPLACE FUNCTION flowops_guard_workflow_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'proposal workflows cannot be deleted' USING ERRCODE = '55000';
    END IF;
    IF OLD.workflow_id <> NEW.workflow_id OR OLD.organization_id <> NEW.organization_id OR OLD.kind <> NEW.kind OR
       OLD.payload_hash <> NEW.payload_hash OR OLD.proposed_by <> NEW.proposed_by OR OLD.proposer_role <> NEW.proposer_role OR
       OLD.proposer_step_up_at <> NEW.proposer_step_up_at OR OLD.proposer_step_up_until <> NEW.proposer_step_up_until OR OLD.proposed_at <> NEW.proposed_at OR OLD.expires_at <> NEW.expires_at THEN
        RAISE EXCEPTION 'proposal workflow identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.state = 'PROPOSED' AND NEW.state IN ('APPROVED_PENDING_CHAIN','APPROVED','CANCELLED','EXPIRED') THEN
        RETURN NEW;
    END IF;
    IF OLD.state = 'APPROVED_PENDING_CHAIN' AND NEW.state = 'APPROVED' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'invalid proposal workflow transition' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_proposal_workflows_transition_guard ON ascp_proposal_workflows;
CREATE TRIGGER ascp_proposal_workflows_transition_guard
BEFORE UPDATE OR DELETE ON ascp_proposal_workflows
FOR EACH ROW EXECUTE FUNCTION flowops_guard_workflow_transition();

DROP TRIGGER IF EXISTS ascp_workflow_actions_immutable ON ascp_workflow_actions;
CREATE TRIGGER ascp_workflow_actions_immutable BEFORE UPDATE OR DELETE ON ascp_workflow_actions
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_events_immutable ON ascp_workflow_events;
CREATE TRIGGER ascp_workflow_events_immutable BEFORE UPDATE OR DELETE ON ascp_workflow_events
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_outbox_immutable ON ascp_workflow_outbox;
CREATE TRIGGER ascp_workflow_outbox_immutable BEFORE UPDATE OR DELETE ON ascp_workflow_outbox
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_proposal_workflows_no_truncate ON ascp_proposal_workflows;
CREATE TRIGGER ascp_proposal_workflows_no_truncate BEFORE TRUNCATE ON ascp_proposal_workflows
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_actions_no_truncate ON ascp_workflow_actions;
CREATE TRIGGER ascp_workflow_actions_no_truncate BEFORE TRUNCATE ON ascp_workflow_actions
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_events_no_truncate ON ascp_workflow_events;
CREATE TRIGGER ascp_workflow_events_no_truncate BEFORE TRUNCATE ON ascp_workflow_events
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_outbox_no_truncate ON ascp_workflow_outbox;
CREATE TRIGGER ascp_workflow_outbox_no_truncate BEFORE TRUNCATE ON ascp_workflow_outbox
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
