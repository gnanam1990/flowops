package referencesigner

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"golang.org/x/crypto/sha3"
)

const maxRawTransactionBytes = 256 * 1024

var transactionHashPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

type AttemptState string

const (
	AttemptPrepared     AttemptState = "PREPARED"
	AttemptBroadcasting AttemptState = "BROADCASTING"
	AttemptSubmitted    AttemptState = "SUBMITTED"
	AttemptAmbiguous    AttemptState = "AMBIGUOUS"
	AttemptRegistered   AttemptState = "REGISTERED"
)

var (
	ErrAttemptConflict     = errors.New("authorization already names a different signer attempt")
	ErrUnsupportedRail     = errors.New("one-way executor supports direct_usdc or escrow funding only")
	ErrPreparedTransaction = errors.New("wallet returned an invalid prepared transaction")
	ErrRegistrationPending = errors.New("broadcast receipt registration is pending")
	ErrBroadcastAmbiguous  = errors.New("transaction broadcast outcome is ambiguous")
)

// PreparedTransaction is the exact signed transaction produced inside the
// customer boundary. The journal persists these bytes before network I/O so a
// restart can never construct a different transaction for the authorization.
type PreparedTransaction struct {
	RawTransaction  []byte `json:"rawTransaction"`
	TransactionHash string `json:"transactionHash"`
	Sender          string `json:"sender"`
}

func (p PreparedTransaction) validate() error {
	if len(p.RawTransaction) == 0 || len(p.RawTransaction) > maxRawTransactionBytes {
		return errors.New("raw transaction must contain 1 to 262144 bytes")
	}
	if !transactionHashPattern.MatchString(p.TransactionHash) {
		return errors.New("transaction hash must be canonical lowercase hex")
	}
	if _, err := canonicalAddress(p.Sender); err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(p.RawTransaction)
	want := "0x" + hex.EncodeToString(hash.Sum(nil))
	if p.TransactionHash != want {
		return errors.New("transaction hash does not match the signed transaction bytes")
	}
	return nil
}

type Attempt struct {
	Authorization       envelope.SignedAuthorization    `json:"authorization"`
	Authorized          Authorized                      `json:"authorized"`
	Prepared            PreparedTransaction             `json:"prepared"`
	State               AttemptState                    `json:"state"`
	PreparedAt          int64                           `json:"preparedAt"`
	BroadcastAt         int64                           `json:"broadcastAt,omitempty"`
	Receipt             *broadcastreceipt.SignedReceipt `json:"receipt,omitempty"`
	ReceiptPublicKeyB64 string                          `json:"receiptPublicKeyB64,omitempty"`
	RegisteredAt        int64                           `json:"registeredAt,omitempty"`
}

func (a Attempt) validate() error {
	if err := a.Authorization.Authorization.Validate(); err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	if a.Authorization.Authorization != a.Authorized.Authorization || a.Authorization.KeyID != a.Authorized.KeyID {
		return errors.New("authorized capability does not match signed authorization")
	}
	digest, err := a.Authorization.Authorization.Digest()
	if err != nil || a.Authorized.Digest != "0x"+hex.EncodeToString(digest[:]) {
		return errors.New("authorized digest does not match signed authorization")
	}
	if a.Authorized.ClaimedAt <= 0 || a.PreparedAt <= 0 || a.PreparedAt < a.Authorized.ClaimedAt {
		return errors.New("authorization claim or preparation time is invalid")
	}
	if err := a.Prepared.validate(); err != nil {
		return fmt.Errorf("prepared transaction: %w", err)
	}
	switch a.State {
	case AttemptPrepared:
		if a.BroadcastAt != 0 || a.Receipt != nil || a.ReceiptPublicKeyB64 != "" || a.RegisteredAt != 0 {
			return errors.New("prepared attempt contains later-state evidence")
		}
	case AttemptBroadcasting:
		if a.BroadcastAt < a.PreparedAt || a.Receipt != nil || a.ReceiptPublicKeyB64 != "" || a.RegisteredAt != 0 {
			return errors.New("broadcasting attempt fields are invalid")
		}
	case AttemptSubmitted, AttemptAmbiguous, AttemptRegistered:
		if err := a.validateReceipt(); err != nil {
			return err
		}
		if a.State == AttemptRegistered {
			if a.RegisteredAt < a.BroadcastAt {
				return errors.New("registration time is invalid")
			}
		} else if a.RegisteredAt != 0 {
			return errors.New("unregistered attempt has a registration time")
		}
	default:
		return errors.New("attempt state is invalid")
	}
	return nil
}

func (a Attempt) validateReceipt() error {
	if a.BroadcastAt < a.PreparedAt || a.Receipt == nil {
		return errors.New("broadcast receipt is missing")
	}
	publicKey, err := base64.StdEncoding.DecodeString(a.ReceiptPublicKeyB64)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || a.ReceiptPublicKeyB64 != base64.StdEncoding.EncodeToString(publicKey) {
		return errors.New("receipt public key is invalid")
	}
	if err := broadcastreceipt.Verify(*a.Receipt, ed25519.PublicKey(publicKey)); err != nil {
		return fmt.Errorf("receipt signature: %w", err)
	}
	r := a.Receipt.Receipt
	wantOutcome := broadcastreceipt.OutcomeSubmitted
	if a.State == AttemptAmbiguous || a.State == AttemptRegistered && r.Outcome == broadcastreceipt.OutcomeAmbiguous {
		wantOutcome = broadcastreceipt.OutcomeAmbiguous
	}
	if r.Outcome != wantOutcome || r.OrganizationID != a.Authorized.Authorization.OrganizationID || r.CustomerID != a.Authorized.Authorization.CustomerID ||
		r.AuthorizationID != a.Authorized.Authorization.AuthorizationID || r.AuthorizationDigest != a.Authorized.Digest || r.TransactionHash != a.Prepared.TransactionHash ||
		r.Sender != a.Prepared.Sender || r.BroadcastAt != a.BroadcastAt {
		return errors.New("receipt does not match the durable signer attempt")
	}
	return nil
}

func clonePrepared(value PreparedTransaction) PreparedTransaction {
	value.RawTransaction = append([]byte(nil), value.RawTransaction...)
	return value
}

func cloneAttempt(value Attempt) Attempt {
	value.Prepared = clonePrepared(value.Prepared)
	if value.Receipt != nil {
		receipt := *value.Receipt
		value.Receipt = &receipt
	}
	return value
}

func unixNow(clock func() time.Time) int64 { return clock().UTC().Unix() }
