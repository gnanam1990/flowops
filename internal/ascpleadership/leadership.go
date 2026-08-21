// Package ascpleadership implements the strongly consistent, drain-before-CAS
// leadership boundary used by controlled ASCP effects.
package ascpleadership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	advisorySeed int64  = 912034761
	maxEpoch     uint64 = 1<<63 - 1
)

var (
	ErrInvalid       = errors.New("invalid ASCP leadership input")
	ErrNotFound      = errors.New("ASCP leadership organization not found")
	ErrNotActive     = errors.New("ASCP leadership is not active")
	ErrEpochChanged  = errors.New("ASCP leadership epoch changed")
	ErrStateConflict = errors.New("ASCP leadership state conflict")
	hashPattern      = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	actorPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
)

type State string

const (
	Active   State = "ACTIVE"
	Draining State = "DRAINING"
)

type Record struct {
	OrganizationID string
	Epoch          uint64
	State          State
	EvidenceDigest string
	Actor          string
	UpdatedAt      time.Time
}

type Postgres struct {
	db    *sql.DB
	clock func() time.Time
}

func NewPostgres(db *sql.DB, clocks ...func() time.Time) (*Postgres, error) {
	if db == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalid
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &Postgres{db: db, clock: clock}, nil
}

func (p *Postgres) Current(ctx context.Context, organizationID string) (uint64, error) {
	record, err := p.Get(ctx, organizationID)
	if err != nil {
		return 0, err
	}
	if record.State != Active {
		return 0, ErrNotActive
	}
	return record.Epoch, nil
}

// Fence invokes effect at most once while holding the same organization-scoped
// PostgreSQL advisory lock required by every drain and epoch transition.
func (p *Postgres) Fence(ctx context.Context, organizationID string, expected uint64, effect func(context.Context) error) error {
	if !validOrganization(organizationID) || expected == 0 || expected > maxEpoch || effect == nil {
		return ErrInvalid
	}
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}
	record, err := get(ctx, tx, organizationID)
	if err != nil {
		return err
	}
	if record.State != Active || record.Epoch != expected {
		return ErrEpochChanged
	}
	if err := effect(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) Bootstrap(ctx context.Context, organizationID, actor, evidence string) (Record, error) {
	if !validMutation(organizationID, actor, evidence) {
		return Record{}, ErrInvalid
	}
	now := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return Record{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO ascp_leadership_epochs
		(organization_id,epoch,state,evidence_digest,actor,updated_at) VALUES ($1,1,'ACTIVE',$2,$3,$4)
		ON CONFLICT DO NOTHING`, organizationID, evidence, actor, now)
	if err != nil {
		return Record{}, err
	}
	if rowsAffected(result) != 1 {
		existing, getErr := get(ctx, tx, organizationID)
		if getErr != nil {
			return Record{}, getErr
		}
		if existing.Epoch != 1 || existing.State != Active || existing.Actor != actor || existing.EvidenceDigest != evidence {
			return Record{}, ErrStateConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_leadership_events
		(organization_id,new_epoch,new_state,evidence_digest,actor,created_at) VALUES ($1,1,'ACTIVE',$2,$3,$4)
		ON CONFLICT (organization_id,new_epoch,new_state) DO NOTHING`, organizationID, evidence, actor, now); err != nil {
		return Record{}, err
	}
	return commitRecord(ctx, tx, organizationID)
}

func (p *Postgres) BeginDrain(ctx context.Context, organizationID string, expected uint64, actor, evidence string) (Record, error) {
	return p.transition(ctx, organizationID, expected, expected, Active, Draining, actor, evidence)
}

func (p *Postgres) Advance(ctx context.Context, organizationID string, expected uint64, actor, evidence string) (Record, error) {
	if expected >= maxEpoch {
		return Record{}, ErrInvalid
	}
	return p.transition(ctx, organizationID, expected, expected+1, Draining, Active, actor, evidence)
}

func (p *Postgres) transition(ctx context.Context, organizationID string, expected, next uint64, from, to State, actor, evidence string) (Record, error) {
	if expected == 0 || expected > maxEpoch || !validMutation(organizationID, actor, evidence) {
		return Record{}, ErrInvalid
	}
	now := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return Record{}, err
	}
	current, err := get(ctx, tx, organizationID)
	if err != nil {
		return Record{}, err
	}
	if current.Epoch != expected {
		return Record{}, ErrEpochChanged
	}
	if current.State != from {
		return Record{}, ErrStateConflict
	}
	var transitionedAt time.Time
	err = tx.QueryRowContext(ctx, `UPDATE ascp_leadership_epochs SET epoch=$2,state=$3,evidence_digest=$4,actor=$5,
		updated_at=GREATEST($6,updated_at+interval '1 microsecond') WHERE organization_id=$1 RETURNING updated_at`,
		organizationID, next, to, evidence, actor, now).Scan(&transitionedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrStateConflict
	}
	if err != nil {
		return Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_leadership_events
		(organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, organizationID, expected, next, from, to, evidence, actor, transitionedAt); err != nil {
		return Record{}, err
	}
	return commitRecord(ctx, tx, organizationID)
}

func (p *Postgres) Get(ctx context.Context, organizationID string) (Record, error) {
	if !validOrganization(organizationID) {
		return Record{}, ErrInvalid
	}
	return get(ctx, p.db, organizationID)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, q queryer, organizationID string) (Record, error) {
	var record Record
	err := q.QueryRowContext(ctx, `SELECT organization_id,epoch,state,evidence_digest,actor,updated_at
		FROM ascp_leadership_epochs WHERE organization_id=$1`, organizationID).Scan(&record.OrganizationID, &record.Epoch, &record.State, &record.EvidenceDigest, &record.Actor, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

func lockOrganization(ctx context.Context, tx *sql.Tx, organizationID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, organizationID, advisorySeed)
	return err
}

func commitRecord(ctx context.Context, tx *sql.Tx, organizationID string) (Record, error) {
	record, err := get(ctx, tx, organizationID)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validMutation(organizationID, actor, evidence string) bool {
	return validOrganization(organizationID) && actorPattern.MatchString(actor) && hashPattern.MatchString(evidence)
}

func validOrganization(value string) bool {
	if len(value) < 1 || len(value) > 200 {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func rowsAffected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return count
}

func (r Record) String() string { return fmt.Sprintf("%s:%d:%s", r.OrganizationID, r.Epoch, r.State) }
