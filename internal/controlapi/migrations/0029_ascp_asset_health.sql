CREATE TABLE IF NOT EXISTS ascp_asset_health (
    chain_id bigint NOT NULL CHECK (chain_id IN (8453,84532)),
    asset text NOT NULL CHECK (asset ~ '^0x[0-9a-f]{40}$' AND asset <> '0x0000000000000000000000000000000000000000'),
    proxy_implementation text NOT NULL CHECK (proxy_implementation ~ '^0x[0-9a-f]{40}$' AND proxy_implementation <> '0x0000000000000000000000000000000000000000'),
    runtime_code_hash text NOT NULL CHECK (runtime_code_hash ~ '^0x[0-9a-f]{64}$' AND runtime_code_hash <> '0x0000000000000000000000000000000000000000000000000000000000000000'),
    quorum smallint NOT NULL CHECK (quorum BETWEEN 2 AND 5),
    state text NOT NULL CHECK (state IN ('NORMAL','TOKEN_PAUSED','ASSET_TRANSFER_BLOCKED','RECOVERING')),
    epoch bigint NOT NULL CHECK (epoch >= 0),
    evidence_digest text CHECK (evidence_digest IS NULL OR evidence_digest ~ '^0x[0-9a-f]{64}$'),
    providers jsonb CHECK (providers IS NULL OR (jsonb_typeof(providers)='array' AND jsonb_array_length(providers) BETWEEN 2 AND 5)),
    finalized_block bigint CHECK (finalized_block IS NULL OR finalized_block > 0),
    observed_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, asset),
    CHECK ((evidence_digest IS NULL) = (providers IS NULL)),
    CHECK ((finalized_block IS NULL) = (observed_at IS NULL))
);

CREATE TABLE IF NOT EXISTS ascp_asset_health_observations (
    evidence_digest text PRIMARY KEY CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    chain_id bigint NOT NULL,
    asset text NOT NULL,
    previous_state text NOT NULL CHECK (previous_state IN ('NORMAL','TOKEN_PAUSED','ASSET_TRANSFER_BLOCKED','RECOVERING')),
    observed_state text NOT NULL CHECK (observed_state IN ('NORMAL','TOKEN_PAUSED','ASSET_TRANSFER_BLOCKED')),
    resulting_state text NOT NULL CHECK (resulting_state IN ('NORMAL','TOKEN_PAUSED','ASSET_TRANSFER_BLOCKED','RECOVERING')),
    epoch bigint NOT NULL CHECK (epoch >= 0),
    providers jsonb NOT NULL CHECK (jsonb_typeof(providers)='array' AND jsonb_array_length(providers) BETWEEN 2 AND 5),
    finalized_block bigint NOT NULL CHECK (finalized_block > 0),
    observed_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    FOREIGN KEY (chain_id, asset) REFERENCES ascp_asset_health(chain_id, asset)
);

CREATE TABLE IF NOT EXISTS ascp_asset_recovery_proofs (
    evidence_digest text PRIMARY KEY CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    chain_id bigint NOT NULL,
    asset text NOT NULL,
    health_epoch bigint NOT NULL CHECK (health_epoch > 0),
    clean_evidence_digest text NOT NULL REFERENCES ascp_asset_health_observations(evidence_digest),
    clean_finalized_block bigint NOT NULL CHECK (clean_finalized_block > 0),
    reconciled_at timestamptz NOT NULL,
    pending_operations bigint NOT NULL CHECK (pending_operations = 0),
    stale_canonical_attempts bigint NOT NULL CHECK (stale_canonical_attempts = 0),
    unclassified_locks bigint NOT NULL CHECK (unclassified_locks = 0),
    recorded_at timestamptz NOT NULL,
    UNIQUE (chain_id, asset, health_epoch),
    FOREIGN KEY (chain_id, asset) REFERENCES ascp_asset_health(chain_id, asset),
    CHECK (reconciled_at <= recorded_at)
);

CREATE TABLE IF NOT EXISTS ascp_asset_reclassifications (
    evidence_digest text NOT NULL CHECK (evidence_digest ~ '^0x[0-9a-f]{64}$'),
    operation_id text NOT NULL REFERENCES ascp_payment_operations(operation_id),
    direction text NOT NULL CHECK (direction IN ('BLOCK','RECOVER')),
    original_block_evidence text,
    from_account text NOT NULL CHECK (from_account IN ('EscrowRestrictedUSDC','TokenBlockedRestrictedUSDC')),
    to_account text NOT NULL CHECK (to_account IN ('EscrowRestrictedUSDC','TokenBlockedRestrictedUSDC')),
    amount_base_units text NOT NULL CHECK (amount_base_units ~ '^[1-9][0-9]{0,77}$'),
    refund_due boolean NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (evidence_digest, operation_id, direction),
    CHECK (from_account <> to_account),
    CHECK (
      (direction='BLOCK' AND original_block_evidence IS NULL AND from_account='EscrowRestrictedUSDC' AND to_account='TokenBlockedRestrictedUSDC') OR
      (direction='RECOVER' AND original_block_evidence IS NOT NULL AND from_account='TokenBlockedRestrictedUSDC' AND to_account='EscrowRestrictedUSDC')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS ascp_asset_reclassifications_one_recovery
ON ascp_asset_reclassifications(original_block_evidence, operation_id)
WHERE direction='RECOVER';

CREATE OR REPLACE FUNCTION flowops_validate_asset_reclassification()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.direction='BLOCK' AND NOT EXISTS (
        SELECT 1 FROM ascp_asset_health_observations h
        JOIN ascp_payment_operations o ON o.operation_id=NEW.operation_id
        WHERE h.evidence_digest=NEW.evidence_digest
          AND h.resulting_state IN ('TOKEN_PAUSED','ASSET_TRANSFER_BLOCKED')
          AND h.chain_id=o.chain_id AND h.asset=o.asset
          AND NEW.amount_base_units=o.amount_base_units
    ) THEN
        RAISE EXCEPTION 'blocked reclassification requires blocking health evidence' USING ERRCODE='23514';
    END IF;
    IF NEW.direction='RECOVER' AND NOT EXISTS (
        SELECT 1 FROM ascp_asset_reclassifications b
        WHERE b.evidence_digest=NEW.original_block_evidence
          AND b.operation_id=NEW.operation_id AND b.direction='BLOCK'
          AND b.amount_base_units=NEW.amount_base_units
          AND b.refund_due=NEW.refund_due
    ) THEN
        RAISE EXCEPTION 'recovery must reverse an exact blocked reclassification' USING ERRCODE='23514';
    END IF;
    IF NEW.direction='RECOVER' AND NOT EXISTS (
        SELECT 1 FROM ascp_asset_recovery_proofs p
        JOIN ascp_payment_operations o ON o.operation_id=NEW.operation_id
        WHERE p.evidence_digest=NEW.evidence_digest
          AND p.chain_id=o.chain_id AND p.asset=o.asset
    ) THEN
        RAISE EXCEPTION 'recovery reclassification requires a durable recovery proof' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_asset_reclassifications_validate ON ascp_asset_reclassifications;
CREATE TRIGGER ascp_asset_reclassifications_validate BEFORE INSERT ON ascp_asset_reclassifications
FOR EACH ROW EXECUTE FUNCTION flowops_validate_asset_reclassification();

CREATE OR REPLACE FUNCTION flowops_guard_asset_health_binding()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'asset health cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF OLD.chain_id<>NEW.chain_id OR OLD.asset<>NEW.asset OR
       OLD.proxy_implementation<>NEW.proxy_implementation OR OLD.runtime_code_hash<>NEW.runtime_code_hash OR
       OLD.quorum<>NEW.quorum OR NEW.updated_at<OLD.updated_at OR
       NEW.epoch<OLD.epoch OR NEW.epoch>OLD.epoch+1 THEN
        RAISE EXCEPTION 'invalid asset health mutation' USING ERRCODE='55000';
    END IF;
    IF OLD.state='RECOVERING' AND NEW.state='NORMAL' THEN
        IF NEW.epoch<>OLD.epoch+1 OR NOT EXISTS (
            SELECT 1 FROM ascp_asset_recovery_proofs p
            WHERE p.evidence_digest=NEW.evidence_digest AND p.chain_id=NEW.chain_id
              AND p.asset=NEW.asset AND p.health_epoch=OLD.epoch
        ) THEN
            RAISE EXCEPTION 'NORMAL recovery requires the current durable recovery proof' USING ERRCODE='55000';
        END IF;
    ELSIF NOT EXISTS (
        SELECT 1 FROM ascp_asset_health_observations o
        WHERE o.evidence_digest=NEW.evidence_digest AND o.chain_id=NEW.chain_id AND o.asset=NEW.asset
          AND o.resulting_state=NEW.state AND o.epoch=NEW.epoch
    ) THEN
        RAISE EXCEPTION 'asset health mutation requires its exact observation' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ascp_asset_health_binding_guard ON ascp_asset_health;
CREATE TRIGGER ascp_asset_health_binding_guard BEFORE UPDATE OR DELETE ON ascp_asset_health
FOR EACH ROW EXECUTE FUNCTION flowops_guard_asset_health_binding();
DROP TRIGGER IF EXISTS ascp_asset_health_observations_immutable ON ascp_asset_health_observations;
CREATE TRIGGER ascp_asset_health_observations_immutable BEFORE UPDATE OR DELETE ON ascp_asset_health_observations
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_asset_recovery_proofs_immutable ON ascp_asset_recovery_proofs;
CREATE TRIGGER ascp_asset_recovery_proofs_immutable BEFORE UPDATE OR DELETE ON ascp_asset_recovery_proofs
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_asset_reclassifications_immutable ON ascp_asset_reclassifications;
CREATE TRIGGER ascp_asset_reclassifications_immutable BEFORE UPDATE OR DELETE ON ascp_asset_reclassifications
FOR EACH ROW EXECUTE FUNCTION flowops_reject_immutable_mutation();

DROP TRIGGER IF EXISTS ascp_asset_health_no_truncate ON ascp_asset_health;
CREATE TRIGGER ascp_asset_health_no_truncate BEFORE TRUNCATE ON ascp_asset_health
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_asset_health_observations_no_truncate ON ascp_asset_health_observations;
CREATE TRIGGER ascp_asset_health_observations_no_truncate BEFORE TRUNCATE ON ascp_asset_health_observations
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_asset_recovery_proofs_no_truncate ON ascp_asset_recovery_proofs;
CREATE TRIGGER ascp_asset_recovery_proofs_no_truncate BEFORE TRUNCATE ON ascp_asset_recovery_proofs
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();
DROP TRIGGER IF EXISTS ascp_asset_reclassifications_no_truncate ON ascp_asset_reclassifications;
CREATE TRIGGER ascp_asset_reclassifications_no_truncate BEFORE TRUNCATE ON ascp_asset_reclassifications
FOR EACH STATEMENT EXECUTE FUNCTION flowops_reject_immutable_mutation();

-- Canonical statement surface: original settlement postings plus the asset
-- health classified subledger, expressed with the same debit-positive and
-- credit-negative convention.
CREATE OR REPLACE VIEW ascp_classified_ledger_postings AS
SELECT t.organization_id,t.operation_id,t.asset,t.evidence_digest,
       'SETTLEMENT'::text AS source,p.line_number,p.account,p.amount_base_units,t.recorded_at
FROM ascp_ledger_transactions t
JOIN ascp_ledger_postings p ON p.transaction_id=t.transaction_id
UNION ALL
SELECT o.organization_id,r.operation_id,o.asset,r.evidence_digest,
       'ASSET_HEALTH'::text AS source,lines.line_number,
       CASE WHEN lines.line_number=1 THEN r.to_account ELSE r.from_account END AS account,
       CASE WHEN lines.line_number=1 THEN r.amount_base_units ELSE '-'||r.amount_base_units END AS amount_base_units,
       r.recorded_at
FROM ascp_asset_reclassifications r
JOIN ascp_payment_operations o ON o.operation_id=r.operation_id
CROSS JOIN (VALUES (1::smallint),(2::smallint)) AS lines(line_number);

CREATE OR REPLACE VIEW ascp_token_blocked_positions AS
SELECT o.organization_id,o.operation_id,o.chain_id,o.asset,o.amount_base_units,
       b.evidence_digest AS block_evidence_digest,b.recorded_at AS blocked_at,
       o.settle_by,(o.settle_by<=CURRENT_TIMESTAMP) AS refund_due
FROM ascp_asset_reclassifications b
JOIN ascp_payment_operations o ON o.operation_id=b.operation_id
WHERE b.direction='BLOCK'
  AND NOT EXISTS (
      SELECT 1 FROM ascp_asset_reclassifications r
      WHERE r.direction='RECOVER' AND r.operation_id=b.operation_id
        AND r.original_block_evidence=b.evidence_digest
  );
