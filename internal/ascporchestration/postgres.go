package ascporchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	"github.com/jackc/pgx/v5/pgconn"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

type operationMaterial struct {
	organizationID, agentID, customerID, agentStatus string
	organizationPaused                               bool
	quoteHash, purchaseSpecHash, directoryContract   string
	quoteJSON, purchaseSpecBytes, requestBody        []byte
	policyVersion, policyJSON                        string
}

func (s *PostgresStore) Evaluate(ctx context.Context, identity Identity, operationID string, cfg EvaluationConfig) (Decision, error) {
	if !validIdentity(identity) || !validHash(operationID) || !validHash(cfg.DecisionID) || !validHash(cfg.ApprovalID) ||
		!canonicalAddress(cfg.EscrowContract) || cfg.AcceptWindow <= 0 || cfg.SettleWindow < minimumSettleWindow {
		return Decision{}, ErrInvalidScope
	}
	for attempt := 0; attempt < 3; attempt++ {
		decision, err := s.evaluateOnce(ctx, identity, operationID, cfg)
		if uniqueViolation(err) {
			existing, replayErr := s.Decision(ctx, identity, operationID)
			if replayErr == nil {
				existing.Replayed = true
				return existing, nil
			}
			if !errors.Is(replayErr, ErrNotFound) {
				return Decision{}, replayErr
			}
		}
		if !serializationFailure(err) {
			return decision, err
		}
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
	}
	return Decision{}, errors.New("ASCP policy evaluation serialization retries exhausted")
}

func (s *PostgresStore) evaluateOnce(ctx context.Context, identity Identity, operationID string, cfg EvaluationConfig) (Decision, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Decision{}, fmt.Errorf("begin ASCP policy evaluation: %w", err)
	}
	defer tx.Rollback()
	material, err := lockOperationMaterial(ctx, tx, identity, operationID)
	if err != nil {
		return Decision{}, err
	}
	if existing, err := loadDecision(ctx, tx, identity, operationID); err == nil {
		existing.Replayed = true
		if err := tx.Commit(); err != nil {
			return Decision{}, fmt.Errorf("commit ASCP policy replay: %w", err)
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Decision{}, err
	}
	if material.policyVersion == "" || material.policyJSON == "" {
		return Decision{}, ErrPolicyUnavailable
	}

	decision, err := deriveDecision(ctx, tx, identity, operationID, material, cfg)
	if err != nil {
		return Decision{}, err
	}
	if decision.Outcome == policy.RequireApproval {
		approval := decision.Approval
		if approval == nil {
			return Decision{}, ErrStateConflict
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ascp_approvals
				(approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at)
			VALUES ($1,$2,$3,'REQUESTED',$4,to_timestamp($5),to_timestamp($6))`, approval.ApprovalID,
			approval.OrganizationID, approval.IntentID, approval.ReviewSnapshotHash, approval.RequestedAt, approval.ExpiresAt); err != nil {
			return Decision{}, fmt.Errorf("create ASCP approval from policy decision: %w", err)
		}
	}
	commitmentJSON, _ := json.Marshal(decision.Commitment)
	reviewJSON, _ := json.Marshal(decision.Review)
	var approvalID any
	if decision.Approval != nil {
		approvalID = decision.Approval.ApprovalID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_policy_decisions
			(decision_id, organization_id, agent_id, operation_id, outcome, reason, policy_version,
			 policy_hash, commitment_hash, commitment_json, review_json, review_snapshot_hash,
			 approval_id, evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,to_timestamp($14))`,
		decision.DecisionID, decision.OrganizationID, decision.AgentID, decision.OperationID,
		decision.Outcome, decision.Reason, decision.PolicyVersion, decision.PolicyHash,
		decision.CommitmentHash, commitmentJSON, reviewJSON, decision.ReviewSnapshotHash,
		approvalID, decision.EvaluatedAt); err != nil {
		return Decision{}, fmt.Errorf("persist ASCP policy decision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, fmt.Errorf("commit ASCP policy decision: %w", err)
	}
	return decision, nil
}

func lockOperationMaterial(ctx context.Context, tx *sql.Tx, identity Identity, operationID string) (operationMaterial, error) {
	var material operationMaterial
	var policyVersion sql.NullString
	var policyJSON []byte
	err := tx.QueryRowContext(ctx, `
		SELECT i.organization_id, i.actor_id, a.customer_id, a.status, o.authorizations_paused,
		       i.quote_hash, i.purchase_spec_hash, i.directory_contract, i.quote_json, i.purchase_spec_bytes, i.request_body,
		       p.version, p.config
		FROM ascp_intents i
		JOIN agents a ON a.organization_id=i.organization_id AND a.id=i.actor_id
		JOIN organizations o ON o.id=i.organization_id
		LEFT JOIN policies p ON p.organization_id=i.organization_id AND p.agent_id=i.actor_id AND p.active=true
		WHERE i.operation_id=$1 AND i.organization_id=$2 AND i.actor_id=$3
		FOR UPDATE OF i`, operationID, identity.OrganizationID, identity.AgentID).Scan(
		&material.organizationID, &material.agentID, &material.customerID, &material.agentStatus,
		&material.organizationPaused, &material.quoteHash, &material.purchaseSpecHash, &material.directoryContract,
		&material.quoteJSON, &material.purchaseSpecBytes, &material.requestBody, &policyVersion, &policyJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return operationMaterial{}, ErrNotFound
	}
	if err != nil {
		return operationMaterial{}, fmt.Errorf("lock ASCP operation for policy evaluation: %w", err)
	}
	material.policyVersion, material.policyJSON = policyVersion.String, string(policyJSON)
	return material, nil
}

func deriveDecision(ctx context.Context, tx *sql.Tx, identity Identity, operationID string, material operationMaterial, cfg EvaluationConfig) (Decision, error) {
	now := cfg.Now.UTC().Truncate(time.Second)
	var quote sellerquote.Quote
	if err := json.Unmarshal(material.quoteJSON, &quote); err != nil || quote.Validate() != nil {
		return Decision{}, errors.New("stored ASCP quote is invalid")
	}
	quoteDigest, err := quote.Digest(material.directoryContract)
	if err != nil || quoteDigest.Hex() != material.quoteHash {
		return Decision{}, errors.New("stored ASCP quote digest is invalid")
	}
	spec, err := purchasespec.ValidatePersisted(material.purchaseSpecBytes, material.requestBody)
	if err != nil || spec.OrgID != identity.OrganizationID || spec.AgentID != identity.AgentID ||
		purchasespec.Hash(material.purchaseSpecBytes) != material.purchaseSpecHash || quote.PurchaseSpecHash != material.purchaseSpecHash {
		return Decision{}, errors.New("stored ASCP purchase binding is invalid")
	}
	var policyConfig policy.Config
	if err := json.Unmarshal([]byte(material.policyJSON), &policyConfig); err != nil || policyConfig.Version != material.policyVersion {
		return Decision{}, errors.New("stored active ASCP policy is invalid")
	}
	engine, err := policy.Compile(policyConfig)
	if err != nil {
		return Decision{}, fmt.Errorf("compile active ASCP policy: %w", err)
	}
	policyHash, err := policy.ConfigHash(policyConfig)
	if err != nil {
		return Decision{}, err
	}
	orgDomain, err := OrgDomain(identity.OrganizationID)
	if err != nil {
		return Decision{}, err
	}
	acceptBy, deliverBy, settleBy, err := DeliveryDeadlines(now, quote.DeclaredWorkTime, quote.VerificationBudgetSeconds, cfg.AcceptWindow, cfg.SettleWindow)
	if err != nil {
		return Decision{}, err
	}
	commitment := executioncommitment.Commitment{
		OrgDomain: orgDomain, OperationID: operationID, Rail: executioncommitment.RailEscrow,
		SchemeVersion: quote.SchemeVersion, Protection: executioncommitment.ProtectionEscrow,
		EscrowContract: cfg.EscrowContract, PurchaseSpecHash: quote.PurchaseSpecHash,
		QuoteHash: material.quoteHash, VerificationSpecHash: quote.VerificationSpecHash,
		DeclaredWorkTime: quote.DeclaredWorkTime, VerificationBudgetSeconds: quote.VerificationBudgetSeconds,
		DirectoryVersion: quote.DirectoryVersion, SellerID: quote.SellerID, ResourceID: quote.ResourceID,
		PayTo: quote.PayTo, AckAuthority: quote.AckAuthority, Amount: quote.AmountBaseUnits,
		ChainID: quote.ChainID, Asset: quote.Asset, QuoteExpiresAt: quote.QuoteExpiresAt,
		AcceptBy: acceptBy, DeliverBy: deliverBy, SettleBy: settleBy,
	}
	commitmentHash, err := commitment.Digest(cfg.EscrowContract, quote.ChainID)
	if err != nil {
		return Decision{}, fmt.Errorf("derive ASCP execution commitment: %w", err)
	}
	review := ascpapproval.Review{
		CommitmentHash: commitmentHash.Hex(), PolicyVersion: policyConfig.Version, PolicyHash: policyHash,
		DirectoryVersion: quote.DirectoryVersion, PayTo: quote.PayTo, AckAuthority: quote.AckAuthority,
		AmountBaseUnits: quote.AmountBaseUnits, VerificationSpecHash: quote.VerificationSpecHash,
		Protection: "ESCROW", ChainID: quote.ChainID, Asset: quote.Asset,
	}
	reviewHash, err := ascpapproval.ReviewHash(review)
	if err != nil {
		return Decision{}, err
	}
	spend, err := policySpendSnapshot(ctx, tx, identity, spec, now)
	if err != nil {
		return Decision{}, err
	}
	policyDecision := engine.Evaluate(policy.Intent{
		OrganizationID: identity.OrganizationID, CustomerID: material.customerID, AgentID: identity.AgentID,
		TaskID: spec.TaskID, ActionID: operationID, Rail: envelope.RailEscrow,
		ChainID: mustChainID(quote.ChainID), Recipient: quote.PayTo, Asset: quote.Asset,
		AmountAtomic: quote.AmountBaseUnits, Resource: quote.ResourceID, Category: spec.Category,
	}, spend)
	if material.agentStatus != "ACTIVE" {
		policyDecision = policy.Decision{Outcome: policy.Deny, Reason: "AGENT_INACTIVE", PolicyVersion: policyConfig.Version}
	} else if material.organizationPaused {
		policyDecision = policy.Decision{Outcome: policy.Deny, Reason: "ORGANIZATION_PAUSED", PolicyVersion: policyConfig.Version}
	} else if now.Unix() < 0 || uint64(now.Unix()) >= quote.QuoteExpiresAt {
		policyDecision = policy.Decision{Outcome: policy.Deny, Reason: "QUOTE_EXPIRED", PolicyVersion: policyConfig.Version}
	}
	decision := Decision{
		DecisionID: cfg.DecisionID, OrganizationID: identity.OrganizationID, AgentID: identity.AgentID,
		OperationID: operationID, Outcome: policyDecision.Outcome, Reason: policyDecision.Reason,
		PolicyVersion: policyConfig.Version, PolicyHash: policyHash, CommitmentHash: commitmentHash.Hex(),
		Commitment: commitment, Review: review, ReviewSnapshotHash: reviewHash, EvaluatedAt: now.Unix(),
	}
	if decision.Outcome == policy.RequireApproval {
		expiresAt := now.Add(ascpapproval.ApprovalTTL)
		quoteExpiry := time.Unix(int64(quote.QuoteExpiresAt), 0).UTC()
		if quoteExpiry.Before(expiresAt) {
			expiresAt = quoteExpiry
		}
		if !expiresAt.After(now) {
			decision.Outcome, decision.Reason = policy.Deny, "QUOTE_EXPIRED"
		} else {
			decision.Approval = &ascpapproval.Approval{
				ApprovalID: cfg.ApprovalID, OrganizationID: identity.OrganizationID,
				IntentID: decision.OperationID, State: ascpapproval.Requested,
				ReviewSnapshotHash: reviewHash, RequestedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
			}
		}
	}
	return decision, nil
}

func policySpendSnapshot(ctx context.Context, tx *sql.Tx, identity Identity, spec purchasespec.Spec, now time.Time) (policy.SpendSnapshot, error) {
	day := now.UTC().Format("2006-01-02")
	taskID := ascpexecauth.BudgetDimensionID(ascpexecauth.BudgetDimensionAgentTask, identity.OrganizationID, identity.AgentID, spec.TaskID)
	dailyID := ascpexecauth.BudgetDimensionID(ascpexecauth.BudgetDimensionAgentDay, identity.OrganizationID, identity.AgentID, day)
	rows, err := tx.QueryContext(ctx, `
		SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable
		FROM ascp_budget_reservations r
		JOIN ascp_intents i ON i.operation_id=r.operation_id
		JOIN ascp_budget_reservation_dimensions rd ON rd.reservation_id=r.reservation_id
		WHERE i.organization_id=$1 AND rd.dimension_id IN ($2,$3)
		  AND r.state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','CONSUMED_ON_RELEASE','RESTORED_ON_REFUND','REORGED_BACK')
		FOR SHARE OF r, rd`, identity.OrganizationID, taskID, dailyID)
	if err != nil {
		return policy.SpendSnapshot{}, fmt.Errorf("read ASCP policy spend snapshot: %w", err)
	}
	defer rows.Close()
	values := map[string]struct{ spent, reserved *big.Int }{
		taskID: {new(big.Int), new(big.Int)}, dailyID: {new(big.Int), new(big.Int)},
	}
	for rows.Next() {
		var dimensionID, amountText, state string
		var refundable bool
		if err := rows.Scan(&dimensionID, &amountText, &state, &refundable); err != nil {
			return policy.SpendSnapshot{}, err
		}
		amount, ok := new(big.Int).SetString(amountText, 10)
		if !ok || amount.Sign() <= 0 || amount.BitLen() > 256 {
			return policy.SpendSnapshot{}, errors.New("stored ASCP reservation amount is invalid")
		}
		value := values[dimensionID]
		if state == string(ascpreservation.Restored) && refundable {
			continue
		}
		if state == string(ascpreservation.Consumed) {
			value.spent.Add(value.spent, amount)
		} else {
			value.reserved.Add(value.reserved, amount)
		}
		values[dimensionID] = value
	}
	if err := rows.Err(); err != nil {
		return policy.SpendSnapshot{}, err
	}
	return policy.SpendSnapshot{
		TaskSpentAtomic: values[taskID].spent.String(), TaskReservedAtomic: values[taskID].reserved.String(),
		DailySpentAtomic: values[dailyID].spent.String(), DailyReservedAtomic: values[dailyID].reserved.String(),
	}, nil
}

func (s *PostgresStore) Decision(ctx context.Context, identity Identity, operationID string) (Decision, error) {
	decision, err := loadDecision(ctx, s.db, identity, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{}, ErrNotFound
	}
	return decision, err
}

type scanner interface{ Scan(...any) error }

func loadDecision(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, identity Identity, operationID string) (Decision, error) {
	var decision Decision
	var commitmentJSON, reviewJSON []byte
	var approvalID, approvalState, approvalSnapshot, decidedBy, cancelReason sql.NullString
	var approvalRequested, approvalExpires, approvalDecided sql.NullTime
	var evaluatedAt time.Time
	err := query.QueryRowContext(ctx, `
		SELECT d.decision_id, d.organization_id, d.agent_id, d.operation_id, d.outcome, d.reason,
		       d.policy_version, d.policy_hash, d.commitment_hash, d.commitment_json, d.review_json,
		       d.review_snapshot_hash, d.evaluated_at,
		       a.approval_id, a.state, a.review_snapshot_hash, a.requested_at, a.expires_at,
		       a.decided_at, a.decided_by, a.cancel_reason
		FROM ascp_policy_decisions d
		LEFT JOIN ascp_approvals a ON a.approval_id=d.approval_id
		WHERE d.operation_id=$1 AND d.organization_id=$2 AND d.agent_id=$3`, operationID,
		identity.OrganizationID, identity.AgentID).Scan(
		&decision.DecisionID, &decision.OrganizationID, &decision.AgentID, &decision.OperationID,
		&decision.Outcome, &decision.Reason, &decision.PolicyVersion, &decision.PolicyHash,
		&decision.CommitmentHash, &commitmentJSON, &reviewJSON, &decision.ReviewSnapshotHash,
		&evaluatedAt, &approvalID, &approvalState, &approvalSnapshot, &approvalRequested,
		&approvalExpires, &approvalDecided, &decidedBy, &cancelReason)
	if err != nil {
		return Decision{}, err
	}
	if err := json.Unmarshal(commitmentJSON, &decision.Commitment); err != nil {
		return Decision{}, fmt.Errorf("decode durable ASCP execution commitment: %w", err)
	}
	if err := json.Unmarshal(reviewJSON, &decision.Review); err != nil {
		return Decision{}, fmt.Errorf("decode durable ASCP approval review: %w", err)
	}
	commitmentHash, commitmentErr := decision.Commitment.Digest(decision.Commitment.EscrowContract, decision.Commitment.ChainID)
	reviewHash, reviewErr := ascpapproval.ReviewHash(decision.Review)
	if commitmentErr != nil || reviewErr != nil || commitmentHash.Hex() != decision.CommitmentHash ||
		decision.Review.CommitmentHash != decision.CommitmentHash || decision.Review.PolicyVersion != decision.PolicyVersion ||
		decision.Review.PolicyHash != decision.PolicyHash || reviewHash != decision.ReviewSnapshotHash {
		return Decision{}, errors.New("durable ASCP policy decision binding is invalid")
	}
	decision.EvaluatedAt = evaluatedAt.UTC().Unix()
	if approvalID.Valid {
		approval := &ascpapproval.Approval{
			ApprovalID: approvalID.String, OrganizationID: decision.OrganizationID, IntentID: decision.OperationID,
			State: ascpapproval.State(approvalState.String), ReviewSnapshotHash: approvalSnapshot.String,
			RequestedAt: approvalRequested.Time.UTC().Unix(), ExpiresAt: approvalExpires.Time.UTC().Unix(),
		}
		if approvalDecided.Valid {
			approval.DecidedAt = approvalDecided.Time.UTC().Unix()
		}
		approval.DecidedBy, approval.CancelReason = decidedBy.String, cancelReason.String
		if approval.ReviewSnapshotHash != decision.ReviewSnapshotHash {
			return Decision{}, errors.New("durable ASCP approval binding is invalid")
		}
		decision.Approval = approval
	}
	return decision, nil
}

func (s *PostgresStore) Approval(ctx context.Context, organizationID, approvalID string) (ascpapproval.Approval, error) {
	approval, err := scanApproval(s.db.QueryRowContext(ctx, `
		SELECT approval_id, organization_id, intent_id, state, review_snapshot_hash,
		       requested_at, expires_at, decided_at, decided_by, cancel_reason
		FROM ascp_approvals WHERE approval_id=$1 AND organization_id=$2`, approvalID, organizationID))
	if errors.Is(err, sql.ErrNoRows) {
		return ascpapproval.Approval{}, ErrNotFound
	}
	return approval, err
}

func (s *PostgresStore) DecideApproval(ctx context.Context, organizationID, approvalID, snapshot string, approved bool, actor string, now time.Time) (ascpapproval.Approval, error) {
	target := ascpapproval.Rejected
	if approved {
		target = ascpapproval.Approved
	}
	approval, err := scanApproval(s.db.QueryRowContext(ctx, `
		UPDATE ascp_approvals SET state=$4, decided_at=$5, decided_by=$6
		WHERE approval_id=$1 AND organization_id=$2 AND review_snapshot_hash=$3
		  AND state='REQUESTED' AND expires_at > $5
		RETURNING approval_id, organization_id, intent_id, state, review_snapshot_hash,
		          requested_at, expires_at, decided_at, decided_by, cancel_reason`,
		approvalID, organizationID, snapshot, target, now.UTC(), actor))
	if err == nil {
		return approval, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ascpapproval.Approval{}, fmt.Errorf("decide scoped ASCP approval: %w", err)
	}
	current, readErr := s.Approval(ctx, organizationID, approvalID)
	if readErr != nil {
		return ascpapproval.Approval{}, readErr
	}
	if current.ReviewSnapshotHash != snapshot {
		return current, ascpapproval.ErrSnapshotMismatch
	}
	if current.State == ascpapproval.Requested && now.Unix() >= current.ExpiresAt {
		if _, expireErr := s.db.ExecContext(ctx, `
			UPDATE ascp_approvals SET state='EXPIRED'
			WHERE approval_id=$1 AND organization_id=$2 AND state='REQUESTED' AND expires_at <= $3`,
			approvalID, organizationID, now.UTC()); expireErr != nil {
			return current, expireErr
		}
		current.State = ascpapproval.Expired
	}
	return current, ascpapproval.ErrNotRequested
}

func scanApproval(row scanner) (ascpapproval.Approval, error) {
	var approval ascpapproval.Approval
	var requestedAt, expiresAt time.Time
	var decidedAt sql.NullTime
	var decidedBy, cancelReason sql.NullString
	err := row.Scan(&approval.ApprovalID, &approval.OrganizationID, &approval.IntentID, &approval.State,
		&approval.ReviewSnapshotHash, &requestedAt, &expiresAt, &decidedAt, &decidedBy, &cancelReason)
	if err != nil {
		return ascpapproval.Approval{}, err
	}
	approval.RequestedAt, approval.ExpiresAt = requestedAt.UTC().Unix(), expiresAt.UTC().Unix()
	if decidedAt.Valid {
		approval.DecidedAt = decidedAt.Time.UTC().Unix()
	}
	approval.DecidedBy, approval.CancelReason = decidedBy.String, cancelReason.String
	return approval, nil
}

func (s *PostgresStore) AuthorizationInput(ctx context.Context, identity Identity, operationID, authorizationID, reservationID string, now time.Time) (ascpexecauth.Input, error) {
	decision, err := s.Decision(ctx, identity, operationID)
	if err != nil {
		return ascpexecauth.Input{}, err
	}
	commitmentHash, err := decision.Commitment.Digest(decision.Commitment.EscrowContract, decision.Commitment.ChainID)
	if err != nil || commitmentHash.Hex() != decision.CommitmentHash || decision.Review.CommitmentHash != decision.CommitmentHash {
		return ascpexecauth.Input{}, ErrStateConflict
	}
	if decision.Outcome == policy.Deny {
		return ascpexecauth.Input{}, ErrDecisionDenied
	}
	if decision.Outcome == policy.RequireApproval {
		if decision.Approval == nil {
			return ascpexecauth.Input{}, ErrApprovalUnavailable
		}
		if now.Unix() >= decision.Approval.ExpiresAt {
			return ascpexecauth.Input{}, ErrApprovalUnavailable
		}
		if decision.Approval.State == ascpapproval.Requested {
			return ascpexecauth.Input{}, ErrApprovalPending
		}
		if decision.Approval.State != ascpapproval.Approved {
			return ascpexecauth.Input{}, ErrApprovalUnavailable
		}
	}
	var quoteJSON, purchaseBytes, requestBody, policyJSON []byte
	var observationDigest string
	err = s.db.QueryRowContext(ctx, `
		SELECT i.quote_json, i.purchase_spec_bytes, i.request_body, p.config, h.observation_digest
		FROM ascp_intents i
		JOIN policies p ON p.organization_id=i.organization_id AND p.agent_id=i.actor_id AND p.active=true
		JOIN ascp_directory_heads h ON h.chain_id=(i.quote_json->>'chainId')::bigint
		                              AND h.directory_contract=i.directory_contract
		JOIN ascp_directory_snapshots s ON s.observation_digest=h.observation_digest
		                              AND s.chain_id=h.chain_id
		                              AND s.directory_contract=h.directory_contract
		                              AND s.directory_version=h.directory_version
		                              AND s.finalized_block_number=h.finalized_block_number
		WHERE i.operation_id=$1 AND i.organization_id=$2 AND i.actor_id=$3`, operationID,
		identity.OrganizationID, identity.AgentID).Scan(&quoteJSON, &purchaseBytes, &requestBody, &policyJSON, &observationDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return ascpexecauth.Input{}, ErrStateConflict
	}
	if err != nil {
		return ascpexecauth.Input{}, fmt.Errorf("build ASCP authorization input: %w", err)
	}
	var quote sellerquote.Quote
	var config policy.Config
	if json.Unmarshal(quoteJSON, &quote) != nil || json.Unmarshal(policyJSON, &config) != nil {
		return ascpexecauth.Input{}, ErrStateConflict
	}
	nowUnix := now.UTC().Unix()
	minimumDeliverySeconds := quote.DeclaredWorkTime + quote.VerificationBudgetSeconds
	if quote.VerificationBudgetSeconds < 120 {
		minimumDeliverySeconds = quote.DeclaredWorkTime + 120
	}
	if nowUnix < 0 || uint64(nowUnix) >= decision.Commitment.AcceptBy || uint64(nowUnix) >= decision.Commitment.QuoteExpiresAt ||
		decision.Commitment.DeliverBy <= uint64(nowUnix) || decision.Commitment.DeliverBy-uint64(nowUnix) < minimumDeliverySeconds ||
		decision.Commitment.SettleBy <= uint64(nowUnix)+uint64(minimumSettleWindow/time.Second) {
		return ascpexecauth.Input{}, ErrOperationExpired
	}
	spec, err := purchasespec.ValidatePersisted(purchaseBytes, requestBody)
	if err != nil {
		return ascpexecauth.Input{}, ErrStateConflict
	}
	dimensions, err := ascpexecauth.RequiredBudgetDimensions(config, spec, now.UTC())
	if err != nil {
		return ascpexecauth.Input{}, err
	}
	quoteExpiry := time.Unix(int64(quote.QuoteExpiresAt), 0).UTC()
	expiresAt := now.UTC().Add(ascpexecauth.ReservationTTL)
	if quoteExpiry.Before(expiresAt) {
		expiresAt = quoteExpiry
	}
	if !expiresAt.After(now.UTC()) {
		return ascpexecauth.Input{}, ErrOperationExpired
	}
	input := ascpexecauth.Input{
		AuthorizationID: authorizationID, IntentID: operationID, Review: decision.Review,
		Reservation: ascpreservation.Request{
			ReservationID: reservationID, OperationID: operationID, Amount: quote.AmountBaseUnits,
			Dimensions: dimensions, ExpiresAt: expiresAt,
		},
	}
	if decision.Outcome == policy.RequireApproval {
		input.ApprovalID = decision.Approval.ApprovalID
		input.ApprovalSnapshotHash = decision.Approval.ReviewSnapshotHash
	} else {
		input.AutoDecisionRef = decision.DecisionID
	}
	input.ExecutionSnapshotHash, err = ascpexecauth.ExecutionSnapshotHash(input, identity.OrganizationID, identity.AgentID, observationDigest)
	if err != nil {
		return ascpexecauth.Input{}, err
	}
	return input, nil
}

func (s *PostgresStore) Authorization(ctx context.Context, identity Identity, operationID string) (Authorization, error) {
	var output Authorization
	var approvalID, reservationID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT a.authorization_id, a.intent_id, d.decision_id, a.approval_id, a.state,
		       a.execution_snapshot_hash, a.reservation_id, a.invalidation_reason
		FROM ascp_execution_authorizations a
		JOIN ascp_intents i ON i.operation_id=a.intent_id
		JOIN ascp_policy_decisions d ON d.operation_id=a.intent_id
		WHERE a.intent_id=$1 AND i.organization_id=$2 AND i.actor_id=$3`, operationID,
		identity.OrganizationID, identity.AgentID).Scan(&output.AuthorizationID, &output.OperationID,
		&output.DecisionID, &approvalID, &output.State, &output.ExecutionSnapshotHash,
		&reservationID, &output.InvalidationReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Authorization{}, ErrNotFound
	}
	if err != nil {
		return Authorization{}, fmt.Errorf("read scoped ASCP authorization: %w", err)
	}
	output.ApprovalID, output.ReservationID = approvalID.String, reservationID.String
	return output, nil
}

func mustChainID(value string) uint64 {
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed
}

func serializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func uniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
