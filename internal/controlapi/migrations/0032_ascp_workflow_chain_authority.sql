ALTER TABLE ascp_proposal_workflows
    ADD COLUMN chain_action text;

ALTER TABLE ascp_proposal_workflows
    ADD CONSTRAINT ascp_proposal_workflows_chain_action CHECK (
        chain_action IS NULL OR chain_action IN (
            'CALL_ESCROW_ADD_VERIFIER','CALL_ESCROW_REVOKE_VERIFIER','CALL_ESCROW_PAUSE',
            'DIRECTORY_PUBLISH','DIRECTORY_CANCEL','DIRECTORY_SET_PUBLISHER','DIRECTORY_SET_PAUSER',
            'DIRECTORY_PAUSE_SELLER','DIRECTORY_UNPAUSE_SELLER',
            'DIRECTORY_REVOKE_QUOTE_KEY','DIRECTORY_UNREVOKE_QUOTE_KEY',
            'AGENT_REGISTER','AGENT_UPDATE_POLICY','AGENT_SET_STATUS','AGENT_SET_REGISTRY_ADMIN',
            'SPEND_SET_AUTHORIZER','SPEND_SET_ALLOWLIST','SPEND_SCHEDULE_CAPS','SPEND_PAUSE',
            'SPEND_INVALIDATE_NONCES','SAFE_ENABLE_MODULE','SAFE_DISABLE_MODULE',
            'SAFE_ADD_OWNER_WITH_THRESHOLD','SAFE_REMOVE_OWNER','SAFE_SWAP_OWNER','SAFE_CHANGE_THRESHOLD'
        )
    );

ALTER TABLE ascp_proposal_workflows
    ADD CONSTRAINT ascp_proposal_workflows_chain_action_kind CHECK (
        chain_action IS NULL OR
        (kind = 'VERIFIER_GOVERNANCE' AND chain_action IN (
            'CALL_ESCROW_ADD_VERIFIER','CALL_ESCROW_REVOKE_VERIFIER','CALL_ESCROW_PAUSE'
        )) OR
        (kind = 'PAYOUT_CHANGE' AND chain_action = 'DIRECTORY_PUBLISH') OR
        (kind = 'DIRECTORY_CANCEL' AND chain_action IN (
            'DIRECTORY_CANCEL','DIRECTORY_PAUSE_SELLER','DIRECTORY_UNPAUSE_SELLER',
            'DIRECTORY_REVOKE_QUOTE_KEY','DIRECTORY_UNREVOKE_QUOTE_KEY'
        )) OR
        (kind = 'ROLE_ADMIN' AND chain_action IN (
            'AGENT_REGISTER','AGENT_UPDATE_POLICY','AGENT_SET_STATUS'
        )) OR
        (kind = 'MODULE_GOVERNANCE' AND chain_action IN (
            'SPEND_SET_AUTHORIZER','SPEND_SET_ALLOWLIST','SPEND_SCHEDULE_CAPS','SPEND_PAUSE',
            'SPEND_INVALIDATE_NONCES','SAFE_ENABLE_MODULE','SAFE_DISABLE_MODULE'
        )) OR
        (kind = 'BREAK_GLASS' AND chain_action IN (
            'DIRECTORY_SET_PUBLISHER','DIRECTORY_SET_PAUSER','AGENT_SET_REGISTRY_ADMIN',
            'SAFE_ADD_OWNER_WITH_THRESHOLD','SAFE_REMOVE_OWNER','SAFE_SWAP_OWNER','SAFE_CHANGE_THRESHOLD'
        ))
    );

DO $$
DECLARE constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT conname FROM pg_constraint
        WHERE conrelid='ascp_proposal_workflows'::regclass AND contype='c'
          AND position('completion_receipt' in pg_get_constraintdef(oid)) > 0
          AND position('APPROVED_PENDING_CHAIN' in pg_get_constraintdef(oid)) > 0
    LOOP
        EXECUTE format('ALTER TABLE ascp_proposal_workflows DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END;
$$;

ALTER TABLE ascp_proposal_workflows
    ADD CONSTRAINT ascp_proposal_workflows_state_shape CHECK (
      (state = 'PROPOSED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR
      (state = 'APPROVED_PENDING_CHAIN' AND approved_by IS NOT NULL AND approver_role IS NOT NULL AND approver_step_up_at <= approved_at AND approved_at - approver_step_up_at <= interval '5 minutes' AND approver_step_up_until > approved_at AND approved_at IS NOT NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR
      (state = 'APPROVED' AND approved_by IS NOT NULL AND approver_role IS NOT NULL AND approver_step_up_at <= approved_at AND approved_at - approver_step_up_at <= interval '5 minutes' AND approver_step_up_until > approved_at AND approved_at IS NOT NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND (((kind IN ('PRODUCTION_GATE','ROLE_ADMIN') AND chain_action IS NULL) AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR ((kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') OR chain_action IS NOT NULL) AND completion_receipt IS NOT NULL AND completion_digest IS NOT NULL AND completed_at IS NOT NULL))) OR
      (state = 'CANCELLED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NOT NULL AND cancelled_at IS NOT NULL AND expired_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL) OR
      (state = 'EXPIRED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NOT NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL)
    );

CREATE OR REPLACE FUNCTION flowops_guard_workflow_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'proposal workflows cannot be deleted' USING ERRCODE = '55000';
    END IF;
    IF OLD.workflow_id <> NEW.workflow_id OR OLD.organization_id <> NEW.organization_id OR OLD.kind <> NEW.kind OR
       OLD.chain_action IS DISTINCT FROM NEW.chain_action OR OLD.payload_hash <> NEW.payload_hash OR
       OLD.proposed_by <> NEW.proposed_by OR OLD.proposer_role <> NEW.proposer_role OR
       OLD.proposer_step_up_at <> NEW.proposer_step_up_at OR OLD.proposer_step_up_until <> NEW.proposer_step_up_until OR
       OLD.proposed_at <> NEW.proposed_at OR OLD.expires_at <> NEW.expires_at THEN
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
