CREATE INDEX IF NOT EXISTS ascp_ledger_transactions_org_asset_recorded_idx
ON ascp_ledger_transactions (organization_id, asset, recorded_at, transaction_id);

CREATE INDEX IF NOT EXISTS ascp_payment_operations_org_asset_state_idx
ON ascp_payment_operations (organization_id, asset, state, operation_id);
