package directoryreader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

var (
	ErrCurrentSnapshotUnavailable = errors.New("current finalized directory snapshot is unavailable")
	ErrCurrentSnapshotStale       = errors.New("current finalized directory snapshot is stale")
	ErrCurrentVersionMismatch     = errors.New("seller quote does not use the current directory version")
	ErrQuoteEvidenceUnavailable   = errors.New("seller quote is absent from the current directory snapshot")
)

// MaterializedResolver reads only quorum-verified snapshots previously sealed
// and recorded by Reader/PostgresStore. It never accepts evidence, a directory
// address, or a version from an API caller as authoritative.
type MaterializedResolver struct {
	db            *sql.DB
	chainID       uint64
	directory     string
	maxAge        time.Duration
	maxFutureSkew time.Duration
	clock         func() time.Time
}

func NewMaterializedResolver(db *sql.DB, chainID uint64, directory string, maxAge, maxFutureSkew time.Duration, clocks ...func() time.Time) (*MaterializedResolver, error) {
	if db == nil || (chainID != 8453 && chainID != 84532) || !address(directory) || maxAge <= 0 || maxAge > 5*time.Minute || maxFutureSkew < 0 || maxFutureSkew > time.Minute || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfiguration
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &MaterializedResolver{db: db, chainID: chainID, directory: directory, maxAge: maxAge, maxFutureSkew: maxFutureSkew, clock: clock}, nil
}

// EvidenceForQuote returns the exact leaf/overlay evidence attached to the
// configured directory's current finalized head. Freshness is measured from
// the quorum observation time, never from a later database write.
func (r *MaterializedResolver) EvidenceForQuote(ctx context.Context, quote sellerquote.Quote) (string, sellerquote.DirectoryEvidence, error) {
	if err := quote.Validate(); err != nil || quote.ChainID != strconv.FormatUint(r.chainID, 10) {
		if err != nil {
			return "", sellerquote.DirectoryEvidence{}, err
		}
		return "", sellerquote.DirectoryEvidence{}, ErrInvalidObservation
	}
	var currentVersion uint64
	var observedAt time.Time
	var sellerID, resourceID, signingKey, payoutAddress, ackAuthority, amount, verificationHash sql.NullString
	var keyEpoch, declaredWorkTime, verificationBudget sql.NullInt64
	var active, revoked sql.NullBool
	err := r.db.QueryRowContext(ctx, `
		SELECT h.directory_version, s.observed_at,
		       e.seller_id, e.resource_id, e.quote_signing_key, e.key_epoch, e.payout_address,
		       e.ack_authority, e.amount_base_units, e.verification_spec_hash, e.declared_work_time,
		       e.verification_budget_seconds, e.active, e.quote_key_revoked
		FROM ascp_directory_heads h
		JOIN ascp_directory_snapshots s
		  ON s.observation_digest=h.observation_digest
		 AND s.chain_id=h.chain_id
		 AND s.directory_contract=h.directory_contract
		 AND s.directory_version=h.directory_version
		 AND s.finalized_block_number=h.finalized_block_number
		LEFT JOIN ascp_directory_quote_evidence e
		       ON e.observation_digest=h.observation_digest AND e.seller_id=$3 AND e.resource_id=$4
		WHERE h.chain_id=$1 AND h.directory_contract=$2`, r.chainID, r.directory, quote.SellerID, quote.ResourceID).Scan(
		&currentVersion, &observedAt, &sellerID, &resourceID, &signingKey, &keyEpoch, &payoutAddress,
		&ackAuthority, &amount, &verificationHash, &declaredWorkTime, &verificationBudget, &active, &revoked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sellerquote.DirectoryEvidence{}, ErrCurrentSnapshotUnavailable
	}
	if err != nil {
		return "", sellerquote.DirectoryEvidence{}, fmt.Errorf("read current directory head: %w", err)
	}
	now := r.clock().UTC()
	observedAt = observedAt.UTC()
	if observedAt.After(now.Add(r.maxFutureSkew)) || now.Sub(observedAt) > r.maxAge {
		return "", sellerquote.DirectoryEvidence{}, ErrCurrentSnapshotStale
	}
	if currentVersion != quote.DirectoryVersion {
		return "", sellerquote.DirectoryEvidence{}, ErrCurrentVersionMismatch
	}
	if !sellerID.Valid || !resourceID.Valid || !signingKey.Valid || !keyEpoch.Valid || !payoutAddress.Valid ||
		!ackAuthority.Valid || !amount.Valid || !verificationHash.Valid || !declaredWorkTime.Valid ||
		!verificationBudget.Valid || !active.Valid || !revoked.Valid || keyEpoch.Int64 <= 0 ||
		declaredWorkTime.Int64 <= 0 || verificationBudget.Int64 <= 0 {
		return "", sellerquote.DirectoryEvidence{}, ErrQuoteEvidenceUnavailable
	}
	evidence := sellerquote.DirectoryEvidence{
		Verified: true, Version: currentVersion, SellerID: sellerID.String, ResourceID: resourceID.String,
		QuoteSigningKey: signingKey.String, KeyEpoch: uint64(keyEpoch.Int64), PayoutAddress: payoutAddress.String,
		AckAuthority: ackAuthority.String, AmountBaseUnits: amount.String, VerificationSpecHash: verificationHash.String,
		DeclaredWorkTime: uint64(declaredWorkTime.Int64), VerificationBudgetSeconds: uint64(verificationBudget.Int64),
		Active: active.Bool, QuoteKeyRevoked: revoked.Bool,
	}
	return r.directory, evidence, nil
}
