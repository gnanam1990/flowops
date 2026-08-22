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

type Sink string

const (
	SinkLegacy              Sink = "LEGACY_CONTROLLED_EFFECT"
	SinkSignerIssuance      Sink = "SIGNER_ISSUANCE"
	SinkVerifierAttestation Sink = "VERIFIER_ATTESTATION"
	SinkKeeperRelay         Sink = "KEEPER_RELAY"
	SinkSellerProxyEgress   Sink = "SELLER_PROXY_EGRESS"
	SinkOutboxDispatch      Sink = "OUTBOX_DISPATCH"
	SinkCheckpointWrite     Sink = "CHECKPOINT_WRITE"
)

type PromotionState string

const (
	PromotionDraining PromotionState = "DRAINING"
	PromotionReady    PromotionState = "READY"
	PromotionCutover  PromotionState = "CUTOVER"
	PromotionComplete PromotionState = "COMPLETE"
)

type PromotionRun struct {
	RunID                    string
	OrganizationID           string
	SourceEpoch              uint64
	TargetEpoch              uint64
	State                    PromotionState
	FinalityMargin           time.Duration
	DrainEvidenceDigest      string
	ReadyEvidenceDigest      string
	CompletionEvidenceDigest string
	StartedAt                time.Time
	ReadyAt                  time.Time
	CutoverAt                time.Time
	CompletedAt              time.Time
}

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
	db              *sql.DB
	clock           func() time.Time
	epochsTable     string
	eventsTable     string
	effectsTable    string
	rejectionsTable string
	promotionsTable string
	schema          string
	intentsTable    string
	bearersTable    string
	verdictsTable   string
	paymentsTable   string
	newEffectID     func() (string, error)
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
		db:              db,
		clock:           clock,
		epochsTable:     `"` + schema + `".ascp_leadership_epochs`,
		eventsTable:     `"` + schema + `".ascp_leadership_events`,
		effectsTable:    `"` + schema + `".ascp_leadership_effects`,
		rejectionsTable: `"` + schema + `".ascp_leadership_rejections`,
		promotionsTable: `"` + schema + `".ascp_promotion_runs`,
		schema:          schema,
		intentsTable:    `"` + schema + `".ascp_intents`,
		bearersTable:    `"` + schema + `".ascp_bearer_registry`,
		verdictsTable:   `"` + schema + `".ascp_verdict_decisions`,
		paymentsTable:   `"` + schema + `".ascp_payment_operations`,
		newEffectID:     randomEffectID,
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
	return p.FenceSink(ctx, organizationID, expected, SinkLegacy, effect)
}

// FenceSink names the controlled side-effect boundary and durably records a
// stale/draining rejection with both the presented and observed epoch.
func (p *Postgres) FenceSink(ctx context.Context, organizationID string, expected uint64, sink Sink, effect func(context.Context) error) error {
	if !validOrganization(organizationID) || expected == 0 || expected > maxEpoch || effect == nil {
		return ErrInvalid
	}
	if !validSink(sink, true) {
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
		if sink != SinkLegacy {
			if _, insertErr := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
				(rejection_id,organization_id,sink,presented_epoch,observed_epoch,observed_state,rejected_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, p.rejectionView(sink)), effectID, organizationID, sink,
				expected, record.Epoch, record.State, startedAt); insertErr != nil {
				return insertErr
			}
			if err := tx.Commit(); err != nil {
				return err
			}
		}
		return ErrEpochChanged
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(effect_id,organization_id,epoch,sink,state,started_at) VALUES ($1,$2,$3,$4,'IN_FLIGHT',$5)`,
		p.effectView(sink)), effectID, organizationID, expected, sink, startedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	effectErr := effect(ctx)
	resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resolveTimeout)
	defer cancel()
	resolveErr := p.completeEffect(resolveCtx, effectID, sink, p.clock().UTC())
	if resolveErr != nil {
		resolveErr = fmt.Errorf("resolve leadership effect %s: %w", effectID, resolveErr)
	}
	return errors.Join(effectErr, resolveErr)
}

func (p *Postgres) rejectionView(sink Sink) string {
	name := map[Sink]string{
		SinkSignerIssuance: "ascp_signer_issuance_rejections", SinkVerifierAttestation: "ascp_verifier_attestation_rejections",
		SinkKeeperRelay: "ascp_keeper_relay_rejections", SinkSellerProxyEgress: "ascp_seller_proxy_egress_rejections",
		SinkOutboxDispatch: "ascp_outbox_dispatch_rejections", SinkCheckpointWrite: "ascp_checkpoint_write_rejections",
	}[sink]
	return `"` + p.schema + `".` + name
}

func (p *Postgres) effectView(sink Sink) string {
	if sink == SinkLegacy {
		return p.effectsTable
	}
	name := map[Sink]string{
		SinkSignerIssuance: "ascp_signer_issuance_effects", SinkVerifierAttestation: "ascp_verifier_attestation_effects",
		SinkKeeperRelay: "ascp_keeper_relay_effects", SinkSellerProxyEgress: "ascp_seller_proxy_egress_effects",
		SinkOutboxDispatch: "ascp_outbox_dispatch_effects", SinkCheckpointWrite: "ascp_checkpoint_write_effects",
	}[sink]
	return `"` + p.schema + `".` + name
}

func validSink(sink Sink, legacy bool) bool {
	if legacy && sink == SinkLegacy {
		return true
	}
	switch sink {
	case SinkSignerIssuance, SinkVerifierAttestation, SinkKeeperRelay, SinkSellerProxyEgress, SinkOutboxDispatch, SinkCheckpointWrite:
		return true
	default:
		return false
	}
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

// BeginPromotion freezes issuance, waits for already-admitted controlled
// effects, and opens the durable bearer/attestation drain record. An exact
// retry after a process crash returns the existing run.
func (p *Postgres) BeginPromotion(ctx context.Context, organizationID string, expected uint64, actor, evidence string, finalityMargin time.Duration) (PromotionRun, error) {
	if finalityMargin < time.Second || finalityMargin > time.Hour || finalityMargin%time.Second != 0 || !validMutation(organizationID, actor, evidence) {
		return PromotionRun{}, ErrInvalid
	}
	record, err := p.BeginDrain(ctx, organizationID, expected, actor, evidence)
	if err != nil {
		record, err = p.Get(ctx, organizationID)
		if err != nil || record.Epoch != expected || record.State != Draining {
			return PromotionRun{}, errors.Join(ErrStateConflict, err)
		}
	}
	runID, err := p.newEffectID()
	if err != nil {
		return PromotionRun{}, err
	}
	now := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return PromotionRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return PromotionRun{}, err
	}
	current, err := get(ctx, tx, p.epochsTable, organizationID)
	if err != nil || current.Epoch != expected || current.State != Draining {
		return PromotionRun{}, errors.Join(ErrStateConflict, err)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(run_id,organization_id,source_epoch,target_epoch,state,finality_margin_seconds,drain_evidence_digest,started_at)
		VALUES ($1,$2,$3,$4,'DRAINING',$5,$6,$7) ON CONFLICT (organization_id,source_epoch) DO NOTHING`,
		p.promotionsTable), runID, organizationID, expected, expected+1, int(finalityMargin/time.Second), evidence, now)
	if err != nil {
		return PromotionRun{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return PromotionRun{}, err
	}
	run, err := loadPromotion(ctx, tx, p.promotionsTable, organizationID, expected, true)
	if err != nil {
		return PromotionRun{}, err
	}
	if changed == 0 && (run.DrainEvidenceDigest != evidence || run.FinalityMargin != finalityMargin) {
		return PromotionRun{}, ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return PromotionRun{}, err
	}
	return run, nil
}

// MarkPromotionReady rechecks the drain under the organization lock. A live
// lock authorization needs a registry-proven terminal outcome; wall-clock
// expiry alone is never accepted. Verifier attestations drain through their
// bounded validity plus the declared finality margin.
func (p *Postgres) MarkPromotionReady(ctx context.Context, organizationID string, sourceEpoch uint64, evidence string) (PromotionRun, error) {
	if !validOrganization(organizationID) || sourceEpoch == 0 || sourceEpoch > maxEpoch || !hashPattern.MatchString(evidence) {
		return PromotionRun{}, ErrInvalid
	}
	now := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return PromotionRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return PromotionRun{}, err
	}
	current, err := get(ctx, tx, p.epochsTable, organizationID)
	if err != nil || current.Epoch != sourceEpoch || current.State != Draining {
		return PromotionRun{}, errors.Join(ErrStateConflict, err)
	}
	run, err := loadPromotion(ctx, tx, p.promotionsTable, organizationID, sourceEpoch, true)
	if err != nil {
		return PromotionRun{}, err
	}
	if run.State == PromotionReady && run.ReadyEvidenceDigest == evidence {
		if err := tx.Commit(); err != nil {
			return PromotionRun{}, err
		}
		return run, nil
	}
	if run.State != PromotionDraining {
		return PromotionRun{}, ErrStateConflict
	}
	var blocked bool
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		EXISTS(SELECT 1 FROM %s b JOIN %s i ON i.operation_id=b.operation_id
		       WHERE i.organization_id=$1 AND b.outcome='LIVE') OR
		EXISTS(SELECT 1 FROM %s v JOIN %s p ON p.call_id=v.call_id
		       WHERE p.organization_id=$1 AND
		       to_timestamp((v.decision_json #>> '{attestation,validUntil}')::bigint) + ($2 * interval '1 second') > $3)`,
		p.bearersTable, p.intentsTable, p.verdictsTable, p.paymentsTable), organizationID,
		int(run.FinalityMargin/time.Second), now).Scan(&blocked)
	if err != nil {
		return PromotionRun{}, err
	}
	if blocked {
		return PromotionRun{}, ErrStateConflict
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state='READY',ready_evidence_digest=$3,ready_at=$4
		WHERE organization_id=$1 AND source_epoch=$2 AND state='DRAINING'`, p.promotionsTable), organizationID, sourceEpoch, evidence, now)
	if err != nil || rowsAffected(result) != 1 {
		return PromotionRun{}, errors.Join(ErrStateConflict, err)
	}
	run, err = loadPromotion(ctx, tx, p.promotionsTable, organizationID, sourceEpoch, false)
	if err != nil {
		return PromotionRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return PromotionRun{}, err
	}
	return run, nil
}

// CompletePromotion succeeds only after post-cutover stale traffic has been
// durably rejected at every controlled sink.
func (p *Postgres) CompletePromotion(ctx context.Context, organizationID string, sourceEpoch uint64, evidence string) (PromotionRun, error) {
	if !validOrganization(organizationID) || sourceEpoch == 0 || sourceEpoch >= maxEpoch || !hashPattern.MatchString(evidence) {
		return PromotionRun{}, ErrInvalid
	}
	now := p.clock().UTC()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return PromotionRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := p.lockOrganization(ctx, tx, organizationID); err != nil {
		return PromotionRun{}, err
	}
	run, err := loadPromotion(ctx, tx, p.promotionsTable, organizationID, sourceEpoch, true)
	if err != nil || run.State != PromotionCutover || run.CutoverAt.IsZero() {
		return PromotionRun{}, errors.Join(ErrStateConflict, err)
	}
	var rejectedSinks int
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(DISTINCT sink) FROM %s
		WHERE organization_id=$1 AND presented_epoch=$2 AND observed_epoch=$3 AND observed_state='ACTIVE' AND rejected_at >= $4`,
		p.rejectionsTable), organizationID, sourceEpoch, sourceEpoch+1, run.CutoverAt).Scan(&rejectedSinks)
	if err != nil {
		return PromotionRun{}, err
	}
	if rejectedSinks != 6 {
		return PromotionRun{}, ErrStateConflict
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s
		SET state='COMPLETE',completion_evidence_digest=$3,completed_at=$4
		WHERE organization_id=$1 AND source_epoch=$2 AND state='CUTOVER'`, p.promotionsTable), organizationID, sourceEpoch, evidence, now)
	if err != nil || rowsAffected(result) != 1 {
		return PromotionRun{}, errors.Join(ErrStateConflict, err)
	}
	run, err = loadPromotion(ctx, tx, p.promotionsTable, organizationID, sourceEpoch, false)
	if err != nil {
		return PromotionRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return PromotionRun{}, err
	}
	return run, nil
}

func (p *Postgres) Promotion(ctx context.Context, organizationID string, sourceEpoch uint64) (PromotionRun, error) {
	if !validOrganization(organizationID) || sourceEpoch == 0 || sourceEpoch >= maxEpoch {
		return PromotionRun{}, ErrInvalid
	}
	return loadPromotion(ctx, p.db, p.promotionsTable, organizationID, sourceEpoch, false)
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

func loadPromotion(ctx context.Context, q queryer, table, organizationID string, sourceEpoch uint64, lock bool) (PromotionRun, error) {
	query := fmt.Sprintf(`SELECT run_id,organization_id,source_epoch,target_epoch,state,finality_margin_seconds,
		drain_evidence_digest,ready_evidence_digest,completion_evidence_digest,started_at,ready_at,cutover_at,completed_at
		FROM %s WHERE organization_id=$1 AND source_epoch=$2`, table)
	if lock {
		query += ` FOR UPDATE`
	}
	var run PromotionRun
	var marginSeconds int
	var readyEvidence, completionEvidence sql.NullString
	var readyAt, cutoverAt, completedAt sql.NullTime
	err := q.QueryRowContext(ctx, query, organizationID, sourceEpoch).Scan(
		&run.RunID, &run.OrganizationID, &run.SourceEpoch, &run.TargetEpoch, &run.State, &marginSeconds,
		&run.DrainEvidenceDigest, &readyEvidence, &completionEvidence, &run.StartedAt, &readyAt, &cutoverAt, &completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PromotionRun{}, ErrNotFound
	}
	if err != nil {
		return PromotionRun{}, err
	}
	run.FinalityMargin = time.Duration(marginSeconds) * time.Second
	run.ReadyEvidenceDigest, run.CompletionEvidenceDigest = readyEvidence.String, completionEvidence.String
	run.StartedAt = run.StartedAt.UTC()
	if readyAt.Valid {
		run.ReadyAt = readyAt.Time.UTC()
	}
	if cutoverAt.Valid {
		run.CutoverAt = cutoverAt.Time.UTC()
	}
	if completedAt.Valid {
		run.CompletedAt = completedAt.Time.UTC()
	}
	return run, nil
}

func (p *Postgres) lockOrganization(ctx context.Context, tx *sql.Tx, organizationID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, organizationID, advisorySeed)
	return err
}

func (p *Postgres) completeEffect(ctx context.Context, effectID string, sink Sink, resolvedAt time.Time) error {
	result, err := p.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s
		SET state='COMPLETED',resolved_at=GREATEST($2,started_at)
		WHERE effect_id=$1 AND state='IN_FLIGHT'`, p.effectView(sink)), effectID, resolvedAt)
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
