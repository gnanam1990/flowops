// Package ascpsignerbinding owns the authoritative customer signer routing
// record for each agent. Agents never write or select these values.
package ascpsignerbinding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const maxAttempts = 3

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	addressPattern    = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	zeroAddress       = "0x" + strings.Repeat("0", 40)

	ErrUnavailable         = errors.New("ASCP signer binding is unavailable")
	ErrInvalid             = errors.New("ASCP signer binding input is invalid")
	ErrNotFound            = errors.New("ASCP signer binding was not found")
	ErrAgentUnavailable    = errors.New("ASCP signer binding agent is unavailable")
	ErrVersionConflict     = errors.New("ASCP signer binding version conflict")
	ErrInUse               = errors.New("ASCP signer binding has outstanding bearer work")
	ErrKeyEpochReuse       = errors.New("ASCP signer binding key epoch was already used")
	ErrIdempotencyConflict = errors.New("ASCP signer binding idempotency key names different input")
)

type Binding struct {
	OrganizationID string    `json:"organizationId"`
	AgentID        string    `json:"agentId"`
	Version        uint64    `json:"version"`
	ChainID        uint64    `json:"chainId"`
	SignerKeyID    string    `json:"signerKeyId"`
	KeyEpoch       uint64    `json:"keyEpoch"`
	ModuleAddress  string    `json:"moduleAddress"`
	SafeAddress    string    `json:"safeAddress"`
	KeeperID       string    `json:"keeperId"`
	UpdatedBy      string    `json:"updatedBy"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type PutRequest struct {
	ExpectedVersion uint64 `json:"expectedVersion"`
	SignerKeyID     string `json:"signerKeyId"`
	KeyEpoch        uint64 `json:"keyEpoch"`
	ModuleAddress   string `json:"moduleAddress"`
	SafeAddress     string `json:"safeAddress"`
	KeeperID        string `json:"keeperId"`
	Reason          string `json:"reason"`
}

type Result struct {
	Binding  Binding `json:"binding"`
	ChangeID string  `json:"changeId"`
	Replayed bool    `json:"replayed,omitempty"`
}

type Store struct {
	db      *sql.DB
	chainID uint64
	clock   func() time.Time
	random  io.Reader
}

func NewStore(db *sql.DB, chainID uint64, clocks ...func() time.Time) (*Store, error) {
	if db == nil || chainID == 0 || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrUnavailable
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &Store{db: db, chainID: chainID, clock: clock, random: rand.Reader}, nil
}

// Put atomically applies a create or rotation, its immutable history, its
// audit event, and its idempotency result. A crash cannot leave a successful
// binding mutation behind a permanently pending command record.
func (s *Store) Put(ctx context.Context, organizationID, agentID, actorID, idempotencyKey string, request PutRequest) (Result, error) {
	request.Reason = strings.TrimSpace(request.Reason)
	if !validPut(organizationID, agentID, actorID, idempotencyKey, request) {
		return Result{}, ErrInvalid
	}
	inputHash, err := putHash(s.chainID, organizationID, agentID, actorID, request)
	if err != nil {
		return Result{}, fmt.Errorf("hash signer binding input: %w", err)
	}
	if replay, found, err := s.fastReplay(ctx, organizationID, agentID, actorID, idempotencyKey, inputHash); err != nil {
		return Result{}, err
	} else if found {
		return replay, nil
	}
	changeID, err := s.identifier()
	if err != nil {
		return Result{}, fmt.Errorf("create signer binding change identifier: %w", err)
	}
	now := s.clock().UTC()
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err := s.putOnce(ctx, organizationID, agentID, actorID, idempotencyKey, inputHash, changeID, now, request)
		if !serializationFailure(err) {
			return result, err
		}
		lastErr = err
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	return Result{}, fmt.Errorf("ASCP signer binding serialization retries exhausted: %w", lastErr)
}

func (s *Store) fastReplay(ctx context.Context, organizationID, agentID, actorID, idempotencyKey, inputHash string) (Result, bool, error) {
	var changeID, storedAgentID, storedActorID, storedInputHash string
	var version uint64
	err := s.db.QueryRowContext(ctx, `
		SELECT change_id,agent_id,actor_id,input_hash,resulting_version
		FROM ascp_agent_signer_binding_changes
		WHERE organization_id=$1 AND idempotency_key=$2`, organizationID, idempotencyKey).Scan(
		&changeID, &storedAgentID, &storedActorID, &storedInputHash, &version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("read ASCP signer binding replay: %w", err)
	}
	if storedAgentID != agentID || storedActorID != actorID || storedInputHash != inputHash {
		return Result{}, false, ErrIdempotencyConflict
	}
	binding, err := scanBinding(s.db.QueryRowContext(ctx, `
		SELECT organization_id,agent_id,version,chain_id,signer_key_id,key_epoch,module_address,safe_address,keeper_id,changed_by,created_at
		FROM ascp_agent_signer_binding_history
		WHERE organization_id=$1 AND agent_id=$2 AND version=$3`, organizationID, agentID, version))
	if err != nil {
		return Result{}, false, fmt.Errorf("read ASCP signer binding replay history: %w", err)
	}
	return Result{Binding: binding, ChangeID: changeID, Replayed: true}, true, nil
}

func (s *Store) Current(ctx context.Context, organizationID, agentID string) (Binding, error) {
	if !identifierPattern.MatchString(organizationID) || !identifierPattern.MatchString(agentID) {
		return Binding{}, ErrInvalid
	}
	binding, err := scanBinding(s.db.QueryRowContext(ctx, currentBindingSQL, organizationID, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, ErrNotFound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("read ASCP signer binding: %w", err)
	}
	if binding.ChainID != s.chainID {
		return Binding{}, ErrNotFound
	}
	return binding, nil
}

func (s *Store) putOnce(ctx context.Context, organizationID, agentID, actorID, idempotencyKey, inputHash, changeID string, now time.Time, request PutRequest) (Result, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Result{}, fmt.Errorf("begin ASCP signer binding update: %w", err)
	}
	defer tx.Rollback()

	var storedChangeID, storedAgentID, storedActorID, storedInputHash string
	var storedVersion uint64
	err = tx.QueryRowContext(ctx, `
		SELECT change_id, agent_id, actor_id, input_hash, resulting_version
		FROM ascp_agent_signer_binding_changes
		WHERE organization_id=$1 AND idempotency_key=$2
		FOR UPDATE`, organizationID, idempotencyKey).Scan(
		&storedChangeID, &storedAgentID, &storedActorID, &storedInputHash, &storedVersion,
	)
	if err == nil {
		if storedAgentID != agentID || storedActorID != actorID || storedInputHash != inputHash {
			return Result{}, ErrIdempotencyConflict
		}
		binding, err := historyBinding(ctx, tx, organizationID, agentID, storedVersion)
		if err != nil {
			return Result{}, fmt.Errorf("read replayed ASCP signer binding: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Result{}, fmt.Errorf("commit replayed ASCP signer binding: %w", err)
		}
		return Result{Binding: binding, ChangeID: storedChangeID, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("read ASCP signer binding idempotency record: %w", err)
	}

	var agentStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM agents WHERE organization_id=$1 AND id=$2 FOR UPDATE`,
		organizationID, agentID,
	).Scan(&agentStatus); errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	} else if err != nil {
		return Result{}, fmt.Errorf("lock ASCP signer binding agent: %w", err)
	}
	if agentStatus == "REVOKED" || agentStatus == "ARCHIVED" {
		return Result{}, ErrAgentUnavailable
	}

	current, err := scanBinding(tx.QueryRowContext(ctx, currentBindingSQL+` FOR UPDATE`, organizationID, agentID))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("lock current ASCP signer binding: %w", err)
	}
	if !exists && request.ExpectedVersion != 0 || exists && request.ExpectedVersion != current.Version {
		return Result{}, ErrVersionConflict
	}

	desired := Binding{
		OrganizationID: organizationID, AgentID: agentID, ChainID: s.chainID,
		SignerKeyID: request.SignerKeyID, KeyEpoch: request.KeyEpoch,
		ModuleAddress: request.ModuleAddress, SafeAddress: request.SafeAddress,
		KeeperID: request.KeeperID, UpdatedBy: actorID, UpdatedAt: now,
	}
	changed := !exists || !sameRouting(current, desired)
	if changed && exists {
		inUse, err := bindingInUse(ctx, tx, organizationID, agentID)
		if err != nil {
			return Result{}, err
		}
		if inUse {
			return Result{}, ErrInUse
		}
		used, err := keyEpochUsed(ctx, tx, organizationID, agentID, desired.SignerKeyID, desired.KeyEpoch)
		if err != nil {
			return Result{}, err
		}
		if used {
			return Result{}, ErrKeyEpochReuse
		}
	}
	if changed {
		if exists {
			desired.Version = current.Version + 1
		} else {
			desired.Version = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ascp_agent_signer_binding_history
				(organization_id,agent_id,version,chain_id,signer_key_id,key_epoch,module_address,safe_address,keeper_id,changed_by,reason,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			desired.OrganizationID, desired.AgentID, desired.Version, desired.ChainID, desired.SignerKeyID,
			desired.KeyEpoch, desired.ModuleAddress, desired.SafeAddress, desired.KeeperID, actorID, request.Reason, now,
		); err != nil {
			return Result{}, fmt.Errorf("append ASCP signer binding history: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ascp_agent_signer_bindings
				(organization_id,agent_id,version,chain_id,signer_key_id,key_epoch,module_address,safe_address,keeper_id,updated_by,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (organization_id,agent_id) DO UPDATE SET
				version=EXCLUDED.version, chain_id=EXCLUDED.chain_id, signer_key_id=EXCLUDED.signer_key_id,
				key_epoch=EXCLUDED.key_epoch, module_address=EXCLUDED.module_address, safe_address=EXCLUDED.safe_address,
				keeper_id=EXCLUDED.keeper_id, updated_by=EXCLUDED.updated_by, updated_at=EXCLUDED.updated_at`,
			desired.OrganizationID, desired.AgentID, desired.Version, desired.ChainID, desired.SignerKeyID,
			desired.KeyEpoch, desired.ModuleAddress, desired.SafeAddress, desired.KeeperID, desired.UpdatedBy, desired.UpdatedAt,
		); err != nil {
			return Result{}, fmt.Errorf("write current ASCP signer binding: %w", err)
		}
		previous, _ := json.Marshal(current)
		if !exists {
			previous = nil
		}
		currentJSON, _ := json.Marshal(desired)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (id,organization_id,actor_id,kind,target_id,previous,current,created_at)
			VALUES ($1,$2,$3,'ascp.signer_binding.changed',$4,$5,$6,$7)`,
			changeID, organizationID, actorID, agentID, previous, currentJSON, now,
		); err != nil {
			return Result{}, fmt.Errorf("audit ASCP signer binding update: %w", err)
		}
	} else {
		desired = current
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_agent_signer_binding_changes
			(change_id,organization_id,agent_id,actor_id,idempotency_key,input_hash,resulting_version,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		changeID, organizationID, agentID, actorID, idempotencyKey, inputHash, desired.Version, now,
	); err != nil {
		return Result{}, fmt.Errorf("record ASCP signer binding idempotency result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit ASCP signer binding update: %w", err)
	}
	return Result{Binding: desired, ChangeID: changeID}, nil
}

const currentBindingSQL = `
	SELECT organization_id,agent_id,version,chain_id,signer_key_id,key_epoch,module_address,safe_address,keeper_id,updated_by,updated_at
	FROM ascp_agent_signer_bindings WHERE organization_id=$1 AND agent_id=$2`

type rowScanner interface{ Scan(...any) error }

func scanBinding(row rowScanner) (Binding, error) {
	var binding Binding
	err := row.Scan(&binding.OrganizationID, &binding.AgentID, &binding.Version, &binding.ChainID,
		&binding.SignerKeyID, &binding.KeyEpoch, &binding.ModuleAddress, &binding.SafeAddress,
		&binding.KeeperID, &binding.UpdatedBy, &binding.UpdatedAt)
	return binding, err
}

func historyBinding(ctx context.Context, tx *sql.Tx, organizationID, agentID string, version uint64) (Binding, error) {
	return scanBinding(tx.QueryRowContext(ctx, `
		SELECT organization_id,agent_id,version,chain_id,signer_key_id,key_epoch,module_address,safe_address,keeper_id,changed_by,created_at
		FROM ascp_agent_signer_binding_history
		WHERE organization_id=$1 AND agent_id=$2 AND version=$3`, organizationID, agentID, version))
}

func bindingInUse(ctx context.Context, tx *sql.Tx, organizationID, agentID string) (bool, error) {
	var inUse bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM ascp_sign_requests s
			JOIN ascp_intents i ON i.operation_id=s.operation_id
			WHERE i.organization_id=$1 AND i.actor_id=$2
			  AND s.state NOT IN ('ACTIVATION_ACKNOWLEDGED','EXPIRED_UNACTIVATED','REFUSED')
			UNION ALL
			SELECT 1
			FROM ascp_bearer_registry b
			JOIN ascp_intents i ON i.operation_id=b.operation_id
			WHERE i.organization_id=$1 AND i.actor_id=$2 AND b.outcome='LIVE'
		)`, organizationID, agentID).Scan(&inUse)
	if err != nil {
		return false, fmt.Errorf("check outstanding ASCP signer binding work: %w", err)
	}
	return inUse, nil
}

func keyEpochUsed(ctx context.Context, tx *sql.Tx, organizationID, agentID, signerKeyID string, keyEpoch uint64) (bool, error) {
	var used bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ascp_agent_signer_binding_history
			WHERE organization_id=$1 AND agent_id=$2 AND signer_key_id=$3 AND key_epoch=$4
		)`, organizationID, agentID, signerKeyID, keyEpoch).Scan(&used)
	if err != nil {
		return false, fmt.Errorf("check ASCP signer binding key epoch history: %w", err)
	}
	return used, nil
}

func sameRouting(left, right Binding) bool {
	return left.ChainID == right.ChainID && left.SignerKeyID == right.SignerKeyID && left.KeyEpoch == right.KeyEpoch &&
		left.ModuleAddress == right.ModuleAddress && left.SafeAddress == right.SafeAddress && left.KeeperID == right.KeeperID
}

func validPut(organizationID, agentID, actorID, idempotencyKey string, request PutRequest) bool {
	return identifierPattern.MatchString(organizationID) && identifierPattern.MatchString(agentID) &&
		identifierPattern.MatchString(actorID) && identifierPattern.MatchString(idempotencyKey) &&
		identifierPattern.MatchString(request.SignerKeyID) && request.KeyEpoch > 0 &&
		addressPattern.MatchString(request.ModuleAddress) && request.ModuleAddress != zeroAddress &&
		addressPattern.MatchString(request.SafeAddress) && request.SafeAddress != zeroAddress &&
		request.ModuleAddress != request.SafeAddress &&
		identifierPattern.MatchString(request.KeeperID) && len(request.Reason) > 0 && len(request.Reason) <= 1024
}

func putHash(chainID uint64, organizationID, agentID, actorID string, request PutRequest) (string, error) {
	raw, err := json.Marshal(struct {
		Domain         string     `json:"domain"`
		ChainID        uint64     `json:"chainId"`
		OrganizationID string     `json:"organizationId"`
		AgentID        string     `json:"agentId"`
		ActorID        string     `json:"actorId"`
		Request        PutRequest `json:"request"`
	}{"ASCP_AGENT_SIGNER_BINDING_CHANGE_V1", chainID, organizationID, agentID, actorID, request})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "0x" + hex.EncodeToString(digest[:]), nil
}

func (s *Store) identifier() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", err
	}
	allZero := true
	for _, current := range value {
		allZero = allZero && current == 0
	}
	if allZero {
		return "", errors.New("random source returned an invalid identifier")
	}
	return "0x" + hex.EncodeToString(value), nil
}

func serializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	// A concurrent first write can surface the history/version unique constraint
	// before PostgreSQL reports a serialization failure. Retrying 23505 inside
	// this bounded transaction converts it to the durable replay, version, or
	// key-epoch semantic result observed on the fresh snapshot.
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01" || postgresError.Code == "23505")
}
