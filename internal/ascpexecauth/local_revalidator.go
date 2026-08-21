package ascpexecauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const (
	reasonIntentBindingChanged = "INTENT_BINDING_CHANGED"
	reasonAgentInactive        = "AGENT_INACTIVE"
	reasonOrganizationPaused   = "ORGANIZATION_PAUSED"
	reasonPolicyChanged        = "POLICY_CHANGED"
	reasonDirectoryStale       = "DIRECTORY_OBSERVATION_STALE"
	reasonDirectoryChanged     = "DIRECTORY_CHANGED"
	reasonSellerUnavailable    = "SELLER_UNAVAILABLE"
	reasonQuoteExpired         = "QUOTE_EXPIRED"
	reasonSnapshotChanged      = "EXECUTION_SNAPSHOT_CHANGED"
	reasonBudgetDimensions     = "BUDGET_DIMENSIONS_CHANGED"
	reasonPurchaseSpecLegacy   = "PURCHASE_SPEC_BYTES_UNAVAILABLE"

	BudgetDimensionAgentCategoryDay = "agent-category-day"
	BudgetDimensionAgentDay         = "agent-day"
	BudgetDimensionAgentTask        = "agent-task"
	BudgetDimensionAgentLifetime    = "agent-lifetime"
	BudgetDimensionOrganizationDay  = "organization-day"
)

// LocalRevalidator reruns rings 1-5 from rows locked by the same serializable
// transaction as the budget reservation. Finalized directory observations are
// produced outside this transaction, but must already be durably materialized.
type LocalRevalidator struct{ maxDirectoryAge time.Duration }

const maximumDirectoryAge = 5 * time.Minute

func NewLocalRevalidator(maxDirectoryAge time.Duration) (*LocalRevalidator, error) {
	if maxDirectoryAge <= 0 || maxDirectoryAge > maximumDirectoryAge {
		return nil, fmt.Errorf("maximum directory observation age must be in (0, %s]", maximumDirectoryAge)
	}
	return &LocalRevalidator{maxDirectoryAge: maxDirectoryAge}, nil
}

func (r *LocalRevalidator) Revalidate(ctx context.Context, tx *sql.Tx, input Input, now time.Time) (string, error) {
	var organizationID, actorID, directoryContract, sellerSigner string
	var directoryVersion uint64
	var quoteJSON, purchaseSpecBytes, requestBody []byte
	var storedPurchaseSpecHash string
	err := tx.QueryRowContext(ctx, `
		SELECT organization_id, actor_id, directory_version, directory_contract, seller_signer,
		       quote_json, purchase_spec_hash, purchase_spec_bytes, request_body
		FROM ascp_intents
		WHERE operation_id=$1
		FOR UPDATE`, input.IntentID).Scan(&organizationID, &actorID, &directoryVersion, &directoryContract,
		&sellerSigner, &quoteJSON, &storedPurchaseSpecHash, &purchaseSpecBytes, &requestBody)
	if errors.Is(err, sql.ErrNoRows) {
		return reasonIntentBindingChanged, nil
	}
	if err != nil {
		return "", fmt.Errorf("read execution intent binding: %w", err)
	}

	var agentStatus string
	var organizationPaused bool
	err = tx.QueryRowContext(ctx, `
		SELECT a.status, o.authorizations_paused
		FROM agents a
		JOIN organizations o ON o.id=a.organization_id
		WHERE a.organization_id=$1 AND a.id=$2
		FOR SHARE OF a, o`, organizationID, actorID).Scan(&agentStatus, &organizationPaused)
	if errors.Is(err, sql.ErrNoRows) {
		return reasonAgentInactive, nil
	}
	if err != nil {
		return "", fmt.Errorf("read active execution agent: %w", err)
	}
	if agentStatus != "ACTIVE" {
		return reasonAgentInactive, nil
	}
	if organizationPaused {
		return reasonOrganizationPaused, nil
	}

	var activePolicyVersion string
	var policyJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT version, config
		FROM policies
		WHERE organization_id=$1 AND agent_id=$2 AND active=true
		FOR SHARE`, organizationID, actorID).Scan(&activePolicyVersion, &policyJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return reasonPolicyChanged, nil
	}
	if err != nil {
		return "", fmt.Errorf("read active execution policy: %w", err)
	}
	var config policy.Config
	if err := json.Unmarshal(policyJSON, &config); err != nil {
		return "", fmt.Errorf("decode active execution policy: %w", err)
	}
	policyHash, err := policy.ConfigHash(config)
	if err != nil {
		return "", fmt.Errorf("validate active execution policy: %w", err)
	}
	if activePolicyVersion != input.Review.PolicyVersion || config.Version != activePolicyVersion || policyHash != input.Review.PolicyHash {
		return reasonPolicyChanged, nil
	}

	var quote sellerquote.Quote
	if err := json.Unmarshal(quoteJSON, &quote); err != nil {
		return "", fmt.Errorf("decode execution seller quote: %w", err)
	}
	if err := quote.Validate(); err != nil {
		return "", fmt.Errorf("validate stored execution seller quote: %w", err)
	}
	if len(purchaseSpecBytes) == 0 {
		return reasonPurchaseSpecLegacy, nil
	}
	spec, err := purchasespec.ValidatePersisted(purchaseSpecBytes, requestBody)
	if err != nil {
		return "", fmt.Errorf("validate stored execution purchase specification: %w", err)
	}
	if spec.OrgID != organizationID || spec.AgentID != actorID ||
		purchasespec.Hash(purchaseSpecBytes) != storedPurchaseSpecHash || quote.PurchaseSpecHash != storedPurchaseSpecHash {
		return reasonIntentBindingChanged, nil
	}
	if directoryVersion != input.Review.DirectoryVersion || quote.DirectoryVersion != directoryVersion ||
		quote.PayTo != input.Review.PayTo || quote.AckAuthority != input.Review.AckAuthority ||
		quote.AmountBaseUnits != input.Review.AmountBaseUnits || quote.AmountBaseUnits != input.Reservation.Amount ||
		quote.VerificationSpecHash != input.Review.VerificationSpecHash || quote.ChainID != input.Review.ChainID ||
		quote.Asset != input.Review.Asset || input.Review.Protection != "ESCROW" {
		return reasonIntentBindingChanged, nil
	}
	requiredDimensions, err := RequiredBudgetDimensions(config, spec, now)
	if err != nil {
		return "", fmt.Errorf("derive required budget dimensions: %w", err)
	}
	if !sameDimensions(input.Reservation.Dimensions, requiredDimensions) {
		return reasonBudgetDimensions, nil
	}
	if now.Unix() < 0 || uint64(now.Unix()) >= quote.QuoteExpiresAt {
		return reasonQuoteExpired, nil
	}

	chainID, err := strconv.ParseUint(quote.ChainID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("parse stored execution chain: %w", err)
	}
	var currentVersion, finalizedBlock, keyEpoch, declaredWorkTime, verificationBudget uint64
	var observationDigest, quoteSigningKey, payoutAddress, ackAuthority, amount, verificationSpecHash string
	var active, quoteKeyRevoked bool
	var observedAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT h.directory_version, h.observation_digest, s.finalized_block_number, s.observed_at,
		       e.quote_signing_key, e.key_epoch, e.payout_address, e.ack_authority,
		       e.amount_base_units, e.verification_spec_hash, e.declared_work_time,
		       e.verification_budget_seconds, e.active, e.quote_key_revoked
		FROM ascp_directory_heads h
		JOIN ascp_directory_snapshots s
		  ON s.observation_digest=h.observation_digest
		 AND s.chain_id=h.chain_id
		 AND s.directory_contract=h.directory_contract
		 AND s.directory_version=h.directory_version
		 AND s.finalized_block_number=h.finalized_block_number
		JOIN ascp_directory_quote_evidence e ON e.observation_digest=h.observation_digest
		WHERE h.chain_id=$1 AND h.directory_contract=$2 AND e.seller_id=$3 AND e.resource_id=$4
		FOR SHARE OF h, s, e`, chainID, directoryContract, quote.SellerID, quote.ResourceID).Scan(
		&currentVersion, &observationDigest, &finalizedBlock, &observedAt, &quoteSigningKey, &keyEpoch,
		&payoutAddress, &ackAuthority, &amount, &verificationSpecHash, &declaredWorkTime,
		&verificationBudget, &active, &quoteKeyRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return reasonDirectoryChanged, nil
	}
	if err != nil {
		return "", fmt.Errorf("read current finalized directory evidence: %w", err)
	}
	if observedAt.After(now.Add(time.Minute)) || now.Sub(observedAt) > r.maxDirectoryAge {
		return reasonDirectoryStale, nil
	}
	if currentVersion != directoryVersion {
		return reasonDirectoryChanged, nil
	}
	if !active || quoteKeyRevoked || quoteSigningKey != sellerSigner || keyEpoch == 0 ||
		payoutAddress != quote.PayTo || ackAuthority != quote.AckAuthority || amount != quote.AmountBaseUnits ||
		verificationSpecHash != quote.VerificationSpecHash || declaredWorkTime != quote.DeclaredWorkTime ||
		verificationBudget != quote.VerificationBudgetSeconds {
		return reasonSellerUnavailable, nil
	}
	if finalizedBlock == 0 {
		return "", errors.New("stored directory head has zero finalized block")
	}

	wantSnapshot, err := ExecutionSnapshotHash(input, organizationID, actorID, observationDigest)
	if err != nil {
		return "", err
	}
	if input.ExecutionSnapshotHash != wantSnapshot {
		return reasonSnapshotChanged, nil
	}
	return "", nil
}

// ExecutionSnapshotHash binds the approval, current local identities, current
// finalized directory observation, and exact requested reservation.
func ExecutionSnapshotHash(input Input, organizationID, actorID, observationDigest string) (string, error) {
	dimensions := append([]ascpreservation.Dimension(nil), input.Reservation.Dimensions...)
	sort.Slice(dimensions, func(i, j int) bool { return dimensions[i].ID < dimensions[j].ID })
	payload := struct {
		Version              string                      `json:"version"`
		OrganizationID       string                      `json:"organizationId"`
		ActorID              string                      `json:"actorId"`
		IntentID             string                      `json:"intentId"`
		ApprovalID           string                      `json:"approvalId"`
		ApprovalSnapshotHash string                      `json:"approvalSnapshotHash"`
		AutoDecisionRef      string                      `json:"autoDecisionRef,omitempty"`
		PolicyVersion        string                      `json:"policyVersion"`
		PolicyHash           string                      `json:"policyHash"`
		DirectoryVersion     uint64                      `json:"directoryVersion"`
		ObservationDigest    string                      `json:"observationDigest"`
		ReservationID        string                      `json:"reservationId"`
		Amount               string                      `json:"amountBaseUnits"`
		Dimensions           []ascpreservation.Dimension `json:"dimensions"`
		ExpiresAt            int64                       `json:"expiresAt"`
	}{
		"ASCP_EXECUTION_SNAPSHOT_V1", organizationID, actorID, input.IntentID, input.ApprovalID,
		input.ApprovalSnapshotHash, input.AutoDecisionRef, input.Review.PolicyVersion, input.Review.PolicyHash,
		input.Review.DirectoryVersion, observationDigest, input.Reservation.ReservationID,
		input.Reservation.Amount, dimensions, input.Reservation.ExpiresAt.UTC().Unix(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode execution snapshot: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "0x" + hex.EncodeToString(digest[:]), nil
}

// RequiredBudgetDimensions derives the complete reservation set from trusted
// policy and canonical PurchaseSpec values. Callers may request this set, but
// LocalRevalidator independently derives and compares it inside SQL.
func RequiredBudgetDimensions(config policy.Config, spec purchasespec.Spec, now time.Time) ([]ascpreservation.Dimension, error) {
	if _, err := policy.ConfigHash(config); err != nil {
		return nil, err
	}
	day := now.UTC().Format("2006-01-02")
	categoryLimit := config.CategoryDailyBudgetAtomic
	if categoryLimit == "" {
		categoryLimit = config.DailyBudgetAtomic
	}
	lifetimeLimit := config.LifetimeBudgetAtomic
	if lifetimeLimit == "" {
		lifetimeLimit = config.DailyBudgetAtomic
	}
	organizationLimit := config.OrganizationDailyBudgetAtomic
	if organizationLimit == "" {
		organizationLimit = config.DailyBudgetAtomic
	}
	dimensions := []ascpreservation.Dimension{
		{ID: BudgetDimensionID(BudgetDimensionAgentCategoryDay, spec.OrgID, spec.AgentID, spec.Category, day), Limit: categoryLimit, Refundable: true},
		{ID: BudgetDimensionID(BudgetDimensionAgentDay, spec.OrgID, spec.AgentID, day), Limit: config.DailyBudgetAtomic, Refundable: true},
		{ID: BudgetDimensionID(BudgetDimensionAgentTask, spec.OrgID, spec.AgentID, spec.TaskID), Limit: config.TaskBudgetAtomic, Refundable: true},
		{ID: BudgetDimensionID(BudgetDimensionAgentLifetime, spec.OrgID, spec.AgentID), Limit: lifetimeLimit, Refundable: false},
		{ID: BudgetDimensionID(BudgetDimensionOrganizationDay, spec.OrgID, day), Limit: organizationLimit, Refundable: true},
	}
	sort.Slice(dimensions, func(i, j int) bool { return dimensions[i].ID < dimensions[j].ID })
	return dimensions, nil
}

// BudgetDimensionID returns the stable opaque accounting key used by both
// policy preflight and the atomic reservation transaction.
func BudgetDimensionID(kind string, parts ...string) string {
	payload := struct {
		Kind  string   `json:"kind"`
		Parts []string `json:"parts"`
	}{kind, parts}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(append([]byte("ASCP_BUDGET_DIMENSION_V1\n"), encoded...))
	return kind + ":" + hex.EncodeToString(digest[:])
}

func sameDimensions(left, right []ascpreservation.Dimension) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]ascpreservation.Dimension(nil), left...)
	right = append([]ascpreservation.Dimension(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].ID < left[j].ID })
	sort.Slice(right, func(i, j int) bool { return right[i].ID < right[j].ID })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
