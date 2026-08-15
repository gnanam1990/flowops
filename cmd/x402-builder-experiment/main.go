// x402-builder-experiment prepares, settles, and independently proves the one
// fixed Base Sepolia Builder Code experiment. It never accepts a private key.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/x402adapter"
	"github.com/gnanam1990/flowops/internal/x402experiment"
)

const maxArtifactBytes = 1 << 20

const (
	designatedPayer = "0x079bDde909e28E437768A06d7001eb40896668d4"
	designatedPayee = "0xC2f0967C4Df966636E4Ac1dad40abdA65536cbb6"
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: x402-builder-experiment prepare|attach-signature|settle|inspect"))
	}
	var err error
	switch os.Args[1] {
	case "prepare":
		err = prepare(os.Args[2:])
	case "attach-signature":
		err = attachSignature(os.Args[2:])
	case "settle":
		err = settle(os.Args[2:])
	case "inspect":
		err = inspect(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func prepare(args []string) error {
	flags := flag.NewFlagSet("prepare", flag.ContinueOnError)
	payer := flags.String("payer", "", "test-USDC payer address")
	payee := flags.String("payee", "", "test-USDC payee address")
	appCode := flags.String("app-code", "", "resource app Builder Code")
	serviceCode := flags.String("service-code", "", "FlowOps client service Builder Code")
	artifactPath := flags.String("artifact", "", "new preparation artifact path")
	typedDataPath := flags.String("typed-data", "", "new cast-compatible typed-data path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *artifactPath == "" || *typedDataPath == "" {
		return errors.New("--artifact and --typed-data are required")
	}
	if !sameAddress(*payer, designatedPayer) || !sameAddress(*payee, designatedPayee) {
		return errors.New("experiment is pinned to the designated FlowOps test payer and payee")
	}
	preparation, err := x402experiment.Prepare(time.Now(), *payer, *payee, *appCode, *serviceCode, nil)
	if err != nil {
		return err
	}
	if err := writeJSONExclusive(*artifactPath, preparation); err != nil {
		return err
	}
	if err := writeJSONExclusive(*typedDataPath, preparation.TypedData); err != nil {
		return err
	}
	return printJSON(struct {
		Status       string `json:"status"`
		Artifact     string `json:"artifact"`
		TypedData    string `json:"typedData"`
		Digest       string `json:"preparationDigest"`
		Confirmation string `json:"requiredSettlementConfirmation"`
		Amount       string `json:"amount"`
	}{"PREPARED_NOT_SIGNED_NOT_SETTLED", *artifactPath, *typedDataPath, preparation.PreparationDigest, x402experiment.ConfirmationWord(), "0.001 test USDC"})
}

func attachSignature(args []string) error {
	flags := flag.NewFlagSet("attach-signature", flag.ContinueOnError)
	artifactPath := flags.String("artifact", "", "preparation artifact path")
	signatureFile := flags.String("signature-file", "-", "cast EIP-712 signature file, or - for stdin")
	out := flags.String("out", "", "new signed artifact path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	preparation, err := readPreparation(*artifactPath)
	if err != nil {
		return err
	}
	signature, err := readSmallText(*signatureFile, 1024)
	if err != nil {
		return err
	}
	preparation.Signature = strings.TrimSpace(signature)
	if err := x402experiment.VerifySignature(preparation); err != nil {
		return err
	}
	if err := writeJSONExclusive(*out, preparation); err != nil {
		return err
	}
	return printJSON(struct{ Status, SignedArtifact, PreparationDigest string }{"SIGNED_NOT_SETTLED", *out, preparation.PreparationDigest})
}

func settle(args []string) error {
	flags := flag.NewFlagSet("settle", flag.ContinueOnError)
	artifactPath := flags.String("artifact", "", "signed artifact path")
	confirmation := flags.String("confirm", "", "exact settlement confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	preparation, err := readPreparation(*artifactPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := x402experiment.Execute(ctx, preparation, *confirmation, nil)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func inspect(args []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	artifactPath := flags.String("artifact", "", "signed artifact path")
	txHash := flags.String("tx", "", "settlement transaction hash")
	signer := flags.String("facilitator-signer", "", "advertised facilitator signer")
	rpc1 := flags.String("rpc-1", "https://sepolia.base.org", "first Base Sepolia RPC")
	rpc2 := flags.String("rpc-2", "https://base-sepolia-rpc.publicnode.com", "second independent Base Sepolia RPC")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !common.IsHexHash(*txHash) {
		return errors.New("--tx must be a 32-byte transaction hash")
	}
	preparation, err := readPreparation(*artifactPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adapter, err := x402adapter.New(x402adapter.Config{
		Network: x402adapter.BaseSepoliaNetwork, ChainID: x402adapter.BaseSepoliaChainID,
		USDCAddress: x402adapter.BaseSepoliaUSDC, MaxAmountAtomic: x402experiment.AmountAtomic,
		MaxTimeoutSeconds: 300, ServiceCodes: []string{preparation.ServiceCode},
	})
	if err != nil {
		return err
	}
	facilitator, err := x402adapter.NewFacilitatorClient(x402experiment.FacilitatorURL, nil)
	if err != nil {
		return err
	}
	supported, err := facilitator.Supported(ctx)
	if err != nil {
		return fmt.Errorf("facilitator support evidence: %w", err)
	}
	conformance := adapter.CheckFacilitator(supported)
	if !conformance.Ready || !containsAddress(conformance.Signers, *signer) {
		return errors.New("facilitator signer is not in the current ready Base Sepolia builder-code advertisement")
	}
	client1, err := newRPCChainClient(*rpc1)
	if err != nil {
		return fmt.Errorf("RPC 1: %w", err)
	}
	client2, err := newRPCChainClient(*rpc2)
	if err != nil {
		return fmt.Errorf("RPC 2: %w", err)
	}
	proof, err := x402experiment.Inspect(ctx, common.HexToHash(*txHash), *signer, preparation, client1, client2)
	if err != nil {
		return err
	}
	return printJSON(proof)
}

func readPreparation(path string) (x402experiment.Preparation, error) {
	if path == "" {
		return x402experiment.Preparation{}, errors.New("--artifact is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return x402experiment.Preparation{}, err
	}
	defer file.Close()
	limited := io.LimitReader(file, maxArtifactBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return x402experiment.Preparation{}, err
	}
	if len(body) > maxArtifactBytes {
		return x402experiment.Preparation{}, errors.New("artifact exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var preparation x402experiment.Preparation
	if err := decoder.Decode(&preparation); err != nil {
		return preparation, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return preparation, errors.New("artifact contains trailing JSON")
	}
	if !sameAddress(preparation.Payer, designatedPayer) || !sameAddress(preparation.Payee, designatedPayee) {
		return preparation, errors.New("artifact payer or payee is not a designated FlowOps test wallet")
	}
	return preparation, nil
}

func readSmallText(path string, limit int64) (string, error) {
	var reader io.Reader = os.Stdin
	var file *os.File
	if path != "-" {
		opened, err := os.Open(path)
		if err != nil {
			return "", err
		}
		file = opened
		defer file.Close()
		reader = file
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(body)) > limit {
		return "", errors.New("signature input is too large")
	}
	return string(body), nil
}

func sameAddress(left, right string) bool {
	return common.IsHexAddress(left) && common.IsHexAddress(right) && common.HexToAddress(left) == common.HexToAddress(right)
}

func containsAddress(values []string, expected string) bool {
	for _, value := range values {
		if sameAddress(value, expected) {
			return true
		}
	}
	return false
}

func writeJSONExclusive(path string, value interface{}) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s without overwrite: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func printJSON(value interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
