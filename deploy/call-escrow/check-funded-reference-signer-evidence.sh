#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
evidence="${FLOWOPS_FUNDED_SIGNER_EVIDENCE:-${repo_root}/docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.json}"
expected_sha256="f3464eaf8e8c874187ab3ec712063e8bfadc916f543dc113cca65f5148792218"

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha256="$(sha256sum "${evidence}" | awk '{print $1}')"
else
  actual_sha256="$(shasum -a 256 "${evidence}" | awk '{print $1}')"
fi
test "${actual_sha256}" = "${expected_sha256}"

jq -e '
  .schemaVersion == 1
  and .evidenceType == "flowops.reference-signer-funded-escrow.v1"
  and .status == "complete-refunded"
  and .mainnetAuthorized == false
  and .network.name == "base-sepolia"
  and .network.chainId == 84532
  and (.network.rpcEvidence | length == 2)
  and (.network.rpcEvidence | map(.url) | sort == ["https://base-sepolia-rpc.publicnode.com", "https://sepolia.base.org"])
  and (.network.rpcEvidence | all(.productionEligible == false and .fundReceiptMatched == true and .refundReceiptMatched == true))
  and .deployment.contract == "0x86e145397f58e71c134c0e054320db929483227a"
  and .deployment.asset == "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
  and .deployment.releaseWindowSeconds == 3600
  and .actors.buyer == "0x079bdde909e28e437768a06d7001eb40896668d4"
  and .actors.provider == "0xc2f0967c4df966636e4ac1dad40abda65536cbb6"
  and .authorization.version == "flowops.authorization.v1"
  and .authorization.authorizationId == "auth_d8842a2102e191a4ce08d6b7cf1d672a"
  and .authorization.rail == "escrow"
  and .authorization.amountAtomic == "100000"
  and .authorization.policyVersion == "policy_sepolia_proof_1"
  and .authorization.issuedAt == 1786770688
  and .authorization.expiresAt == 1786770988
  and .authorization.authorizationDigest == "0x891539dafbbb4bc6f134a54c4dc4ac0f66226f7530dd3fe9020d4fd5b9e7461f"
  and (.authorization.nonce | test("^0x[0-9a-f]{64}$"))
  and (.authorization.receiptSignature | test("^0x[0-9a-f]{128}$"))
  and .signer.sourceCommit == "fa32fad28b73d0095b62880e3fbf30459563c9a0"
  and .signer.binarySha256 == "d479ec31ce976d8a65fe5736386125a3a3e5d022632d43d3b407d49536ffb8f4"
  and .signer.vcsModified == false
  and .signer.customerKeyCustodiedByFlowOps == false
  and .signer.pilotLimits.maximumPerActionAtomic == "1000000"
  and .signer.pilotLimits.maximumOutstandingAtomic == "10000000"
  and .signer.pilotLimits.exactApprovalOnly == true
  and .call.derivationDomain == "FLOWOPS_CALL_ESCROW_V1"
  and .call.callId == "0xa9cb4708de15f8f3a9ced649a949aab3539a5c9f1cab00186c48c324f10b8e3e"
  and .call.taskDigest == "0x8dfdb628c5ff29bdcd2c3d87d143fa2dd2dd06db935d2cdd268d85b5f35ef3b0"
  and .call.requestDigest == "0x1184e3c363ec508d7f56fe8d3f2873f730c81f3fa30f2462bf989d6fd0d3cc97"
  and .call.acknowledgeBy == 1786771587
  and .call.deliverBy == 1786772487
  and .call.finalState == "REFUNDED"
  and .call.pendingTransition == null
  and (.transitions | length == 2)
  and .transitions[0].action == "FUND"
  and .transitions[0].transactionHash == "0x0bacd7dff777cc646d1f48984e7a240fd914d416f5b93c14831c3fbcedaf89ab"
  and .transitions[0].sender == .actors.buyer
  and .transitions[0].recipient == .deployment.contract
  and .transitions[0].nativeValueAtomic == "0"
  and .transitions[0].receiptStatus == 1
  and .transitions[0].blockNumber == 45501208
  and .transitions[0].blockHash == "0x5361b553f9cd635b3024023fec4bac723c2af083b9bba785f302795719914970"
  and .transitions[0].finalityCheckedHead >= .transitions[0].blockNumber
  and (.transitions[0].ledgerTransactionId | test("^escrow_[0-9a-f]{64}$"))
  and .transitions[0].usdcTransfer.from == .actors.buyer
  and .transitions[0].usdcTransfer.to == .deployment.contract
  and .transitions[0].usdcTransfer.amountAtomic == .authorization.amountAtomic
  and .transitions[1].action == "REFUND"
  and .transitions[1].transactionHash == "0x8813d944c1851279ef5bbc4899f47dba1f87841b6bc2029738dd2647b06107e6"
  and .transitions[1].sender == .actors.buyer
  and .transitions[1].recipient == .deployment.contract
  and .transitions[1].nativeValueAtomic == "0"
  and .transitions[1].receiptStatus == 1
  and .transitions[1].refundedFromState == 1
  and .transitions[1].blockNumber == 45501671
  and .transitions[1].blockHash == "0xe0b733e46cf91a16b258322e31933f92de087b9927c7153f3892496e451775ab"
  and .transitions[1].blockNumber > .transitions[0].blockNumber
  and .transitions[1].finalityCheckedHead >= .transitions[1].blockNumber
  and (.transitions[1].ledgerTransactionId | test("^escrow_[0-9a-f]{64}$"))
  and .transitions[1].usdcTransfer.from == .deployment.contract
  and .transitions[1].usdcTransfer.to == .actors.buyer
  and .transitions[1].usdcTransfer.amountAtomic == .authorization.amountAtomic
  and .terminalChecks.escrowUsdcAtomic == "0"
  and .terminalChecks.buyerAllowanceToEscrowAtomic == "0"
  and (.limitations | length == 5)
  and (.limitations | any(test("authorizes no Base mainnet action")))
  and (.limitations | any(test("not production-eligible")))
  and (.limitations | any(test("manually")))
' "${evidence}" >/dev/null

git -C "${repo_root}" cat-file -e "$(jq -r '.signer.sourceCommit' "${evidence}")^{commit}"

domain_hash="$(cast keccak "$(jq -r '.call.derivationDomain' "${evidence}")")"
encoded_call_id="$(cast abi-encode 'f(bytes32,uint256,address,address,bytes32,bytes32)' \
  "${domain_hash}" \
  "$(jq -r '.network.chainId' "${evidence}")" \
  "$(jq -r '.deployment.contract' "${evidence}")" \
  "$(jq -r '.actors.buyer' "${evidence}")" \
  "$(jq -r '.call.taskDigest' "${evidence}")" \
  "$(jq -r '.call.requestDigest' "${evidence}")")"
test "$(cast keccak "${encoded_call_id}")" = "$(jq -r '.call.callId' "${evidence}")"

expected_fund_calldata="$(cast calldata 'fund(bytes32,address,uint256,bytes32,bytes32,uint64,uint64)' \
  "$(jq -r '.call.callId' "${evidence}")" \
  "$(jq -r '.actors.provider' "${evidence}")" \
  "$(jq -r '.authorization.amountAtomic' "${evidence}")" \
  "$(jq -r '.call.taskDigest' "${evidence}")" \
  "$(jq -r '.call.requestDigest' "${evidence}")" \
  "$(jq -r '.call.acknowledgeBy' "${evidence}")" \
  "$(jq -r '.call.deliverBy' "${evidence}")")"
test "${expected_fund_calldata}" = "$(jq -r '.transitions[0].calldata' "${evidence}")"

expected_refund_calldata="$(cast calldata 'refundExpired(bytes32)' "$(jq -r '.call.callId' "${evidence}")")"
test "${expected_refund_calldata}" = "$(jq -r '.transitions[1].calldata' "${evidence}")"

printf 'validated funded Base Sepolia reference-signer FUND to REFUND evidence; mainnet remains unauthorized\n'
