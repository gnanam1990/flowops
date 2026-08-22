package ascpadaptation

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpring6"
)

type idempotentHSM interface {
	Sign(context.Context, ascpring6.HSMRequest) (ascpring6.HSMResult, error)
}

// HSMSigner adapts the isolated Ring 6 HSM protocol for the platform-owned
// adaptation key. The key never enters the control-plane process. The digest
// itself is the stable HSM idempotency key, and every response is recovered
// against the configured public address before it can be persisted.
type HSMSigner struct {
	hsm           idempotentHSM
	keyID         string
	keyEpoch      uint64
	signerAddress string
}

func NewHSMSigner(hsm idempotentHSM, keyID string, keyEpoch uint64, signerAddress string) (*HSMSigner, error) {
	if hsm == nil || !identifier(keyID) || keyEpoch == 0 || !canonicalSigner(signerAddress) {
		return nil, errors.New("adaptation HSM signer configuration is invalid")
	}
	return &HSMSigner{hsm: hsm, keyID: keyID, keyEpoch: keyEpoch, signerAddress: signerAddress}, nil
}

func (s *HSMSigner) SignDigest(ctx context.Context, digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, ErrInvalidGrant
	}
	digestHex := "0x" + hex.EncodeToString(digest)
	result, err := s.hsm.Sign(ctx, ascpring6.HSMRequest{
		IdempotencyKey: digestHex, KeyID: s.keyID, KeyEpoch: s.keyEpoch, Digest: digestHex,
	})
	if err != nil {
		clear(result.Signature)
		return nil, err
	}
	if !identifier(result.OperationHandle) || result.Digest != digestHex {
		clear(result.Signature)
		return nil, ErrInvalidGrant
	}
	signature := append([]byte(nil), result.Signature...)
	clear(result.Signature)
	if len(signature) != crypto.SignatureLength {
		clear(signature)
		return nil, ErrInvalidGrant
	}
	decoded, err := decodeSignature("0x" + hex.EncodeToString(signature))
	if err != nil {
		clear(signature)
		return nil, err
	}
	publicKey, err := crypto.SigToPub(digest, decoded)
	clear(decoded)
	if err != nil || strings.ToLower(crypto.PubkeyToAddress(*publicKey).Hex()) != s.signerAddress {
		clear(signature)
		return nil, ErrInvalidGrant
	}
	return signature, nil
}
