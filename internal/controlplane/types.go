package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

const intentDomain = "flowops:payment-intent:v1\n"

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

type State string

const (
	StateDenied          State = "DENIED"
	StatePendingApproval State = "PENDING_APPROVAL"
	StateApproved        State = "APPROVED"
	StateRejected        State = "REJECTED"
	StateIssued          State = "ISSUED"
	StateExpired         State = "EXPIRED"
)

type PaymentIntent struct {
	IntentID       string                `json:"intentId"`
	OrganizationID string                `json:"organizationId"`
	CustomerID     string                `json:"customerId"`
	AgentID        string                `json:"agentId"`
	TaskID         string                `json:"taskId"`
	ActionID       string                `json:"actionId"`
	Rail           envelope.Rail         `json:"rail"`
	ChainID        uint64                `json:"chainId"`
	Recipient      string                `json:"recipient"`
	Asset          string                `json:"asset"`
	AmountAtomic   string                `json:"amountAtomic"`
	Resource       string                `json:"resource"`
	Category       string                `json:"category"`
	Purpose        string                `json:"purpose"`
	Escrow         *envelope.EscrowTerms `json:"escrow,omitempty"`
}

func (i PaymentIntent) Validate() error {
	for name, value := range map[string]string{
		"intentId": i.IntentID, "organizationId": i.OrganizationID, "customerId": i.CustomerID,
		"agentId": i.AgentID, "taskId": i.TaskID, "actionId": i.ActionID,
	} {
		if !idPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if i.Rail != envelope.RailX402 && i.Rail != envelope.RailDirect && i.Rail != envelope.RailEscrow {
		return errors.New("rail is unsupported")
	}
	if i.Rail == envelope.RailEscrow {
		if i.Escrow == nil {
			return errors.New("escrow terms are required for the escrow rail")
		}
		if err := i.Escrow.Validate(i.ChainID, i.Recipient); err != nil {
			return fmt.Errorf("escrow: %w", err)
		}
	} else if i.Escrow != nil {
		return errors.New("escrow terms are forbidden on non-escrow rails")
	}
	if i.ChainID == 0 {
		return errors.New("chainId must be positive")
	}
	for name, value := range map[string]string{"recipient": i.Recipient, "asset": i.Asset} {
		normalized, err := envelope.NormalizeAddress(value)
		if err != nil || normalized != value {
			return fmt.Errorf("%s must be a canonical lowercase EVM address", name)
		}
	}
	if _, ok := canonicalPositiveInteger(i.AmountAtomic); !ok {
		return errors.New("amountAtomic must be a canonical positive integer")
	}
	if strings.TrimSpace(i.Resource) == "" || len(i.Resource) > 2048 {
		return errors.New("resource must contain 1 to 2048 non-whitespace characters")
	}
	if strings.TrimSpace(i.Category) == "" || len(i.Category) > 128 {
		return errors.New("category must contain 1 to 128 non-whitespace characters")
	}
	if strings.TrimSpace(i.Purpose) == "" || len(i.Purpose) > 1024 {
		return errors.New("purpose must contain 1 to 1024 non-whitespace characters")
	}
	return nil
}

func (i PaymentIntent) Digest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	message := append([]byte(intentDomain), encoded...)
	sum := sha256.Sum256(message)
	return "0x" + hex.EncodeToString(sum[:]), nil
}

type Record struct {
	RequestID         string                  `json:"requestId"`
	Intent            PaymentIntent           `json:"intent"`
	IntentDigest      string                  `json:"intentDigest"`
	RequestDigest     string                  `json:"requestDigest"`
	Decision          policy.Decision         `json:"decision"`
	State             State                   `json:"state"`
	SubmittedAt       int64                   `json:"submittedAt"`
	ApprovalExpiresAt int64                   `json:"approvalExpiresAt"`
	Approval          *Approval               `json:"approval,omitempty"`
	Authorization     *envelope.Authorization `json:"authorization,omitempty"`
}

type ApprovalAction string

const (
	Approve ApprovalAction = "APPROVE"
	Reject  ApprovalAction = "REJECT"
)

type Approval struct {
	Action        ApprovalAction `json:"action"`
	Actor         string         `json:"actor"`
	Note          string         `json:"note,omitempty"`
	RequestDigest string         `json:"requestDigest"`
	DecidedAt     int64          `json:"decidedAt"`
}

type submittedPayload struct {
	Record Record `json:"record"`
}

type approvalPayload struct {
	Approval Approval `json:"approval"`
}

type issuedPayload struct {
	Authorization envelope.Authorization `json:"authorization"`
}

type expiredPayload struct {
	Reason string `json:"reason"`
}

func requestDigest(requestID, intentDigest string, decision policy.Decision, expiresAt int64) (string, error) {
	input := struct {
		RequestID     string         `json:"requestId"`
		IntentDigest  string         `json:"intentDigest"`
		PolicyVersion string         `json:"policyVersion"`
		PolicyOutcome policy.Outcome `json:"policyOutcome"`
		PolicyReason  policy.Reason  `json:"policyReason"`
		ExpiresAt     int64          `json:"approvalExpiresAt"`
	}{requestID, intentDigest, decision.PolicyVersion, decision.Outcome, decision.Reason, expiresAt}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("flowops:approval-request:v1\n"), encoded...))
	return "0x" + hex.EncodeToString(sum[:]), nil
}

func decisionState(decision policy.Decision) (State, error) {
	switch decision.Outcome {
	case policy.Deny:
		return StateDenied, nil
	case policy.RequireApproval:
		return StatePendingApproval, nil
	case policy.AutoApprove:
		return StateApproved, nil
	default:
		return "", fmt.Errorf("unsupported policy outcome %q", decision.Outcome)
	}
}

func cloneRecord(record Record) Record {
	if record.Intent.Escrow != nil {
		escrow := *record.Intent.Escrow
		record.Intent.Escrow = &escrow
	}
	if record.Approval != nil {
		approval := *record.Approval
		record.Approval = &approval
	}
	if record.Authorization != nil {
		authorization := *record.Authorization
		if authorization.Escrow != nil {
			escrow := *authorization.Escrow
			authorization.Escrow = &escrow
		}
		record.Authorization = &authorization
	}
	return record
}

func canonicalPositiveInteger(value string) (string, bool) {
	if value == "" || value[0] == '0' {
		return "", false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return value, true
}
