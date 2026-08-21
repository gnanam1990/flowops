package ascpkeeper

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type DecodedCall struct {
	Action              Action
	CanonicalPayload    []byte
	AuthorizationDigest string
	Signature           []byte
}

// CallDecoder must ABI-decode the exact configured contract selector and
// return the action-specific payload bytes in their canonical encoding.
type CallDecoder interface {
	Decode(context.Context, Action, []byte) (DecodedCall, error)
}

type ExactBindingVerifier struct{ decoder CallDecoder }

func NewExactBindingVerifier(decoder CallDecoder) (*ExactBindingVerifier, error) {
	if decoder == nil {
		return nil, ErrInvalidConfig
	}
	return &ExactBindingVerifier{decoder: decoder}, nil
}

func (v *ExactBindingVerifier) Verify(ctx context.Context, job Job, tx UnsignedTransaction, artifact []byte) error {
	decoded, err := v.decoder.Decode(ctx, job.Action, tx.Data)
	if err != nil {
		return err
	}
	if decoded.Action != job.Action || !bytes.Equal(decoded.CanonicalPayload, job.CanonicalPayload) ||
		canonicalPayloadHash(decoded.CanonicalPayload) != job.CanonicalPayloadHash {
		return ErrInvalidTransaction
	}
	if !job.RequiresBearer() {
		if len(artifact) != 0 || len(decoded.Signature) != 0 || decoded.AuthorizationDigest != "" {
			return ErrInvalidTransaction
		}
		return nil
	}
	if decoded.AuthorizationDigest != job.AuthorizationDigest || !bytes.Equal(decoded.Signature, artifact) || len(artifact) != 65 {
		return ErrInvalidTransaction
	}
	signature := append([]byte(nil), artifact...)
	defer clear(signature)
	if signature[64] >= 27 {
		signature[64] -= 27
	}
	if signature[64] > 1 {
		return ErrInvalidTransaction
	}
	if !crypto.ValidateSignatureValues(signature[64], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:64]), true) {
		return ErrInvalidTransaction
	}
	publicKey, err := crypto.SigToPub(common.HexToHash(job.AuthorizationDigest).Bytes(), signature)
	if err != nil {
		return errors.Join(ErrInvalidTransaction, err)
	}
	recovered := strings.ToLower(crypto.PubkeyToAddress(*publicKey).Hex())
	if recovered != job.SignerAddress {
		return ErrInvalidTransaction
	}
	return nil
}
