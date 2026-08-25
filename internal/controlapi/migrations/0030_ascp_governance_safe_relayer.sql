CREATE OR REPLACE FUNCTION flowops_governance_observers_valid(observer_values jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN jsonb_typeof(observer_values) = 'array' AND jsonb_array_length(observer_values) BETWEEN 2 AND 5 THEN
        (SELECT count(*) = count(DISTINCT item) AND
                bool_and(jsonb_typeof(item) = 'string' AND item #>> '{}' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')
         FROM jsonb_array_elements(observer_values) AS items(item))
    ELSE false END
$$;

CREATE TABLE IF NOT EXISTS ascp_governance_relay_jobs (
    outbox_id text PRIMARY KEY CHECK (outbox_id ~ '^0x[0-9a-f]{64}$'),
    workflow_id text NOT NULL,
    organization_id text NOT NULL,
    command_json jsonb NOT NULL,
    state text NOT NULL CHECK (state IN (
        'AWAITING_SIGNATURES','READY','BROADCASTING','SUBMITTED','PENDING',
        'RETRYABLE_EXACT','REAPPROVAL_REQUIRED','FINALIZED_OBSERVED'
    )),
    prepared_json jsonb,
    artifact_handle text,
    authorization_key text,
    authorization_hash text,
    outer_json jsonb,
    last_outcome_json jsonb,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 10),
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (workflow_id, organization_id),
    FOREIGN KEY (outbox_id) REFERENCES ascp_workflow_outbox(outbox_id),
    FOREIGN KEY (workflow_id, organization_id) REFERENCES ascp_proposal_workflows(workflow_id, organization_id),
    CHECK (jsonb_typeof(command_json) = 'object'),
    CHECK (prepared_json IS NULL OR jsonb_typeof(prepared_json) = 'object'),
    CHECK (outer_json IS NULL OR jsonb_typeof(outer_json) = 'object'),
    CHECK (last_outcome_json IS NULL OR jsonb_typeof(last_outcome_json) = 'object'),
    CHECK (artifact_handle IS NULL OR artifact_handle ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    CHECK (authorization_key IS NULL OR authorization_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    CHECK (authorization_hash IS NULL OR authorization_hash ~ '^0x[0-9a-f]{64}$'),
    CHECK ((lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL) OR
           (lease_owner IS NOT NULL AND lease_token ~ '^[0-9a-f]{32}$' AND lease_expires_at IS NOT NULL)),
    CHECK (
      (state = 'AWAITING_SIGNATURES' AND prepared_json IS NULL AND artifact_handle IS NULL AND
       authorization_key IS NULL AND authorization_hash IS NULL AND outer_json IS NULL AND
       last_outcome_json IS NULL AND attempt_count = 0) OR
      (state = 'READY' AND prepared_json IS NOT NULL AND artifact_handle IS NOT NULL AND
       authorization_key IS NOT NULL AND authorization_hash IS NOT NULL AND outer_json IS NULL AND
       last_outcome_json IS NULL AND attempt_count = 0) OR
      (state = 'BROADCASTING' AND prepared_json IS NOT NULL AND artifact_handle IS NOT NULL AND
       authorization_key IS NOT NULL AND authorization_hash IS NOT NULL AND outer_json IS NOT NULL) OR
      (state = 'SUBMITTED' AND
       prepared_json IS NOT NULL AND artifact_handle IS NOT NULL AND authorization_key IS NOT NULL AND
       authorization_hash IS NOT NULL AND outer_json IS NOT NULL AND attempt_count > 0) OR
      (state IN ('PENDING','RETRYABLE_EXACT','FINALIZED_OBSERVED') AND
       prepared_json IS NOT NULL AND artifact_handle IS NOT NULL AND authorization_key IS NOT NULL AND
       authorization_hash IS NOT NULL AND outer_json IS NOT NULL AND last_outcome_json IS NOT NULL AND attempt_count > 0) OR
      (state = 'REAPPROVAL_REQUIRED' AND prepared_json IS NOT NULL AND artifact_handle IS NOT NULL AND
       authorization_key IS NOT NULL AND authorization_hash IS NOT NULL AND
       ((attempt_count = 0 AND ((outer_json IS NULL AND last_outcome_json IS NULL) OR
                                (outer_json IS NOT NULL AND last_outcome_json IS NOT NULL))) OR
        (attempt_count > 0 AND outer_json IS NOT NULL AND last_outcome_json IS NOT NULL)))
    )
);

CREATE INDEX IF NOT EXISTS ascp_governance_relay_jobs_claim_idx
ON ascp_governance_relay_jobs (state, updated_at, workflow_id);

CREATE TABLE IF NOT EXISTS ascp_governance_relay_authorizations (
    organization_id text NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    input_hash text NOT NULL CHECK (input_hash ~ '^0x[0-9a-f]{64}$'),
    workflow_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, idempotency_key),
    FOREIGN KEY (workflow_id, organization_id) REFERENCES ascp_proposal_workflows(workflow_id, organization_id)
);

CREATE TABLE IF NOT EXISTS ascp_workflow_safe_retry_proofs (
    proof_id text PRIMARY KEY CHECK (proof_id ~ '^0x[0-9a-f]{64}$'),
    workflow_id text NOT NULL,
    organization_id text NOT NULL,
    previous_transaction_hash text NOT NULL CHECK (previous_transaction_hash ~ '^0x[0-9a-f]{64}$'),
    retry_transaction_hash text NOT NULL CHECK (retry_transaction_hash ~ '^0x[0-9a-f]{64}$'),
    outcome text NOT NULL CHECK (outcome IN ('DROPPED','REORGED')),
    previous_canonical boolean NOT NULL CHECK (previous_canonical = false),
    safe_address text NOT NULL CHECK (safe_address ~ '^0x[0-9a-f]{40}$' AND safe_address <> '0x0000000000000000000000000000000000000000'),
    safe_nonce numeric(20,0) NOT NULL CHECK (safe_nonce >= 0),
    safe_tx_hash text NOT NULL CHECK (safe_tx_hash ~ '^0x[0-9a-f]{64}$'),
    exec_calldata_hash text NOT NULL CHECK (exec_calldata_hash ~ '^0x[0-9a-f]{64}$'),
    verified_payload_hash text NOT NULL CHECK (verified_payload_hash ~ '^0x[0-9a-f]{64}$'),
    observers jsonb NOT NULL CHECK (flowops_governance_observers_valid(observers)),
    evidence_digest text NOT NULL CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    evidence_json jsonb NOT NULL CHECK (jsonb_typeof(evidence_json) = 'object'),
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (workflow_id, previous_transaction_hash, retry_transaction_hash, evidence_digest),
    FOREIGN KEY (workflow_id, organization_id) REFERENCES ascp_proposal_workflows(workflow_id, organization_id),
    CHECK ((evidence_json->>'workflowId' = workflow_id AND
            evidence_json->>'previousTransactionHash' = previous_transaction_hash AND
            evidence_json->>'retryTransactionHash' = retry_transaction_hash AND
            evidence_json->>'outcome' = outcome AND
            (evidence_json->>'previousCanonical')::boolean = previous_canonical AND
            evidence_json->>'safeAddress' = safe_address AND
            (evidence_json->>'safeNonce')::numeric = safe_nonce AND
            evidence_json->>'safeTxHash' = safe_tx_hash AND
            evidence_json->>'execCalldataHash' = exec_calldata_hash AND
            evidence_json->>'verifiedPayloadHash' = verified_payload_hash AND
            evidence_json->'observers' = observers AND
            evidence_json->>'evidenceDigest' = evidence_digest AND
            (evidence_json->>'observedAt')::bigint = extract(epoch FROM observed_at)::bigint) IS TRUE)
);

ALTER TABLE ascp_workflow_actions DROP CONSTRAINT IF EXISTS ascp_workflow_actions_action_v2_check;
ALTER TABLE ascp_workflow_actions DROP CONSTRAINT IF EXISTS ascp_workflow_actions_action_v3_check;
ALTER TABLE ascp_workflow_actions
    ADD CONSTRAINT ascp_workflow_actions_action_v3_check CHECK (action IN (
        'CREATE','APPROVE','CANCEL','COMPLETE','EXPIRE','SUBMIT','SUBMIT_RECOVERED','SUBMIT_PROVEN_RETRY','CONFIRM','FINALIZE',
        'FAIL_REVERTED','FAIL_REORGED','FAIL_TIMED_OUT','FAIL_REQUIRES_REAPPROVAL',
        'MIGRATE_EXPIRE','MIGRATE_REAPPROVAL'
    ));

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
    IF OLD.state = 'APPROVED_PENDING_CHAIN' AND NEW.state = 'SUBMITTED' AND (
        EXISTS (
            SELECT 1 FROM ascp_governance_relay_jobs relay
            WHERE relay.workflow_id = OLD.workflow_id AND relay.organization_id = OLD.organization_id
              AND relay.state = 'BROADCASTING' AND relay.attempt_count = 0
              AND relay.outer_json->>'transactionHash' = NEW.submission_transaction_hash
              AND relay.command_json->>'payloadHash' = OLD.payload_hash
              AND relay.prepared_json->>'payloadHash' = OLD.payload_hash
              AND relay.prepared_json->>'safeTxHash' = relay.outer_json->>'safeTxHash'
              AND relay.prepared_json->>'execCalldataHash' = relay.outer_json->>'execCalldataHash'
        ) OR EXISTS (
            SELECT 1 FROM ascp_workflow_receipt_ownership receipt
            WHERE receipt.workflow_id = OLD.workflow_id AND receipt.organization_id = OLD.organization_id
              AND receipt.chain_id = OLD.chain_id AND receipt.transaction_hash = NEW.submission_transaction_hash
        )
    ) THEN RETURN NEW; END IF;
    IF OLD.state = 'APPROVED_PENDING_CHAIN' AND NEW.state IN ('TIMED_OUT','REQUIRES_REAPPROVAL') THEN RETURN NEW; END IF;
    IF OLD.state = 'SUBMITTED' AND NEW.state IN ('REVERTED','REORGED','TIMED_OUT','REQUIRES_REAPPROVAL') AND
       NEW.submission_transaction_hash = OLD.submission_transaction_hash THEN RETURN NEW; END IF;
    IF OLD.state = 'SUBMITTED' AND NEW.state = 'CONFIRMED' AND (
       NEW.submission_transaction_hash = OLD.submission_transaction_hash OR EXISTS (
           SELECT 1 FROM ascp_workflow_receipt_ownership receipt
           WHERE receipt.workflow_id = OLD.workflow_id AND receipt.organization_id = OLD.organization_id
             AND receipt.chain_id = OLD.chain_id AND receipt.transaction_hash = NEW.submission_transaction_hash
             AND EXISTS (SELECT 1 FROM ascp_workflow_safe_retry_proofs proof
                         WHERE proof.workflow_id = OLD.workflow_id AND proof.organization_id = OLD.organization_id
                           AND (proof.previous_transaction_hash = NEW.submission_transaction_hash OR
                                proof.retry_transaction_hash = NEW.submission_transaction_hash))
       )
    ) THEN RETURN NEW; END IF;
    IF OLD.state = 'CONFIRMED' AND NEW.state IN ('REVERTED','REORGED','REQUIRES_REAPPROVAL') AND
       NEW.submission_transaction_hash = OLD.submission_transaction_hash THEN RETURN NEW; END IF;
    IF OLD.state = 'CONFIRMED' AND NEW.state = 'FINALIZED' AND (
       NEW.submission_transaction_hash = OLD.submission_transaction_hash OR EXISTS (
           SELECT 1 FROM ascp_workflow_receipt_ownership receipt
           WHERE receipt.workflow_id = OLD.workflow_id AND receipt.organization_id = OLD.organization_id
             AND receipt.chain_id = OLD.chain_id AND receipt.transaction_hash = NEW.submission_transaction_hash
             AND EXISTS (SELECT 1 FROM ascp_workflow_safe_retry_proofs proof
                         WHERE proof.workflow_id = OLD.workflow_id AND proof.organization_id = OLD.organization_id
                           AND (proof.previous_transaction_hash = NEW.submission_transaction_hash OR
                                proof.retry_transaction_hash = NEW.submission_transaction_hash))
       )
    ) THEN RETURN NEW; END IF;
    IF OLD.state IN ('REORGED','REVERTED','TIMED_OUT') AND NEW.state = 'REQUIRES_REAPPROVAL' THEN RETURN NEW; END IF;
    IF OLD.state IN ('REORGED','TIMED_OUT') AND NEW.state = 'SUBMITTED' AND EXISTS (
        SELECT 1
        FROM ascp_workflow_safe_retry_proofs proof
        JOIN ascp_governance_relay_jobs relay
          ON relay.workflow_id = proof.workflow_id AND relay.organization_id = proof.organization_id
        WHERE proof.workflow_id = OLD.workflow_id AND proof.organization_id = OLD.organization_id
          AND proof.previous_transaction_hash = OLD.submission_transaction_hash
          AND proof.retry_transaction_hash = NEW.submission_transaction_hash
          AND proof.verified_payload_hash = OLD.payload_hash
          AND proof.previous_canonical = false
          AND proof.safe_address = relay.prepared_json->'safeTransaction'->>'safe'
          AND proof.safe_nonce = (relay.prepared_json->'safeTransaction'->>'nonce')::numeric
          AND proof.safe_tx_hash = relay.prepared_json->>'safeTxHash'
          AND proof.exec_calldata_hash = relay.prepared_json->>'execCalldataHash'
          AND proof.outcome = relay.last_outcome_json->>'outcome'
          AND proof.previous_transaction_hash = relay.last_outcome_json->>'outerTransactionHash'
          AND proof.previous_canonical = (relay.last_outcome_json->>'previousCanonical')::boolean
          AND proof.safe_nonce = (relay.last_outcome_json->>'currentSafeNonce')::numeric
          AND proof.safe_tx_hash = relay.last_outcome_json->>'safeTxHash'
          AND proof.exec_calldata_hash = relay.last_outcome_json->>'execCalldataHash'
          AND proof.verified_payload_hash = relay.last_outcome_json->>'verifiedPayloadHash'
          AND proof.observers = relay.last_outcome_json->'observers'
          AND proof.evidence_digest = relay.last_outcome_json->>'evidenceDigest'
          AND proof.observed_at = date_trunc('second', (relay.last_outcome_json->>'observedAt')::timestamptz)
          AND relay.state = 'BROADCASTING' AND relay.attempt_count > 0
          AND proof.retry_transaction_hash = relay.outer_json->>'transactionHash'
          AND relay.outer_json->>'safeTxHash' = relay.prepared_json->>'safeTxHash'
          AND relay.outer_json->>'execCalldataHash' = relay.prepared_json->>'execCalldataHash'
    ) THEN RETURN NEW; END IF;
    IF OLD.state IN ('REVERTED','REORGED','TIMED_OUT','REQUIRES_REAPPROVAL') AND NEW.state = 'SUBMITTED' AND EXISTS (
        SELECT 1 FROM ascp_workflow_receipt_ownership receipt
        WHERE receipt.workflow_id = OLD.workflow_id AND receipt.organization_id = OLD.organization_id
          AND receipt.chain_id = OLD.chain_id AND receipt.transaction_hash = NEW.submission_transaction_hash
    ) AND (
        NEW.submission_transaction_hash = OLD.submission_transaction_hash OR EXISTS (
            SELECT 1 FROM ascp_governance_relay_jobs relay
            WHERE relay.workflow_id = OLD.workflow_id AND relay.organization_id = OLD.organization_id
              AND (relay.outer_json->>'transactionHash' = NEW.submission_transaction_hash OR EXISTS (
                  SELECT 1 FROM ascp_workflow_safe_retry_proofs proof
                  WHERE proof.workflow_id = OLD.workflow_id AND proof.organization_id = OLD.organization_id
                    AND (proof.previous_transaction_hash = NEW.submission_transaction_hash OR
                         proof.retry_transaction_hash = NEW.submission_transaction_hash)
              ))
        )
    ) THEN RETURN NEW; END IF;
    RAISE EXCEPTION 'invalid proposal workflow transition' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_proposal_workflows_transition_guard ON ascp_proposal_workflows;
CREATE TRIGGER ascp_proposal_workflows_transition_guard
BEFORE UPDATE OR DELETE ON ascp_proposal_workflows
FOR EACH ROW EXECUTE FUNCTION flowops_guard_workflow_transition();

CREATE OR REPLACE FUNCTION flowops_guard_governance_relay_job()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    source_topic text;
    source_payload jsonb;
    workflow_state text;
    workflow_id_value text;
    workflow_organization_id text;
    workflow_kind text;
    workflow_payload_hash text;
    workflow_chain_id numeric;
    workflow_contract_address text;
    workflow_function_selector text;
    workflow_calldata text;
    workflow_governance_action jsonb;
    workflow_approved_by text;
    workflow_approved_at timestamptz;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'governance relay jobs cannot be deleted' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        SELECT outbox.topic, outbox.payload_json, proposal.state, proposal.workflow_id,
               proposal.organization_id, proposal.kind, proposal.payload_hash, proposal.chain_id,
               proposal.contract_address, proposal.function_selector, proposal.calldata,
               proposal.governance_action, proposal.approved_by, proposal.approved_at
        INTO source_topic, source_payload, workflow_state, workflow_id_value,
             workflow_organization_id, workflow_kind, workflow_payload_hash, workflow_chain_id,
             workflow_contract_address, workflow_function_selector, workflow_calldata,
             workflow_governance_action, workflow_approved_by, workflow_approved_at
        FROM ascp_workflow_outbox outbox
        JOIN ascp_proposal_workflows proposal
          ON proposal.workflow_id = outbox.workflow_id AND proposal.organization_id = outbox.organization_id
        WHERE outbox.outbox_id = NEW.outbox_id;
        IF (FOUND AND source_topic = 'ascp.governance.execute' AND source_payload = NEW.command_json AND
           workflow_state = 'APPROVED_PENDING_CHAIN' AND NEW.workflow_id = workflow_id_value AND
           NEW.organization_id = workflow_organization_id AND
           NEW.command_json->>'version' = 'ASCP_GOVERNANCE_EXECUTION_V1' AND
           NEW.command_json->>'workflowId' = workflow_id_value AND
           NEW.command_json->>'organizationId' = workflow_organization_id AND
           NEW.command_json->>'kind' = workflow_kind AND
           NEW.command_json->>'payloadHash' = workflow_payload_hash AND
           (NEW.command_json->>'chainId')::numeric = workflow_chain_id AND
           NEW.command_json->>'contractAddress' = workflow_contract_address AND
           NEW.command_json->>'functionSelector' = workflow_function_selector AND
           NEW.command_json->>'calldata' = workflow_calldata AND
           NEW.command_json->>'value' = '0' AND NEW.command_json->>'operation' = 'CALL' AND
           NEW.command_json->'governanceAction' = workflow_governance_action AND
           NEW.command_json->>'approvedBy' = workflow_approved_by AND
           (NEW.command_json->>'approvedAt')::bigint = extract(epoch FROM workflow_approved_at)::bigint AND
           (NEW.command_json->>'executeAfter')::bigint = extract(epoch FROM workflow_approved_at)::bigint + 1 AND
           NEW.command_json->>'approvalActionHash' ~ '^0x[0-9a-f]{64}$') IS NOT TRUE THEN
            RAISE EXCEPTION 'governance relay command is not the approved workflow command' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.outbox_id <> NEW.outbox_id OR OLD.workflow_id <> NEW.workflow_id OR OLD.organization_id <> NEW.organization_id OR
       OLD.command_json <> NEW.command_json OR OLD.created_at <> NEW.created_at OR
       OLD.prepared_json IS NOT NULL AND OLD.prepared_json <> NEW.prepared_json OR
       OLD.artifact_handle IS NOT NULL AND OLD.artifact_handle <> NEW.artifact_handle OR
       OLD.authorization_key IS NOT NULL AND OLD.authorization_key <> NEW.authorization_key OR
       OLD.authorization_hash IS NOT NULL AND OLD.authorization_hash <> NEW.authorization_hash OR
       OLD.outer_json IS NOT NULL AND NOT (OLD.state = 'RETRYABLE_EXACT' AND NEW.state = 'BROADCASTING') AND
           OLD.outer_json <> NEW.outer_json OR
       OLD.last_outcome_json IS DISTINCT FROM NEW.last_outcome_json AND
           NOT (OLD.state IN ('SUBMITTED','PENDING') AND NEW.state IN ('PENDING','RETRYABLE_EXACT','REAPPROVAL_REQUIRED','FINALIZED_OBSERVED') OR
                OLD.state = 'RETRYABLE_EXACT' AND NEW.state IN ('RETRYABLE_EXACT','REAPPROVAL_REQUIRED','FINALIZED_OBSERVED') OR
                OLD.state = 'BROADCASTING' AND NEW.state = 'REAPPROVAL_REQUIRED') OR
       NEW.attempt_count <> OLD.attempt_count AND
           NOT (OLD.state = 'BROADCASTING' AND NEW.state = 'SUBMITTED' AND NEW.attempt_count = OLD.attempt_count + 1) THEN
        RAISE EXCEPTION 'governance relay identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF (NEW.state = 'RETRYABLE_EXACT' OR (OLD.state = 'BROADCASTING' AND NEW.state = 'REAPPROVAL_REQUIRED')) AND (
       NEW.last_outcome_json->>'outcome' IN ('DROPPED','REORGED') AND
       (NEW.last_outcome_json->>'previousCanonical')::boolean = false AND
       NEW.last_outcome_json->>'workflowId' = NEW.workflow_id AND
       NEW.last_outcome_json->>'outerTransactionHash' = OLD.outer_json->>'transactionHash' AND
       NEW.last_outcome_json->>'safeAddress' = NEW.prepared_json->'safeTransaction'->>'safe' AND
       (NEW.last_outcome_json->>'currentSafeNonce')::numeric = (NEW.prepared_json->'safeTransaction'->>'nonce')::numeric AND
       NEW.last_outcome_json->>'safeTxHash' = NEW.prepared_json->>'safeTxHash' AND
       NEW.last_outcome_json->>'execCalldataHash' = NEW.prepared_json->>'execCalldataHash' AND
       NEW.last_outcome_json->>'verifiedPayloadHash' = NEW.command_json->>'payloadHash' AND
       flowops_governance_observers_valid(NEW.last_outcome_json->'observers')
    ) IS NOT TRUE THEN
        RAISE EXCEPTION 'retryable governance relay evidence is not exact' USING ERRCODE = '55000';
    END IF;
    IF OLD.state = NEW.state THEN RETURN NEW; END IF;
    IF OLD.state = 'AWAITING_SIGNATURES' AND NEW.state = 'READY' THEN RETURN NEW; END IF;
    IF OLD.state IN ('READY','RETRYABLE_EXACT') AND NEW.state = 'BROADCASTING' AND NEW.attempt_count < 10 AND (
       NEW.outer_json->>'handle' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' AND
       NEW.outer_json->>'transactionHash' ~ '^0x[0-9a-f]{64}$' AND
       NEW.outer_json->>'transactionHash' <> ('0x' || repeat('0',64)) AND
       NEW.outer_json->>'safeTxHash' = NEW.prepared_json->>'safeTxHash' AND
       NEW.outer_json->>'execCalldataHash' = NEW.prepared_json->>'execCalldataHash'
    ) IS TRUE THEN RETURN NEW; END IF;
    IF OLD.state IN ('READY','RETRYABLE_EXACT') AND NEW.state = 'REAPPROVAL_REQUIRED' THEN RETURN NEW; END IF;
    IF OLD.state = 'BROADCASTING' AND NEW.state IN ('SUBMITTED','REAPPROVAL_REQUIRED') THEN RETURN NEW; END IF;
    IF OLD.state IN ('SUBMITTED','PENDING') AND NEW.state IN ('PENDING','RETRYABLE_EXACT','REAPPROVAL_REQUIRED','FINALIZED_OBSERVED') THEN RETURN NEW; END IF;
    IF OLD.state = 'RETRYABLE_EXACT' AND NEW.state = 'FINALIZED_OBSERVED' THEN RETURN NEW; END IF;
    RAISE EXCEPTION 'invalid governance relay transition' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_governance_relay_jobs_guard ON ascp_governance_relay_jobs;
CREATE TRIGGER ascp_governance_relay_jobs_guard
BEFORE INSERT OR UPDATE OR DELETE ON ascp_governance_relay_jobs
FOR EACH ROW EXECUTE FUNCTION flowops_guard_governance_relay_job();

DROP TRIGGER IF EXISTS ascp_governance_relay_jobs_no_truncate ON ascp_governance_relay_jobs;
CREATE TRIGGER ascp_governance_relay_jobs_no_truncate BEFORE TRUNCATE ON ascp_governance_relay_jobs
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_governance_relay_authorizations_immutable ON ascp_governance_relay_authorizations;
CREATE TRIGGER ascp_governance_relay_authorizations_immutable BEFORE UPDATE OR DELETE ON ascp_governance_relay_authorizations
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_governance_relay_authorizations_no_truncate ON ascp_governance_relay_authorizations;
CREATE TRIGGER ascp_governance_relay_authorizations_no_truncate BEFORE TRUNCATE ON ascp_governance_relay_authorizations
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_safe_retry_proofs_immutable ON ascp_workflow_safe_retry_proofs;
CREATE TRIGGER ascp_workflow_safe_retry_proofs_immutable BEFORE UPDATE OR DELETE ON ascp_workflow_safe_retry_proofs
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_workflow_safe_retry_proofs_no_truncate ON ascp_workflow_safe_retry_proofs;
CREATE TRIGGER ascp_workflow_safe_retry_proofs_no_truncate BEFORE TRUNCATE ON ascp_workflow_safe_retry_proofs
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
