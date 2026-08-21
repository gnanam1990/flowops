package ascpverifier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// PostgresVerifierKeyGate reads only observations already finalized by the
// chain observer. It cannot activate, revoke, or advance a verifier epoch.
type PostgresVerifierKeyGate struct {
	db     *sql.DB
	maxAge time.Duration
	clock  func() time.Time
}

func NewPostgresVerifierKeyGate(db *sql.DB, maxAge time.Duration, clocks ...func() time.Time) (*PostgresVerifierKeyGate, error) {
	if db == nil || maxAge < time.Second || maxAge > 10*time.Minute || len(clocks) > 1 {
		return nil, ErrInvalidConfiguration
	}
	clock := time.Now
	if len(clocks) == 1 {
		if clocks[0] == nil {
			return nil, ErrInvalidConfiguration
		}
		clock = clocks[0]
	}
	return &PostgresVerifierKeyGate{db: db, maxAge: maxAge, clock: clock}, nil
}

func (g *PostgresVerifierKeyGate) CheckActive(ctx context.Context, chainID, escrow string, signer common.Address, epoch uint64) error {
	if chainID == "" || !canonicalAddress(escrow) || signer == (common.Address{}) || epoch == 0 || epoch > math.MaxInt64 {
		return ErrInvalidConfiguration
	}
	var observedEpoch int64
	var active bool
	var observed time.Time
	var evidence string
	err := verifierQueryRower(ctx, g.db).QueryRowContext(ctx, `SELECT verifier_epoch,active,observed_at,evidence_digest
		FROM ascp_verifier_key_observations
		WHERE chain_id=$1 AND escrow_contract=$2 AND verifier_address=$3
		ORDER BY finalized_block DESC,finalized_log_index DESC LIMIT 1`, chainID, strings.ToLower(escrow), strings.ToLower(signer.Hex())).
		Scan(&observedEpoch, &active, &observed, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: finalized verifier observation is missing", ErrVerifierInactive)
	}
	if err != nil {
		return fmt.Errorf("%w: read finalized verifier observation: %v", ErrStateUnavailable, err)
	}
	now := g.clock().UTC()
	observed = observed.UTC()
	if observedEpoch != int64(epoch) || !active || !canonicalHash(evidence, true) || observed.After(now) || now.Sub(observed) > g.maxAge {
		return ErrVerifierInactive
	}
	return nil
}

var _ VerifierKeyGate = (*PostgresVerifierKeyGate)(nil)
