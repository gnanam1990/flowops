// Package ascpcapacity owns hard admission capacity and AC-34 load evidence.
package ascpcapacity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrExhausted     = errors.New("ASCP active-operation capacity exhausted")
	ErrLimitMismatch = errors.New("ASCP capacity limit does not match deployment configuration")
	ErrConflict      = errors.New("ASCP capacity admission conflict")
)

type PostgresGate struct {
	maxActive int
}

func NewPostgresGate(maxActive int) (*PostgresGate, error) {
	if maxActive < 1 || maxActive > 100000 {
		return nil, errors.New("maximum active operations must be between 1 and 100000")
	}
	return &PostgresGate{maxActive: maxActive}, nil
}

func (g *PostgresGate) Acquire(ctx context.Context, tx *sql.Tx, operationID, reservationID string, now time.Time) error {
	if tx == nil || operationID == "" || reservationID == "" || now.IsZero() {
		return ErrConflict
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT ascp_acquire_capacity($1,$2,$3,$4)`, operationID, reservationID,
		g.maxActive, now.UTC()).Scan(&status); err != nil {
		return fmt.Errorf("acquire ASCP capacity: %w", err)
	}
	switch status {
	case "ACQUIRED":
		return nil
	case "EXHAUSTED":
		return ErrExhausted
	case "LIMIT_MISMATCH":
		return ErrLimitMismatch
	default:
		return fmt.Errorf("%w: database status %s", ErrConflict, status)
	}
}
