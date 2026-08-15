// Package x402experiment provides the deliberately narrow, Base Sepolia-only
// Builder Code conformance experiment. It never accepts or stores a private key.
package x402experiment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	x402types "github.com/x402-foundation/x402/go/v2/types"
)

const (
	Version          = 1
	Network          = "eip155:84532"
	ChainID          = 84532
	Asset            = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	AmountAtomic     = "1000"
	FacilitatorURL   = "https://x402.org/facilitator"
	TokenName        = "USDC"
	TokenVersion     = "2"
	TransferMethod   = "eip3009"
	confirmationWord = "SETTLE_BASE_SEPOLIA_0.001_TEST_USDC"
	digestDomain     = "flowops:x402-builder-experiment:v1\n"
)

var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

type Preparation struct {
	Version           int                           `json:"version"`
	CreatedAt         time.Time                     `json:"createdAt"`
	FacilitatorURL    string                        `json:"facilitatorUrl"`
	Payer             string                        `json:"payer"`
	Payee             string                        `json:"payee"`
	AppCode           string                        `json:"appCode"`
	ServiceCode       string                        `json:"serviceCode"`
	Requirements      x402types.PaymentRequirements `json:"paymentRequirements"`
	Authorization     Authorization                 `json:"authorization"`
	Extensions        map[string]interface{}        `json:"extensions"`
	TypedData         TypedData                     `json:"typedData"`
	PreparationDigest string                        `json:"preparationDigest"`
	Signature         string                        `json:"signature,omitempty"`
}

type digestInput struct {
	Version        int                           `json:"version"`
	CreatedAt      time.Time                     `json:"createdAt"`
	FacilitatorURL string                        `json:"facilitatorUrl"`
	Payer          string                        `json:"payer"`
	Payee          string                        `json:"payee"`
	AppCode        string                        `json:"appCode"`
	ServiceCode    string                        `json:"serviceCode"`
	Requirements   x402types.PaymentRequirements `json:"paymentRequirements"`
	Authorization  Authorization                 `json:"authorization"`
	Extensions     map[string]interface{}        `json:"extensions"`
	TypedData      TypedData                     `json:"typedData"`
}

type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

type TypedData struct {
	Types       apitypes.Types            `json:"types"`
	PrimaryType string                    `json:"primaryType"`
	Domain      TypedDataDomain           `json:"domain"`
	Message     apitypes.TypedDataMessage `json:"message"`
}

type TypedDataDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

type SettlementClient interface {
	Verify(context.Context, []byte, []byte) (*x402.VerifyResponse, error)
	Settle(context.Context, []byte, []byte) (*x402.SettleResponse, error)
}

type ExecuteResult struct {
	PreparationDigest string               `json:"preparationDigest"`
	Verify            *x402.VerifyResponse `json:"verify"`
	Settlement        *x402.SettleResponse `json:"settlement"`
}

func ConfirmationWord() string { return confirmationWord }

func Prepare(now time.Time, payer, payee, appCode, serviceCode string, random io.Reader) (Preparation, error) {
	if random == nil {
		random = rand.Reader
	}
	payer, err := canonicalAddress(payer)
	if err != nil {
		return Preparation{}, fmt.Errorf("payer: %w", err)
	}
	payee, err = canonicalAddress(payee)
	if err != nil {
		return Preparation{}, fmt.Errorf("payee: %w", err)
	}
	if payer == payee {
		return Preparation{}, errors.New("payer and payee must differ")
	}
	if !codePattern.MatchString(appCode) || !codePattern.MatchString(serviceCode) {
		return Preparation{}, errors.New("builder codes must be 1-32 lowercase letters, digits, or underscores")
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return Preparation{}, fmt.Errorf("create nonce: %w", err)
	}
	now = now.UTC().Truncate(time.Second)
	authorization := Authorization{
		From: payer, To: payee, Value: AmountAtomic,
		ValidAfter:  fmt.Sprint(now.Add(-5 * time.Second).Unix()),
		ValidBefore: fmt.Sprint(now.Add(15 * time.Minute).Unix()),
		Nonce:       "0x" + hex.EncodeToString(nonce),
	}
	requirements := x402types.PaymentRequirements{
		Scheme: "exact", Network: Network, Asset: Asset, Amount: AmountAtomic,
		PayTo: payee, MaxTimeoutSeconds: 300,
		Extra: map[string]interface{}{"assetTransferMethod": TransferMethod, "name": TokenName, "version": TokenVersion},
	}
	extensions := map[string]interface{}{
		"builder-code": map[string]interface{}{"info": map[string]interface{}{"a": appCode, "s": []string{serviceCode}}},
	}
	typedData := typedDataFor(authorization)
	preparation := Preparation{
		Version: Version, CreatedAt: now, FacilitatorURL: FacilitatorURL,
		Payer: payer, Payee: payee, AppCode: appCode, ServiceCode: serviceCode,
		Requirements: requirements, Authorization: authorization, Extensions: extensions, TypedData: typedData,
	}
	preparation.PreparationDigest, err = digest(preparation)
	if err != nil {
		return Preparation{}, err
	}
	return preparation, nil
}

func Validate(preparation Preparation, now time.Time) error {
	if preparation.Version != Version || preparation.FacilitatorURL != FacilitatorURL {
		return errors.New("experiment version or facilitator changed")
	}
	expected, err := digest(preparation)
	if err != nil {
		return err
	}
	if preparation.PreparationDigest != expected {
		return errors.New("preparation digest mismatch")
	}
	if preparation.Requirements.Scheme != "exact" || preparation.Requirements.Network != Network ||
		preparation.Requirements.Asset != Asset || preparation.Requirements.Amount != AmountAtomic ||
		preparation.Requirements.PayTo != preparation.Payee || preparation.Requirements.MaxTimeoutSeconds != 300 {
		return errors.New("payment requirements changed from the fixed experiment")
	}
	if preparation.Authorization.From != preparation.Payer || preparation.Authorization.To != preparation.Payee || preparation.Authorization.Value != AmountAtomic {
		return errors.New("authorization does not match fixed payer, payee, and amount")
	}
	if !common.IsHexAddress(preparation.Payer) || !common.IsHexAddress(preparation.Payee) || preparation.Payer != common.HexToAddress(preparation.Payer).Hex() || preparation.Payee != common.HexToAddress(preparation.Payee).Hex() {
		return errors.New("payer and payee must retain canonical checksum form")
	}
	if len(preparation.Authorization.Nonce) != 66 {
		return errors.New("authorization nonce must be exactly 32 bytes")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(preparation.Authorization.Nonce, "0x")); err != nil {
		return errors.New("authorization nonce must be hexadecimal")
	}
	validAfter, afterErr := strconv.ParseInt(preparation.Authorization.ValidAfter, 10, 64)
	validBefore, err := strconv.ParseInt(preparation.Authorization.ValidBefore, 10, 64)
	if afterErr != nil || err != nil || validAfter != preparation.CreatedAt.Add(-5*time.Second).Unix() || validBefore != preparation.CreatedAt.Add(15*time.Minute).Unix() {
		return errors.New("authorization validity window changed")
	}
	if now.UTC().Unix() < validAfter || now.UTC().Unix() >= validBefore {
		return errors.New("authorization is expired or invalid")
	}
	if !codePattern.MatchString(preparation.AppCode) || !codePattern.MatchString(preparation.ServiceCode) {
		return errors.New("builder code is invalid")
	}
	expectedExtensions := map[string]interface{}{
		"builder-code": map[string]interface{}{"info": map[string]interface{}{"a": preparation.AppCode, "s": []string{preparation.ServiceCode}}},
	}
	actualJSON, actualErr := json.Marshal(preparation.Extensions)
	expectedJSON, expectedErr := json.Marshal(expectedExtensions)
	if actualErr != nil || expectedErr != nil || !bytes.Equal(actualJSON, expectedJSON) {
		return errors.New("builder-code extension does not exactly match the fixed app and service codes")
	}
	if preparation.Requirements.Extra["assetTransferMethod"] != TransferMethod || preparation.Requirements.Extra["name"] != TokenName || preparation.Requirements.Extra["version"] != TokenVersion {
		return errors.New("USDC transfer method or EIP-712 domain changed")
	}
	if typedHash, err := typedDataHash(preparation.TypedData); err != nil || !typedDataMatches(preparation, typedHash) {
		if err != nil {
			return fmt.Errorf("typed data: %w", err)
		}
		return errors.New("typed data does not match authorization")
	}
	return nil
}

func VerifySignature(preparation Preparation) error {
	if err := Validate(preparation, time.Now()); err != nil {
		return err
	}
	signature := strings.TrimPrefix(preparation.Signature, "0x")
	raw, err := hex.DecodeString(signature)
	if err != nil || len(raw) != 65 {
		return errors.New("signature must be a 65-byte hex EIP-712 signature")
	}
	if raw[64] >= 27 {
		raw[64] -= 27
	}
	if raw[64] > 1 {
		return errors.New("signature recovery id is invalid")
	}
	hash, err := typedDataHash(preparation.TypedData)
	if err != nil {
		return err
	}
	publicKey, err := crypto.SigToPub(hash, raw)
	if err != nil {
		return fmt.Errorf("recover signature: %w", err)
	}
	if crypto.PubkeyToAddress(*publicKey) != common.HexToAddress(preparation.Payer) {
		return errors.New("signature was not produced by the fixed payer")
	}
	return nil
}

func Execute(ctx context.Context, preparation Preparation, confirmation string, client SettlementClient) (ExecuteResult, error) {
	if confirmation != confirmationWord {
		return ExecuteResult{}, errors.New("exact settlement confirmation is required")
	}
	if err := VerifySignature(preparation); err != nil {
		return ExecuteResult{}, err
	}
	if client == nil {
		parsed, _ := url.Parse(FacilitatorURL)
		httpClient := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		client = x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: parsed.String(), HTTPClient: httpClient})
	}
	payload, requirements, err := wireBytes(preparation)
	if err != nil {
		return ExecuteResult{}, err
	}
	verified, err := client.Verify(ctx, payload, requirements)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("facilitator verify: %w", err)
	}
	if !verified.IsValid || !strings.EqualFold(verified.Payer, preparation.Payer) {
		return ExecuteResult{}, errors.New("facilitator did not verify the exact payer")
	}
	settled, err := client.Settle(ctx, payload, requirements)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("facilitator settle: %w", err)
	}
	if !settled.Success || settled.Network != x402.Network(Network) || !strings.EqualFold(settled.Payer, preparation.Payer) || !common.IsHexHash(settled.Transaction) {
		return ExecuteResult{}, errors.New("facilitator returned an invalid settlement result")
	}
	return ExecuteResult{PreparationDigest: preparation.PreparationDigest, Verify: verified, Settlement: settled}, nil
}

func wireBytes(preparation Preparation) ([]byte, []byte, error) {
	payload := x402types.PaymentPayload{
		X402Version: 2,
		Payload: map[string]interface{}{
			"signature":     preparation.Signature,
			"authorization": preparation.Authorization,
		},
		Accepted:   preparation.Requirements,
		Extensions: preparation.Extensions,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	requirementsBytes, err := json.Marshal(preparation.Requirements)
	return payloadBytes, requirementsBytes, err
}

func typedDataFor(auth Authorization) TypedData {
	return TypedData{
		Types: apitypes.Types{
			"EIP712Domain":              {{Name: "name", Type: "string"}, {Name: "version", Type: "string"}, {Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}},
			"TransferWithAuthorization": {{Name: "from", Type: "address"}, {Name: "to", Type: "address"}, {Name: "value", Type: "uint256"}, {Name: "validAfter", Type: "uint256"}, {Name: "validBefore", Type: "uint256"}, {Name: "nonce", Type: "bytes32"}},
		},
		PrimaryType: "TransferWithAuthorization",
		Domain:      TypedDataDomain{Name: TokenName, Version: TokenVersion, ChainID: ChainID, VerifyingContract: Asset},
		Message:     apitypes.TypedDataMessage{"from": auth.From, "to": auth.To, "value": auth.Value, "validAfter": auth.ValidAfter, "validBefore": auth.ValidBefore, "nonce": auth.Nonce},
	}
}

func typedDataHash(data TypedData) ([]byte, error) {
	chainID := ethmath.HexOrDecimal256(*big.NewInt(int64(data.Domain.ChainID)))
	hash, _, err := apitypes.TypedDataAndHash(apitypes.TypedData{
		Types: data.Types, PrimaryType: data.PrimaryType,
		Domain:  apitypes.TypedDataDomain{Name: data.Domain.Name, Version: data.Domain.Version, ChainId: &chainID, VerifyingContract: data.Domain.VerifyingContract},
		Message: data.Message,
	})
	return hash, err
}

func typedDataMatches(preparation Preparation, hash []byte) bool {
	expectedHash, err := typedDataHash(typedDataFor(preparation.Authorization))
	return err == nil && bytes.Equal(hash, expectedHash)
}

func digest(preparation Preparation) (string, error) {
	input := digestInput{
		Version: preparation.Version, CreatedAt: preparation.CreatedAt, FacilitatorURL: preparation.FacilitatorURL,
		Payer: preparation.Payer, Payee: preparation.Payee, AppCode: preparation.AppCode, ServiceCode: preparation.ServiceCode,
		Requirements: preparation.Requirements, Authorization: preparation.Authorization, Extensions: preparation.Extensions, TypedData: preparation.TypedData,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(digestDomain), encoded...))
	return "0x" + hex.EncodeToString(sum[:]), nil
}

func canonicalAddress(value string) (string, error) {
	if !common.IsHexAddress(value) {
		return "", errors.New("must be a 20-byte EVM address")
	}
	return common.HexToAddress(value).Hex(), nil
}
