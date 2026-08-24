package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/policy"
)

// DashboardProjection is an organization-scoped projection of durable ASCP
// records. It never invents wallet balances: every amount comes from a policy,
// reservation, payment operation, or append-only ledger row.
type DashboardProjection struct {
	Available        bool                   `json:"available"`
	PendingApprovals []DashboardApproval    `json:"pendingApprovals"`
	Assets           []DashboardAsset       `json:"assets"`
	AgentBudgets     []DashboardAgentBudget `json:"agentBudgets"`
	Activity         []DashboardActivity    `json:"activity"`
}

type DashboardApproval struct {
	ApprovalID    string    `json:"approvalId"`
	ReviewDigest  string    `json:"reviewDigest"`
	OperationID   string    `json:"operationId"`
	AgentID       string    `json:"agentId"`
	TaskID        string    `json:"taskId"`
	Category      string    `json:"category"`
	Reason        string    `json:"reason"`
	PolicyVersion string    `json:"policyVersion"`
	Recipient     string    `json:"recipient"`
	Asset         string    `json:"asset"`
	ChainID       uint64    `json:"chainId"`
	AssetSymbol   string    `json:"assetSymbol,omitempty"`
	AssetDecimals *uint8    `json:"assetDecimals,omitempty"`
	AmountAtomic  string    `json:"amountAtomic"`
	RequestedAt   time.Time `json:"requestedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func decorateDashboardApprovals(projection *DashboardProjection, chainID uint64) {
	for index := range projection.PendingApprovals {
		approval := &projection.PendingApprovals[index]
		approval.ChainID = chainID
		symbol, decimals, known := knownDashboardAsset(chainID, approval.Asset)
		if known {
			approval.AssetSymbol = symbol
			approval.AssetDecimals = &decimals
		}
	}
}

func knownDashboardAsset(chainID uint64, asset string) (string, uint8, bool) {
	switch {
	case chainID == 8453 && asset == "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913":
		return "USDC", 6, true
	case chainID == 84532 && asset == "0x036cbd53842c5426634e7929541ec2318f3dcf7e":
		return "USDC", 6, true
	default:
		return "", 0, false
	}
}

// DashboardAsset contains subledger effects and operation exposure, not an
// ERC-20 balance. WalletDeltaAtomic, EscrowRestrictedAtomic,
// RecognizedExpenseAtomic, and SpentTodayAtomic are signed posting deltas.
// ReservedAtomic, PendingChainAtomic, and UnresolvedAtomic are unsigned
// operation exposure and are rejected if the durable aggregate is negative.
type DashboardAsset struct {
	Asset                   string `json:"asset"`
	WalletDeltaAtomic       string `json:"walletDeltaAtomic"`
	EscrowRestrictedAtomic  string `json:"escrowRestrictedAtomic"`
	RecognizedExpenseAtomic string `json:"recognizedExpenseAtomic"`
	SpentTodayAtomic        string `json:"spentTodayAtomic"`
	ReservedAtomic          string `json:"reservedAtomic"`
	PendingChainAtomic      string `json:"pendingChainAtomic"`
	UnresolvedAtomic        string `json:"unresolvedAtomic"`
}

type DashboardAgentBudget struct {
	AgentID                  string `json:"agentId"`
	Asset                    string `json:"asset,omitempty"`
	DailyLimitAtomic         string `json:"dailyLimitAtomic"`
	SpentTodayAtomic         string `json:"spentTodayAtomic"`
	ReservedAtomic           string `json:"reservedAtomic"`
	AvailableAtomic          string `json:"availableAtomic"`
	CurrentTaskID            string `json:"currentTaskId,omitempty"`
	ActivePolicy             bool   `json:"activePolicy"`
	PolicyVersion            string `json:"policyVersion,omitempty"`
	PolicyConfigurationValid bool   `json:"policyConfigurationValid"`
}

type DashboardActivity struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	AgentID      string    `json:"agentId,omitempty"`
	TaskID       string    `json:"taskId,omitempty"`
	Asset        string    `json:"asset,omitempty"`
	AmountAtomic string    `json:"amountAtomic,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	OccurredAt   time.Time `json:"occurredAt"`
}

type DashboardReader interface {
	DashboardProjection(context.Context, string, time.Time) (DashboardProjection, error)
}

func unavailableDashboardProjection() DashboardProjection {
	return DashboardProjection{Available: false, PendingApprovals: []DashboardApproval{}, Assets: []DashboardAsset{}, AgentBudgets: []DashboardAgentBudget{}, Activity: []DashboardActivity{}}
}

func (s *PostgresStore) DashboardProjection(ctx context.Context, organizationID string, now time.Time) (DashboardProjection, error) {
	if !identifierPattern.MatchString(organizationID) || now.IsZero() {
		return DashboardProjection{}, errors.New("dashboard projection scope is invalid")
	}
	projection := DashboardProjection{Available: true}
	var err error
	if projection.PendingApprovals, err = s.dashboardApprovals(ctx, organizationID, now.UTC()); err != nil {
		return DashboardProjection{}, err
	}
	if projection.Assets, err = s.dashboardAssets(ctx, organizationID, now.UTC()); err != nil {
		return DashboardProjection{}, err
	}
	if projection.AgentBudgets, err = s.dashboardAgentBudgets(ctx, organizationID, now.UTC()); err != nil {
		return DashboardProjection{}, err
	}
	if projection.Activity, err = s.dashboardActivity(ctx, organizationID); err != nil {
		return DashboardProjection{}, err
	}
	return projection, nil
}

func (s *PostgresStore) dashboardApprovals(ctx context.Context, organizationID string, now time.Time) ([]DashboardApproval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.approval_id,a.review_snapshot_hash,i.operation_id,i.actor_id,
		       COALESCE(i.purchase_spec_json->>'taskId',''),COALESCE(i.purchase_spec_json->>'category',''),
		       d.reason,d.policy_version,COALESCE(d.review_json->>'payTo',''),COALESCE(d.review_json->>'asset',''),
		       COALESCE(d.review_json->>'amountBaseUnits',''),a.requested_at,a.expires_at
		FROM ascp_approvals a
		JOIN ascp_intents i ON i.operation_id=a.intent_id AND i.organization_id=a.organization_id
		JOIN ascp_policy_decisions d ON d.operation_id=i.operation_id AND d.approval_id=a.approval_id AND d.organization_id=a.organization_id
		WHERE a.organization_id=$1 AND a.state='REQUESTED' AND a.expires_at>$2
		ORDER BY a.requested_at,a.approval_id`, organizationID, now)
	if err != nil {
		return nil, fmt.Errorf("read dashboard approvals: %w", err)
	}
	defer rows.Close()
	result := make([]DashboardApproval, 0)
	for rows.Next() {
		var item DashboardApproval
		if err := rows.Scan(&item.ApprovalID, &item.ReviewDigest, &item.OperationID, &item.AgentID, &item.TaskID,
			&item.Category, &item.Reason, &item.PolicyVersion, &item.Recipient, &item.Asset, &item.AmountAtomic,
			&item.RequestedAt, &item.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan dashboard approval: %w", err)
		}
		item.RequestedAt, item.ExpiresAt = item.RequestedAt.UTC(), item.ExpiresAt.UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard approvals: %w", err)
	}
	return result, nil
}

type mutableDashboardAsset struct {
	wallet, escrow, expense, today, reserved, pending, unresolved *big.Int
}

func newMutableDashboardAsset() *mutableDashboardAsset {
	return &mutableDashboardAsset{new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int)}
}

func (s *PostgresStore) dashboardAssets(ctx context.Context, organizationID string, now time.Time) ([]DashboardAsset, error) {
	assets := map[string]*mutableDashboardAsset{}
	get := func(asset string) *mutableDashboardAsset {
		if assets[asset] == nil {
			assets[asset] = newMutableDashboardAsset()
		}
		return assets[asset]
	}
	dayStart := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.asset,p.account,SUM(p.amount_base_units::numeric)::text,
		       SUM(CASE WHEN t.recorded_at >= $2 AND t.recorded_at < $3 THEN p.amount_base_units::numeric ELSE 0 END)::text
		FROM ascp_ledger_transactions t
		JOIN ascp_ledger_postings p ON p.transaction_id=t.transaction_id
		WHERE t.organization_id=$1
		GROUP BY t.asset,p.account
		ORDER BY t.asset,p.account`, organizationID, dayStart, dayStart.Add(24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("read dashboard ledger: %w", err)
	}
	for rows.Next() {
		var asset, account, amountText, todayText string
		if err := rows.Scan(&asset, &account, &amountText, &todayText); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan dashboard ledger: %w", err)
		}
		amount, ok := new(big.Int).SetString(amountText, 10)
		today, todayOK := new(big.Int).SetString(todayText, 10)
		if !ok || !todayOK {
			rows.Close()
			return nil, errors.New("dashboard ledger contains an invalid amount")
		}
		entry := get(asset)
		switch account {
		case "WalletAvailableUSDC":
			entry.wallet.Add(entry.wallet, amount)
		case "EscrowRestrictedUSDC":
			entry.escrow.Add(entry.escrow, amount)
		case "SellerExpense":
			entry.expense.Add(entry.expense, amount)
			entry.today.Add(entry.today, today)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close dashboard ledger: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard ledger: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT o.asset,
		       SUM(CASE WHEN r.state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','REORGED_BACK') THEN o.amount_base_units::numeric ELSE 0 END)::text,
		       SUM(CASE WHEN o.state IN ('LOCK_SUBMITTED','LOCKED_SAFE') THEN o.amount_base_units::numeric ELSE 0 END)::text,
		       SUM(CASE WHEN o.state IN ('PENDING_CHAIN_RECOVERY','QUARANTINED','REORGED_BACK') THEN o.amount_base_units::numeric ELSE 0 END)::text
		FROM ascp_payment_operations o
		JOIN ascp_budget_reservations r ON r.reservation_id=o.reservation_id
		WHERE o.organization_id=$1
		GROUP BY o.asset ORDER BY o.asset`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read dashboard operation exposure: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var asset, reservedText, pendingText, unresolvedText string
		if err := rows.Scan(&asset, &reservedText, &pendingText, &unresolvedText); err != nil {
			return nil, fmt.Errorf("scan dashboard operation exposure: %w", err)
		}
		reserved, reservedOK := new(big.Int).SetString(reservedText, 10)
		pending, pendingOK := new(big.Int).SetString(pendingText, 10)
		unresolved, unresolvedOK := new(big.Int).SetString(unresolvedText, 10)
		if !reservedOK || !pendingOK || !unresolvedOK || reserved.Sign() < 0 || pending.Sign() < 0 || unresolved.Sign() < 0 {
			return nil, errors.New("dashboard operation contains an invalid amount")
		}
		entry := get(asset)
		entry.reserved.Add(entry.reserved, reserved)
		entry.pending.Add(entry.pending, pending)
		entry.unresolved.Add(entry.unresolved, unresolved)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard operation exposure: %w", err)
	}
	keys := make([]string, 0, len(assets))
	for asset := range assets {
		keys = append(keys, asset)
	}
	sort.Strings(keys)
	result := make([]DashboardAsset, 0, len(keys))
	for _, asset := range keys {
		value := assets[asset]
		result = append(result, DashboardAsset{Asset: asset, WalletDeltaAtomic: value.wallet.String(),
			EscrowRestrictedAtomic: value.escrow.String(), RecognizedExpenseAtomic: value.expense.String(),
			SpentTodayAtomic: value.today.String(), ReservedAtomic: value.reserved.String(),
			PendingChainAtomic: value.pending.String(), UnresolvedAtomic: value.unresolved.String()})
	}
	return result, nil
}

type mutableAgentBudget struct {
	item            DashboardAgentBudget
	dailyDimension  string
	spent, reserved *big.Int
}

func (s *PostgresStore) dashboardAgentBudgets(ctx context.Context, organizationID string, now time.Time) ([]DashboardAgentBudget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id,p.version,p.config
		FROM agents a
		LEFT JOIN policies p ON p.organization_id=a.organization_id AND p.agent_id=a.id AND p.active=true
		WHERE a.organization_id=$1
		ORDER BY a.id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read dashboard policies: %w", err)
	}
	budgets := map[string]*mutableAgentBudget{}
	day := now.UTC().Format("2006-01-02")
	for rows.Next() {
		var agentID string
		var version sql.NullString
		var raw []byte
		if err := rows.Scan(&agentID, &version, &raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan dashboard policy: %w", err)
		}
		value := &mutableAgentBudget{item: DashboardAgentBudget{AgentID: agentID}, spent: new(big.Int), reserved: new(big.Int)}
		if version.Valid {
			value.item.ActivePolicy, value.item.PolicyVersion = true, version.String
			var config policy.Config
			if json.Unmarshal(raw, &config) == nil && config.Version == version.String {
				if _, compileErr := policy.Compile(config); compileErr == nil {
					value.item.PolicyConfigurationValid = true
					value.item.DailyLimitAtomic = config.DailyBudgetAtomic
					if len(config.AllowedAssets) == 1 {
						value.item.Asset = config.AllowedAssets[0]
					} else {
						value.item.PolicyConfigurationValid = false
					}
				}
			}
		}
		value.dailyDimension = ascpexecauth.BudgetDimensionID(ascpexecauth.BudgetDimensionAgentDay, organizationID, agentID, day)
		budgets[agentID] = value
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close dashboard policies: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard policies: %w", err)
	}

	dimensionIDs := make([]string, 0, len(budgets))
	for _, budget := range budgets {
		dimensionIDs = append(dimensionIDs, budget.dailyDimension)
	}
	sort.Strings(dimensionIDs)
	dimensionJSON, _ := json.Marshal(dimensionIDs)
	rows, err = s.db.QueryContext(ctx, `
		SELECT i.actor_id,rd.dimension_id,r.amount_base_units,r.state,rd.refundable
		FROM ascp_budget_reservations r
		JOIN ascp_intents i ON i.operation_id=r.operation_id
		JOIN ascp_budget_reservation_dimensions rd ON rd.reservation_id=r.reservation_id
		WHERE i.organization_id=$1 AND rd.dimension_id IN (SELECT jsonb_array_elements_text($2::jsonb))
		  AND r.state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','CONSUMED_ON_RELEASE','RESTORED_ON_REFUND','REORGED_BACK')
		ORDER BY i.actor_id,r.reservation_id`, organizationID, string(dimensionJSON))
	if err != nil {
		return nil, fmt.Errorf("read dashboard agent spend: %w", err)
	}
	for rows.Next() {
		var agentID, dimensionID, amountText, state string
		var refundable bool
		if err := rows.Scan(&agentID, &dimensionID, &amountText, &state, &refundable); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan dashboard agent spend: %w", err)
		}
		value := budgets[agentID]
		if value == nil || dimensionID != value.dailyDimension {
			continue
		}
		amount, ok := new(big.Int).SetString(amountText, 10)
		if !ok || amount.Sign() <= 0 {
			rows.Close()
			return nil, errors.New("dashboard reservation contains an invalid amount")
		}
		if state == string(ascpreservation.Restored) && refundable {
			continue
		}
		if state == string(ascpreservation.Consumed) {
			value.spent.Add(value.spent, amount)
		} else {
			value.reserved.Add(value.reserved, amount)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close dashboard agent spend: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard agent spend: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (actor_id) actor_id,COALESCE(purchase_spec_json->>'taskId','')
		FROM ascp_intents WHERE organization_id=$1
		ORDER BY actor_id,created_at DESC,operation_id DESC`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read dashboard current tasks: %w", err)
	}
	for rows.Next() {
		var agentID, taskID string
		if err := rows.Scan(&agentID, &taskID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan dashboard current task: %w", err)
		}
		if value := budgets[agentID]; value != nil {
			value.item.CurrentTaskID = taskID
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close dashboard current tasks: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard current tasks: %w", err)
	}

	keys := make([]string, 0, len(budgets))
	for agentID := range budgets {
		keys = append(keys, agentID)
	}
	sort.Strings(keys)
	result := make([]DashboardAgentBudget, 0, len(keys))
	for _, agentID := range keys {
		value := budgets[agentID]
		if value.item.PolicyConfigurationValid {
			value.item.SpentTodayAtomic = value.spent.String()
			value.item.ReservedAtomic = value.reserved.String()
			limit, _ := new(big.Int).SetString(value.item.DailyLimitAtomic, 10)
			available := new(big.Int).Sub(limit, new(big.Int).Add(value.spent, value.reserved))
			if available.Sign() < 0 {
				available.SetInt64(0)
			}
			value.item.AvailableAtomic = available.String()
		} else {
			value.item.DailyLimitAtomic = ""
		}
		result = append(result, value.item)
	}
	return result, nil
}

func (s *PostgresStore) dashboardActivity(ctx context.Context, organizationID string) ([]DashboardActivity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,kind,state,agent_id,task_id,asset,amount_atomic,detail,occurred_at FROM (
			SELECT o.operation_id AS id,'PAYMENT_OPERATION' AS kind,o.state,i.actor_id AS agent_id,
			       COALESCE(i.purchase_spec_json->>'taskId','') AS task_id,o.asset,o.amount_base_units AS amount_atomic,
			       COALESCE(i.purchase_spec_json->>'category','') AS detail,o.updated_at AS occurred_at
			FROM ascp_payment_operations o JOIN ascp_intents i ON i.operation_id=o.operation_id AND i.organization_id=o.organization_id
			WHERE o.organization_id=$1
			UNION ALL
			SELECT a.approval_id,'ASCP_APPROVAL',a.state,i.actor_id,COALESCE(i.purchase_spec_json->>'taskId',''),
			       COALESCE(d.review_json->>'asset',''),COALESCE(d.review_json->>'amountBaseUnits',''),d.reason,
			       COALESCE(a.decided_at,a.requested_at)
			FROM ascp_approvals a JOIN ascp_intents i ON i.operation_id=a.intent_id AND i.organization_id=a.organization_id
			JOIN ascp_policy_decisions d ON d.operation_id=i.operation_id AND d.approval_id=a.approval_id AND d.organization_id=a.organization_id
			WHERE a.organization_id=$1
			UNION ALL
			SELECT d.decision_id,'POLICY_DECISION',d.outcome,d.agent_id,
			       COALESCE(i.purchase_spec_json->>'taskId',''),COALESCE(d.review_json->>'asset',''),
			       COALESCE(d.review_json->>'amountBaseUnits',''),d.reason,d.evaluated_at
			FROM ascp_policy_decisions d
			JOIN ascp_intents i ON i.operation_id=d.operation_id AND i.organization_id=d.organization_id
			WHERE d.organization_id=$1
			UNION ALL
			SELECT t.transaction_id,'ASCP_LEDGER',t.kind,o.agent_id,
			       COALESCE(i.purchase_spec_json->>'taskId',''),t.asset,o.amount_base_units,t.evidence_digest,t.recorded_at
			FROM ascp_ledger_transactions t
			JOIN ascp_payment_operations o ON o.operation_id=t.operation_id AND o.organization_id=t.organization_id
			JOIN ascp_intents i ON i.operation_id=o.operation_id AND i.organization_id=t.organization_id
			WHERE t.organization_id=$1
			UNION ALL
			SELECT e.id,'CONTROL_AUDIT',e.kind,'','','','',e.target_id,e.created_at
			FROM audit_events e WHERE e.organization_id=$1
		) activity
		ORDER BY occurred_at DESC,id DESC LIMIT 100`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read dashboard activity: %w", err)
	}
	defer rows.Close()
	result := make([]DashboardActivity, 0)
	for rows.Next() {
		var item DashboardActivity
		if err := rows.Scan(&item.ID, &item.Kind, &item.State, &item.AgentID, &item.TaskID, &item.Asset,
			&item.AmountAtomic, &item.Detail, &item.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan dashboard activity: %w", err)
		}
		item.OccurredAt = item.OccurredAt.UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard activity: %w", err)
	}
	return result, nil
}
