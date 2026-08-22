package ascpadaptation

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpring6"
)

type hsmStub struct {
	result ascpring6.HSMResult
	err    error
	input  ascpring6.HSMRequest
}

func (s *hsmStub) Sign(_ context.Context, request ascpring6.HSMRequest) (ascpring6.HSMResult, error) {
	s.input = request
	if s.err != nil {
		return ascpring6.HSMResult{}, s.err
	}
	return s.result, nil
}

func TestHSMSignerBindsIdempotencyDigestAndRecoversConfiguredKey(t *testing.T) {
	key, err := crypto.HexToECDSA(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	digest := crypto.Keccak256([]byte("adaptation"))
	signature, err := crypto.Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	stub := &hsmStub{result: ascpring6.HSMResult{OperationHandle: "adaptation_hsm_1", Signature: signature}}
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	signer, err := NewHSMSigner(stub, "adaptation_key_1", 3, address)
	if err != nil {
		t.Fatal(err)
	}
	stub.result.Digest = "0x" + strings.ToLower(hex.EncodeToString(digest))
	got, err := signer.SignDigest(t.Context(), digest)
	if err != nil || len(got) != crypto.SignatureLength || stub.input.IdempotencyKey != stub.input.Digest || stub.input.KeyID != "adaptation_key_1" || stub.input.KeyEpoch != 3 {
		t.Fatalf("signature=%x request=%+v err=%v", got, stub.input, err)
	}
	clear(got)

	stub.result.Signature = append([]byte(nil), signature...)
	stub.result.Digest = "0x" + strings.Repeat("f", 64)
	if _, err := signer.SignDigest(t.Context(), digest); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("wrong digest error=%v", err)
	}
	wrong, _ := crypto.HexToECDSA(strings.Repeat("b", 64))
	stub.result.Digest = "0x" + hex.EncodeToString(digest)
	stub.result.Signature, _ = crypto.Sign(digest, wrong)
	if _, err := signer.SignDigest(t.Context(), digest); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("wrong signer error=%v", err)
	}
}
