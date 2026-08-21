CREATE TABLE IF NOT EXISTS ascp_payment_operations (
    operation_id text PRIMARY KEY REFERENCES ascp_intents(operation_id),
    organization_id text NOT NULL REFERENCES organizations(id),
    agent_id text NOT NULL,
    authorization_id text NOT NULL UNIQUE REFERENCES ascp_execution_authorizations(authorization_id),
    reservation_id text NOT NULL UNIQUE REFERENCES ascp_budget_reservations(reservation_id),
    bearer_digest text NOT NULL UNIQUE REFERENCES ascp_bearer_registry(digest),
    commitment_hash text NOT NULL UNIQUE CHECK (commitment_hash ~ '^0x[0-9a-f]{64}$'),
    call_id text NOT NULL UNIQUE CHECK (call_id ~ '^0x[0-9a-f]{64}$'),
    chain_id bigint NOT NULL CHECK (chain_id IN (8453,84532)),
    escrow_contract text NOT NULL CHECK (escrow_contract ~ '^0x[0-9a-f]{40}$'),
    asset text NOT NULL CHECK (asset ~ '^0x[0-9a-f]{40}$'),
    buyer text NOT NULL CHECK (buyer ~ '^0x[0-9a-f]{40}$'),
    pay_to text NOT NULL CHECK (pay_to ~ '^0x[0-9a-f]{40}$'),
    amount_base_units text NOT NULL CHECK (amount_base_units ~ '^[1-9][0-9]{0,77}$'),
    settle_by timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN (
        'AUTH_SIGNED','LOCK_SUBMITTED','LOCKED_SAFE','LOCKED_FINALIZED','RELEASED_FINALIZED',
        'REFUNDED_FINALIZED','REORGED_BACK','PENDING_CHAIN_RECOVERY','QUARANTINED'
    )),
    locked_transaction_hash text UNIQUE CHECK (locked_transaction_hash IS NULL OR locked_transaction_hash ~ '^0x[0-9a-f]{64}$'),
    locked_block_number bigint CHECK (locked_block_number IS NULL OR locked_block_number > 0),
    locked_block_hash text CHECK (locked_block_hash IS NULL OR locked_block_hash ~ '^0x[0-9a-f]{64}$'),
    terminal_action text CHECK (terminal_action IS NULL OR terminal_action IN ('RELEASE','REFUND')),
    terminal_transaction_hash text UNIQUE CHECK (terminal_transaction_hash IS NULL OR terminal_transaction_hash ~ '^0x[0-9a-f]{64}$'),
    terminal_block_number bigint CHECK (terminal_block_number IS NULL OR terminal_block_number > 0),
    terminal_block_hash text CHECK (terminal_block_hash IS NULL OR terminal_block_hash ~ '^0x[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id),
    CHECK (buyer <> pay_to),
    CHECK ((locked_block_number IS NULL) = (locked_block_hash IS NULL)),
    CHECK ((terminal_action IS NULL) = (terminal_transaction_hash IS NULL)),
    CHECK ((terminal_block_number IS NULL) = (terminal_block_hash IS NULL)),
    CHECK (state = 'AUTH_SIGNED' OR locked_transaction_hash IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS ascp_payment_operations_org_updated_idx
ON ascp_payment_operations (organization_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS ascp_payment_operations_recovery_idx
ON ascp_payment_operations (state, updated_at)
WHERE state IN ('AUTH_SIGNED','LOCK_SUBMITTED','REORGED_BACK','PENDING_CHAIN_RECOVERY','QUARANTINED');

CREATE TABLE IF NOT EXISTS ascp_payment_attempts (
    operation_id text NOT NULL REFERENCES ascp_payment_operations(operation_id),
    action text NOT NULL CHECK (action IN ('LOCK','RELEASE','REFUND')),
    transaction_hash text NOT NULL UNIQUE CHECK (transaction_hash ~ '^0x[0-9a-f]{64}$'),
    delivery_hash text CHECK (delivery_hash IS NULL OR delivery_hash ~ '^0x[0-9a-f]{64}$'),
    evidence_hash text CHECK (evidence_hash IS NULL OR evidence_hash ~ '^0x[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('SUBMITTED','CONFIRMED_SAFE','FINALIZED','REVERTED','REORGED','QUARANTINED')),
    registered_at timestamptz NOT NULL,
    resolved_at timestamptz,
    block_number bigint CHECK (block_number IS NULL OR block_number > 0),
    block_hash text CHECK (block_hash IS NULL OR block_hash ~ '^0x[0-9a-f]{64}$'),
    evidence_digest text CHECK (evidence_digest IS NULL OR evidence_digest ~ '^0x[0-9a-f]{64}$'),
    canonical_checked_at timestamptz,
    PRIMARY KEY (operation_id, action),
    CHECK ((action = 'RELEASE' AND delivery_hash IS NOT NULL AND evidence_hash IS NOT NULL) OR
           (action IN ('LOCK','REFUND') AND delivery_hash IS NULL AND evidence_hash IS NULL)),
    CHECK ((block_number IS NULL) = (block_hash IS NULL)),
    CHECK ((state = 'SUBMITTED' AND resolved_at IS NULL AND block_number IS NULL AND evidence_digest IS NULL) OR
           (state <> 'SUBMITTED' AND resolved_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS ascp_payment_attempts_pending_idx
ON ascp_payment_attempts (registered_at, operation_id)
WHERE state IN ('SUBMITTED','CONFIRMED_SAFE');

CREATE UNIQUE INDEX IF NOT EXISTS ascp_payment_attempts_one_terminal_idx
ON ascp_payment_attempts (operation_id)
WHERE action IN ('RELEASE','REFUND');

CREATE TABLE IF NOT EXISTS ascp_chain_observations (
    evidence_digest text PRIMARY KEY CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    operation_id text NOT NULL REFERENCES ascp_payment_operations(operation_id),
    action text NOT NULL CHECK (action IN ('LOCK','RELEASE','REFUND')),
    finality text NOT NULL CHECK (finality IN ('SAFE','FINALIZED','REORGED')),
    transaction_hash text NOT NULL CHECK (transaction_hash ~ '^0x[0-9a-f]{64}$'),
    block_number bigint NOT NULL CHECK (block_number > 0),
    block_hash text NOT NULL CHECK (block_hash ~ '^0x[0-9a-f]{64}$'),
    canonical_block_hash text CHECK (canonical_block_hash IS NULL OR canonical_block_hash ~ '^0x[0-9a-f]{64}$'),
    confirmed_head bigint NOT NULL CHECK (confirmed_head >= block_number),
    providers jsonb NOT NULL CHECK (jsonb_typeof(providers) = 'array' AND jsonb_array_length(providers) BETWEEN 2 AND 5),
    observed_at timestamptz NOT NULL,
    UNIQUE (operation_id, action, finality, block_hash),
    CHECK ((finality = 'REORGED') = (canonical_block_hash IS NOT NULL)),
    CHECK (canonical_block_hash IS NULL OR canonical_block_hash <> block_hash)
);

CREATE TABLE IF NOT EXISTS ascp_ledger_transactions (
    transaction_id text PRIMARY KEY CHECK (transaction_id ~ '^0x[0-9a-f]{64}$'),
    organization_id text NOT NULL REFERENCES organizations(id),
    operation_id text NOT NULL REFERENCES ascp_payment_operations(operation_id),
    kind text NOT NULL CHECK (kind IN ('LOCK_FINALIZED','RELEASE_FINALIZED','REFUND_FINALIZED','REVERSAL')),
    asset text NOT NULL CHECK (asset ~ '^0x[0-9a-f]{40}$'),
    evidence_digest text NOT NULL REFERENCES ascp_chain_observations(evidence_digest),
    reversal_of text UNIQUE REFERENCES ascp_ledger_transactions(transaction_id),
    recorded_at timestamptz NOT NULL,
    CHECK ((kind = 'REVERSAL') = (reversal_of IS NOT NULL)),
    UNIQUE (operation_id, kind, evidence_digest)
);

CREATE TABLE IF NOT EXISTS ascp_ledger_postings (
    transaction_id text NOT NULL REFERENCES ascp_ledger_transactions(transaction_id),
    line_number smallint NOT NULL CHECK (line_number BETWEEN 1 AND 2),
    account text NOT NULL CHECK (account IN (
        'WalletAvailableUSDC','EscrowRestrictedUSDC','SellerExpense',
        'TokenBlockedRestrictedUSDC','RefundReceivable','EscrowWriteOffExpense',
        'WalletETH','KeeperRelayerETH','NetworkFeeETH'
    )),
    amount_base_units text NOT NULL CHECK (amount_base_units ~ '^-?[1-9][0-9]{0,77}$'),
    PRIMARY KEY (transaction_id, line_number)
);

CREATE OR REPLACE FUNCTION flowops_reject_ascp_financial_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ASCP financial history is append-only' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS ascp_chain_observations_append_only ON ascp_chain_observations;
CREATE TRIGGER ascp_chain_observations_append_only
BEFORE UPDATE OR DELETE ON ascp_chain_observations
FOR EACH ROW EXECUTE FUNCTION flowops_reject_ascp_financial_history_mutation();

DROP TRIGGER IF EXISTS ascp_ledger_transactions_append_only ON ascp_ledger_transactions;
CREATE TRIGGER ascp_ledger_transactions_append_only
BEFORE UPDATE OR DELETE ON ascp_ledger_transactions
FOR EACH ROW EXECUTE FUNCTION flowops_reject_ascp_financial_history_mutation();

DROP TRIGGER IF EXISTS ascp_ledger_postings_append_only ON ascp_ledger_postings;
CREATE TRIGGER ascp_ledger_postings_append_only
BEFORE UPDATE OR DELETE ON ascp_ledger_postings
FOR EACH ROW EXECUTE FUNCTION flowops_reject_ascp_financial_history_mutation();

CREATE OR REPLACE FUNCTION flowops_validate_ascp_ledger_transaction()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    posting_count integer;
    posting_sum numeric;
    expected_amount numeric;
    matched_count integer;
BEGIN
    SELECT count(*), COALESCE(sum(amount_base_units::numeric), 0)
      INTO posting_count, posting_sum
      FROM ascp_ledger_postings
     WHERE transaction_id = NEW.transaction_id;
    IF posting_count <> 2 OR posting_sum <> 0 THEN
        RAISE EXCEPTION 'ASCP ledger transaction must contain exactly two balanced postings' USING ERRCODE = '23514';
    END IF;

    SELECT amount_base_units::numeric INTO expected_amount
      FROM ascp_payment_operations WHERE operation_id = NEW.operation_id;
    IF NEW.kind = 'LOCK_FINALIZED' THEN
        SELECT count(*) INTO matched_count FROM ascp_ledger_postings
         WHERE transaction_id = NEW.transaction_id AND
               ((account = 'EscrowRestrictedUSDC' AND amount_base_units::numeric = expected_amount) OR
                (account = 'WalletAvailableUSDC' AND amount_base_units::numeric = -expected_amount));
    ELSIF NEW.kind = 'RELEASE_FINALIZED' THEN
        SELECT count(*) INTO matched_count FROM ascp_ledger_postings
         WHERE transaction_id = NEW.transaction_id AND
               ((account = 'SellerExpense' AND amount_base_units::numeric = expected_amount) OR
                (account = 'EscrowRestrictedUSDC' AND amount_base_units::numeric = -expected_amount));
    ELSIF NEW.kind = 'REFUND_FINALIZED' THEN
        SELECT count(*) INTO matched_count FROM ascp_ledger_postings
         WHERE transaction_id = NEW.transaction_id AND
               ((account = 'WalletAvailableUSDC' AND amount_base_units::numeric = expected_amount) OR
                (account = 'EscrowRestrictedUSDC' AND amount_base_units::numeric = -expected_amount));
    ELSE
        SELECT count(*) INTO matched_count
          FROM ascp_ledger_postings reversal
          JOIN ascp_ledger_postings original
            ON original.transaction_id = NEW.reversal_of
           AND original.line_number = reversal.line_number
           AND original.account = reversal.account
           AND original.amount_base_units::numeric = -reversal.amount_base_units::numeric
         WHERE reversal.transaction_id = NEW.transaction_id;
    END IF;
    IF matched_count <> 2 THEN
        RAISE EXCEPTION 'ASCP ledger postings do not match their classified transaction' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_ledger_transaction_balanced ON ascp_ledger_transactions;
CREATE CONSTRAINT TRIGGER ascp_ledger_transaction_balanced
AFTER INSERT ON ascp_ledger_transactions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION flowops_validate_ascp_ledger_transaction();
