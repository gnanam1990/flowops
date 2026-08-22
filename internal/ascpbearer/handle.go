// Package ascpbearer implements both halves of D7 without crossing their
// trust boundary: activation.go stores only control-plane metadata, while this
// file models the isolated signer ledger that alone retains encrypted bytes.
package ascpbearer

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

type State string

const (
	Prepared State = "PREPARED"
	Active   State = "ACTIVE"
	Released State = "RELEASED"
	Expired  State = "EXPIRED"
	Terminal State = "TERMINAL"
)

var (
	ErrTransition = errors.New("invalid bearer handle transition")
	ErrMismatch   = errors.New("prepared handle binding mismatch")
	ErrKeeper     = errors.New("prepared artifact release is not authorized for this keeper")
)

// Handle is safe to return across the signer boundary. It deliberately has no
// signature or ciphertext field.
type Handle struct {
	ID                   string    `json:"id"`
	RequestID            string    `json:"requestId"`
	AuthorizationID      string    `json:"authorizationId"`
	ReservationID        string    `json:"reservationId"`
	ActionID             string    `json:"actionId"`
	OperationID          string    `json:"operationId"`
	SignerRequestHash    string    `json:"signerRequestHash"`
	CanonicalPayloadHash string    `json:"canonicalPayloadHash"`
	Digest               string    `json:"digest"`
	Nonce                string    `json:"nonce"`
	SignerKeyID          string    `json:"signerKeyId"`
	KeyEpoch             uint64    `json:"keyEpoch"`
	KeeperID             string    `json:"keeperId"`
	ValidAfter           time.Time `json:"validAfter"`
	ValidUntil           time.Time `json:"validUntil"`
	State                State     `json:"state"`
	Outcome              string    `json:"outcome,omitempty"`
}

type PrepareInput struct {
	RequestID            string
	AuthorizationID      string
	ReservationID        string
	ActionID             string
	OperationID          string
	SignerRequestHash    string
	CanonicalPayloadHash string
	Digest               string
	Nonce                string
	SignerKeyID          string
	KeyEpoch             uint64
	KeeperID             string
	ValidAfter           time.Time
	ValidUntil           time.Time
	Signature            []byte
}

type ActivationProof struct {
	RequestID            string    `json:"requestId"`
	HandleID             string    `json:"handleId"`
	OperationID          string    `json:"operationId"`
	Digest               string    `json:"digest"`
	Nonce                string    `json:"nonce"`
	PrimaryMirrorDigest  string    `json:"primaryMirrorDigest"`
	ActivationOccurredAt time.Time `json:"activationOccurredAt"`
}

// ActivationVerifier belongs inside the signer trust boundary. A production
// implementation verifies control-plane acknowledgment authentication and the
// exact primary-WORM registry digest; callers cannot replace it per request.
type ActivationVerifier interface {
	VerifyActivation(context.Context, Handle, ActivationProof) error
	ProveUnactivated(context.Context, Handle, time.Time) error
}

type ArtifactCipher interface {
	Encrypt(context.Context, []byte, []byte) ([]byte, error)
	Decrypt(context.Context, []byte, []byte) ([]byte, error)
}

type signerRecord struct {
	Handle     Handle
	Encrypted  []byte
	Activation *ActivationProof `json:"activation,omitempty"`
}

// SignerStore is the signer-side state machine. Memory is only a model; a
// durable adapter must persist every transition before acknowledging it.
type SignerStore struct {
	mu       sync.Mutex
	byID     map[string]signerRecord
	byAction map[string]string
	cipher   ArtifactCipher
	verifier ActivationVerifier
	clock    func() time.Time
	newID    func() (string, error)
	journal  *signerJournal
}

func NewSignerStore(cipher ArtifactCipher, verifier ActivationVerifier, clock func() time.Time, random io.Reader) (*SignerStore, error) {
	if cipher == nil || verifier == nil {
		return nil, errors.New("artifact cipher and activation verifier are required")
	}
	if clock == nil {
		clock = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &SignerStore{byID: make(map[string]signerRecord), byAction: make(map[string]string), cipher: cipher, verifier: verifier, clock: clock, newID: opaqueIDSource(random)}, nil
}

func (s *SignerStore) Prepare(ctx context.Context, input PrepareInput) (Handle, error) {
	now := s.clock().UTC()
	input.ValidAfter, input.ValidUntil = input.ValidAfter.UTC(), input.ValidUntil.UTC()
	if !hash(input.RequestID) || !hash(input.AuthorizationID) || !hash(input.ReservationID) ||
		!identifier(input.ActionID) || !hash(input.OperationID) || !hash(input.SignerRequestHash) || !hash(input.CanonicalPayloadHash) ||
		!hash(input.Digest) || !hash(input.Nonce) || !identifier(input.SignerKeyID) || input.KeyEpoch == 0 ||
		!identifier(input.KeeperID) || input.ValidAfter.Before(now.Add(-time.Minute)) ||
		input.ValidAfter.After(now.Add(time.Minute)) || !input.ValidAfter.Before(input.ValidUntil) ||
		!now.Before(input.ValidUntil) || input.ValidUntil.Sub(input.ValidAfter) > maximumAuthorizationWindow ||
		len(input.Signature) < 64 || len(input.Signature) > 16*1024 {
		return Handle{}, ErrTransition
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actionKey := signerActionKey(input.OperationID, input.ActionID)
	if id, exists := s.byAction[actionKey]; exists {
		record := s.byID[id]
		if !samePrepareBinding(record.Handle, input) {
			return Handle{}, ErrMismatch
		}
		stored, err := s.cipher.Decrypt(ctx, artifactAAD(record.Handle), record.Encrypted)
		if err != nil {
			return Handle{}, fmt.Errorf("decrypt stored signer artifact for replay: %w", err)
		}
		defer clear(stored)
		if len(stored) != len(input.Signature) || subtle.ConstantTimeCompare(stored, input.Signature) != 1 {
			return Handle{}, ErrMismatch
		}
		return record.Handle, nil
	}
	var id string
	for attempt := 0; attempt < 3; attempt++ {
		candidate, err := s.newID()
		if err != nil {
			return Handle{}, err
		}
		if _, collision := s.byID[candidate]; !collision {
			id = candidate
			break
		}
	}
	if id == "" {
		return Handle{}, errors.New("generate unique opaque prepared handle")
	}
	handle := Handle{
		ID: id, RequestID: input.RequestID, AuthorizationID: input.AuthorizationID,
		ReservationID: input.ReservationID, ActionID: input.ActionID, OperationID: input.OperationID,
		SignerRequestHash: input.SignerRequestHash, CanonicalPayloadHash: input.CanonicalPayloadHash,
		Digest: input.Digest, Nonce: input.Nonce,
		SignerKeyID: input.SignerKeyID, KeyEpoch: input.KeyEpoch, KeeperID: input.KeeperID,
		ValidAfter: input.ValidAfter, ValidUntil: input.ValidUntil, State: Prepared,
	}
	encrypted, err := s.cipher.Encrypt(ctx, artifactAAD(handle), input.Signature)
	if err != nil {
		return Handle{}, fmt.Errorf("encrypt signer artifact: %w", err)
	}
	record := signerRecord{Handle: handle, Encrypted: append([]byte(nil), encrypted...)}
	if err := s.persist(ctx, now, record); err != nil {
		return Handle{}, err
	}
	s.byID[id] = record
	s.byAction[actionKey] = id
	return handle, nil
}

// PreparedFor returns a durable exact replay without asking an HSM or signing
// engine to sign again. Only a still-valid PREPARED record is replayable.
func (s *SignerStore) PreparedFor(operationID, actionID, signerRequestHash string) (Handle, bool, error) {
	if !hash(operationID) || !identifier(actionID) || !hash(signerRequestHash) {
		return Handle{}, false, ErrTransition
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, exists := s.byAction[signerActionKey(operationID, actionID)]
	if !exists {
		return Handle{}, false, nil
	}
	record := s.byID[id]
	if record.Handle.SignerRequestHash != signerRequestHash {
		return Handle{}, true, ErrMismatch
	}
	if record.Handle.State != Prepared || !s.clock().UTC().Before(record.Handle.ValidUntil) {
		return Handle{}, true, ErrTransition
	}
	return record.Handle, true, nil
}

func (s *SignerStore) Activate(ctx context.Context, id string, proof ActivationProof) (Handle, error) {
	now := s.clock().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[id]
	if !ok {
		return Handle{}, ErrTransition
	}
	proof.ActivationOccurredAt = proof.ActivationOccurredAt.UTC()
	if record.Handle.State == Active || record.Handle.State == Released || record.Handle.State == Terminal {
		if record.Activation != nil && sameActivationProof(*record.Activation, proof) {
			return record.Handle, nil
		}
		return Handle{}, ErrMismatch
	}
	if record.Handle.State != Prepared || now.Before(record.Handle.ValidAfter) || !now.Before(record.Handle.ValidUntil) || !exactActivationProof(record.Handle, proof) {
		return Handle{}, ErrTransition
	}
	if err := s.verifier.VerifyActivation(ctx, record.Handle, proof); err != nil {
		return Handle{}, fmt.Errorf("verify signer activation: %w", err)
	}
	record.Handle.State = Active
	record.Activation = &proof
	if err := s.persist(ctx, now, record); err != nil {
		return Handle{}, err
	}
	s.byID[id] = record
	return record.Handle, nil
}

func (s *SignerStore) Release(ctx context.Context, id, keeperID string) (Handle, []byte, error) {
	now := s.clock().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[id]
	if !ok {
		return Handle{}, nil, ErrTransition
	}
	if keeperID != record.Handle.KeeperID {
		return Handle{}, nil, ErrKeeper
	}
	if (record.Handle.State != Active && record.Handle.State != Released) || now.Before(record.Handle.ValidAfter) || !now.Before(record.Handle.ValidUntil) {
		return Handle{}, nil, ErrTransition
	}
	artifact, err := s.cipher.Decrypt(ctx, artifactAAD(record.Handle), record.Encrypted)
	if err != nil {
		return Handle{}, nil, fmt.Errorf("decrypt signer artifact for release: %w", err)
	}
	if record.Handle.State == Active {
		record.Handle.State = Released
		if err := s.persist(ctx, now, record); err != nil {
			clear(artifact)
			return Handle{}, nil, err
		}
		s.byID[id] = record
	}
	return record.Handle, artifact, nil
}

func (s *SignerStore) Expire(ctx context.Context, id string) (Handle, error) {
	now := s.clock().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[id]
	if !ok || record.Handle.State != Prepared || now.Before(record.Handle.ValidUntil) {
		return Handle{}, ErrTransition
	}
	if err := s.verifier.ProveUnactivated(ctx, record.Handle, now); err != nil {
		return Handle{}, fmt.Errorf("prove signer handle unactivated: %w", err)
	}
	record.Handle.State = Expired
	record.Handle.Outcome = "EXPIRED_UNACTIVATED"
	if err := s.persist(ctx, now, record); err != nil {
		return Handle{}, err
	}
	s.byID[id] = record
	return record.Handle, nil
}

// ProveAndExpireUnactivated returns a signer-ledger proof bound to the exact
// control-plane request. A missing record is a valid negative signer result:
// no signature reached the durable prepare boundary. A matching PREPARED
// record is first independently proved unactivated and durably expired.
func (s *SignerStore) ProveAndExpireUnactivated(ctx context.Context, requestID, operationID, actionID, inputHash string) (UnactivatedProof, error) {
	if !hash(requestID) || !hash(operationID) || !identifier(actionID) || !hash(inputHash) {
		return UnactivatedProof{}, ErrTransition
	}
	now := s.clock().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	proof := UnactivatedProof{
		RequestID: requestID, OperationID: operationID, ActionID: actionID, InputHash: inputHash,
		Status: "EXPIRED_UNACTIVATED", ProvenAt: now,
	}
	id, exists := s.byAction[signerActionKey(operationID, actionID)]
	if exists {
		record := s.byID[id]
		if record.Handle.RequestID != requestID || record.Handle.OperationID != operationID ||
			record.Handle.SignerRequestHash != inputHash {
			return UnactivatedProof{}, ErrMismatch
		}
		if record.Handle.State == Expired {
			proof.HandleID = record.Handle.ID
		} else {
			if record.Handle.State != Prepared || now.Before(record.Handle.ValidUntil) {
				return UnactivatedProof{}, ErrTransition
			}
			if err := s.verifier.ProveUnactivated(ctx, record.Handle, now); err != nil {
				return UnactivatedProof{}, fmt.Errorf("prove signer handle unactivated: %w", err)
			}
			record.Handle.State = Expired
			record.Handle.Outcome = "EXPIRED_UNACTIVATED"
			if err := s.persist(ctx, now, record); err != nil {
				return UnactivatedProof{}, err
			}
			s.byID[id] = record
			proof.HandleID = record.Handle.ID
		}
	}
	digest, err := UnactivatedProofDigest(proof)
	if err != nil {
		return UnactivatedProof{}, err
	}
	proof.ProofDigest = digest
	return proof, nil
}

// Finalize retains ciphertext through the permanent replay horizon. Deletion
// is a separate retention ceremony, not a normal lifecycle transition.
func (s *SignerStore) Finalize(id, outcome string) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[id]
	if !ok || record.Handle.State != Released || outcome == "" || len(outcome) > 128 {
		return Handle{}, ErrTransition
	}
	record.Handle.State = Terminal
	record.Handle.Outcome = outcome
	if err := s.persist(context.Background(), s.clock().UTC(), record); err != nil {
		return Handle{}, err
	}
	s.byID[id] = record
	return record.Handle, nil
}

func (s *SignerStore) persist(ctx context.Context, at time.Time, record signerRecord) error {
	if s.journal == nil {
		return nil
	}
	if err := s.journal.Append(ctx, at, record); err != nil {
		return fmt.Errorf("persist signer ledger transition: %w", err)
	}
	return nil
}

func (s *SignerStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journal == nil {
		return nil
	}
	err := s.journal.Close()
	s.journal = nil
	return err
}

func (s *SignerStore) encryptedForTest(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.byID[id].Encrypted...)
}

func samePrepareBinding(handle Handle, input PrepareInput) bool {
	return handle.RequestID == input.RequestID && handle.AuthorizationID == input.AuthorizationID &&
		handle.ReservationID == input.ReservationID && handle.ActionID == input.ActionID && handle.OperationID == input.OperationID &&
		handle.SignerRequestHash == input.SignerRequestHash && handle.CanonicalPayloadHash == input.CanonicalPayloadHash && handle.Digest == input.Digest &&
		handle.Nonce == input.Nonce && handle.SignerKeyID == input.SignerKeyID && handle.KeyEpoch == input.KeyEpoch &&
		handle.KeeperID == input.KeeperID && handle.ValidAfter.Equal(input.ValidAfter) && handle.ValidUntil.Equal(input.ValidUntil)
}

func exactActivationProof(handle Handle, proof ActivationProof) bool {
	return proof.RequestID == handle.RequestID && proof.HandleID == handle.ID && proof.OperationID == handle.OperationID &&
		proof.Digest == handle.Digest && proof.Nonce == handle.Nonce && hash(proof.PrimaryMirrorDigest) &&
		!proof.ActivationOccurredAt.Before(handle.ValidAfter) && proof.ActivationOccurredAt.Before(handle.ValidUntil)
}

func sameActivationProof(left, right ActivationProof) bool {
	return left.RequestID == right.RequestID && left.HandleID == right.HandleID &&
		left.OperationID == right.OperationID && left.Digest == right.Digest && left.Nonce == right.Nonce &&
		left.PrimaryMirrorDigest == right.PrimaryMirrorDigest && left.ActivationOccurredAt.Equal(right.ActivationOccurredAt)
}

func artifactAAD(handle Handle) []byte {
	return []byte("ASCP_SIGNER_ARTIFACT_V2\n" + handle.ID + "\n" + handle.RequestID + "\n" + handle.AuthorizationID + "\n" +
		handle.ReservationID + "\n" + handle.ActionID + "\n" + handle.OperationID + "\n" +
		handle.SignerRequestHash + "\n" + handle.CanonicalPayloadHash + "\n" + handle.Digest + "\n" + handle.Nonce + "\n" + handle.SignerKeyID + "\n" +
		strconv.FormatUint(handle.KeyEpoch, 10) + "\n" + handle.KeeperID + "\n" +
		handle.ValidAfter.UTC().Format(time.RFC3339Nano) + "\n" + handle.ValidUntil.UTC().Format(time.RFC3339Nano))
}

func signerActionKey(operationID, actionID string) string { return operationID + "\n" + actionID }

func opaqueIDSource(random io.Reader) func() (string, error) {
	return func() (string, error) {
		bytes := make([]byte, 32)
		if _, err := io.ReadFull(random, bytes); err != nil {
			return "", fmt.Errorf("generate opaque prepared handle: %w", err)
		}
		return "asph_" + base64.RawURLEncoding.EncodeToString(bytes), nil
	}
}

type AESGCMCipher struct {
	aead   cipher.AEAD
	random io.Reader
}

func NewAESGCMCipher(key []byte, random io.Reader) (*AESGCMCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("signer artifact encryption key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(append([]byte(nil), key...))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	return &AESGCMCipher{aead: aead, random: random}, nil
}

func (c *AESGCMCipher) Encrypt(_ context.Context, aad, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (c *AESGCMCipher) Decrypt(_ context.Context, aad, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < c.aead.NonceSize()+c.aead.Overhead() {
		return nil, errors.New("encrypted signer artifact is truncated")
	}
	nonce := ciphertext[:c.aead.NonceSize()]
	return c.aead.Open(nil, nonce, ciphertext[c.aead.NonceSize():], aad)
}
