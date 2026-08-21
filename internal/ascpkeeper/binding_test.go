package ascpkeeper

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type decoderFixture struct{ decoded DecodedCall }

func (f *decoderFixture) Decode(context.Context, Action, []byte) (DecodedCall, error) {
	return f.decoded, nil
}

func TestExactBindingVerifierRecoversSignerAndRejectsSubstitution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	jobInput := signedInput(now)
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	jobInput.SignerAddress = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	jobInput.AuthorizationDigest = testHash(500)
	signature, err := crypto.Sign(common.HexToHash(jobInput.AuthorizationDigest).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{JobID: jobInput.JobID, OperationID: jobInput.OperationID, OrganizationID: jobInput.OrganizationID, Action: jobInput.Action,
		ChainID: jobInput.ChainID, KeeperID: jobInput.KeeperID, GasPayer: jobInput.GasPayer, Target: jobInput.Target, ValueWei: jobInput.ValueWei,
		CanonicalPayload: jobInput.CanonicalPayload, CanonicalPayloadHash: jobInput.CanonicalPayloadHash, AuthorizationDigest: jobInput.AuthorizationDigest,
		SignerHandle: jobInput.SignerHandle, SignerAddress: jobInput.SignerAddress, ValidAfter: jobInput.ValidAfter, ValidBefore: jobInput.ValidBefore,
		EligibleAfter: jobInput.EligibleAfter, LeadershipEpoch: jobInput.LeadershipEpoch}
	decoder := &decoderFixture{decoded: DecodedCall{Action: job.Action, CanonicalPayload: append([]byte(nil), job.CanonicalPayload...), AuthorizationDigest: job.AuthorizationDigest, Signature: signature}}
	verifier, _ := NewExactBindingVerifier(decoder)
	tx := UnsignedTransaction{Data: []byte{1, 2, 3, 4}}
	if err := verifier.Verify(context.Background(), job, tx, signature); err != nil {
		t.Fatal(err)
	}
	decoder.decoded.CanonicalPayload = []byte("substituted")
	if err := verifier.Verify(context.Background(), job, tx, signature); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("payload substitution error=%v", err)
	}
	decoder.decoded.CanonicalPayload = job.CanonicalPayload
	decoder.decoded.Signature = append([]byte(nil), signature...)
	decoder.decoded.Signature[0] ^= 1
	if err := verifier.Verify(context.Background(), job, tx, signature); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("signature substitution error=%v", err)
	}
}
