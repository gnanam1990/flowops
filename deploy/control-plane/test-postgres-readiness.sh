#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
cd "$repo_root"

go test -race ./internal/dbreadiness ./cmd/postgres-readiness ./cmd/flowops-admin ./internal/controlapi

grant_file=deploy/control-plane/configure-runtime-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS' \
    'REVOKE CREATE ON SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public' \
    'GRANT SELECT, INSERT, UPDATE ON commands' \
    'GRANT SELECT, INSERT ON audit_events, control_events' \
    'GRANT SELECT, INSERT ON ascp_intents, ascp_financial_tombstones, ascp_policy_decisions, ascp_execution_authorizations' \
	'GRANT SELECT, INSERT ON ascp_adaptation_grants' \
	'GRANT UPDATE (state, remaining_attempts, consumed_operation_id, consumed_at)' \
	'GRANT SELECT, INSERT ON ascp_proposal_workflows, ascp_workflow_actions' \
	'GRANT UPDATE (state, approved_by, approver_role, approver_step_up_at, approver_step_up_until, approved_at' \
	'GRANT SELECT, INSERT, UPDATE ON ascp_approvals, ascp_budget_reservations' \
	'GRANT SELECT ON ascp_bearer_handles, ascp_bearer_registry' \
	'GRANT SELECT, INSERT ON ascp_sign_requests, ascp_signer_outbox' \
	'GRANT SELECT, INSERT, UPDATE ON ascp_agent_signer_bindings' \
	'GRANT SELECT, INSERT ON ascp_agent_signer_binding_history' \
	'GRANT UPDATE (state) ON ascp_bearer_handles' \
	'GRANT UPDATE (outcome)' \
	'GRANT SELECT ON ascp_payment_operations' \
	'GRANT SELECT, INSERT ON ascp_payment_attempts, ascp_chain_observations' \
	'GRANT SELECT, INSERT ON ascp_keeper_jobs' \
	'GRANT SELECT ON ascp_seller_jobs, ascp_seller_responses' \
	'GRANT SELECT ON ascp_leadership_epochs' \
	'GRANT INSERT (job_id,operation_id,organization_id,chain_id,leadership_epoch,deliver_by,method,request_url' \
	'GRANT SELECT, INSERT ON ascp_events' \
	'GRANT SELECT ON ascp_event_checkpoints' \
	'GRANT UPDATE (state, locked_transaction_hash, locked_block_number, locked_block_hash' \
	'GRANT UPDATE (state, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at)'
do
    grep -F "$required" "$grant_file" >/dev/null
done

if grep -Eq 'GRANT (ALL|DELETE|TRUNCATE|TRIGGER|REFERENCES)' "$grant_file"; then
    echo "runtime grant script contains a forbidden broad privilege" >&2
    exit 1
fi

keeper_grant_file=deploy/control-plane/configure-keeper-role.sql

keeper_contract_is_complete() {
	awk 'BEGIN {
		RS=";"
		allowed["\\SET ON_ERROR_STOP ON \\IF :{?KEEPER_ROLE} \\ELSE \\ECHO \047KEEPER_ROLE PSQL VARIABLE IS REQUIRED\047 SELECT 1/0"]=1
		allowed["\\ENDIF SELECT COALESCE((SELECT ROLCANLOGIN FROM PG_ROLES WHERE ROLNAME = :\047KEEPER_ROLE\047), FALSE) AS KEEPER_ROLE_CAN_LOGIN \\GSET \\IF :KEEPER_ROLE_CAN_LOGIN \\ELSE \\ECHO \047KEEPER_ROLE MUST EXIST AND HAVE LOGIN\047 SELECT 1/0"]=1
		allowed["\\ENDIF SELECT EXISTS ( SELECT 1 FROM PG_AUTH_MEMBERS WHERE MEMBER = (SELECT OID FROM PG_ROLES WHERE ROLNAME = :\047KEEPER_ROLE\047) OR ROLEID = (SELECT OID FROM PG_ROLES WHERE ROLNAME = :\047KEEPER_ROLE\047) ) AS KEEPER_ROLE_HAS_MEMBERSHIP \\GSET \\IF :KEEPER_ROLE_HAS_MEMBERSHIP \\ECHO \047KEEPER_ROLE MUST NOT PARTICIPATE IN ROLE MEMBERSHIPS\047 SELECT 1/0"]=1
		allowed["\\ENDIF SELECT EXISTS ( SELECT 1 FROM PG_SHDEPEND WHERE REFCLASSID = \047PG_AUTHID\047::REGCLASS AND REFOBJID = (SELECT OID FROM PG_ROLES WHERE ROLNAME = :\047KEEPER_ROLE\047) AND DEPTYPE = \047O\047 ) AS KEEPER_ROLE_OWNS_DATABASE_OBJECT \\GSET \\IF :KEEPER_ROLE_OWNS_DATABASE_OBJECT \\ECHO \047KEEPER_ROLE MUST NOT OWN DATABASE OBJECTS\047 SELECT 1/0"]=1
		allowed["\\ENDIF BEGIN"]=1
		allowed["ALTER ROLE :\"KEEPER_ROLE\" NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT"]=1
		allowed["ALTER ROLE :\"KEEPER_ROLE\" SET SEARCH_PATH = PUBLIC"]=1
		allowed["SELECT FORMAT(\047REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC\047, CURRENT_DATABASE()) \\GEXEC SELECT FORMAT(\047REVOKE TEMPORARY ON DATABASE %I FROM %I\047, CURRENT_DATABASE(), :\047KEEPER_ROLE\047) \\GEXEC REVOKE CREATE ON SCHEMA PUBLIC FROM PUBLIC"]=1
		allowed["REVOKE CREATE ON SCHEMA PUBLIC FROM :\"KEEPER_ROLE\""]=1
		allowed["GRANT USAGE ON SCHEMA PUBLIC TO :\"KEEPER_ROLE\""]=1
		allowed["REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA PUBLIC FROM PUBLIC"]=1
		allowed["REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA PUBLIC FROM :\"KEEPER_ROLE\""]=1
		allowed["REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA PUBLIC FROM PUBLIC"]=1
		allowed["REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA PUBLIC FROM :\"KEEPER_ROLE\""]=1
		allowed["REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA PUBLIC FROM PUBLIC"]=1
		allowed["REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA PUBLIC FROM :\"KEEPER_ROLE\""]=1
		allowed["ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC"]=1
		allowed["GRANT SELECT, INSERT ON PUBLIC.ASCP_KEEPER_JOBS, PUBLIC.ASCP_KEEPER_NONCE_SEQUENCES, PUBLIC.ASCP_KEEPER_TX_ATTEMPTS TO :\"KEEPER_ROLE\""]=1
		allowed["GRANT SELECT ON PUBLIC.ASCP_LEADERSHIP_EPOCHS TO :\"KEEPER_ROLE\""]=1
		allowed["GRANT UPDATE (LEASE_OWNER, LEASE_TOKEN, LEASE_EXPIRES_AT, NONCE, STATE, ATTEMPT_COUNT, CURRENT_ATTEMPT, LAST_ERROR, UPDATED_AT) ON PUBLIC.ASCP_KEEPER_JOBS TO :\"KEEPER_ROLE\""]=1
		allowed["GRANT UPDATE (NEXT_NONCE, UPDATED_AT) ON PUBLIC.ASCP_KEEPER_NONCE_SEQUENCES TO :\"KEEPER_ROLE\""]=1
		allowed["GRANT UPDATE (STATE, BROADCAST_AT, LAST_ERROR, EVIDENCE_DIGEST, OBSERVED_AT) ON PUBLIC.ASCP_KEEPER_TX_ATTEMPTS TO :\"KEEPER_ROLE\""]=1
		allowed["COMMIT"]=1
		expected_count=23
	}
	function normalize(raw, lines, count, idx, line, result) {
		count=split(raw, lines, "\n")
		result=""
		for (idx=1; idx<=count; idx++) {
			line=lines[idx]
			sub(/[[:space:]]*--.*/, "", line)
			result=result " " line
		}
		result=toupper(result)
		gsub(/[[:space:]]+/, " ", result)
		sub(/^ /, "", result)
		sub(/ $/, "", result)
		return result
	}
	{
		if ($0 ~ /\/\*|\*\//) { unsafe=1; next }
		statement=normalize($0)
		if (statement == "") next
		if (!(statement in allowed) || ++seen[statement] != 1) unsafe=1
		accepted++
	}
	END {
		exit(unsafe || accepted != expected_count)
	}' "$@"
}

if ! keeper_contract_is_complete "$keeper_grant_file"; then
	echo "keeper role contract is missing, commented, or unsafe" >&2
	exit 1
fi

if { cat "$keeper_grant_file"; printf '%s\n' 'ALTER ROLE :"keeper_role" SUPERUSER;'; } | keeper_contract_is_complete; then
	echo "keeper role checker accepted an appended SUPERUSER statement" >&2
	exit 1
fi
if sed 's/^GRANT SELECT ON public.ascp_leadership_epochs/GRANT SELECT, DELETE ON public.ascp_leadership_epochs/' "$keeper_grant_file" | keeper_contract_is_complete; then
	echo "keeper role checker accepted a substituted DELETE grant" >&2
	exit 1
fi
if sed 's/^GRANT UPDATE (next_nonce, updated_at)/GRANT UPDATE/' "$keeper_grant_file" | keeper_contract_is_complete; then
	echo "keeper role checker accepted table-wide UPDATE" >&2
	exit 1
fi
if sed 's/^GRANT USAGE ON SCHEMA public/GRANT EXECUTE ON FUNCTION public.dangerous()/' "$keeper_grant_file" | keeper_contract_is_complete; then
	echo "keeper role checker accepted routine execution" >&2
	exit 1
fi
if sed 's/^GRANT USAGE ON SCHEMA public/GRANT UPDATE\/\*hidden\*\//g' "$keeper_grant_file" | keeper_contract_is_complete; then
	echo "keeper role checker accepted a block-comment mutation" >&2
	exit 1
fi
if sed 's/^ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;/-- &/' "$keeper_grant_file" | keeper_contract_is_complete; then
	echo "keeper role checker accepted commented future-routine protection" >&2
	exit 1
fi
if sed 's/^REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC;/-- &/' "$keeper_grant_file" | keeper_contract_is_complete; then
	echo "keeper role checker accepted commented routine revocation" >&2
	exit 1
fi

asset_health_grant_file=deploy/control-plane/configure-asset-health-role.sql
for required in \
	'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT' \
	'asset_health_role must not participate in role memberships' \
	'asset_health_role must not own database objects' \
	'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC' \
	'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC' \
	'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC' \
	'ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC' \
	'GRANT SELECT, INSERT ON ascp_asset_health' \
	'GRANT UPDATE (state,epoch,evidence_digest,providers,finalized_block,observed_at,updated_at)' \
	'ascp_asset_health_observations, ascp_asset_recovery_proofs' \
	'GRANT SELECT ON ascp_payment_operations, ascp_payment_attempts, ascp_ledger_transactions'
do
	grep -F "$required" "$asset_health_grant_file" >/dev/null
done
if grep -Eiq 'GRANT[[:space:]]+(ALL|DELETE|TRUNCATE|TRIGGER|REFERENCES)([[:space:],]|$)|GRANT[[:space:]]+UPDATE[[:space:]]+ON' "$asset_health_grant_file"; then
	echo "asset health role contains a forbidden broad privilege" >&2
	exit 1
fi

rails_grant_file=deploy/control-plane/configure-rails-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS' \
	'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC' \
	'GRANT SELECT ON ascp_seller_jobs, ascp_seller_attempts, ascp_seller_responses' \
	'ascp_leadership_effects TO :"rails_role"' \
	'GRANT SELECT ON ascp_payment_operations' \
	'GRANT SELECT ON ascp_leadership_epochs' \
    'GRANT INSERT ON ascp_seller_attempts, ascp_seller_responses' \
	'GRANT INSERT (effect_id,organization_id,epoch,state,started_at)' \
	'GRANT UPDATE (state,resolved_at) ON ascp_leadership_effects' \
	'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC' \
	'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"rails_role"' \
	'GRANT EXECUTE ON FUNCTION public.ascp_current_event_head()' \
    'GRANT UPDATE (state,eligible_after,lease_owner,lease_token,lease_expires_at,attempt_count' \
    'GRANT UPDATE (state,completed_at,result_code) ON ascp_seller_attempts'
do
	grep -F "$required" "$rails_grant_file" >/dev/null
done

rails_grants_are_safe() {
	awk 'BEGIN { RS=";" }
	{
		statement=toupper($0)
		if (statement !~ /GRANT/) next
		if (statement ~ /\/\*|--/) exit 1
		gsub(/[[:space:]]+/, " ", statement)
		sub(/^ /, "", statement)
		sub(/ $/, "", statement)
		if (statement ~ /^GRANT EXECUTE/) {
			if (statement == "GRANT EXECUTE ON FUNCTION PUBLIC.ASCP_CURRENT_EVENT_HEAD() TO :\"RAILS_ROLE\"") next
			exit 1
		}
		sub(/^.*GRANT /, "", statement)
		split(statement, parts, " ON ")
		privileges=parts[1]
		gsub(/[ ]*,[ ]*/, ",", privileges)
		if (privileges ~ /(^|,)(ALL([ ]+PRIVILEGES)?|DELETE|TRUNCATE|TRIGGER|REFERENCES|UPDATE)(,|$)/) exit 1
	}' "$@"
}

if ! rails_grants_are_safe "$rails_grant_file"; then
	echo "rails grant script contains a forbidden broad privilege" >&2
	exit 1
fi

if printf '%s\n' 'GRANT SELECT, DELETE ON ascp_seller_jobs TO role;' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject a mixed DELETE grant" >&2
	exit 1
fi
if printf '%s\n' 'GRANT UPDATE ON ascp_seller_jobs TO role;' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject table-wide UPDATE" >&2
	exit 1
fi
if printf '%s\n' 'grant select, delete on ascp_seller_jobs to role;' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject lowercase DELETE" >&2
	exit 1
fi
if printf '%s\n' 'GRANT UPDATE, SELECT ON ascp_seller_jobs TO role;' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject mixed table-wide UPDATE" >&2
	exit 1
fi
if printf '%s\n' 'GRANT SELECT,UPDATE ON ascp_seller_jobs TO role;' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject no-space mixed table-wide UPDATE" >&2
	exit 1
fi
if printf '%s\n' 'GRANT UPDATE/*hidden*/ ON ascp_seller_jobs TO role;' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject a commented broad grant" >&2
	exit 1
fi
if printf '%s\n' 'GRANT EXECUTE ON FUNCTION public.dangerous() TO :"rails_role";' | rails_grants_are_safe; then
	echo "rails grant checker failed to reject unrelated function execution" >&2
	exit 1
fi
printf '%s\n' 'GRANT UPDATE (state) ON ascp_seller_jobs TO role;' | rails_grants_are_safe

event_head_migration=internal/controlapi/migrations/0019_restricted_event_head.sql
for required in \
	'SECURITY DEFINER' \
	'SET search_path = pg_catalog' \
	'FROM %1$I.ascp_events AS event' \
	'REVOKE ALL PRIVILEGES ON FUNCTION %I.ascp_current_event_head() FROM PUBLIC'
do
	grep -F "$required" "$event_head_migration" >/dev/null
done

leadership_grant_file=deploy/control-plane/configure-leadership-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT' \
    'leadership_role must exist and have LOGIN' \
    'leadership_role must not participate in role memberships' \
    'leadership_role must not own database objects' \
    'SELECT 1 FROM pg_shdepend' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC' \
    'GRANT SELECT ON ascp_leadership_epochs, ascp_leadership_events, ascp_leadership_effects' \
    'GRANT INSERT (organization_id,epoch,state,evidence_digest,actor,updated_at)' \
    'GRANT INSERT (organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)' \
    'GRANT UPDATE (epoch,state,evidence_digest,actor,updated_at)' \
    'GRANT UPDATE (state,resolved_at,resolution_actor,resolution_evidence_digest)' \
    'GRANT USAGE, SELECT ON SEQUENCE ascp_leadership_events_event_id_seq'
do
    grep -F "$required" "$leadership_grant_file" >/dev/null
done

leadership_grants_are_safe() {
    awk 'BEGIN { RS=";" }
    {
        statement=toupper($0)
        if (statement !~ /GRANT/) next
        if (statement ~ /\/\*|--/) exit 1
        gsub(/[[:space:]]+/, " ", statement)
        sub(/^ /, "", statement)
        sub(/ $/, "", statement)
        if (statement == "GRANT USAGE ON SCHEMA PUBLIC TO :\"LEADERSHIP_ROLE\"") next
        if (statement == "GRANT SELECT ON ASCP_LEADERSHIP_EPOCHS, ASCP_LEADERSHIP_EVENTS, ASCP_LEADERSHIP_EFFECTS TO :\"LEADERSHIP_ROLE\"") next
        if (statement == "GRANT INSERT (ORGANIZATION_ID,EPOCH,STATE,EVIDENCE_DIGEST,ACTOR,UPDATED_AT) ON ASCP_LEADERSHIP_EPOCHS TO :\"LEADERSHIP_ROLE\"") next
        if (statement == "GRANT INSERT (ORGANIZATION_ID,PREVIOUS_EPOCH,NEW_EPOCH,PREVIOUS_STATE,NEW_STATE,EVIDENCE_DIGEST,ACTOR,CREATED_AT) ON ASCP_LEADERSHIP_EVENTS TO :\"LEADERSHIP_ROLE\"") next
        if (statement == "GRANT UPDATE (EPOCH,STATE,EVIDENCE_DIGEST,ACTOR,UPDATED_AT) ON ASCP_LEADERSHIP_EPOCHS TO :\"LEADERSHIP_ROLE\"") next
        if (statement == "GRANT UPDATE (STATE,RESOLVED_AT,RESOLUTION_ACTOR,RESOLUTION_EVIDENCE_DIGEST) ON ASCP_LEADERSHIP_EFFECTS TO :\"LEADERSHIP_ROLE\"") next
        if (statement == "GRANT USAGE, SELECT ON SEQUENCE ASCP_LEADERSHIP_EVENTS_EVENT_ID_SEQ TO :\"LEADERSHIP_ROLE\"") next
        exit 1
    }' "$@"
}

if ! leadership_grants_are_safe "$leadership_grant_file"; then
    echo "leadership grant script contains a forbidden broad privilege" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT, DELETE ON ascp_leadership_epochs TO role;' | leadership_grants_are_safe; then
    echo "leadership grant checker failed to reject DELETE" >&2
    exit 1
fi
if printf '%s\n' 'GRANT UPDATE ON ascp_leadership_epochs TO role;' | leadership_grants_are_safe; then
    echo "leadership grant checker failed to reject table-wide UPDATE" >&2
    exit 1
fi
if printf '%s\n' 'GRANT INSERT ON ascp_leadership_epochs TO role;' | leadership_grants_are_safe; then
    echo "leadership grant checker failed to reject table-wide INSERT" >&2
    exit 1
fi
if printf '%s\n' 'GRANT UPDATE/*hidden*/ ON ascp_leadership_epochs TO role;' | leadership_grants_are_safe; then
    echo "leadership grant checker failed to reject a commented broad grant" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT ON unrelated_table TO :"leadership_role";' | leadership_grants_are_safe; then
    echo "leadership grant checker failed to reject unrelated table SELECT" >&2
    exit 1
fi
if printf '%s\n' 'GRANT USAGE, SELECT ON SEQUENCE unrelated_sequence TO :"leadership_role";' | leadership_grants_are_safe; then
    echo "leadership grant checker failed to reject unrelated sequence access" >&2
    exit 1
fi
printf '%s\n' 'GRANT UPDATE (state,resolved_at,resolution_actor,resolution_evidence_digest) ON ascp_leadership_effects TO :"leadership_role";' | leadership_grants_are_safe

checkpointer_grant_file=deploy/control-plane/configure-checkpointer-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS' \
    'GRANT SELECT ON ascp_events' \
    'GRANT SELECT, INSERT ON ascp_event_checkpoints'
do
    grep -F "$required" "$checkpointer_grant_file" >/dev/null
done

if grep -Eq 'GRANT (ALL|DELETE|UPDATE|TRUNCATE|TRIGGER|REFERENCES)' "$checkpointer_grant_file"; then
    echo "checkpointer grant script contains a forbidden broad privilege" >&2
    exit 1
fi

recovery_grant_file=deploy/control-plane/configure-recovery-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT' \
    'ALTER ROLE :"recovery_role" SET default_transaction_read_only = on' \
    'recovery_role must exist and have LOGIN' \
    'recovery_role must not participate in role memberships' \
    'recovery_role must not own database objects' \
    'ALTER ROLE :"recovery_role" SET search_path = public' \
    'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC' \
    "REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'recovery_role'" \
    'REVOKE CREATE ON SCHEMA public FROM PUBLIC' \
    'REVOKE CREATE ON SCHEMA public FROM :"recovery_role"' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"recovery_role"' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"recovery_role"' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"recovery_role"' \
    'ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC' \
    'GRANT SELECT ON public.ascp_events, public.ascp_event_checkpoints'
do
    grep -F "$required" "$recovery_grant_file" >/dev/null
done

recovery_grants_are_safe() {
	awk 'BEGIN { RS=";" }
	{
		statement=toupper($0)
		if (statement !~ /GRANT/) next
		if (statement ~ /\/\*|--/) exit 1
		gsub(/[[:space:]]+/, " ", statement)
		sub(/^ /, "", statement)
		sub(/ $/, "", statement)
		if (statement == "GRANT USAGE ON SCHEMA PUBLIC TO :\"RECOVERY_ROLE\"") next
		if (statement == "GRANT SELECT ON PUBLIC.ASCP_EVENTS, PUBLIC.ASCP_EVENT_CHECKPOINTS TO :\"RECOVERY_ROLE\"") next
		exit 1
	}' "$@"
}

if ! recovery_grants_are_safe "$recovery_grant_file"; then
    echo "recovery grant script contains a forbidden write or execution privilege" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT,' '    INSERT ON public.ascp_events TO :"recovery_role";' | recovery_grants_are_safe; then
    echo "recovery grant checker failed to reject a multiline write privilege" >&2
    exit 1
fi
if printf '%s\n' 'grant select, delete on public.ascp_events to :"recovery_role";' | recovery_grants_are_safe; then
    echo "recovery grant checker failed to reject a lowercase write privilege" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT/*hidden*/, INSERT ON public.ascp_events TO :"recovery_role";' | recovery_grants_are_safe; then
    echo "recovery grant checker failed to reject a comment-obfuscated privilege" >&2
    exit 1
fi
printf '%s\n' 'GRANT USAGE ON SCHEMA public TO :"recovery_role";' \
    'GRANT SELECT ON public.ascp_events, public.ascp_event_checkpoints TO :"recovery_role";' | recovery_grants_are_safe

verifier_migration_base=internal/controlapi/migrations/0020_ascp_verifier_runtime.sql
verifier_migration_hardening=internal/controlapi/migrations/0021_harden_ascp_verifier_runtime.sql
for required in \
    'CREATE SEQUENCE IF NOT EXISTS ascp_verdict_nonce_seq' \
    'CREATE TABLE IF NOT EXISTS ascp_verdict_decisions' \
    'CREATE TABLE IF NOT EXISTS ascp_verifier_key_observations' \
    'CREATE TABLE ascp_verifier_key_observations_v2' \
    'finalized_log_index bigint NOT NULL' \
    'CREATE TABLE IF NOT EXISTS ascp_verifier_intake_replays' \
    'reject_ascp_verifier_immutable_mutation' \
    'reject_ascp_verifier_replay_mutation' \
    'prune_ascp_verifier_intake_replays' \
    'REVOKE ALL ON FUNCTION prune_ascp_verifier_intake_replays() FROM PUBLIC' \
    'BEFORE TRUNCATE ON ascp_verdict_decisions' \
    'BEFORE TRUNCATE ON ascp_verifier_key_observations' \
    'BEFORE TRUNCATE ON ascp_verifier_key_observations_v2' \
    'BEFORE TRUNCATE ON ascp_verifier_intake_replays'
do
    grep -F "$required" "$verifier_migration_base" "$verifier_migration_hardening" >/dev/null
done

verifier_grant_file=deploy/control-plane/configure-verifier-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT' \
    'ALTER ROLE :"verifier_role" SET search_path = public' \
    'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC' \
    "REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'verifier_role'" \
    'REVOKE CREATE ON SCHEMA public FROM PUBLIC' \
    'REVOKE CREATE ON SCHEMA public FROM :"verifier_role"' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"verifier_role"' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"verifier_role"' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"verifier_role"' \
    'ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC'
do
    grep -F "$required" "$verifier_grant_file" >/dev/null
done

verifier_default_privileges_are_safe() {
    awk 'BEGIN { sql=""; found=0; unsafe=0 }
    {
        if ($0 ~ /\/\*|\*\/|--/) unsafe=1
        sql=sql "\n" $0
    }
    END {
        if (unsafe) exit 1
        count=split(sql, statements, ";")
        for (i=1; i<=count; i++) {
            statement=toupper(statements[i])
            gsub(/[[:space:]]+/, " ", statement)
            sub(/^ /, "", statement)
            sub(/ $/, "", statement)
            if (statement == "ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC") found=1
        }
        exit(found ? 0 : 1)
    }'
}
verifier_default_privileges_are_safe <"$verifier_grant_file"
if sed '/ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC/d' "$verifier_grant_file" | verifier_default_privileges_are_safe; then
    echo "verifier role checker accepted missing future-routine protection" >&2
    exit 1
fi
if printf '%s\n' '-- ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;' | verifier_default_privileges_are_safe; then
    echo "verifier role checker accepted commented future-routine protection" >&2
    exit 1
fi
if printf '%s\n' '-- ignored; ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;' | verifier_default_privileges_are_safe; then
    echo "verifier role checker accepted semicolon-split comment protection" >&2
    exit 1
fi

verifier_grants_are_safe() {
    awk 'BEGIN { RS=";" }
    {
        statement=toupper($0)
        if (statement !~ /GRANT/) next
        if (statement ~ /\/\*|--/) exit 1
        gsub(/[[:space:]]+/, " ", statement)
        sub(/^ /, "", statement)
        sub(/ $/, "", statement)
        if (statement == "GRANT USAGE ON SCHEMA PUBLIC TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT SELECT, INSERT ON PUBLIC.ASCP_VERDICT_DECISIONS TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT SELECT ON PUBLIC.ASCP_VERIFIER_KEY_OBSERVATIONS_V2 TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT INSERT ON PUBLIC.ASCP_VERIFIER_INTAKE_REPLAYS TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT USAGE, SELECT ON SEQUENCE PUBLIC.ASCP_VERDICT_NONCE_SEQ TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT EXECUTE ON FUNCTION PUBLIC.PRUNE_ASCP_VERIFIER_INTAKE_REPLAYS() TO :\"VERIFIER_ROLE\"") next
        exit 1
    }' "$@"
}

if ! verifier_grants_are_safe "$verifier_grant_file"; then
    echo "verifier grant script contains an unapproved privilege" >&2
    exit 1
fi
if printf '%s\n' 'grant select, update on public.ascp_verdict_decisions to :"verifier_role";' | verifier_grants_are_safe; then
    echo "verifier grant checker failed to reject lowercase UPDATE" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT/*hidden*/, DELETE ON public.ascp_verdict_decisions TO :"verifier_role";' | verifier_grants_are_safe; then
    echo "verifier grant checker failed to reject a comment-obfuscated privilege" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT ON public.ascp_payment_operations TO :"verifier_role";' | verifier_grants_are_safe; then
    echo "verifier grant checker failed to reject unrelated table access" >&2
    exit 1
fi
verifier_grants_are_safe "$verifier_grant_file"

bearer_grant_file=deploy/control-plane/configure-bearer-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT' \
    'ALTER ROLE :"bearer_role" SET search_path = public' \
    'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC' \
    "REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'bearer_role'" \
    'REVOKE CREATE ON SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"bearer_role"' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"bearer_role"' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"bearer_role"' \
    'ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC'
do
    grep -F "$required" "$bearer_grant_file" >/dev/null
done

bearer_grants_are_safe() {
    awk 'BEGIN { RS=";" }
    {
        statement=toupper($0)
        if (statement !~ /GRANT/) next
        if (statement ~ /\/\*|--/) exit 1
        gsub(/[[:space:]]+/, " ", statement)
        sub(/^ /, "", statement)
        sub(/ $/, "", statement)
        if (statement == "GRANT USAGE ON SCHEMA PUBLIC TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT SELECT ON PUBLIC.ASCP_SIGN_REQUESTS, PUBLIC.ASCP_SIGNER_OUTBOX, PUBLIC.ASCP_EXECUTION_AUTHORIZATIONS, PUBLIC.ASCP_BUDGET_RESERVATIONS, PUBLIC.ASCP_POLICY_DECISIONS, PUBLIC.ASCP_BEARER_REGISTRY TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT INSERT (HANDLE_ID, OPERATION_ID, PAYLOAD_HASH, DIGEST, NONCE, STATE, VALID_UNTIL, CREATED_AT) ON PUBLIC.ASCP_BEARER_HANDLES TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT INSERT (DIGEST, INSTRUMENT_TYPE, SIGNATURE_REF, NONCE, ISSUED_AT, VALID_UNTIL, SIGNER_KEY_ID, KEY_EPOCH, OPERATION_ID, AUTHORIZATION_ID, RESERVATION_ID, MODULE_ADDRESS, SAFE_ADDRESS, OUTCOME, CREATED_AT) ON PUBLIC.ASCP_BEARER_REGISTRY TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT INSERT (OPERATION_ID, ORGANIZATION_ID, AGENT_ID, AUTHORIZATION_ID, RESERVATION_ID, BEARER_DIGEST, COMMITMENT_HASH, CALL_ID, CHAIN_ID, ESCROW_CONTRACT, ASSET, BUYER, PAY_TO, AMOUNT_BASE_UNITS, SETTLE_BY, STATE, CREATED_AT, UPDATED_AT) ON PUBLIC.ASCP_PAYMENT_OPERATIONS TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT INSERT (EVENT_ID, REQUEST_ID, OPERATION_ID, KIND, PAYLOAD, STATE, CREATED_AT) ON PUBLIC.ASCP_SIGNER_OUTBOX TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT UPDATE (LEASE_OWNER, LEASE_TOKEN, LEASE_EXPIRES_AT, ATTEMPT_COUNT, NEXT_ATTEMPT_AT, LAST_ERROR, PREPARED_HANDLE, STATE, PREPARED_AT, ACTIVATED_AT, PRIMARY_MIRROR_DIGEST, MIRRORED_AT, ACKNOWLEDGED_AT, UNACTIVATED_PROOF, EXPIRED_AT) ON PUBLIC.ASCP_SIGN_REQUESTS TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT UPDATE (STATE) ON PUBLIC.ASCP_BUDGET_RESERVATIONS TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT UPDATE (PRIMARY_MIRROR_DIGEST) ON PUBLIC.ASCP_BEARER_REGISTRY TO :\"BEARER_ROLE\"") next
        if (statement == "GRANT UPDATE (STATE, ATTEMPTS, DELIVERED_AT, CANCELLED_AT, LAST_ERROR) ON PUBLIC.ASCP_SIGNER_OUTBOX TO :\"BEARER_ROLE\"") next
        exit 1
    }' "$@"
}

if ! bearer_grants_are_safe "$bearer_grant_file"; then
    echo "bearer role grant script contains an unapproved privilege" >&2
    exit 1
fi
if printf '%s\n' 'grant select, update on public.ascp_policy_decisions to :"bearer_role";' | bearer_grants_are_safe; then
    echo "bearer role checker accepted table-wide UPDATE" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT/*hidden*/, DELETE ON public.ascp_sign_requests TO :"bearer_role";' | bearer_grants_are_safe; then
    echo "bearer role checker accepted a comment-obfuscated privilege" >&2
    exit 1
fi
if printf '%s\n' 'GRANT SELECT ON public.credentials TO :"bearer_role";' | bearer_grants_are_safe; then
    echo "bearer role checker accepted unrelated credential access" >&2
    exit 1
fi

bearer_role_alters_are_safe() {
    awk 'BEGIN { RS=";" }
    {
        statement=toupper($0)
        if (statement !~ /ALTER ROLE/) next
        if (statement ~ /\/\*|--/) exit 1
        gsub(/[[:space:]]+/, " ", statement)
        sub(/^ /, "", statement)
        sub(/ $/, "", statement)
        if (statement == "ALTER ROLE :\"BEARER_ROLE\" NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT") next
        if (statement == "ALTER ROLE :\"BEARER_ROLE\" SET SEARCH_PATH = PUBLIC") next
        exit 1
    }' "$@"
}

bearer_role_alters_are_safe "$bearer_grant_file"
if { cat "$bearer_grant_file"; printf '%s\n' 'ALTER ROLE :"bearer_role" SUPERUSER;'; } | bearer_role_alters_are_safe; then
    echo "bearer role checker accepted an appended SUPERUSER statement" >&2
    exit 1
fi
if printf '%s\n' 'ALTER ROLE :"bearer_role" SET session_preload_libraries = attacker;' | bearer_role_alters_are_safe; then
    echo "bearer role checker accepted an unreviewed role setting" >&2
    exit 1
fi

if [ -n "${FLOWOPS_DATABASE_URL:-}" ]; then
    go run ./cmd/postgres-readiness sql
else
    echo "live managed PostgreSQL SQL proof: NOT RUN (FLOWOPS_DATABASE_URL unset)"
fi

if [ -n "${FLOWOPS_DB_EVIDENCE_PUBLIC_KEY_B64:-}" ] && [ -n "${FLOWOPS_DB_EVIDENCE_FILE:-}" ]; then
    go run ./cmd/postgres-readiness provider-evidence <"$FLOWOPS_DB_EVIDENCE_FILE"
else
    echo "live managed PostgreSQL provider proof: NOT RUN (public key or evidence file unset)"
fi
