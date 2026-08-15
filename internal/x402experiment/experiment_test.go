package x402experiment

import (
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	x402 "github.com/x402-foundation/x402/go/v2"
)

var fixedNow = time.Now().UTC().Truncate(time.Second)

func prepared(t *testing.T) (Preparation, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	prep, err := Prepare(fixedNow, crypto.PubkeyToAddress(key.PublicKey).Hex(), "0xC2f0967C4Df966636E4Ac1dad40abdA65536cbb6", "flowops_evidence", "flowops_client", zeroReader{})
	if err != nil {
		t.Fatal(err)
	}
	return prep, key
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) { clear(buffer); return len(buffer), nil }

func signPreparation(t *testing.T, prep Preparation, key *ecdsa.PrivateKey) Preparation {
	t.Helper()
	hash, err := typedDataHash(prep.TypedData)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(hash, key)
	if err != nil {
		t.Fatal(err)
	}
	prep.Signature = "0x" + hex.EncodeToString(signature)
	return prep
}

func TestPrepareAndVerifySignature(t *testing.T) {
	prep, key := prepared(t)
	if prep.Requirements.Amount != AmountAtomic || len(prep.Authorization.Nonce) != 66 {
		t.Fatalf("unexpected preparation: %+v", prep)
	}
	prep = signPreparation(t, prep, key)
	if err := VerifySignature(prep); err != nil {
		t.Fatal(err)
	}
}

func TestValidationRejectsEveryLoadBearingMutation(t *testing.T) {
	tests := map[string]func(*Preparation){
		"digest":               func(p *Preparation) { p.PreparationDigest = "0x00" },
		"amount":               func(p *Preparation) { p.Requirements.Amount = "1001" },
		"payee":                func(p *Preparation) { p.Requirements.PayTo = p.Payer },
		"network":              func(p *Preparation) { p.Requirements.Network = "eip155:8453" },
		"asset":                func(p *Preparation) { p.Requirements.Asset = p.Payer },
		"authorization amount": func(p *Preparation) { p.Authorization.Value = "1001" },
		"authorization payer":  func(p *Preparation) { p.Authorization.From = p.Payee },
		"authorization window": func(p *Preparation) { p.Authorization.ValidBefore = "9999999999" },
		"authorization nonce":  func(p *Preparation) { p.Authorization.Nonce = "0x01" },
		"app extension":        func(p *Preparation) { p.Extensions["builder-code"] = map[string]interface{}{} },
		"transfer method":      func(p *Preparation) { p.Requirements.Extra["assetTransferMethod"] = "permit2" },
		"typed data":           func(p *Preparation) { p.TypedData.Message["value"] = "1001" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			prep, _ := prepared(t)
			mutate(&prep)
			if err := Validate(prep, fixedNow); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
}

func TestWrongPayerSignatureIsRejected(t *testing.T) {
	prep, _ := prepared(t)
	wrong, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	prep = signPreparation(t, prep, wrong)
	if err := VerifySignature(prep); err == nil {
		t.Fatal("wrong signer accepted")
	}
}

type mockSettlementClient struct {
	verifyCalls int
	settleCalls int
	verify      *x402.VerifyResponse
	settle      *x402.SettleResponse
	err         error
}

func (m *mockSettlementClient) Verify(context.Context, []byte, []byte) (*x402.VerifyResponse, error) {
	m.verifyCalls++
	if m.err != nil {
		return nil, m.err
	}
	return m.verify, nil
}
func (m *mockSettlementClient) Settle(context.Context, []byte, []byte) (*x402.SettleResponse, error) {
	m.settleCalls++
	if m.err != nil {
		return nil, m.err
	}
	return m.settle, nil
}

func TestExecuteRequiresConfirmationBeforeFacilitatorCall(t *testing.T) {
	prep, key := prepared(t)
	prep = signPreparation(t, prep, key)
	client := &mockSettlementClient{}
	if _, err := Execute(context.Background(), prep, "", client); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	if client.verifyCalls != 0 || client.settleCalls != 0 {
		t.Fatal("facilitator called before confirmation")
	}
}

func TestExecuteVerifiesThenSettlesExactPayload(t *testing.T) {
	prep, key := prepared(t)
	prep = signPreparation(t, prep, key)
	client := &mockSettlementClient{
		verify: &x402.VerifyResponse{IsValid: true, Payer: prep.Payer},
		settle: &x402.SettleResponse{Success: true, Payer: prep.Payer, Transaction: common.HexToHash("0x1234").Hex(), Network: x402.Network(Network)},
	}
	result, err := Execute(context.Background(), prep, ConfirmationWord(), client)
	if err != nil {
		t.Fatal(err)
	}
	if result.Settlement.Transaction != common.HexToHash("0x1234").Hex() || client.verifyCalls != 1 || client.settleCalls != 1 {
		t.Fatalf("result=%+v calls=%d/%d", result, client.verifyCalls, client.settleCalls)
	}
}

func TestExecuteDoesNotSettleFailedVerification(t *testing.T) {
	prep, key := prepared(t)
	prep = signPreparation(t, prep, key)
	client := &mockSettlementClient{verify: &x402.VerifyResponse{IsValid: false, Payer: prep.Payer}}
	if _, err := Execute(context.Background(), prep, ConfirmationWord(), client); err == nil {
		t.Fatal("invalid verification accepted")
	}
	if client.settleCalls != 0 {
		t.Fatal("settle called after invalid verification")
	}
}

type fakeChain struct {
	tx      *types.Transaction
	receipt *types.Receipt
	err     error
}

func (f fakeChain) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return f.tx, false, f.err
}
func (f fakeChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return f.receipt, f.err
}

func TestInspectRequiresQuorumExactTransferAndBuilderSuffix(t *testing.T) {
	prep, _ := prepared(t)
	facilitator, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte{0xde, 0xad, 0xbe, 0xef}, builderSuffix(prep.AppCode, prep.ServiceCode, "facilitator")...)
	tx, err := types.SignNewTx(facilitator, types.LatestSignerForChainID(big.NewInt(ChainID)), &types.DynamicFeeTx{ChainID: big.NewInt(ChainID), Nonce: 1, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 200000, To: ptrAddress(common.HexToAddress(Asset)), Data: data})
	if err != nil {
		t.Fatal(err)
	}
	blockHash := common.HexToHash("0xbeef")
	receipt := &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: tx.Hash(), BlockHash: blockHash, BlockNumber: big.NewInt(100), Logs: []*types.Log{{Address: common.HexToAddress(Asset), Topics: []common.Hash{transferTopic, common.BytesToHash(common.HexToAddress(prep.Payer).Bytes()), common.BytesToHash(common.HexToAddress(prep.Payee).Bytes())}, Data: common.LeftPadBytes(big.NewInt(1000).Bytes(), 32)}}}
	client := fakeChain{tx: tx, receipt: receipt}
	proof, err := Inspect(context.Background(), tx.Hash(), crypto.PubkeyToAddress(facilitator.PublicKey).Hex(), prep, client, client)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.TransferVerified || proof.Attribution.AppCode != prep.AppCode || proof.Attribution.ServiceCodes[0] != prep.ServiceCode {
		t.Fatalf("proof=%+v", proof)
	}

	badReceipt := *receipt
	badReceipt.BlockHash = common.HexToHash("0xdead")
	if _, err := Inspect(context.Background(), tx.Hash(), crypto.PubkeyToAddress(facilitator.PublicKey).Hex(), prep, client, fakeChain{tx: tx, receipt: &badReceipt}); err == nil {
		t.Fatal("RPC disagreement accepted")
	}
	alteredLogReceipt := *receipt
	alteredLog := *receipt.Logs[0]
	alteredLog.Data = common.LeftPadBytes(big.NewInt(999).Bytes(), 32)
	alteredLogReceipt.Logs = []*types.Log{&alteredLog}
	if _, err := Inspect(context.Background(), tx.Hash(), crypto.PubkeyToAddress(facilitator.PublicKey).Hex(), prep, client, fakeChain{tx: tx, receipt: &alteredLogReceipt}); err == nil {
		t.Fatal("RPC log disagreement accepted")
	}

	wrongTransfer := *receipt
	wrongTransfer.Logs = []*types.Log{}
	if _, err := Inspect(context.Background(), tx.Hash(), crypto.PubkeyToAddress(facilitator.PublicKey).Hex(), prep, fakeChain{tx: tx, receipt: &wrongTransfer}, fakeChain{tx: tx, receipt: &wrongTransfer}); err == nil {
		t.Fatal("missing Transfer accepted")
	}
}

func ptrAddress(value common.Address) *common.Address { return &value }

func builderSuffix(app, service, wallet string) []byte {
	cbor := []byte{0xa3, 0x61, 'a', byte(0x60 + len(app))}
	cbor = append(cbor, app...)
	cbor = append(cbor, 0x61, 's', 0x81, byte(0x60+len(service)))
	cbor = append(cbor, service...)
	cbor = append(cbor, 0x61, 'w', byte(0x60+len(wallet)))
	cbor = append(cbor, wallet...)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(cbor)))
	marker, _ := hex.DecodeString("80218021802180218021802180218021")
	return append(append(append(cbor, length...), 0x02), marker...)
}
