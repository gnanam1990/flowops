package ascpverifier

import (
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const VerdictAttestationTypeString = "VerdictAttestation(bytes32 callId,bytes32 commitmentHash,address escrowContract,uint64 verifierEpoch,bytes32 verificationSpecHash,bytes32 verifierSoftwareHash,bytes32 deliveryHash,uint64 deliveredAt,bytes32 evidenceHash,uint8 verdict,uint256 verdictNonce,uint64 issuedAt,uint64 validUntil)"

const (
	ContractVerdictRelease     uint8 = 1
	ContractVerdictEarlyRefund uint8 = 2
	MaximumAttestationWindow         = 15 * 60
)

var (
	ErrInvalidAttestation = errors.New("invalid verdict attestation")
	attestationTypeHash   = crypto.Keccak256Hash([]byte(VerdictAttestationTypeString))
	domainTypeHash        = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash              = crypto.Keccak256Hash([]byte("ASCP"))
	versionHash           = crypto.Keccak256Hash([]byte("4"))
	decimalPattern        = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
)

type Attestation struct {
	CallID               string `json:"callId"`
	CommitmentHash       string `json:"commitmentHash"`
	EscrowContract       string `json:"escrowContract"`
	VerifierEpoch        uint64 `json:"verifierEpoch"`
	VerificationSpecHash string `json:"verificationSpecHash"`
	VerifierSoftwareHash string `json:"verifierSoftwareHash"`
	DeliveryHash         string `json:"deliveryHash"`
	DeliveredAt          uint64 `json:"deliveredAt"`
	EvidenceHash         string `json:"evidenceHash"`
	Verdict              uint8  `json:"verdict"`
	VerdictNonce         string `json:"verdictNonce"`
	IssuedAt             uint64 `json:"issuedAt"`
	ValidUntil           uint64 `json:"validUntil"`
}

func (a Attestation) Digest(chainID string) (common.Hash, error) {
	if !canonicalHash(a.CallID, true) || !canonicalHash(a.CommitmentHash, true) || !canonicalAddress(a.EscrowContract) ||
		a.VerifierEpoch == 0 || !canonicalHash(a.VerificationSpecHash, true) || !canonicalHash(a.VerifierSoftwareHash, true) ||
		!canonicalHash(a.DeliveryHash, true) || a.DeliveredAt == 0 || !canonicalHash(a.EvidenceHash, true) ||
		(a.Verdict != ContractVerdictRelease && a.Verdict != ContractVerdictEarlyRefund) || a.IssuedAt == 0 ||
		a.DeliveredAt > a.IssuedAt || a.ValidUntil <= a.IssuedAt || a.ValidUntil-a.IssuedAt > MaximumAttestationWindow {
		return common.Hash{}, ErrInvalidAttestation
	}
	nonce, err := parseUint256(a.VerdictNonce, false)
	if err != nil {
		return common.Hash{}, ErrInvalidAttestation
	}
	chain, err := parseUint256(chainID, true)
	if err != nil {
		return common.Hash{}, ErrInvalidAttestation
	}
	structHash := crypto.Keccak256Hash(
		attestationTypeHash[:], hashWord(a.CallID), hashWord(a.CommitmentHash), addressWord(a.EscrowContract),
		uintWord(new(big.Int).SetUint64(a.VerifierEpoch)), hashWord(a.VerificationSpecHash), hashWord(a.VerifierSoftwareHash),
		hashWord(a.DeliveryHash), uintWord(new(big.Int).SetUint64(a.DeliveredAt)), hashWord(a.EvidenceHash),
		uintWord(new(big.Int).SetUint64(uint64(a.Verdict))), uintWord(nonce), uintWord(new(big.Int).SetUint64(a.IssuedAt)),
		uintWord(new(big.Int).SetUint64(a.ValidUntil)),
	)
	domain := crypto.Keccak256Hash(
		domainTypeHash[:], nameHash[:], versionHash[:], uintWord(chain), addressWord(a.EscrowContract),
	)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domain[:], structHash[:]), nil
}

func canonicalHash(value string, nonZero bool) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 32 {
		return false
	}
	return !nonZero || common.BytesToHash(decoded) != (common.Hash{})
}

func canonicalAddress(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && value == strings.ToLower(value) &&
		common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func parseUint256(value string, positive bool) (*big.Int, error) {
	if !decimalPattern.MatchString(value) {
		return nil, ErrInvalidAttestation
	}
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok || integer.BitLen() > 256 || positive && integer.Sign() == 0 {
		return nil, ErrInvalidAttestation
	}
	return integer, nil
}

func hashWord(value string) []byte { return common.HexToHash(value).Bytes() }
func addressWord(value string) []byte {
	return common.LeftPadBytes(common.HexToAddress(value).Bytes(), 32)
}
func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }
