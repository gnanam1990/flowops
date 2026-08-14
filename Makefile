.PHONY: test check fmt-check solidity-fmt-check deployment-evidence-check test-deployment-evidence mainnet-readiness-check test-mainnet-readiness dashboard-deps dashboard-check smoke-dashboard smoke-x402-readonly smoke-evidence-fetch smoke-reconciliation smoke-signer-executor smoke-reference-signer smoke-pilot-limits smoke-escrow smoke-escrow-deployment smoke-escrow-mainnet-readiness smoke-escrow-reconciliation smoke-escrow-durable

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

deployment-evidence-check:
	deploy/call-escrow/check-base-sepolia-evidence.sh

test-deployment-evidence:
	deploy/call-escrow/test-base-sepolia-evidence.sh

mainnet-readiness-check:
	deploy/call-escrow/check-base-mainnet-readiness.sh

test-mainnet-readiness:
	deploy/call-escrow/test-base-mainnet-readiness.sh
	forge test --match-path contracts/test/DeployCallEscrowBaseMainnet.t.sol

dashboard-deps:
	npm ci --prefix apps/dashboard

dashboard-check: dashboard-deps
	npm audit --omit=dev --audit-level=high --prefix apps/dashboard
	npm run lint --prefix apps/dashboard
	npm test --prefix apps/dashboard

check: fmt-check solidity-fmt-check test-deployment-evidence test-mainnet-readiness dashboard-check
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

smoke-reference-signer:
	go test -race -run '^(TestReferenceSignerNoFundsEndToEnd|TestClefAdapterPreparesValidatesAndBroadcastsExactTransaction|TestClefAdapterRejectsWalletMutationBeforeBroadcast|TestQuorumChainGateRequiresFreshCanonicalAgreement)$$' ./cmd/reference-signer ./pkg/referencewallet ./pkg/referencesigner

smoke-pilot-limits:
	go test -race -run '^(TestLimitsCheckExactBoundaries|TestInitialBaseMainnetProfileIsExact|TestBaseMainnetReadinessRecordMatchesProfile|TestPilotLimitsOverridePermissivePolicyAndSurviveRestart|TestExecutorPilotOutstandingLimitIsDurableAndPreWallet|TestLoadConfigRejectsUnsafePilotLimits|TestLoadConfigPinsInitialBaseMainnetPilotLimits|TestLoadConfigRejectsLegacyV1AfterRequiredLimitMigration)$$' ./pkg/pilotlimits ./internal/controlplane ./pkg/referencesigner ./cmd/control-plane-api ./cmd/reference-signer

smoke-escrow:
	forge test --match-path contracts/test/CallEscrow.t.sol --match-test 'test_(acceptDeliveryOnlyBuyerAndPaysOnlyProvider|refundAcknowledgedButUndeliveredOnlyAfterDeliveryDeadline|reentrancyCannotFinalizeAnotherExpiredPosition)' -vv

smoke-escrow-deployment:
	forge test --match-path contracts/test/DeployCallEscrowBaseSepolia.t.sol -vv

smoke-escrow-mainnet-readiness:
	deploy/call-escrow/smoke-base-mainnet-readiness.sh
	forge test --match-path contracts/test/DeployCallEscrowBaseMainnet.t.sol --match-test test_promotedHarnessDeploysPinnedConstructorWithoutAdminRole --fork-url https://mainnet.base.org -vv

smoke-escrow-reconciliation:
	go test -race -run '^(TestObserverSetDecodesCompleteEscrowReleaseLifecycle|TestObserverSetDecodesBothEscrowRefundPaths|TestEscrowReceiptRejectsSubstitutionDuplicateAndWrongLogOrder)$$' ./internal/reconciliation

smoke-escrow-durable:
	go test -race -run '^(TestEscrowAuthorizationBindsEveryCallTermAndRejectsCrossRailTerms|TestPaymentIntentRequiresExactRailSpecificEscrowTerms|TestEscrowAuthorizationCannotOutliveOrStartAfterAcknowledgeDeadline|TestEscrowRegistrarDerivesImmutableIntentOnlyFromIssuedAuthorization|TestEscrowAPIRequiresTenantAuthorizationAndTransactionHashIdempotency|TestDurableEscrowReleaseLifecyclePostsCanonicalLedgerAndSurvivesRestart|TestDurableEscrowRefundReversesLockedExposureWithoutInventingExpense|TestDurableEscrowRejectsSubstitutionReplayAndTransitionDuringUnhealthyRegistration|TestDurableEscrowPendingTransitionBecomesChainRecoveryAndCannotFinalizeDuringHalt|TestDurableEscrowReorgReversesEveryDependentLedgerAndQuarantinesPosition|TestDurableEscrowFinalityRequiresExactCanonicalQuorum|TestDurableEscrowRevertedTransitionMustReachCanonicalFinalityBeforeRetry|TestReorgedRevertedTransitionDoesNotReverseIndependentConfirmedSuffix|TestDurableEscrowRejectsForgedDeliveryTimingEvidence|TestDurableEscrowLedgerBindingsRejectMissingAlteredAndUnreferencedEntries|TestTransactionHashCannotBeRegisteredAcrossDirectAndEscrowRails|TestEscrowFinalityAndReorgRemainInvisibleWhenJournalAppendFails|TestWorkerContinuouslyReconcilesAndFinalizesDurableEscrowTransition)$$' ./pkg/envelope ./internal/controlplane ./internal/controlapi ./internal/reconciliation
