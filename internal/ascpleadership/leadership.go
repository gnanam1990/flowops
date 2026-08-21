// Package ascpleadership implements the strongly consistent, drain-before-CAS
// leadership boundary used by controlled ASCP effects.
package ascpleadership

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	advisorySeed   int64  = 912034761
	maxEpoch       uint64 = 1<<63 - 1
	drainPoll             = 25 * time.Millisecond
	resolveTimeout        = 30 * time.Second
)

var (
	ErrInvalid       = errors.New("invalid ASCP leadership input")
	ErrNotFound      = errors.New("ASCP leadership organization not found")
	ErrEpochChanged  = errors.New("ASCP leadership epoch changed")
	ErrStateConflict = errors.New("ASCP leadership state conflict")
	hashPattern      = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	actorPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
	schemaPattern    = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

// State is the durable admission state for one organization's current epoch.
type State string

const (
	Active   State = "ACTIVE"
	Draining State = "DRAINING"
)

// Record is the authoritative leadership state returned to services/operators.
type Record struct {
	OrganizationID string
	Epoch          uint64
	State          State
	EvidenceDigest string
	Actor          string
	UpdatedAt      time.Time
}

// Postgres implements durable leadership admission, drain, and recovery.
type Postgres struct {
	db           *sql.DB
	clock        func() time.Time
	epochsTable  string
	eventsTable  string
	effectsTable string
	newEffectID  func() (string, error)
}

// NewPostgres binds the adapter to a validated, explicitly qualified schema.
func NewPostgres(db *sql.DB, schema string, clocks ...func() time.Time) (*Postgres, error) {
	if db == nil || !schemaPattern.MatchString(schema) || isTemporarySchema(schema) || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalid
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &Postgres{
		db:           db,
		clock:        clock,
		epochsTable:  `"` + schema + `".ascp_leadership_epochs`,
		eventsTable:  `"` + schema + `".ascp_leadership_events`,
		effectsTable: `"` + schema + `".ascp_leadership_effects`,
		newEffectID:  randomEffectID,
	}, nil
}

func isTemporarySchema(schema string) bool {
	return schema == "pg_temp" || strings.HasPrefix(schema, "pg_temp_") || strings.HasPrefix(schema, "pg_toast_temp_")
}

// Current returns the active epoch and fails with ErrEpochChanged while draining.
func (p *Postgres) Current(ctx context.Context, organizationID string) (uint64, error) {
	record, err := p.Get(ctx, organizationID)
	if err != nil {
		return 0, err
	}
	if record.State != Active {
		return 0, ErrEpochChanged
	}
	return record.Epoch, nil
}

// Fence durably admits effect at most once while holding the same
// organization-scoped PostgreSQL advisory lock required by drain and epoch
// transitions. The callback runs after admission commits and must use an
// independent transaction; its writes are not rolled back by Fence. Drain can
// enter DRAINING while the callback runs but cannot return,
// and the epoch cannot advance, until the durable effect is resolved.
func (p *Postgres) Fence(ctx context.Context, organizationID string, expected uint64, effect func(context.Context) error) error {
	if !validOrganization(organizationID) || expected == 0 || expected > maxEpoch || effect == nil {
		return ErrInvalid
	}
	effectID, err := p.newEffectID()
	if err != nil {
		return fmt.Errorf("create leadership effect id: %w", err)
	}
	startedAt := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}
	record, err := get(ctx, tx, p.epochsTable, organizationID)
	if err != nil {
		return err
	}
	if record.State != Active || record.Epoch != expected {
		return ErrEpochChanged
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(effect_id,organization_id,epoch,state,started_at) VALUES ($1,$2,$3,'IN_FLIGHT',$4)`,
		p.effectsTable), effectID, organizationID, expected, startedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	effectErr := effect(ctx)
	resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resolveTimeout)
	defer cancel()
	resolveErr := p.completeEffect(resolveCtx, effectID, p.clock().UTC())
	if resolveErr != nil {
		resolveErr = fmt.Errorf("resolve leadership effect %s: %w", effectID, resolveErr)
	}
	return errors.Join(effectErr, resolveErr)
}

// Bootstrap creates epoch one or returns the exact idempotent replay.
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
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return Record{}, err
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(organization_id,epoch,state,evidence_digest,actor,updated_at) VALUES ($1,1,'ACTIVE',$2,$3,$4)
		ON CONFLICT DO NOTHING`, p.epochsTable), organizationID, evidence, actor, now)
	if err != nil {
		return Record{}, err
	}
	if rowsAffected(result) != 1 {
		existing, getErr := get(ctx, tx, p.epochsTable, organizationID)
		if getErr != nil {
			return Record{}, getErr
		}
		if existing.Epoch != 1 || existing.State != Active || existing.Actor != actor || existing.EvidenceDigest != evidence {
			return Record{}, ErrStateConflict
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(organization_id,new_epoch,new_state,evidence_digest,actor,created_at) VALUES ($1,1,'ACTIVE',$2,$3,$4)
		ON CONFLICT (organization_id,new_epoch,new_state) DO NOTHING`, p.eventsTable), organizationID, evidence, actor, now); err != nil {
		return Record{}, err
	}
	return commitRecord(ctx, tx, p.epochsTable, organizationID)
}

// BeginDrain blocks new admission and waits for every admitted effect to resolve.
func (p *Postgres) BeginDrain(ctx context.Context, organizationID string, expected uint64, actor, evidence string) (Record, error) {
	record, err := p.transition(ctx, organizationID, expected, expected, Active, Draining, actor, evidence)
	if err != nil {
		return Record{}, err
	}
	if err := p.waitForNoEffects(ctx, organizationID, expected); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Advance activates the next epoch after the draining epoch has no live effects.
func (p *Postgres) Advance(ctx context.Context, organizationID string, expected uint64, actor, evidence string) (Record, error) {
	if expected >= maxEpoch {
		return Record{}, ErrInvalid
	}
	return p.transition(ctx, organizationID, expected, expected+1, Draining, Active, actor, evidence)
}

// AbandonEffect is an explicit operator recovery after the organization is
// draining and the old effect host has been proved dead. The actor and evidence
// are retained on the immutable effect record.
func (p *Postgres) AbandonEffect(ctx context.Context, organizationID string, expected uint64, effectID, actor, evidence string) error {
	if expected == 0 || expected > maxEpoch || !validMutation(organizationID, actor, evidence) || !hashPattern.MatchString(effectID) {
		return ErrInvalid
	}
	now := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}
	record, err := get(ctx, tx, p.epochsTable, organizationID)
	if err != nil {
		return err
	}
	if record.Epoch != expected {
		return ErrEpochChanged
	}
	if record.State != Draining {
		return ErrStateConflict
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s
		SET state='ABANDONED',resolved_at=GREATEST($5,started_at),resolution_actor=$3,
			resolution_evidence_digest=$4
		WHERE effect_id=$1 AND organization_id=$2 AND epoch=$6 AND state='IN_FLIGHT'`,
		p.effectsTable), effectID, organizationID, actor, evidence, now, expected)
	if err != nil {
		return err
	}
	if rowsAffected(result) != 1 {
		return ErrStateConflict
	}
	return tx.Commit()
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
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return Record{}, err
	}
	current, err := get(ctx, tx, p.epochsTable, organizationID)
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
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`UPDATE %s SET epoch=$2,state=$3,evidence_digest=$4,actor=$5,
		updated_at=GREATEST($6,updated_at+interval '1 microsecond') WHERE organization_id=$1 RETURNING updated_at`,
		p.epochsTable), organizationID, next, to, evidence, actor, now).Scan(&transitionedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrStateConflict
	}
	if err != nil {
		return Record{}, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, p.eventsTable), organizationID, expected, next, from, to, evidence, actor, transitionedAt); err != nil {
		return Record{}, err
	}
	return commitRecord(ctx, tx, p.epochsTable, organizationID)
}

// Get returns leadership state without requiring it to be active.
func (p *Postgres) Get(ctx context.Context, organizationID string) (Record, error) {
	if !validOrganization(organizationID) {
		return Record{}, ErrInvalid
	}
	return get(ctx, p.db, p.epochsTable, organizationID)
}

// InFlightEffectIDs returns the durable recovery queue for an exact epoch.
func (p *Postgres) InFlightEffectIDs(ctx context.Context, organizationID string, epoch uint64) ([]string, error) {
	if !validOrganization(organizationID) || epoch == 0 || epoch > maxEpoch {
		return nil, ErrInvalid
	}
	rows, err := p.db.QueryContext(ctx, fmt.Sprintf(`SELECT effect_id FROM %s
		WHERE organization_id=$1 AND epoch=$2 AND state='IN_FLIGHT' ORDER BY effect_id`,
		p.effectsTable), organizationID, epoch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var effectIDs []string
	for rows.Next() {
		var effectID string
		if err := rows.Scan(&effectID); err != nil {
			return nil, err
		}
		effectIDs = append(effectIDs, effectID)
	}
	return effectIDs, rows.Err()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, q queryer, epochsTable, organizationID string) (Record, error) {
	var record Record
	err := q.QueryRowContext(ctx, fmt.Sprintf(`SELECT organization_id,epoch,state,evidence_digest,actor,updated_at
		FROM %s WHERE organization_id=$1`, epochsTable), organizationID).Scan(&record.OrganizationID, &record.Epoch, &record.State, &record.EvidenceDigest, &record.Actor, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

func (p *Postgres) lockOrganization(ctx context.Context, tx *sql.Tx, organizationID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, organizationID, advisorySeed)
	return err
}

func (p *Postgres) completeEffect(ctx context.Context, effectID string, resolvedAt time.Time) error {
	result, err := p.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s
		SET state='COMPLETED',resolved_at=GREATEST($2,started_at)
		WHERE effect_id=$1 AND state='IN_FLIGHT'`, p.effectsTable), effectID, resolvedAt)
	if err != nil {
		return err
	}
	if rowsAffected(result) != 1 {
		return ErrStateConflict
	}
	return nil
}

func (p *Postgres) waitForNoEffects(ctx context.Context, organizationID string, epoch uint64) error {
	ticker := time.NewTicker(drainPoll)
	defer ticker.Stop()
	for {
		var inFlight bool
		if err := p.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT EXISTS (
			SELECT 1 FROM %s WHERE organization_id=$1 AND epoch=$2 AND state='IN_FLIGHT'
		)`, p.effectsTable), organizationID, epoch).Scan(&inFlight); err != nil {
			return err
		}
		if !inFlight {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func commitRecord(ctx context.Context, tx *sql.Tx, epochsTable, organizationID string) (Record, error) {
	record, err := get(ctx, tx, epochsTable, organizationID)
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
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func randomEffectID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(raw[:]), nil
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
