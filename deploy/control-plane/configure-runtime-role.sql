\set ON_ERROR_STOP on
\if :{?runtime_role}
\else
\echo 'runtime_role psql variable is required'
\quit 2
\endif

BEGIN;

ALTER ROLE :"runtime_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"runtime_role";
GRANT USAGE ON SCHEMA public TO :"runtime_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"runtime_role";

GRANT SELECT, UPDATE ON organizations, agents TO :"runtime_role";
GRANT SELECT ON credentials, policies, sites_identity_providers,
    sites_memberships, flowops_schema_migrations TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON commands TO :"runtime_role";
GRANT SELECT, INSERT ON audit_events, control_events TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_intents, ascp_policy_decisions, ascp_execution_authorizations,
    ascp_budget_reservation_dimensions, ascp_directory_snapshots,
    ascp_directory_quote_evidence TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_adaptation_grants TO :"runtime_role";
GRANT UPDATE (state, remaining_attempts, consumed_operation_id, consumed_at)
    ON ascp_adaptation_grants TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_proposal_workflows, ascp_workflow_actions,
    ascp_workflow_events, ascp_workflow_outbox, ascp_workflow_receipt_ownership TO :"runtime_role";
GRANT UPDATE (state, approved_by, approver_role, approver_step_up_at, approver_step_up_until, approved_at,
    cancelled_by, cancelled_at, expired_at, completion_receipt, completion_digest, completed_at)
    ON ascp_proposal_workflows TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON ascp_approvals, ascp_budget_reservations,
    ascp_directory_heads TO :"runtime_role";
GRANT SELECT ON ascp_bearer_handles, ascp_bearer_registry TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_sign_requests, ascp_signer_outbox TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON ascp_agent_signer_bindings TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_agent_signer_binding_history,
    ascp_agent_signer_binding_changes TO :"runtime_role";
GRANT SELECT ON ascp_payment_operations TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_payment_attempts, ascp_chain_observations,
    ascp_ledger_transactions, ascp_ledger_postings TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_keeper_jobs TO :"runtime_role";
GRANT SELECT ON ascp_seller_jobs, ascp_seller_responses TO :"runtime_role";
GRANT SELECT ON ascp_leadership_epochs, ascp_leadership_effects TO :"runtime_role";
GRANT INSERT (job_id,operation_id,organization_id,chain_id,leadership_epoch,deliver_by,method,request_url,
    headers_json,request_body,canonical_spec_json,offer_json,payment_json,binding_json,locked_transaction_hash,
    payer,validated_chain_time,input_hash,eligible_after,created_at,updated_at)
    ON ascp_seller_jobs TO :"runtime_role";
GRANT SELECT, INSERT ON ascp_events TO :"runtime_role";
GRANT SELECT ON ascp_event_checkpoints TO :"runtime_role";
GRANT UPDATE (state) ON ascp_bearer_handles TO :"runtime_role";
GRANT UPDATE (outcome)
    ON ascp_bearer_registry TO :"runtime_role";
GRANT UPDATE (state, locked_transaction_hash, locked_block_number, locked_block_hash,
    terminal_action, terminal_transaction_hash, terminal_block_number, terminal_block_hash, updated_at)
    ON ascp_payment_operations TO :"runtime_role";
GRANT UPDATE (state, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at)
    ON ascp_payment_attempts TO :"runtime_role";

COMMIT;
