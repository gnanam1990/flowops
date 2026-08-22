// Package ascpadaptationsigner adapts the isolated Ring 6 HSM boundary to the
// adaptation grant issuer. It is intentionally separate from the grant model
// consumed by agent-facing code.
package ascpadaptationsigner

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpring6"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

type idempotentHSM interface {
	Sign(context.Context, ascpring6.HSMRequest) (ascpring6.HSMResult, error)
}

// HSMSigner binds one adaptation signing key and validates every HSM response
// before the signature can enter the grant store.
type HSMSigner struct {
	hsm           idempotentHSM
	keyID         string
	keyEpoch      uint64
	signerAddress string
}

func New(hsm idempotentHSM, keyID string, keyEpoch uint64, signerAddress string) (*HSMSigner, error) {
	if hsm == nil || !identifier(keyID) || keyEpoch == 0 || !canonicalSigner(signerAddress) {
		return nil, errors.New("adaptation HSM signer configuration is invalid")
	}
	return &HSMSigner{hsm: hsm, keyID: keyID, keyEpoch: keyEpoch, signerAddress: signerAddress}, nil
}

func (s *HSMSigner) SignDigest(ctx context.Context, digest []byte) ([]byte, error) {
	if len(digest) != common.HashLength {
		return nil, ascpadaptation.ErrInvalidGrant
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
		return nil, ascpadaptation.ErrInvalidGrant
	}
	signature := append([]byte(nil), result.Signature...)
	clear(result.Signature)
	if !validSignature(signature) {
		clear(signature)
		return nil, ascpadaptation.ErrInvalidGrant
	}
	publicKey, err := crypto.SigToPub(digest, signature)
	if err != nil || strings.ToLower(crypto.PubkeyToAddress(*publicKey).Hex()) != s.signerAddress {
		clear(signature)
		return nil, ascpadaptation.ErrInvalidGrant
	}
	return signature, nil
}

func validSignature(signature []byte) bool {
	return len(signature) == crypto.SignatureLength && signature[64] <= 1 &&
		crypto.ValidateSignatureValues(signature[64], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:64]), true)
}

func canonicalSigner(value string) bool {
	return len(value) == 42 && value == strings.ToLower(value) && common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func identifier(value string) bool { return len(value) <= 128 && envelope.ValidIdentifier(value) }
