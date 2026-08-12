package controlapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

var (
	ErrUnauthenticated      = errors.New("authentication failed")
	ErrForbidden            = errors.New("operation is not allowed")
	ErrNotFound             = errors.New("resource not found")
	ErrIdempotencyConflict  = errors.New("idempotency key names different input")
	ErrCommandAlreadyClosed = errors.New("command is already complete")
)

type PrincipalKind string

const (
	PrincipalHuman PrincipalKind = "HUMAN"
	PrincipalAgent PrincipalKind = "AGENT"
)

type Role string

const (
	RoleOwner     Role = "OWNER"
	RoleAdmin     Role = "ADMIN"
	RoleDeveloper Role = "DEVELOPER"
	RoleFinance   Role = "FINANCE"
	RoleApprover  Role = "APPROVER"
	RoleAuditor   Role = "AUDITOR"
	RoleViewer    Role = "VIEWER"
	RoleAgent     Role = "AGENT"
)

type Permission string

const (
	PermissionRead         Permission = "READ"
	PermissionCreateIntent Permission = "CREATE_INTENT"
	PermissionIssue        Permission = "ISSUE_AUTHORIZATION"
	PermissionDecide       Permission = "DECIDE_APPROVAL"
	PermissionPause        Permission = "PAUSE_AGENT"
	PermissionReadCommand  Permission = "READ_COMMAND"
)

type Principal struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organizationId"`
	Kind           PrincipalKind `json:"kind"`
	Role           Role          `json:"role"`
	AgentID        string        `json:"agentId,omitempty"`
	Scopes         []string      `json:"scopes,omitempty"`
	StepUpUntil    time.Time     `json:"stepUpUntil,omitempty"`
	ReadOnly       bool          `json:"readOnly,omitempty"`
}

func (p Principal) Valid() bool {
	if !identifierPattern.MatchString(p.ID) || !identifierPattern.MatchString(p.OrganizationID) {
		return false
	}
	if p.Kind == PrincipalAgent {
		return p.Role == RoleAgent && identifierPattern.MatchString(p.AgentID)
	}
	if p.Kind != PrincipalHuman || p.AgentID != "" {
		return false
	}
	switch p.Role {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleFinance, RoleApprover, RoleAuditor, RoleViewer:
		return true
	default:
		return false
	}
}

func (p Principal) Can(permission Permission) bool {
	if !p.Valid() {
		return false
	}
	switch permission {
	case PermissionRead, PermissionReadCommand:
		return true
	case PermissionCreateIntent, PermissionIssue, PermissionDecide, PermissionPause:
		if p.ReadOnly {
			return false
		}
	}
	switch permission {
	case PermissionCreateIntent, PermissionIssue:
		return p.Role == RoleAgent || p.Role == RoleDeveloper || p.Role == RoleAdmin || p.Role == RoleOwner
	case PermissionDecide:
		return p.Role == RoleApprover || p.Role == RoleFinance || p.Role == RoleAdmin || p.Role == RoleOwner
	case PermissionPause:
		return p.Role == RoleAdmin || p.Role == RoleOwner
	default:
		return false
	}
}

func (p Principal) HasStepUp(now time.Time) bool {
	return p.Kind == PrincipalHuman && p.StepUpUntil.After(now.UTC())
}

func TokenDigest(token string) [32]byte { return sha256.Sum256([]byte(token)) }

type AgentStatus string

const (
	AgentDraft       AgentStatus = "DRAFT"
	AgentActive      AgentStatus = "ACTIVE"
	AgentPaused      AgentStatus = "PAUSED"
	AgentQuarantined AgentStatus = "QUARANTINED"
	AgentRevoked     AgentStatus = "REVOKED"
	AgentArchived    AgentStatus = "ARCHIVED"
)

type Agent struct {
	OrganizationID string      `json:"organizationId"`
	ID             string      `json:"id"`
	CustomerID     string      `json:"customerId"`
	Name           string      `json:"name"`
	Purpose        string      `json:"purpose"`
	Status         AgentStatus `json:"status"`
	UpdatedAt      time.Time   `json:"updatedAt"`
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (o Organization) Valid() bool {
	return identifierPattern.MatchString(o.ID) && strings.TrimSpace(o.Name) != "" && len(o.Name) <= 200
}

func (a Agent) Valid() bool {
	if !identifierPattern.MatchString(a.OrganizationID) || !identifierPattern.MatchString(a.ID) || !identifierPattern.MatchString(a.CustomerID) || a.Name == "" {
		return false
	}
	switch a.Status {
	case AgentDraft, AgentActive, AgentPaused, AgentQuarantined, AgentRevoked, AgentArchived:
		return true
	default:
		return false
	}
}

type CommandState string

const (
	CommandPending   CommandState = "PENDING"
	CommandSucceeded CommandState = "SUCCEEDED"
	CommandFailed    CommandState = "FAILED"
)

type Command struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	ActorID        string          `json:"actorId"`
	Kind           string          `json:"kind"`
	TargetID       string          `json:"targetId,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey"`
	InputDigest    string          `json:"inputDigest"`
	State          CommandState    `json:"state"`
	Result         json.RawMessage `json:"result,omitempty"`
	ErrorCode      string          `json:"errorCode,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
}

type AuditEvent struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	ActorID        string          `json:"actorId"`
	Kind           string          `json:"kind"`
	TargetID       string          `json:"targetId"`
	Previous       json.RawMessage `json:"previous,omitempty"`
	Current        json.RawMessage `json:"current,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}
