.PHONY: test check fmt-check solidity-fmt-check smoke-x402-readonly smoke-evidence-fetch smoke-reconciliation smoke-escrow

test:
	go test -race ./...
	forge test

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

solidity-fmt-check:
	forge fmt --check

check: fmt-check solidity-fmt-check
	go vet ./...
	go test -race ./...
	forge build --sizes
	forge test

smoke-x402-readonly:
	go run ./cmd/x402-conformance

smoke-evidence-fetch:
	go test -race -run '^TestHandlerSmoke$$' ./internal/evidencefetch

smoke-reconciliation:
	go test -race -run '^(TestHaltDrillPreservesAmbiguousExecutionAndRecoversOnce|TestCanonicalReorgReversesLedgerAndRequiresFreshOutcome|TestSmokeChainHaltStopsBothAuthorizationBoundaries)$$' ./internal/reconciliation ./internal/controlplane

smoke-escrow:
	forge test --match-path contracts/test/CallEscrow.t.sol --match-test 'test_(acceptDeliveryOnlyBuyerAndPaysOnlyProvider|refundAcknowledgedButUndeliveredOnlyAfterDeliveryDeadline|reentrancyCannotFinalizeAnotherExpiredPosition)' -vv
