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
	'GRANT UPDATE (state, locked_transaction_hash, locked_block_number, locked_block_hash' \
	'GRANT UPDATE (state, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at)'
do
    grep -F "$required" "$grant_file" >/dev/null
done

if grep -Eq 'GRANT (ALL|DELETE|TRUNCATE|TRIGGER|REFERENCES)' "$grant_file"; then
    echo "runtime grant script contains a forbidden broad privilege" >&2
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
