// Package safegovernance constructs and verifies the exact Safe transaction
// used for an approved FlowOps governance command. It supports only canonical
// EIP-712 EOA owner signatures; contract-owner and approved-hash signatures
// are deliberately rejected until they have an independently reviewed proof
// adapter.
package safegovernance

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	OperationCall uint8 = 0
	maxDataBytes        = 64 * 1024
)

var (
	ErrInvalidTransaction = errors.New("invalid Safe governance transaction")
	ErrInvalidOwners      = errors.New("invalid Safe owner snapshot")
	ErrInvalidSignatures  = errors.New("invalid Safe owner signatures")

	domainTypeHash = crypto.Keccak256Hash([]byte("EIP712Domain(uint256 chainId,address verifyingContract)"))
	safeTxTypeHash = crypto.Keccak256Hash([]byte("SafeTx(address to,uint256 value,bytes data,uint8 operation,uint256 safeTxGas,uint256 baseGas,uint256 gasPrice,address gasToken,address refundReceiver,uint256 nonce)"))
	execSelector   = crypto.Keccak256([]byte("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)"))[:4]
)

type Transaction struct {
	ChainID        uint64 `json:"chainId"`
	Safe           string `json:"safe"`
	To             string `json:"to"`
	Value          string `json:"value"`
	Data           []byte `json:"data"`
	Operation      uint8  `json:"operation"`
	SafeTxGas      string `json:"safeTxGas"`
	BaseGas        string `json:"baseGas"`
	GasPrice       string `json:"gasPrice"`
	GasToken       string `json:"gasToken"`
	RefundReceiver string `json:"refundReceiver"`
	Nonce          uint64 `json:"nonce"`
}

type OwnerSnapshot struct {
	Owners    []string `json:"owners"`
	Threshold int      `json:"threshold"`
}

// NewTransaction returns the sole FlowOps governance Safe transaction shape.
// All Safe refund fields are zero and CALL is the only operation; the outer
// relay account pays gas independently.
func NewTransaction(chainID uint64, safe, target string, data []byte, nonce uint64) (Transaction, error) {
	tx := Transaction{
		ChainID: chainID, Safe: safe, To: target, Value: "0", Data: append([]byte(nil), data...),
		Operation: OperationCall, SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
		GasToken: zeroAddress(), RefundReceiver: zeroAddress(), Nonce: nonce,
	}
	if err := tx.Validate(); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}

func (t Transaction) Validate() error {
	if t.ChainID != 8453 && t.ChainID != 84532 || !canonicalAddress(t.Safe, false) ||
		!canonicalAddress(t.To, false) || t.Safe == t.To || t.Value != "0" || len(t.Data) < 4 ||
		len(t.Data) > maxDataBytes || t.Operation != OperationCall || t.SafeTxGas != "0" ||
		t.BaseGas != "0" || t.GasPrice != "0" || t.GasToken != zeroAddress() ||
		t.RefundReceiver != zeroAddress() {
		return ErrInvalidTransaction
	}
	return nil
}

func (t Transaction) Hash() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	domainArgs, err := wordArguments("bytes32", "uint256", "address")
	if err != nil {
		return "", err
	}
	domain, err := domainArgs.Pack(domainTypeHash, new(big.Int).SetUint64(t.ChainID), common.HexToAddress(t.Safe))
	if err != nil {
		return "", err
	}
	txArgs, err := wordArguments("bytes32", "address", "uint256", "bytes32", "uint8", "uint256", "uint256", "uint256", "address", "address", "uint256")
	if err != nil {
		return "", err
	}
	encoded, err := txArgs.Pack(
		safeTxTypeHash, common.HexToAddress(t.To), big.NewInt(0), crypto.Keccak256Hash(t.Data), t.Operation,
		big.NewInt(0), big.NewInt(0), big.NewInt(0), common.Address{}, common.Address{}, new(big.Int).SetUint64(t.Nonce),
	)
	if err != nil {
		return "", err
	}
	domainHash, txHash := crypto.Keccak256Hash(domain), crypto.Keccak256Hash(encoded)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domainHash[:], txHash[:]).Hex(), nil
}

// ExecCalldata builds Safe.execTransaction calldata and rejects signature
// bundles that the exact finalized owner snapshot would not accept.
func (t Transaction) ExecCalldata(snapshot OwnerSnapshot, signatures []byte) ([]byte, error) {
	digest, err := t.Hash()
	if err != nil {
		return nil, err
	}
	if err := VerifySignatures(digest, snapshot, signatures); err != nil {
		return nil, err
	}
	args, err := execArguments()
	if err != nil {
		return nil, err
	}
	encoded, err := args.Pack(
		common.HexToAddress(t.To), big.NewInt(0), t.Data, t.Operation, big.NewInt(0), big.NewInt(0),
		big.NewInt(0), common.Address{}, common.Address{}, signatures,
	)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), execSelector...), encoded...), nil
}

// VerifyExecCalldata independently ABI-decodes and re-encodes the outer Safe
// call. Trailing bytes, offset tricks, changed signatures, and any inner-field
// substitution fail closed.
func (t Transaction) VerifyExecCalldata(snapshot OwnerSnapshot, calldata []byte) error {
	signatures, err := SignaturesFromExecCalldata(calldata)
	if err != nil {
		return err
	}
	want, err := t.ExecCalldata(snapshot, signatures)
	if err != nil || !bytes.Equal(want, calldata) {
		return errors.Join(ErrInvalidTransaction, err)
	}
	return nil
}

// SignaturesFromExecCalldata performs the strict ABI decode needed by durable
// relay code to bind a released artifact to its persisted signature digest.
func SignaturesFromExecCalldata(calldata []byte) ([]byte, error) {
	if len(calldata) < 4 || !bytes.Equal(calldata[:4], execSelector) {
		return nil, ErrInvalidTransaction
	}
	args, err := execArguments()
	if err != nil {
		return nil, err
	}
	values, err := args.Unpack(calldata[4:])
	if err != nil || len(values) != 10 {
		return nil, ErrInvalidTransaction
	}
	signatures, ok := values[9].([]byte)
	if !ok {
		return nil, ErrInvalidTransaction
	}
	return append([]byte(nil), signatures...), nil
}

func VerifySignatures(digest string, snapshot OwnerSnapshot, signatures []byte) error {
	owners, err := canonicalOwners(snapshot)
	if err != nil || !canonicalHash(digest) || len(signatures) != snapshot.Threshold*65 {
		return errors.Join(ErrInvalidSignatures, err)
	}
	digestBytes, err := hex.DecodeString(strings.TrimPrefix(digest, "0x"))
	if err != nil || len(digestBytes) != 32 {
		return ErrInvalidSignatures
	}
	ownerSet := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		ownerSet[owner] = struct{}{}
	}
	last := ""
	for offset := 0; offset < len(signatures); offset += 65 {
		signature := append([]byte(nil), signatures[offset:offset+65]...)
		v := signature[64]
		if v != 27 && v != 28 {
			return ErrInvalidSignatures
		}
		signature[64] -= 27
		if !crypto.ValidateSignatureValues(signature[64], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:64]), true) {
			return ErrInvalidSignatures
		}
		publicKey, recoverErr := crypto.SigToPub(digestBytes, signature)
		clear(signature)
		if recoverErr != nil {
			return errors.Join(ErrInvalidSignatures, recoverErr)
		}
		owner := strings.ToLower(crypto.PubkeyToAddress(*publicKey).Hex())
		if _, ok := ownerSet[owner]; !ok || last != "" && owner <= last {
			return ErrInvalidSignatures
		}
		last = owner
	}
	return nil
}

func canonicalOwners(snapshot OwnerSnapshot) ([]string, error) {
	if snapshot.Threshold < 1 || snapshot.Threshold > len(snapshot.Owners) || len(snapshot.Owners) > 50 {
		return nil, ErrInvalidOwners
	}
	owners := append([]string(nil), snapshot.Owners...)
	for index, owner := range owners {
		if !canonicalAddress(owner, false) || index > 0 && owners[index-1] >= owner {
			return nil, ErrInvalidOwners
		}
	}
	return owners, nil
}

func execArguments() (abi.Arguments, error) {
	return wordArguments("address", "uint256", "bytes", "uint8", "uint256", "uint256", "uint256", "address", "address", "bytes")
}

func wordArguments(names ...string) (abi.Arguments, error) {
	arguments := make(abi.Arguments, len(names))
	for index, name := range names {
		type_, err := abi.NewType(name, "", nil)
		if err != nil {
			return nil, fmt.Errorf("create ABI type %s: %w", name, err)
		}
		arguments[index] = abi.Argument{Type: type_}
	}
	return arguments, nil
}

func CanonicalOwners(owners []string) []string {
	result := append([]string(nil), owners...)
	sort.Strings(result)
	return result
}

func canonicalHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil && value != "0x"+strings.Repeat("0", 64)
}

func canonicalAddress(value string, allowZero bool) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) || !common.IsHexAddress(value) {
		return false
	}
	return allowZero || common.HexToAddress(value) != (common.Address{})
}

func zeroAddress() string { return "0x" + strings.Repeat("0", 40) }
