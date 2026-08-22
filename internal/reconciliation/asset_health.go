package reconciliation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/crypto"
)

const eip1967ImplementationSlot = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"

type AssetHealthRequest struct {
	Asset  string
	Buyer  string
	Escrow string
}

type AssetHealthEvidence struct {
	Provider            string
	ChainID             uint64
	Asset               string
	ProxyImplementation string
	RuntimeCodeHash     string
	Paused              bool
	BuyerBlacklisted    bool
	EscrowBlacklisted   bool
	TransferFailure     bool
	FinalizedBlock      uint64
	FinalizedBlockHash  string
}

type AssetHealthResult struct {
	Evidence []AssetHealthEvidence
	Failures map[string]string
}

// AssetHealthQuorum reads the exact proxy implementation and code hash plus
// Circle pause/blacklist surfaces at one finalized block per provider.
func (s *ObserverSet) AssetHealthQuorum(ctx context.Context, request AssetHealthRequest) AssetHealthResult {
	if !validAssetHealthAddress(request.Asset) {
		return AssetHealthResult{Failures: map[string]string{"configuration": "asset is invalid"}}
	}
	if !validAssetHealthAddress(request.Buyer) {
		return AssetHealthResult{Failures: map[string]string{"configuration": "buyer is invalid"}}
	}
	if !validAssetHealthAddress(request.Escrow) {
		return AssetHealthResult{Failures: map[string]string{"configuration": "escrow is invalid"}}
	}
	type result struct {
		provider string
		evidence AssetHealthEvidence
		err      error
	}
	results := make(chan result, len(s.providers))
	var group sync.WaitGroup
	for _, provider := range s.providers {
		provider := provider
		group.Add(1)
		go func() {
			defer group.Done()
			evidence, err := s.assetHealth(ctx, provider, request)
			results <- result{provider: provider.Name, evidence: evidence, err: err}
		}()
	}
	group.Wait()
	close(results)
	output := AssetHealthResult{Failures: make(map[string]string)}
	for result := range results {
		if result.err != nil {
			output.Failures[result.provider] = result.err.Error()
			continue
		}
		output.Evidence = append(output.Evidence, result.evidence)
	}
	if len(output.Failures) == 0 {
		output.Failures = nil
	}
	return output
}

func (s *ObserverSet) assetHealth(ctx context.Context, provider RPCProvider, request AssetHealthRequest) (AssetHealthEvidence, error) {
	if err := s.verifyChain(ctx, provider); err != nil {
		return AssetHealthEvidence{}, err
	}
	finalized, err := s.block(ctx, provider, "finalized")
	if err != nil {
		return AssetHealthEvidence{}, err
	}
	tag := fmt.Sprintf("0x%x", finalized.number)
	var storage string
	if err := s.call(ctx, provider, "eth_getStorageAt", []any{request.Asset, eip1967ImplementationSlot, tag}, &storage); err != nil {
		return AssetHealthEvidence{}, err
	}
	implementation, err := addressFromStorageWord(storage)
	if err != nil {
		return AssetHealthEvidence{}, errors.New("USDC proxy implementation slot is invalid")
	}
	var code string
	if err := s.call(ctx, provider, "eth_getCode", []any{implementation, tag}, &code); err != nil {
		return AssetHealthEvidence{}, err
	}
	decodedCode, err := decodeHexBytes(code)
	if err != nil || len(decodedCode) == 0 {
		return AssetHealthEvidence{}, errors.New("USDC implementation code is empty or invalid")
	}
	paused, err := s.callBool(ctx, provider, request.Asset, "paused()", nil, tag)
	if err != nil {
		return AssetHealthEvidence{}, err
	}
	buyerBlacklisted, err := s.callBool(ctx, provider, request.Asset, "isBlacklisted(address)", []byte(request.Buyer[2:]), tag)
	if err != nil {
		return AssetHealthEvidence{}, err
	}
	escrowBlacklisted, err := s.callBool(ctx, provider, request.Asset, "isBlacklisted(address)", []byte(request.Escrow[2:]), tag)
	if err != nil {
		return AssetHealthEvidence{}, err
	}
	transferFailure := s.transferProbeFailed(ctx, provider, request, tag)
	return AssetHealthEvidence{Provider: provider.Name, ChainID: s.chainID, Asset: request.Asset,
		ProxyImplementation: implementation, RuntimeCodeHash: strings.ToLower(crypto.Keccak256Hash(decodedCode).Hex()), Paused: paused,
		BuyerBlacklisted: buyerBlacklisted, EscrowBlacklisted: escrowBlacklisted, TransferFailure: transferFailure,
		FinalizedBlock: finalized.number, FinalizedBlockHash: finalized.hash}, nil
}

func (s *ObserverSet) transferProbeFailed(ctx context.Context, provider RPCProvider, request AssetHealthRequest, tag string) bool {
	data := crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]
	destination, err := hex.DecodeString(request.Escrow[2:])
	if err != nil || len(destination) != 20 {
		return true
	}
	data = append(data, make([]byte, 12)...)
	data = append(data, destination...)
	data = append(data, make([]byte, 32)...)
	var result string
	err = s.call(ctx, provider, "eth_call", []any{map[string]string{
		"from": request.Buyer, "to": request.Asset, "data": "0x" + hex.EncodeToString(data),
	}, tag}, &result)
	if err != nil {
		return true
	}
	decoded, err := decodeHexBytes(result)
	if err != nil || len(decoded) != 32 {
		return true
	}
	for _, value := range decoded[:31] {
		if value != 0 {
			return true
		}
	}
	return decoded[31] != 1
}

func (s *ObserverSet) callBool(ctx context.Context, provider RPCProvider, contract, signature string, rawAddressHex []byte, tag string) (bool, error) {
	data := crypto.Keccak256([]byte(signature))[:4]
	if len(rawAddressHex) > 0 {
		decoded, err := hex.DecodeString(string(rawAddressHex))
		if err != nil || len(decoded) != 20 {
			return false, errors.New("blacklist subject is invalid")
		}
		data = append(data, make([]byte, 12)...)
		data = append(data, decoded...)
	}
	var result string
	if err := s.call(ctx, provider, "eth_call", []any{map[string]string{"to": contract, "data": "0x" + hex.EncodeToString(data)}, tag}, &result); err != nil {
		return false, err
	}
	decoded, err := decodeHexBytes(result)
	if err != nil || len(decoded) != 32 {
		return false, errors.New("USDC boolean call returned invalid data")
	}
	for _, value := range decoded[:31] {
		if value != 0 {
			return false, errors.New("USDC boolean call returned non-canonical data")
		}
	}
	if decoded[31] > 1 {
		return false, errors.New("USDC boolean call returned non-boolean data")
	}
	return decoded[31] == 1, nil
}

func addressFromStorageWord(value string) (string, error) {
	decoded, err := decodeHexBytes(value)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("address word is invalid")
	}
	for _, prefix := range decoded[:12] {
		if prefix != 0 {
			return "", errors.New("address word has a nonzero prefix")
		}
	}
	address := addressFromWord(decoded)
	if !validAssetHealthAddress(address) {
		return "", errors.New("address word is zero or invalid")
	}
	return address, nil
}

func validAssetHealthAddress(value string) bool {
	return addressPattern.MatchString(value) && value != "0x"+strings.Repeat("0", 40)
}

func decodeHexBytes(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "0x") || len(value)%2 != 0 || value != strings.ToLower(value) {
		return nil, errors.New("hex bytes are not canonical")
	}
	return hex.DecodeString(value[2:])
}
