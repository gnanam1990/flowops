package ascpverifier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// PostgresOperationOrganizationReader resolves the immutable operation-to-
// organization binding without granting the verifier access to intent policy,
// approval, or financial fields.
type PostgresOperationOrganizationReader struct {
	db *sql.DB
}

func NewPostgresOperationOrganizationReader(db *sql.DB) (*PostgresOperationOrganizationReader, error) {
	if db == nil {
		return nil, ErrInvalidConfiguration
	}
	return &PostgresOperationOrganizationReader{db: db}, nil
}

func (r *PostgresOperationOrganizationReader) Organization(ctx context.Context, operationID string) (string, error) {
	if !canonicalHash(operationID, true) {
		return "", ErrInvalidDelivery
	}
	var organizationID string
	err := r.db.QueryRowContext(ctx, `SELECT organization_id FROM ascp_intents WHERE operation_id=$1`, operationID).Scan(&organizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidDelivery
	}
	if err != nil {
		return "", fmt.Errorf("%w: resolve operation organization: %v", ErrStateUnavailable, err)
	}
	if strings.TrimSpace(organizationID) == "" || strings.ContainsAny(organizationID, " \t\r\n") {
		return "", fmt.Errorf("%w: invalid operation organization", ErrStateUnavailable)
	}
	return organizationID, nil
}

var _ OperationOrganizationReader = (*PostgresOperationOrganizationReader)(nil)
