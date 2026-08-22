ALTER TABLE ascp_proposal_workflows
    ADD COLUMN IF NOT EXISTS chain_id bigint,
    ADD COLUMN IF NOT EXISTS contract_address text,
    ADD COLUMN IF NOT EXISTS function_selector text,
    ADD COLUMN IF NOT EXISTS calldata text,
    ADD COLUMN IF NOT EXISTS governance_action jsonb,
    ADD COLUMN IF NOT EXISTS submission_transaction_hash text,
    ADD COLUMN IF NOT EXISTS submitted_at timestamptz,
    ADD COLUMN IF NOT EXISTS confirmed_at timestamptz,
    ADD COLUMN IF NOT EXISTS terminal_reason text,
    ADD COLUMN IF NOT EXISTS terminal_at timestamptz;

DROP TRIGGER IF EXISTS ascp_proposal_workflows_transition_guard ON ascp_proposal_workflows;

-- Replace only state-dependent checks from 0027. Identity, role, hash, TTL,
-- step-up, and proposer/approver separation checks remain independently
-- enforced.
DO $$
DECLARE constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT conname FROM pg_constraint
        WHERE conrelid = 'ascp_proposal_workflows'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%state%'
    LOOP
        EXECUTE format('ALTER TABLE ascp_proposal_workflows DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END $$;

CREATE TEMP TABLE flowops_v0029_legacy_transitions (
    workflow_id text PRIMARY KEY,
    prior_state text NOT NULL
) ON COMMIT DROP;

INSERT INTO flowops_v0029_legacy_transitions (workflow_id, prior_state)
SELECT workflow_id, state
FROM ascp_proposal_workflows
WHERE kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN')
  AND governance_action IS NULL
  AND state IN ('PROPOSED','APPROVED_PENDING_CHAIN');

-- Migrate the legacy terminal name without losing existing receipt evidence.
UPDATE ascp_proposal_workflows
SET state = 'FINALIZED',
    submission_transaction_hash = CASE WHEN completion_receipt IS NOT NULL THEN completion_receipt->>'transactionHash' END,
    submitted_at = CASE WHEN completion_receipt IS NOT NULL THEN COALESCE(completed_at, approved_at) END,
    confirmed_at = CASE WHEN completion_receipt IS NOT NULL THEN COALESCE(completed_at, approved_at) END,
    completed_at = COALESCE(completed_at, approved_at)
WHERE state = 'APPROVED';

-- A pre-0029 chain proposal has only a caller-supplied payload hash. It has no
-- server-derived action or immutable calldata and therefore cannot be safely
-- relayed after this upgrade. Preserve finalized history, but fail every live
-- legacy proposal closed and require a fresh typed proposal when approval had
-- already occurred.
UPDATE ascp_proposal_workflows workflow
SET state = CASE transition.prior_state
        WHEN 'PROPOSED' THEN 'EXPIRED'
        ELSE 'REQUIRES_REAPPROVAL'
    END,
    expired_at = CASE WHEN transition.prior_state = 'PROPOSED' THEN GREATEST(statement_timestamp(), workflow.proposed_at) ELSE NULL END,
    terminal_reason = CASE WHEN transition.prior_state = 'APPROVED_PENDING_CHAIN' THEN 'PRECONDITION_CHANGED' ELSE NULL END,
    terminal_at = CASE WHEN transition.prior_state = 'APPROVED_PENDING_CHAIN' THEN GREATEST(statement_timestamp(), workflow.approved_at) ELSE NULL END
FROM flowops_v0029_legacy_transitions transition
WHERE workflow.workflow_id = transition.workflow_id;

ALTER TABLE ascp_proposal_workflows
    ADD CONSTRAINT ascp_proposal_workflows_state_v2_check CHECK (state IN (
        'PROPOSED','APPROVED_PENDING_CHAIN','SUBMITTED','CONFIRMED','FINALIZED',
        'REVERTED','REORGED','TIMED_OUT','REQUIRES_REAPPROVAL','CANCELLED','EXPIRED'
    )),
    ADD CONSTRAINT ascp_proposal_workflows_submission_hash_v2_check CHECK (
        submission_transaction_hash IS NULL OR submission_transaction_hash ~ '^0x[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT ascp_proposal_workflows_governance_binding_v2_check CHECK (
      (kind IN ('PRODUCTION_GATE','ROLE_ADMIN') AND chain_id IS NULL AND contract_address IS NULL AND
       function_selector IS NULL AND calldata IS NULL AND governance_action IS NULL) OR
      (kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND (
        (chain_id IS NOT NULL AND contract_address IS NOT NULL AND function_selector IS NOT NULL AND
         calldata IS NOT NULL AND governance_action IS NOT NULL AND chain_id IN (8453,84532) AND
         contract_address ~ '^0x[0-9a-f]{40}$' AND
         contract_address <> '0x0000000000000000000000000000000000000000' AND
         function_selector ~ '^0x[0-9a-f]{8}$' AND calldata ~ '^0x[0-9a-f]+$' AND
         length(calldata) BETWEEN 10 AND 131082 AND length(calldata) % 2 = 0 AND
         left(calldata,10) = function_selector AND jsonb_typeof(governance_action) = 'object' AND
         governance_action @> jsonb_build_object('chainId', chain_id, 'contractAddress', contract_address) AND
         governance_action->>'type' IS NOT NULL AND
         num_nonnulls(
           governance_action->'callEscrowAddVerifier', governance_action->'callEscrowRevokeVerifier',
           governance_action->'callEscrowPause', governance_action->'spendAuthorizer',
           governance_action->'spendAllowlist', governance_action->'spendCaps',
           governance_action->'spendPause', governance_action->'spendInvalidateNonces',
           governance_action->'directoryApprove', governance_action->'directoryCancel'
         ) = 1 AND
         (governance_action->'callEscrowAddVerifier' IS NULL OR jsonb_typeof(governance_action->'callEscrowAddVerifier') = 'object') AND
         (governance_action->'callEscrowRevokeVerifier' IS NULL OR jsonb_typeof(governance_action->'callEscrowRevokeVerifier') = 'object') AND
         (governance_action->'callEscrowPause' IS NULL OR jsonb_typeof(governance_action->'callEscrowPause') = 'object') AND
         (governance_action->'spendAuthorizer' IS NULL OR jsonb_typeof(governance_action->'spendAuthorizer') = 'object') AND
         (governance_action->'spendAllowlist' IS NULL OR jsonb_typeof(governance_action->'spendAllowlist') = 'object') AND
         (governance_action->'spendCaps' IS NULL OR jsonb_typeof(governance_action->'spendCaps') = 'object') AND
         (governance_action->'spendPause' IS NULL OR jsonb_typeof(governance_action->'spendPause') = 'object') AND
         (governance_action->'spendInvalidateNonces' IS NULL OR jsonb_typeof(governance_action->'spendInvalidateNonces') = 'object') AND
         (governance_action->'directoryApprove' IS NULL OR jsonb_typeof(governance_action->'directoryApprove') = 'object') AND
         (governance_action->'directoryCancel' IS NULL OR jsonb_typeof(governance_action->'directoryCancel') = 'object') AND
         ((kind = 'PAYOUT_CHANGE' AND governance_action->>'type' = 'DIRECTORY_APPROVE') OR
          (kind = 'SIGNER_CAPS' AND governance_action->>'type' = 'SPEND_CAPS') OR
          (kind = 'VERIFIER_GOVERNANCE' AND governance_action->>'type' IN ('CALL_ESCROW_ADD_VERIFIER','CALL_ESCROW_REVOKE_VERIFIER')) OR
          (kind = 'BREAK_GLASS' AND governance_action->>'type' IN ('CALL_ESCROW_PAUSE','SPEND_PAUSE')) OR
          (kind = 'MODULE_GOVERNANCE' AND governance_action->>'type' IN ('SPEND_AUTHORIZER','SPEND_ALLOWLIST','SPEND_INVALIDATE_NONCES')) OR
          (kind = 'DIRECTORY_CANCEL' AND governance_action->>'type' = 'DIRECTORY_CANCEL'))) OR
        (chain_id IS NULL AND contract_address IS NULL AND function_selector IS NULL AND calldata IS NULL AND
         governance_action IS NULL AND state IN ('FINALIZED','REQUIRES_REAPPROVAL','CANCELLED','EXPIRED'))
      ))
    ),
    ADD CONSTRAINT ascp_proposal_workflows_terminal_reason_v2_check CHECK (
        terminal_reason IS NULL OR terminal_reason IN ('RECEIPT_REJECTED','MINED_REVERT','REORG_DETECTED','SUBMISSION_TIMEOUT','SAFE_NONCE_CONFLICT','PRECONDITION_CHANGED')
    ),
    ADD CONSTRAINT ascp_proposal_workflows_lifecycle_v2_check CHECK (
      (state = 'PROPOSED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL) OR
      (state = 'APPROVED_PENDING_CHAIN' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approver_role IS NOT NULL AND approver_step_up_at IS NOT NULL AND approver_step_up_until IS NOT NULL AND approved_at IS NOT NULL AND approver_step_up_at <= approved_at AND approved_at - approver_step_up_at <= interval '5 minutes' AND approver_step_up_until > approved_at AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NULL AND submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL) OR
      (state = 'SUBMITTED' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND submitted_at >= approved_at AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL) OR
      (state = 'CONFIRMED' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND confirmed_at IS NOT NULL AND submitted_at >= approved_at AND confirmed_at >= submitted_at AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL) OR
      (state = 'FINALIZED' AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND completed_at IS NOT NULL AND completed_at >= approved_at AND terminal_reason IS NULL AND terminal_at IS NULL AND ((kind IN ('PRODUCTION_GATE','ROLE_ADMIN') AND submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL) OR (kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND confirmed_at IS NOT NULL AND submitted_at >= approved_at AND confirmed_at >= submitted_at AND completed_at >= confirmed_at AND completion_receipt IS NOT NULL AND completion_digest IS NOT NULL AND completion_digest ~ '^0x[0-9a-f]{64}$'))) OR
      (state = 'REVERTED' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND submitted_at >= approved_at AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NOT NULL AND terminal_reason = 'MINED_REVERT' AND terminal_at IS NOT NULL AND terminal_at >= submitted_at) OR
      (state = 'REORGED' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND submitted_at >= approved_at AND (confirmed_at IS NULL OR confirmed_at >= submitted_at) AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NOT NULL AND terminal_reason = 'REORG_DETECTED' AND terminal_at IS NOT NULL AND terminal_at >= submitted_at) OR
      (state = 'TIMED_OUT' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND ((submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL) OR (submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND submitted_at >= approved_at AND confirmed_at IS NULL)) AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NOT NULL AND terminal_reason = 'SUBMISSION_TIMEOUT' AND terminal_at IS NOT NULL AND terminal_at >= approved_at) OR
      (state = 'REQUIRES_REAPPROVAL' AND kind NOT IN ('PRODUCTION_GATE','ROLE_ADMIN') AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND ((submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL) OR (submission_transaction_hash IS NOT NULL AND submitted_at IS NOT NULL AND submitted_at >= approved_at AND (confirmed_at IS NULL OR confirmed_at >= submitted_at))) AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NOT NULL AND terminal_reason IN ('RECEIPT_REJECTED','SAFE_NONCE_CONFLICT','PRECONDITION_CHANGED') AND terminal_at IS NOT NULL AND terminal_at >= approved_at) OR
      (state = 'CANCELLED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NOT NULL AND cancelled_at IS NOT NULL AND expired_at IS NULL AND submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL) OR
      (state = 'EXPIRED' AND approved_by IS NULL AND approver_role IS NULL AND approver_step_up_at IS NULL AND approver_step_up_until IS NULL AND approved_at IS NULL AND cancelled_by IS NULL AND cancelled_at IS NULL AND expired_at IS NOT NULL AND submission_transaction_hash IS NULL AND submitted_at IS NULL AND confirmed_at IS NULL AND completion_receipt IS NULL AND completion_digest IS NULL AND completed_at IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL)
    );

ALTER TABLE ascp_workflow_actions DROP CONSTRAINT IF EXISTS ascp_workflow_actions_action_check;
ALTER TABLE ascp_workflow_actions DROP CONSTRAINT IF EXISTS ascp_workflow_actions_result_state_check;
ALTER TABLE ascp_workflow_actions
    ADD CONSTRAINT ascp_workflow_actions_action_v2_check CHECK (action IN (
        'CREATE','APPROVE','CANCEL','COMPLETE','EXPIRE','SUBMIT','SUBMIT_RECOVERED','CONFIRM','FINALIZE',
        'FAIL_REVERTED','FAIL_REORGED','FAIL_TIMED_OUT','FAIL_REQUIRES_REAPPROVAL',
        'MIGRATE_EXPIRE','MIGRATE_REAPPROVAL'
    )),
    ADD CONSTRAINT ascp_workflow_actions_result_state_v2_check CHECK (result_state IN (
        'PROPOSED','APPROVED_PENDING_CHAIN','APPROVED','SUBMITTED','CONFIRMED','FINALIZED',
        'REVERTED','REORGED','TIMED_OUT','REQUIRES_REAPPROVAL','CANCELLED','EXPIRED'
    ));

ALTER TABLE ascp_workflow_events DROP CONSTRAINT IF EXISTS ascp_workflow_events_event_kind_check;
ALTER TABLE ascp_workflow_events
    ADD CONSTRAINT ascp_workflow_events_event_kind_v2_check CHECK (event_kind IN (
        'PROPOSED','APPROVED_PENDING_CHAIN','APPROVED','SUBMITTED','CONFIRMED','FINALIZED',
        'REVERTED','REORGED','TIMED_OUT','REQUIRES_REAPPROVAL','CANCELLED','EXPIRED'
    ));

ALTER TABLE ascp_workflow_outbox DROP CONSTRAINT IF EXISTS ascp_workflow_outbox_topic_check;
ALTER TABLE ascp_workflow_outbox
    ADD CONSTRAINT ascp_workflow_outbox_topic_v2_check CHECK (topic IN ('ascp.workflow.changed','ascp.governance.execute'));

-- Record the fail-closed legacy transitions after the expanded action/event
-- constraints exist. IDs are deterministic migration identifiers, not
-- evidence digests; the rows remain protected by the existing immutability
-- and no-truncate triggers.
INSERT INTO ascp_workflow_actions
    (organization_id, actor_id, action, idempotency_key, input_hash, workflow_id, result_state, created_at)
SELECT workflow.organization_id, 'SYSTEM_MIGRATION',
       CASE transition.prior_state WHEN 'PROPOSED' THEN 'MIGRATE_EXPIRE' ELSE 'MIGRATE_REAPPROVAL' END,
       'v0029:' || workflow.workflow_id,
       '0x' || md5('v0029:action:' || workflow.workflow_id || ':' || transition.prior_state) ||
               md5('v0029:action:2:' || workflow.workflow_id || ':' || transition.prior_state),
       workflow.workflow_id, workflow.state, COALESCE(workflow.expired_at, workflow.terminal_at)
FROM flowops_v0029_legacy_transitions transition
JOIN ascp_proposal_workflows workflow USING (workflow_id);

INSERT INTO ascp_workflow_events
    (event_id, workflow_id, organization_id, actor_id, event_kind, event_json, created_at)
SELECT '0x' || md5('v0029:event:' || workflow.workflow_id || ':' || transition.prior_state) ||
               md5('v0029:event:2:' || workflow.workflow_id || ':' || transition.prior_state),
       workflow.workflow_id, workflow.organization_id, 'SYSTEM_MIGRATION', workflow.state,
       to_jsonb(workflow), COALESCE(workflow.expired_at, workflow.terminal_at)
FROM flowops_v0029_legacy_transitions transition
JOIN ascp_proposal_workflows workflow USING (workflow_id);

INSERT INTO ascp_workflow_outbox
    (outbox_id, workflow_id, organization_id, topic, payload_json, created_at)
SELECT '0x' || md5('v0029:outbox:' || workflow.workflow_id || ':' || transition.prior_state) ||
               md5('v0029:outbox:2:' || workflow.workflow_id || ':' || transition.prior_state),
       workflow.workflow_id, workflow.organization_id, 'ascp.workflow.changed',
       to_jsonb(workflow), COALESCE(workflow.expired_at, workflow.terminal_at)
FROM flowops_v0029_legacy_transitions transition
JOIN ascp_proposal_workflows workflow USING (workflow_id);

CREATE OR REPLACE FUNCTION flowops_guard_workflow_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'proposal workflows cannot be deleted' USING ERRCODE = '55000';
    END IF;
    IF OLD.workflow_id <> NEW.workflow_id OR OLD.organization_id <> NEW.organization_id OR OLD.kind <> NEW.kind OR
       OLD.payload_hash <> NEW.payload_hash OR OLD.chain_id IS DISTINCT FROM NEW.chain_id OR
       OLD.contract_address IS DISTINCT FROM NEW.contract_address OR OLD.function_selector IS DISTINCT FROM NEW.function_selector OR
       OLD.calldata IS DISTINCT FROM NEW.calldata OR OLD.governance_action IS DISTINCT FROM NEW.governance_action OR
       OLD.proposed_by <> NEW.proposed_by OR OLD.proposer_role <> NEW.proposer_role OR
       OLD.proposer_step_up_at <> NEW.proposer_step_up_at OR OLD.proposer_step_up_until <> NEW.proposer_step_up_until OR OLD.proposed_at <> NEW.proposed_at OR OLD.expires_at <> NEW.expires_at THEN
        RAISE EXCEPTION 'proposal workflow identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.state = 'PROPOSED' AND NEW.state IN ('APPROVED_PENDING_CHAIN','FINALIZED','CANCELLED','EXPIRED') THEN RETURN NEW; END IF;
    IF OLD.state = 'APPROVED_PENDING_CHAIN' AND NEW.state IN ('SUBMITTED','TIMED_OUT','REQUIRES_REAPPROVAL') THEN RETURN NEW; END IF;
    IF OLD.state = 'SUBMITTED' AND NEW.state IN ('CONFIRMED','REVERTED','REORGED','TIMED_OUT','REQUIRES_REAPPROVAL') THEN RETURN NEW; END IF;
    IF OLD.state = 'CONFIRMED' AND NEW.state IN ('FINALIZED','REVERTED','REORGED','REQUIRES_REAPPROVAL') THEN RETURN NEW; END IF;
    IF OLD.state = 'REORGED' AND NEW.state = 'REQUIRES_REAPPROVAL' THEN RETURN NEW; END IF;
    IF OLD.state = 'REVERTED' AND NEW.state = 'REQUIRES_REAPPROVAL' THEN RETURN NEW; END IF;
    IF OLD.state = 'TIMED_OUT' AND NEW.state = 'REQUIRES_REAPPROVAL' THEN RETURN NEW; END IF;
    RAISE EXCEPTION 'invalid proposal workflow transition' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER ascp_proposal_workflows_transition_guard
BEFORE UPDATE OR DELETE ON ascp_proposal_workflows
FOR EACH ROW EXECUTE FUNCTION flowops_guard_workflow_transition();
