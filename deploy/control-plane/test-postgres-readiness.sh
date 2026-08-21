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
    'GRANT SELECT, INSERT ON ascp_intents, ascp_policy_decisions, ascp_execution_authorizations' \
	'GRANT SELECT, INSERT, UPDATE ON ascp_approvals, ascp_budget_reservations' \
	'GRANT SELECT, INSERT ON ascp_bearer_handles, ascp_sign_requests' \
	'GRANT UPDATE (state) ON ascp_bearer_handles' \
	'GRANT UPDATE (prepared_handle, state, prepared_at, activated_at' \
	'GRANT UPDATE (primary_mirror_digest, outcome)' \
	'GRANT UPDATE (state, attempts, delivered_at)' \
	'GRANT SELECT, INSERT ON ascp_payment_operations, ascp_payment_attempts' \
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
for required in \
	'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS' \
    'GRANT SELECT, INSERT ON ascp_keeper_jobs, ascp_keeper_nonce_sequences' \
    'GRANT UPDATE (lease_owner, lease_token, lease_expires_at, nonce, state' \
    'GRANT UPDATE (next_nonce, updated_at)' \
    'GRANT UPDATE (state, broadcast_at, last_error, evidence_digest, observed_at)'
do
    grep -F "$required" "$keeper_grant_file" >/dev/null
done

if grep -Eq 'GRANT (ALL|DELETE|TRUNCATE|TRIGGER|REFERENCES)' "$keeper_grant_file"; then
    echo "keeper grant script contains a forbidden broad privilege" >&2
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

verifier_migration=internal/controlapi/migrations/0020_ascp_verifier_runtime.sql
for required in \
    'CREATE SEQUENCE IF NOT EXISTS ascp_verdict_nonce_seq' \
    'CREATE TABLE IF NOT EXISTS ascp_verdict_decisions' \
    'CREATE TABLE IF NOT EXISTS ascp_verifier_key_observations' \
    'CREATE TABLE IF NOT EXISTS ascp_verifier_intake_replays' \
    'reject_ascp_verifier_immutable_mutation'
do
    grep -F "$required" "$verifier_migration" >/dev/null
done

verifier_grant_file=deploy/control-plane/configure-verifier-role.sql
for required in \
    'NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS NOINHERIT' \
    'ALTER ROLE :"verifier_role" SET search_path = public' \
    'REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC' \
    "REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'verifier_role'" \
    'REVOKE CREATE ON SCHEMA public FROM PUBLIC' \
    'REVOKE CREATE ON SCHEMA public FROM :"verifier_role"' \
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"verifier_role"' \
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"verifier_role"' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM PUBLIC' \
    'REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA public FROM :"verifier_role"'
do
    grep -F "$required" "$verifier_grant_file" >/dev/null
done

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
        if (statement == "GRANT SELECT ON PUBLIC.ASCP_VERIFIER_KEY_OBSERVATIONS TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT INSERT ON PUBLIC.ASCP_VERIFIER_INTAKE_REPLAYS TO :\"VERIFIER_ROLE\"") next
        if (statement == "GRANT USAGE, SELECT ON SEQUENCE PUBLIC.ASCP_VERDICT_NONCE_SEQ TO :\"VERIFIER_ROLE\"") next
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
