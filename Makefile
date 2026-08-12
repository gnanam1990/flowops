.PHONY: test check fmt-check solidity-fmt-check dashboard-deps dashboard-check smoke-dashboard smoke-x402-readonly smoke-evidence-fetch smoke-reconciliation smoke-signer-executor smoke-escrow

GO_PACKAGES := ./cmd/... ./internal/... ./pkg/...
GO_FILES := $(shell git ls-files '*.go')

test:
	go test -race $(GO_PACKAGES)
	forge test
	npm test --prefix apps/dashboard

fmt-check:
	@test -z "$$(gofmt -l $(GO_FILES))" || (gofmt -l $(GO_FILES) && exit 1)

solidity-fmt-check:
	forge fmt --check

dashboard-deps:
	npm ci --prefix apps/dashboard

dashboard-check: dashboard-deps
	npm audit --omit=dev --audit-level=high --prefix apps/dashboard
	npm run lint --prefix apps/dashboard
	npm test --prefix apps/dashboard

check: fmt-check solidity-fmt-check dashboard-check
	go vet $(GO_PACKAGES)
	go test -race $(GO_PACKAGES)
	forge build --sizes
	forge test

smoke-dashboard:
	npm test --prefix apps/dashboard

smoke-x402-readonly:
	go run ./cmd/x402-conformance

smoke-evidence-fetch:
	go test -race -run '^TestHandlerSmoke$$' ./internal/evidencefetch

smoke-reconciliation:
	go test -race -run '^(TestHaltDrillPreservesAmbiguousExecutionAndRecoversOnce|TestCanonicalReorgReversesLedgerAndRequiresFreshOutcome|TestWorkerFinalizesCanonicalReceiptExactlyOnce|TestWorkerPersistsPositiveFinalityAndDoesNotPollItAgain|TestWorkerReorgAtomicallyReversesSettlement|TestSmokeChainHaltStopsBothAuthorizationBoundaries)$$' ./internal/reconciliation ./internal/controlplane

smoke-signer-executor:
	go test -race -run '^(TestExecutorBroadcastsAndRegistersExactlyOnce|TestExecutorBroadcastErrorBecomesDurableAmbiguousWithoutRetry|TestLostLocalRegistrationAckRetriesReceiptOnlyAfterRestart|TestRestartFromPreparedBroadcastsOnce|TestRestartFromBroadcastingMarksAmbiguousWithoutWallet|TestRemovingFlowOpsTrustStopsPreparedAttempt)$$' ./pkg/referencesigner

smoke-escrow:
	forge test --match-path contracts/test/CallEscrow.t.sol --match-test 'test_(acceptDeliveryOnlyBuyerAndPaysOnlyProvider|refundAcknowledgedButUndeliveredOnlyAfterDeliveryDeadline|reentrancyCannotFinalizeAnotherExpiredPosition)' -vv
