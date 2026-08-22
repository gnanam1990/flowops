package safegovernance

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestSafeTransactionHashSignaturesAndExactExecCalldata(t *testing.T) {
	keys := testKeys(t, 3)
	owners := ownerAddresses(keys)
	tx, err := NewTransaction(84532, "0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222", []byte{0xde, 0xad, 0xbe, 0xef, 1}, 7)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tx.Hash()
	if err != nil {
		t.Fatal(err)
	}
	// Independently generated with Foundry cast ABI encoding and keccak, not
	// with this package's helpers. This pins the deployed Safe EIP-712 layout.
	if digest != "0xf09dbfcba941f691b3811ab82b19691c4f70ba429953f0796a0ed7c0bc7e034c" {
		t.Fatalf("Safe EIP-712 golden digest=%s", digest)
	}
	signatures := signSorted(t, digest, keys[:2])
	snapshot := OwnerSnapshot{Owners: owners, Threshold: 2}
	calldata, err := tx.ExecCalldata(snapshot, signatures)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.VerifyExecCalldata(snapshot, calldata); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(calldata[:4]) != "6a761202" {
		t.Fatalf("Safe execTransaction selector=%x", calldata[:4])
	}
	if got := crypto.Keccak256Hash(calldata).Hex(); got == "0x"+strings.Repeat("0", 64) {
		t.Fatal("exec calldata hash was zero")
	}

	mutations := map[string]func([]byte) []byte{
		"selector":      func(value []byte) []byte { value[0] ^= 1; return value },
		"inner target":  func(value []byte) []byte { value[4+31] ^= 1; return value },
		"trailing byte": func(value []byte) []byte { return append(value, 0) },
		"signature":     func(value []byte) []byte { value[len(value)-65] ^= 1; return value },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := mutate(append([]byte(nil), calldata...))
			if bytes.Equal(changed, calldata) {
				t.Fatal("mutation was ineffective")
			}
			if err := tx.VerifyExecCalldata(snapshot, changed); err == nil {
				t.Fatal("mutated Safe calldata was accepted")
			}
		})
	}
}

func TestSafeSignaturesRejectWrongOwnerOrderThresholdAndModes(t *testing.T) {
	keys := testKeys(t, 4)
	owners := ownerAddresses(keys[:3])
	tx, err := NewTransaction(8453, "0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222", []byte{1, 2, 3, 4}, 1)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := tx.Hash()
	valid := signSorted(t, digest, keys[:2])
	snapshot := OwnerSnapshot{Owners: owners, Threshold: 2}
	if err := VerifySignatures(digest, snapshot, valid); err != nil {
		t.Fatal(err)
	}

	wrongOwner := signSorted(t, digest, []*ecdsa.PrivateKey{keys[0], keys[3]})
	reversed := append(append([]byte(nil), valid[65:]...), valid[:65]...)
	approvedHashMode := append([]byte(nil), valid...)
	approvedHashMode[64] = 1
	highS := append([]byte(nil), valid...)
	for index := 32; index < 64; index++ {
		highS[index] = 0xff
	}
	for name, candidate := range map[string][]byte{
		"wrong owner": wrongOwner, "wrong order": reversed, "approved hash mode": approvedHashMode,
		"high s": highS, "extra signature": append(append([]byte(nil), valid...), valid[:65]...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifySignatures(digest, snapshot, candidate); err == nil {
				t.Fatal("unsafe signatures were accepted")
			}
		})
	}
}

func TestSafeTransactionRejectsMutableRefundAndDomainFields(t *testing.T) {
	tx, err := NewTransaction(84532, "0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222", []byte{1, 2, 3, 4}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Transaction){
		"chain":        func(value *Transaction) { value.ChainID = 1 },
		"safe":         func(value *Transaction) { value.Safe = value.To },
		"value":        func(value *Transaction) { value.Value = "1" },
		"delegatecall": func(value *Transaction) { value.Operation = 1 },
		"safe gas":     func(value *Transaction) { value.SafeTxGas = "1" },
		"refund":       func(value *Transaction) { value.RefundReceiver = "0x3333333333333333333333333333333333333333" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := tx
			mutate(&changed)
			if _, err := changed.Hash(); err == nil {
				t.Fatal("unsafe transaction was accepted")
			}
		})
	}
}

func testKeys(t *testing.T, count int) []*ecdsa.PrivateKey {
	t.Helper()
	keys := make([]*ecdsa.PrivateKey, count)
	for index := range keys {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[index] = key
	}
	return keys
}

func ownerAddresses(keys []*ecdsa.PrivateKey) []string {
	owners := make([]string, len(keys))
	for index, key := range keys {
		owners[index] = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	}
	return CanonicalOwners(owners)
}

func signSorted(t *testing.T, digest string, keys []*ecdsa.PrivateKey) []byte {
	t.Helper()
	type pair struct {
		owner string
		bytes []byte
	}
	pairs := make([]pair, len(keys))
	for index, key := range keys {
		signature, err := crypto.Sign(commonHash(t, digest), key)
		if err != nil {
			t.Fatal(err)
		}
		signature[64] += 27
		pairs[index] = pair{owner: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), bytes: signature}
	}
	for left := 0; left < len(pairs); left++ {
		for right := left + 1; right < len(pairs); right++ {
			if pairs[right].owner < pairs[left].owner {
				pairs[left], pairs[right] = pairs[right], pairs[left]
			}
		}
	}
	var result []byte
	for _, item := range pairs {
		result = append(result, item.bytes...)
	}
	return result
}

func commonHash(t *testing.T, digest string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(strings.TrimPrefix(digest, "0x"))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("decode digest: %v", err)
	}
	return decoded
}
