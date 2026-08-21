\set ON_ERROR_STOP on
\if :{?bearer_role}
\else
\echo 'bearer_role psql variable is required'
SELECT 1/0;
\endif

SELECT COALESCE((SELECT rolcanlogin FROM pg_roles WHERE rolname = :'bearer_role'), false) AS bearer_role_can_login \gset
\if :bearer_role_can_login
\else
\echo 'bearer_role must exist and have LOGIN'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_auth_members
    WHERE member = (SELECT oid FROM pg_roles WHERE rolname = :'bearer_role')
       OR roleid = (SELECT oid FROM pg_roles WHERE rolname = :'bearer_role')
) AS bearer_role_has_membership \gset
\if :bearer_role_has_membership
\echo 'bearer_role must not participate in role memberships'
SELECT 1/0;
\endif

SELECT EXISTS (
    SELECT 1 FROM pg_shdepend
    WHERE refclassid = 'pg_authid'::regclass
      AND refobjid = (SELECT oid FROM pg_roles WHERE rolname = :'bearer_role')
      AND deptype = 'o'
) AS bearer_role_owns_database_object \gset
\if :bearer_role_owns_database_object
\echo 'bearer_role must not own database objects'
SELECT 1/0;
\endif

BEGIN;

ALTER ROLE :"bearer_role"
    NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE :"bearer_role" SET search_path = public;

SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'bearer_role') \gexec

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM :"bearer_role";
GRANT USAGE ON SCHEMA public TO :"bearer_role";

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"bearer_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"bearer_role";
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"bearer_role";
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

GRANT SELECT ON public.ascp_sign_requests, public.ascp_signer_outbox,
    public.ascp_execution_authorizations, public.ascp_budget_reservations,
    public.ascp_policy_decisions, public.ascp_bearer_registry TO :"bearer_role";
GRANT INSERT (handle_id, operation_id, payload_hash, digest, nonce, state, valid_until, created_at)
    ON public.ascp_bearer_handles TO :"bearer_role";
GRANT INSERT (digest, instrument_type, signature_ref, nonce, issued_at, valid_until,
    signer_key_id, key_epoch, operation_id, authorization_id, reservation_id,
    module_address, safe_address, outcome, created_at)
    ON public.ascp_bearer_registry TO :"bearer_role";
GRANT INSERT (operation_id, organization_id, agent_id, authorization_id, reservation_id,
    bearer_digest, commitment_hash, call_id, chain_id, escrow_contract, asset, buyer, pay_to,
    amount_base_units, settle_by, state, created_at, updated_at)
    ON public.ascp_payment_operations TO :"bearer_role";
GRANT INSERT (event_id, request_id, operation_id, kind, payload, state, created_at)
    ON public.ascp_signer_outbox TO :"bearer_role";
GRANT UPDATE (lease_owner, lease_token, lease_expires_at, attempt_count,
    next_attempt_at, last_error, prepared_handle, state, prepared_at, activated_at,
    primary_mirror_digest, mirrored_at, acknowledged_at, unactivated_proof, expired_at)
    ON public.ascp_sign_requests TO :"bearer_role";
GRANT UPDATE (state) ON public.ascp_budget_reservations TO :"bearer_role";
GRANT UPDATE (primary_mirror_digest) ON public.ascp_bearer_registry TO :"bearer_role";
GRANT UPDATE (state, attempts, delivered_at, cancelled_at, last_error)
    ON public.ascp_signer_outbox TO :"bearer_role";

COMMIT;
