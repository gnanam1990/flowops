.PHONY: test check fmt-check solidity-fmt-check acceptance-manifest-check test-ascp-external-acceptance verify-ascp-external-acceptance test-ascp-local-acceptance-rehearsal ascp-local-acceptance-rehearsal deployment-evidence-check test-deployment-evidence ascp-sepolia-evidence-check test-ascp-sepolia-evidence verify-ascp-sepolia-deployment ascp-sepolia-activation-evidence-check test-ascp-sepolia-activation-evidence verify-ascp-sepolia-activation test-ascp-directory-release test-ascp-directory-presign test-ascp-directory-relay test-ascp-directory-transaction-preview test-ascp-canonical-usdc-fork test-ascp-asset-recovery-accounting verify-ascp-sepolia-directory-v1-readiness funded-signer-evidence-check mainnet-readiness-check mainnet-final-audit test-mainnet-final-audit test-mainnet-readiness test-mainnet-deployer-verification test-security-review-package test-proposal-anchor test-mainnet-intent-anchor verify-ascp-sepolia-asset dashboard-deps dashboard-check smoke-dashboard smoke-x402-readonly smoke-x402-builder-experiment smoke-evidence-fetch smoke-reconciliation smoke-reconciliation-operator smoke-postgres-readiness smoke-signer-executor smoke-reference-signer smoke-escrow-signer smoke-funded-signer-evidence smoke-pilot-limits smoke-rpc-admission smoke-escrow smoke-escrow-deployment smoke-ascp-sepolia-deployment smoke-escrow-mainnet-readiness smoke-escrow-reconciliation smoke-escrow-durable smoke-mcp

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

acceptance-manifest-check:
	go test -race ./internal/acceptance ./cmd/acceptance-report
	go run ./cmd/acceptance-report

test-ascp-external-acceptance:
	go test -race ./internal/acceptanceexternal ./cmd/ascp-external-acceptance
	go run ./cmd/ascp-external-acceptance requirements >/dev/null

verify-ascp-external-acceptance:
	@test -n "$${FLOWOPS_EXTERNAL_ACCEPTANCE_BUNDLE:-}" || (echo "FLOWOPS_EXTERNAL_ACCEPTANCE_BUNDLE is required" >&2; exit 1)
	@test -n "$${FLOWOPS_EXTERNAL_ACCEPTANCE_EVIDENCE_ROOT:-}" || (echo "FLOWOPS_EXTERNAL_ACCEPTANCE_EVIDENCE_ROOT is required" >&2; exit 1)
	go run ./cmd/ascp-external-acceptance verify "$${FLOWOPS_EXTERNAL_ACCEPTANCE_BUNDLE}" deployments/base-sepolia-ascp-external-acceptance-profile-v1.json "$${FLOWOPS_EXTERNAL_ACCEPTANCE_EVIDENCE_ROOT}"

test-ascp-local-acceptance-rehearsal:
	deploy/ascp/test-local-external-acceptance-rehearsal.sh

ascp-local-acceptance-rehearsal: test-ascp-local-acceptance-rehearsal
	@test -n "$${FLOWOPS_TEST_DATABASE_URL:-}" || (echo "FLOWOPS_TEST_DATABASE_URL is required" >&2; exit 1)
	deploy/ascp/run-local-external-acceptance-rehearsal.sh

deployment-evidence-check:
	deploy/call-escrow/check-base-sepolia-evidence.sh

test-deployment-evidence:
	deploy/call-escrow/test-base-sepolia-evidence.sh

ascp-sepolia-evidence-check:
	deploy/ascp/check-base-sepolia-deployment-evidence.sh

test-ascp-sepolia-evidence:
	deploy/ascp/test-base-sepolia-deployment-evidence.sh

verify-ascp-sepolia-deployment:
	deploy/ascp/verify-base-sepolia-deployment-readonly.sh

ascp-sepolia-activation-evidence-check:
	deploy/ascp/check-base-sepolia-activation-evidence.sh

test-ascp-sepolia-activation-evidence:
	deploy/ascp/test-base-sepolia-activation-evidence.sh

verify-ascp-sepolia-activation:
	deploy/ascp/verify-base-sepolia-activation-readonly.sh

test-ascp-directory-release:
	go test -race ./pkg/directoryrelease ./cmd/ascp-directory-release

test-ascp-directory-presign:
	go test -race ./pkg/directoryrelease ./cmd/ascp-directory-presign

test-ascp-directory-relay:
	go test -race ./pkg/directoryrelease ./cmd/ascp-directory-relay

test-ascp-directory-transaction-preview:
	go test -race ./pkg/directoryrelease ./cmd/ascp-directory-transaction-preview

test-ascp-canonical-usdc-fork:
	@test -n "$${BASE_MAINNET_FORK_RPC_URL:-}" || (echo "BASE_MAINNET_FORK_RPC_URL is required" >&2; exit 1)
	forge test --match-path contracts/test/CanonicalUSDCFork.t.sol

test-ascp-asset-recovery-accounting:
	go test -race ./internal/ascpsettlement -run '^TestPostgresStoreRetriesExactReverted(Lock|Transfer)OnlyDuringFreshAssetRecovery$$'

verify-ascp-sepolia-directory-v1-readiness:
	deploy/ascp/verify-base-sepolia-directory-v1-readiness.sh

funded-signer-evidence-check:
	deploy/call-escrow/check-funded-reference-signer-evidence.sh

mainnet-readiness-check:
	deploy/call-escrow/check-base-mainnet-readiness.sh

mainnet-final-audit:
	@deploy/call-escrow/audit-base-mainnet-readiness.sh --report

test-mainnet-final-audit:
	deploy/call-escrow/test-base-mainnet-audit.sh

test-mainnet-readiness:
	deploy/call-escrow/test-base-mainnet-readiness.sh
	forge test --match-path contracts/test/DeployCallEscrowBaseMainnet.t.sol

test-proposal-anchor:
	deploy/proposal-anchor/test-base-mainnet-proposal-anchor.sh
	forge test --match-path 'contracts/test/*ProposalAnchor*.t.sol'

test-mainnet-intent-anchor:
	deploy/intent-anchor/check-base-mainnet-intent-anchor-promotion.sh
	forge test --match-path 'contracts/test/*FlowOpsIntentAnchor*.t.sol'
	@actual="$$(forge inspect contracts/src/FlowOpsIntentAnchor.sol:FlowOpsIntentAnchor deployedBytecode | sed 's/^0x//' | xxd -r -p | shasum -a 256 | awk '{print $$1}')"; expected="$$(node --input-type=module -e "import('./apps/dashboard/app/mainnet/intent-codec.js').then((m) => console.log(m.EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256.slice(2)))")"; test "$$actual" = "$$expected"
	npm run lint --prefix apps/dashboard
	npm test --prefix apps/dashboard

test-mainnet-deployer-verification:
	deploy/call-escrow/test-base-mainnet-deployer-verification.sh

test-security-review-package:
	security/call-escrow/test-review-package.sh

verify-ascp-sepolia-asset:
	deploy/ascp/verify-base-sepolia-asset.sh

dashboard-deps:
	npm ci --prefix apps/dashboard

dashboard-check: dashboard-deps
	npm audit --omit=dev --audit-level=high --prefix apps/dashboard
	npm run lint --prefix apps/dashboard
	npm test --prefix apps/dashboard

check: fmt-check solidity-fmt-check acceptance-manifest-check test-ascp-external-acceptance test-deployment-evidence test-ascp-sepolia-evidence test-ascp-sepolia-activation-evidence test-mainnet-readiness test-proposal-anchor test-mainnet-intent-anchor test-mainnet-deployer-verification test-security-review-package test-mainnet-final-audit smoke-rpc-admission smoke-postgres-readiness dashboard-check
	go vet $(GO_PACKAGES)
	go test -race $(GO_PACKAGES)
	forge build --sizes
	forge test

smoke-dashboard:
	npm test --prefix apps/dashboard

smoke-x402-readonly:
	go run ./cmd/x402-conformance

smoke-x402-builder-experiment:
	go test -race -run '^(TestPrepareAndVerifySignature|TestValidationRejectsEveryLoadBearingMutation|TestWrongPayerSignatureIsRejected|TestExecuteRequiresConfirmationBeforeFacilitatorCall|TestExecuteVerifiesThenSettlesExactPayload|TestExecuteDoesNotSettleFailedVerification|TestInspectRequiresQuorumExactTransferAndBuilderSuffix)$$' ./internal/x402experiment
	go run ./cmd/x402-conformance

smoke-evidence-fetch:
	go test -race -run '^TestHandlerSmoke$$' ./internal/evidencefetch

smoke-mcp:
	go test -race ./internal/mcp ./internal/controlapi ./cmd/control-plane-api

smoke-reconciliation:
	go test -race -run '^(TestHaltDrillPreservesAmbiguousExecutionAndRecoversOnce|TestCanonicalReorgReversesLedgerAndRequiresFreshOutcome|TestWorkerFinalizesCanonicalReceiptExactlyOnce|TestWorkerPersistsPositiveFinalityAndDoesNotPollItAgain|TestWorkerReorgAtomicallyReversesSettlement|TestSmokeChainHaltStopsBothAuthorizationBoundaries)$$' ./internal/reconciliation ./internal/controlplane

smoke-reconciliation-operator:
	go test -race -run '^(TestOrganizationViewSeparatesProvedAssetAggregatesAndExceptions|TestOperatorReconciliationIsTenantBoundAndQuarantinePreservesUnprovenOutcome|TestOperatorClientReadsReconciliationAndQuarantinesWithoutClaimingOutcome)$$' ./internal/reconciliation ./internal/controlapi ./cmd/flowops-operator
	npm test --prefix apps/dashboard

smoke-postgres-readiness:
	deploy/control-plane/test-postgres-readiness.sh

smoke-signer-executor:
	go test -race -run '^(TestExecutorBroadcastsAndRegistersExactlyOnce|TestExecutorBroadcastErrorBecomesDurableAmbiguousWithoutRetry|TestLostLocalRegistrationAckRetriesReceiptOnlyAfterRestart|TestRestartFromPreparedBroadcastsOnce|TestRestartFromBroadcastingMarksAmbiguousWithoutWallet|TestRemovingFlowOpsTrustStopsPreparedAttempt)$$' ./pkg/referencesigner

smoke-reference-signer:
	go test -race -run '^(TestReferenceSignerNoFundsEndToEnd|TestClefAdapterPreparesValidatesAndBroadcastsExactTransaction|TestClefAdapterRejectsWalletMutationBeforeBroadcast|TestQuorumChainGateRequiresFreshCanonicalAgreement)$$' ./cmd/reference-signer ./pkg/referencewallet ./pkg/referencesigner

smoke-escrow-signer:
	go test -race -run '^(TestReferenceSignerEscrowNoFundsEndToEnd|TestEscrowClefAdapterPreparesValidatesAndBroadcastsExactFund|TestEscrowClefAdapterFailsClosedBeforeWallet|TestEscrowClefAdapterRejectsWalletMutation|TestFundDataMatchesSolidityABI|TestSignerEscrowBroadcastDerivesAttestedFundAndAcceptsDelayedCallback|TestSignerEscrowBroadcastHTTPBoundaryIsSeparateAndFailClosed|TestAttestedEscrowBroadcastPersistsExactCustomerProofDuringChainPause|TestAttestedEscrowBroadcastReplayAfterResolutionReturnsOriginalProof|TestHTTPEscrowRegistrationSinkRequiresDurableAttestationEcho|TestHTTPEscrowRegistrationSinkAcceptsResolvedAttestationReplay|TestExecutorPilotOutstandingLimitIsDurableAndPreWallet)$$' ./cmd/reference-signer ./pkg/referencewallet ./pkg/referencesigner ./internal/controlapi ./internal/reconciliation

smoke-funded-signer-evidence:
	deploy/call-escrow/smoke-funded-reference-signer-evidence.sh

smoke-pilot-limits:
	go test -race -run '^(TestLimitsCheckExactBoundaries|TestInitialBaseMainnetProfileIsExact|TestBaseMainnetReadinessRecordMatchesProfile|TestPilotLimitsOverridePermissivePolicyAndSurviveRestart|TestExecutorPilotOutstandingLimitIsDurableAndPreWallet|TestLoadConfigRejectsUnsafePilotLimits|TestLoadConfigPinsInitialBaseMainnetPilotLimits|TestLoadConfigRejectsLegacyV1AfterRequiredLimitMigration|TestLoadConfigRequiresOneExplicitRailAndEscrowTuple)$$' ./pkg/pilotlimits ./internal/controlplane ./pkg/referencesigner ./cmd/control-plane-api ./cmd/reference-signer

smoke-rpc-admission:
	deploy/control-plane/smoke-rpc-admission.sh
	go test -race -run '^(TestLoadConfigRequiresProductionRPCAdmissionOnBaseMainnet|TestLoadConfigRejectsProductionRPCAdmissionOnBaseSepolia)$$' ./cmd/reference-signer

smoke-escrow:
	forge test --match-path contracts/test/CallEscrow.t.sol --match-test 'test_(acceptDeliveryOnlyBuyerAndPaysOnlyProvider|refundAcknowledgedButUndeliveredOnlyAfterDeliveryDeadline|reentrancyCannotFinalizeAnotherExpiredPosition)' -vv

smoke-escrow-deployment:
	forge test --match-path contracts/test/DeployCallEscrowBaseSepolia.t.sol -vv

smoke-ascp-sepolia-deployment:
	forge test --match-path contracts/test/DeployASCPBaseSepolia.t.sol -vv

smoke-escrow-mainnet-readiness:
	deploy/call-escrow/smoke-base-mainnet-readiness.sh
	forge test --match-path contracts/test/DeployCallEscrowBaseMainnet.t.sol --match-test test_promotedHarnessDeploysPinnedConstructorWithoutAdminRole --fork-url https://mainnet.base.org -vv

smoke-escrow-reconciliation:
	go test -race -run '^(TestObserverSetDecodesCompleteEscrowReleaseLifecycle|TestObserverSetDecodesBothEscrowRefundPaths|TestEscrowReceiptRejectsSubstitutionDuplicateAndWrongLogOrder)$$' ./internal/reconciliation

smoke-escrow-durable:
	go test -race -run '^(TestEscrowAuthorizationBindsEveryCallTermAndRejectsCrossRailTerms|TestPaymentIntentRequiresExactRailSpecificEscrowTerms|TestEscrowAuthorizationCannotOutliveOrStartAfterAcknowledgeDeadline|TestEscrowRegistrarDerivesImmutableIntentOnlyFromIssuedAuthorization|TestEscrowAPIRequiresTenantAuthorizationAndTransactionHashIdempotency|TestDurableEscrowReleaseLifecyclePostsCanonicalLedgerAndSurvivesRestart|TestDurableEscrowRefundReversesLockedExposureWithoutInventingExpense|TestDurableEscrowRejectsSubstitutionReplayAndTransitionDuringUnhealthyRegistration|TestDurableEscrowPendingTransitionBecomesChainRecoveryAndCannotFinalizeDuringHalt|TestDurableEscrowReorgReversesEveryDependentLedgerAndQuarantinesPosition|TestDurableEscrowFinalityRequiresExactCanonicalQuorum|TestDurableEscrowRevertedTransitionMustReachCanonicalFinalityBeforeRetry|TestReorgedRevertedTransitionDoesNotReverseIndependentConfirmedSuffix|TestDurableEscrowRejectsForgedDeliveryTimingEvidence|TestDurableEscrowLedgerBindingsRejectMissingAlteredAndUnreferencedEntries|TestTransactionHashCannotBeRegisteredAcrossDirectAndEscrowRails|TestEscrowFinalityAndReorgRemainInvisibleWhenJournalAppendFails|TestWorkerContinuouslyReconcilesAndFinalizesDurableEscrowTransition)$$' ./pkg/envelope ./internal/controlplane ./internal/controlapi ./internal/reconciliation
