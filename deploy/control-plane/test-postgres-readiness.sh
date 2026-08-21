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
    'GRANT SELECT ON ascp_payment_operations' \
    'GRANT INSERT ON ascp_seller_attempts, ascp_seller_responses' \
    'GRANT UPDATE (state,eligible_after,lease_owner,lease_token,lease_expires_at,attempt_count' \
    'GRANT UPDATE (state,completed_at,result_code) ON ascp_seller_attempts'
do
	grep -F "$required" "$rails_grant_file" >/dev/null
done

rails_grants_are_safe() {
	awk 'BEGIN { RS=";" }
	/GRANT/ {
		statement=toupper($0)
		gsub(/[[:space:]]+/, " ", statement)
		sub(/^.*GRANT /, "", statement)
		split(statement, parts, " ON ")
		privileges=parts[1]
		if (privileges ~ /(^|, )(ALL([ ]+PRIVILEGES)?|DELETE|TRUNCATE|TRIGGER|REFERENCES)(,|$)/ || privileges ~ /(^|, )UPDATE$/) exit 1
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
printf '%s\n' 'GRANT UPDATE (state) ON ascp_seller_jobs TO role;' | rails_grants_are_safe

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
