package controlapi

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestBootstrapSiteOwnerCreatesAllRowsInOneAuditedTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := siteOwnerBootstrapFixture()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(siteProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT name FROM organizations`).WithArgs(input.OrganizationID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO organizations`).WithArgs(input.OrganizationID, input.OrganizationName).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT exchange_token_digest, enabled`).WithArgs(input.SiteProjectID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO sites_identity_providers`).WithArgs(input.SiteProjectID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT id, site_project_id, site_user_key, email_digest`).WithArgs(input.SiteProjectID, input.SiteUserKey).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO sites_memberships`).WithArgs(
		input.MembershipID, input.SiteProjectID, input.SiteUserKey, sqlmock.AnyArg(), input.OrganizationID, input.PrincipalID,
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO audit_events`).WithArgs(
		input.AuditID, input.OrganizationID, input.ActorID, input.MembershipID, sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := BootstrapSiteOwner(context.Background(), db, input)
	if err != nil || !result.OrganizationCreated || !result.ProviderCreated || !result.MembershipCreated {
		t.Fatalf("bootstrap result = %+v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapSiteOwnerRefusesImplicitIdentityOrTokenReplacement(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := siteOwnerBootstrapFixture()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(siteProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT name FROM organizations`).WithArgs(input.OrganizationID).WillReturnRows(
		sqlmock.NewRows([]string{"name"}).AddRow("Different Organization"),
	)
	mock.ExpectRollback()
	if _, err := BootstrapSiteOwner(context.Background(), db, input); !errors.Is(err, ErrProvisioningConflict) {
		t.Fatalf("identity conflict error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRotateSiteExchangeTokenRequiresActiveMatchingOwner(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := siteExchangeRotationFixture()
	oldDigest := TokenDigest("old_exchange_token_123456789012345678901234")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(siteProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.exchange_token_digest`).WithArgs(input.SiteProjectID, input.MembershipID).WillReturnRows(
		sqlmock.NewRows([]string{"exchange_token_digest", "enabled", "organization_id", "principal_id", "role", "status"}).AddRow(
			oldDigest[:], true, input.OrganizationID, "different_owner", RoleOwner, "ACTIVE",
		),
	)
	mock.ExpectRollback()
	if _, err := RotateSiteExchangeToken(context.Background(), db, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("substituted owner error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRotateSiteExchangeTokenUpdatesAndAuditsAtomically(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := siteExchangeRotationFixture()
	oldDigest := TokenDigest("old_exchange_token_123456789012345678901234")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(siteProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.exchange_token_digest`).WithArgs(input.SiteProjectID, input.MembershipID).WillReturnRows(
		sqlmock.NewRows([]string{"exchange_token_digest", "enabled", "organization_id", "principal_id", "role", "status"}).AddRow(
			oldDigest[:], true, input.OrganizationID, input.ActorID, RoleOwner, "ACTIVE",
		),
	)
	mock.ExpectExec(`UPDATE sites_identity_providers`).WithArgs(input.SiteProjectID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO audit_events`).WithArgs(
		input.AuditID, input.OrganizationID, input.ActorID, input.SiteProjectID, sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	rotated, err := RotateSiteExchangeToken(context.Background(), db, input)
	if err != nil || !rotated {
		t.Fatalf("rotation = %v, %v", rotated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDisableSiteIdentityProviderRequiresOwnerAndAuditsKillSwitch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := SiteProviderDisable{
		AuditID: "audit_sites_disable", ActorID: "owner_flowops", OrganizationID: "org_flowops",
		SiteProjectID: "appgprj_flowops", MembershipID: "membership_flowops_owner",
	}
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(siteProvisioningLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.enabled`).WithArgs(input.SiteProjectID, input.MembershipID).WillReturnRows(
		sqlmock.NewRows([]string{"enabled", "organization_id", "principal_id", "role", "status"}).AddRow(
			true, input.OrganizationID, input.ActorID, RoleOwner, "ACTIVE",
		),
	)
	mock.ExpectExec(`UPDATE sites_identity_providers`).WithArgs(input.SiteProjectID).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO audit_events`).WithArgs(
		input.AuditID, input.OrganizationID, input.ActorID, input.SiteProjectID, sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	disabled, err := DisableSiteIdentityProvider(context.Background(), db, input)
	if err != nil || !disabled {
		t.Fatalf("provider disable = %v, %v", disabled, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func siteOwnerBootstrapFixture() SiteOwnerBootstrap {
	return SiteOwnerBootstrap{
		AuditID: "audit_sites_bootstrap", ActorID: "owner_flowops", OrganizationID: "org_flowops",
		OrganizationName: "FlowOps Pilot", SiteProjectID: "appgprj_flowops", SiteUserKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Email: "owner@example.com", PrincipalID: "owner_flowops", MembershipID: "membership_flowops_owner",
		ExchangeToken: "flowops_exchange_token_123456789012345678901234567890",
	}
}

func siteExchangeRotationFixture() SiteExchangeTokenRotation {
	return SiteExchangeTokenRotation{
		AuditID: "audit_sites_rotate", ActorID: "owner_flowops", OrganizationID: "org_flowops",
		SiteProjectID: "appgprj_flowops", MembershipID: "membership_flowops_owner",
		ExchangeToken: "flowops_exchange_token_rotated_123456789012345678901234",
	}
}
