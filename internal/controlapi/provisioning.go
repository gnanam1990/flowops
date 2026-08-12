package controlapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const siteProvisioningLock = int64(703247667733)

var ErrProvisioningConflict = errors.New("provisioned identity differs from requested state")

type SiteOwnerBootstrap struct {
	AuditID          string `json:"auditId"`
	ActorID          string `json:"actorId"`
	OrganizationID   string `json:"organizationId"`
	OrganizationName string `json:"organizationName"`
	SiteProjectID    string `json:"siteProjectId"`
	SiteUserKey      string `json:"siteUserKey"`
	Email            string `json:"email"`
	PrincipalID      string `json:"principalId"`
	MembershipID     string `json:"membershipId"`
	ExchangeToken    string `json:"exchangeToken"`
}

type SiteOwnerBootstrapResult struct {
	OrganizationCreated bool `json:"organizationCreated"`
	ProviderCreated     bool `json:"providerCreated"`
	MembershipCreated   bool `json:"membershipCreated"`
}

type SiteExchangeTokenRotation struct {
	AuditID        string `json:"auditId"`
	ActorID        string `json:"actorId"`
	OrganizationID string `json:"organizationId"`
	SiteProjectID  string `json:"siteProjectId"`
	MembershipID   string `json:"membershipId"`
	ExchangeToken  string `json:"exchangeToken"`
}

type SiteProviderDisable struct {
	AuditID        string `json:"auditId"`
	ActorID        string `json:"actorId"`
	OrganizationID string `json:"organizationId"`
	SiteProjectID  string `json:"siteProjectId"`
	MembershipID   string `json:"membershipId"`
}

func BootstrapSiteOwner(ctx context.Context, db *sql.DB, input SiteOwnerBootstrap) (SiteOwnerBootstrapResult, error) {
	if db == nil {
		return SiteOwnerBootstrapResult{}, errors.New("database is required")
	}
	if err := input.validate(); err != nil {
		return SiteOwnerBootstrapResult{}, err
	}
	emailDigest, _ := normalizedEmailDigest(input.Email)
	tokenDigest := TokenDigest(input.ExchangeToken)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return SiteOwnerBootstrapResult{}, fmt.Errorf("begin site owner bootstrap: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, siteProvisioningLock); err != nil {
		return SiteOwnerBootstrapResult{}, fmt.Errorf("lock site provisioning: %w", err)
	}

	result := SiteOwnerBootstrapResult{}
	var organizationName string
	err = tx.QueryRowContext(ctx, `SELECT name FROM organizations WHERE id = $1 FOR UPDATE`, input.OrganizationID).Scan(&organizationName)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO organizations (id, name) VALUES ($1, $2)`, input.OrganizationID, input.OrganizationName); err != nil {
			return SiteOwnerBootstrapResult{}, fmt.Errorf("create organization: %w", err)
		}
		result.OrganizationCreated = true
	case err != nil:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("read organization: %w", err)
	case organizationName != input.OrganizationName:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("%w: organization name", ErrProvisioningConflict)
	}

	var storedTokenDigest []byte
	var providerOrganization string
	var providerEnabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT exchange_token_digest, enabled, organization_id
		FROM sites_identity_providers
		WHERE site_project_id = $1
		FOR UPDATE`, input.SiteProjectID).Scan(&storedTokenDigest, &providerEnabled, &providerOrganization)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sites_identity_providers (site_project_id, exchange_token_digest, organization_id)
			VALUES ($1, $2, $3)`, input.SiteProjectID, tokenDigest[:], input.OrganizationID); err != nil {
			return SiteOwnerBootstrapResult{}, fmt.Errorf("create Sites identity provider: %w", err)
		}
		result.ProviderCreated = true
	case err != nil:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("read Sites identity provider: %w", err)
	case !providerEnabled:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("%w: Sites identity provider is disabled", ErrProvisioningConflict)
	case providerOrganization != input.OrganizationID:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("%w: Sites project belongs to another organization", ErrProvisioningConflict)
	case len(storedTokenDigest) != len(tokenDigest) || subtle.ConstantTimeCompare(storedTokenDigest, tokenDigest[:]) != 1:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("%w: exchange token rotation must use the rotation command", ErrProvisioningConflict)
	}

	var stored SiteMembership
	var storedEmailDigest []byte
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT id, site_project_id, site_user_key, email_digest, organization_id, principal_id, role, status
		FROM sites_memberships
		WHERE site_project_id = $1 AND site_user_key = $2
		FOR UPDATE`, input.SiteProjectID, input.SiteUserKey).Scan(
		&stored.ID, &stored.SiteProjectID, &stored.SiteUserKey, &storedEmailDigest,
		&stored.OrganizationID, &stored.PrincipalID, &stored.Role, &status,
	)
	wanted := SiteMembership{
		ID: input.MembershipID, SiteProjectID: input.SiteProjectID, SiteUserKey: input.SiteUserKey,
		OrganizationID: input.OrganizationID, PrincipalID: input.PrincipalID, Role: RoleOwner,
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sites_memberships
				(id, site_project_id, site_user_key, email_digest, organization_id, principal_id, role, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'OWNER', 'ACTIVE')`,
			input.MembershipID, input.SiteProjectID, input.SiteUserKey, emailDigest[:], input.OrganizationID, input.PrincipalID,
		); err != nil {
			return SiteOwnerBootstrapResult{}, fmt.Errorf("create Sites owner membership: %w", err)
		}
		result.MembershipCreated = true
	case err != nil:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("read Sites owner membership: %w", err)
	case stored != wanted || status != "ACTIVE" || len(storedEmailDigest) != len(emailDigest) || subtle.ConstantTimeCompare(storedEmailDigest, emailDigest[:]) != 1:
		return SiteOwnerBootstrapResult{}, fmt.Errorf("%w: Sites owner membership", ErrProvisioningConflict)
	}

	if result.OrganizationCreated || result.ProviderCreated || result.MembershipCreated {
		current, err := json.Marshal(map[string]any{
			"membershipId": input.MembershipID, "principalId": input.PrincipalID,
			"role": RoleOwner, "siteProjectId": input.SiteProjectID, "status": "ACTIVE",
		})
		if err != nil {
			return SiteOwnerBootstrapResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (id, organization_id, actor_id, kind, target_id, current)
			VALUES ($1, $2, $3, 'sites.owner.bootstrapped', $4, $5)`,
			input.AuditID, input.OrganizationID, input.ActorID, input.MembershipID, current,
		); err != nil {
			return SiteOwnerBootstrapResult{}, fmt.Errorf("audit Sites owner bootstrap: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SiteOwnerBootstrapResult{}, fmt.Errorf("commit Sites owner bootstrap: %w", err)
	}
	return result, nil
}

func RotateSiteExchangeToken(ctx context.Context, db *sql.DB, input SiteExchangeTokenRotation) (bool, error) {
	if db == nil {
		return false, errors.New("database is required")
	}
	if err := input.validate(); err != nil {
		return false, err
	}
	tokenDigest := TokenDigest(input.ExchangeToken)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, fmt.Errorf("begin Sites exchange-token rotation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, siteProvisioningLock); err != nil {
		return false, fmt.Errorf("lock site provisioning: %w", err)
	}
	var storedDigest []byte
	var storedOrganization, storedPrincipal string
	var storedRole Role
	var membershipStatus string
	var providerEnabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT p.exchange_token_digest, p.enabled, m.organization_id, m.principal_id, m.role, m.status
		FROM sites_identity_providers p
		JOIN sites_memberships m ON m.site_project_id = p.site_project_id AND m.organization_id = p.organization_id
		WHERE p.site_project_id = $1 AND m.id = $2
		FOR UPDATE OF p, m`, input.SiteProjectID, input.MembershipID).Scan(
		&storedDigest, &providerEnabled, &storedOrganization, &storedPrincipal, &storedRole, &membershipStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("authorize Sites exchange-token rotation: %w", err)
	}
	if !providerEnabled || membershipStatus != "ACTIVE" || storedRole != RoleOwner ||
		storedOrganization != input.OrganizationID || storedPrincipal != input.ActorID {
		return false, ErrForbidden
	}
	if len(storedDigest) == len(tokenDigest) && subtle.ConstantTimeCompare(storedDigest, tokenDigest[:]) == 1 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit no-op Sites exchange-token rotation: %w", err)
		}
		return false, nil
	}
	updateResult, err := tx.ExecContext(ctx, `
		UPDATE sites_identity_providers
		SET exchange_token_digest = $2, rotated_at = now()
		WHERE site_project_id = $1`, input.SiteProjectID, tokenDigest[:])
	if err != nil {
		return false, fmt.Errorf("rotate Sites exchange token: %w", err)
	}
	if affected, err := updateResult.RowsAffected(); err != nil || affected != 1 {
		return false, errors.New("rotate Sites exchange token: provider changed during rotation")
	}
	current, _ := json.Marshal(map[string]any{"siteProjectId": input.SiteProjectID, "rotated": true})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (id, organization_id, actor_id, kind, target_id, current)
		VALUES ($1, $2, $3, 'sites.exchange_token.rotated', $4, $5)`,
		input.AuditID, input.OrganizationID, input.ActorID, input.SiteProjectID, current,
	); err != nil {
		return false, fmt.Errorf("audit Sites exchange-token rotation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Sites exchange-token rotation: %w", err)
	}
	return true, nil
}

func DisableSiteIdentityProvider(ctx context.Context, db *sql.DB, input SiteProviderDisable) (bool, error) {
	if db == nil {
		return false, errors.New("database is required")
	}
	if err := input.validate(); err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, fmt.Errorf("begin Sites provider disable: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, siteProvisioningLock); err != nil {
		return false, fmt.Errorf("lock site provisioning: %w", err)
	}
	var storedOrganization, storedPrincipal string
	var storedRole Role
	var membershipStatus string
	var providerEnabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT p.enabled, m.organization_id, m.principal_id, m.role, m.status
		FROM sites_identity_providers p
		JOIN sites_memberships m ON m.site_project_id = p.site_project_id AND m.organization_id = p.organization_id
		WHERE p.site_project_id = $1 AND m.id = $2
		FOR UPDATE OF p, m`, input.SiteProjectID, input.MembershipID).Scan(
		&providerEnabled, &storedOrganization, &storedPrincipal, &storedRole, &membershipStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("authorize Sites provider disable: %w", err)
	}
	if membershipStatus != "ACTIVE" || storedRole != RoleOwner ||
		storedOrganization != input.OrganizationID || storedPrincipal != input.ActorID {
		return false, ErrForbidden
	}
	if !providerEnabled {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit no-op Sites provider disable: %w", err)
		}
		return false, nil
	}
	updateResult, err := tx.ExecContext(ctx, `
		UPDATE sites_identity_providers
		SET enabled = false, rotated_at = now()
		WHERE site_project_id = $1 AND enabled = true`, input.SiteProjectID)
	if err != nil {
		return false, fmt.Errorf("disable Sites identity provider: %w", err)
	}
	if affected, err := updateResult.RowsAffected(); err != nil || affected != 1 {
		return false, errors.New("disable Sites identity provider: provider changed during disable")
	}
	current, _ := json.Marshal(map[string]any{"siteProjectId": input.SiteProjectID, "enabled": false})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (id, organization_id, actor_id, kind, target_id, current)
		VALUES ($1, $2, $3, 'sites.provider.disabled', $4, $5)`,
		input.AuditID, input.OrganizationID, input.ActorID, input.SiteProjectID, current,
	); err != nil {
		return false, fmt.Errorf("audit Sites provider disable: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Sites provider disable: %w", err)
	}
	return true, nil
}

func (input SiteOwnerBootstrap) validate() error {
	organization := Organization{ID: input.OrganizationID, Name: input.OrganizationName}
	membership := SiteMembership{
		ID: input.MembershipID, SiteProjectID: input.SiteProjectID, SiteUserKey: input.SiteUserKey,
		OrganizationID: input.OrganizationID, PrincipalID: input.PrincipalID, Role: RoleOwner,
	}
	if !organization.Valid() || !membership.Valid() || !envelope.ValidIdentifier(input.AuditID) ||
		!envelope.ValidIdentifier(input.ActorID) || input.ActorID != input.PrincipalID || !validExchangeToken(input.ExchangeToken) {
		return errors.New("Sites owner bootstrap input is invalid")
	}
	if _, err := normalizedEmailDigest(input.Email); err != nil {
		return errors.New("Sites owner bootstrap input is invalid")
	}
	return nil
}

func (input SiteExchangeTokenRotation) validate() error {
	if !envelope.ValidIdentifier(input.AuditID) || !envelope.ValidIdentifier(input.ActorID) ||
		!envelope.ValidIdentifier(input.OrganizationID) || !envelope.ValidIdentifier(input.SiteProjectID) ||
		!envelope.ValidIdentifier(input.MembershipID) || !validExchangeToken(input.ExchangeToken) {
		return errors.New("Sites exchange-token rotation input is invalid")
	}
	return nil
}

func (input SiteProviderDisable) validate() error {
	if !envelope.ValidIdentifier(input.AuditID) || !envelope.ValidIdentifier(input.ActorID) ||
		!envelope.ValidIdentifier(input.OrganizationID) || !envelope.ValidIdentifier(input.SiteProjectID) ||
		!envelope.ValidIdentifier(input.MembershipID) {
		return errors.New("Sites provider disable input is invalid")
	}
	return nil
}

func validExchangeToken(token string) bool {
	if len(token) < 43 || len(token) > 512 || strings.TrimSpace(token) != token || !utf8.ValidString(token) {
		return false
	}
	for _, character := range token {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
