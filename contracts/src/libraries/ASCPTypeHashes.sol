// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

/// @notice Compile-time registry for every normative ASCP v4 EIP-712 type.
/// @dev This library has no storage, external calls, or deployable authority.
///      Keep its strings byte-identical to schemas/ascp-typed-data-v4.registry.json.
library ASCPTypeHashes {
    bytes32 internal constant EIP712_DOMAIN =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");
    bytes32 internal constant EXECUTION_COMMITMENT = keccak256(
        "ExecutionCommitment(bytes32 orgDomain,bytes32 operationId,uint8 rail,uint16 schemeVersion,uint8 protection,address escrowContract,bytes32 purchaseSpecHash,bytes32 quoteHash,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 directoryVersion,bytes32 sellerId,bytes32 resourceId,address payTo,address ackAuthority,uint256 amount,uint256 chainId,address asset,uint64 quoteExpiresAt,uint64 acceptBy,uint64 deliverBy,uint64 settleBy)"
    );
    bytes32 internal constant SELLER_QUOTE = keccak256(
        "SellerQuote(bytes32 purchaseSpecHash,bytes32 sellerId,bytes32 resourceId,uint64 directoryVersion,uint16 schemeVersion,uint256 chainId,address asset,uint256 amountBaseUnits,address payTo,address ackAuthority,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 quoteExpiresAt,bytes32 quoteNonce)"
    );
    bytes32 internal constant LOCK_AUTHORIZATION = keccak256(
        "LockAuthorization(bytes32 orgDomain,address safe,address module,bytes32 operationId,bytes32 commitmentHash,bytes32 calldataHash,address escrow,uint256 amount,uint256 nonce,uint64 validAfter,uint64 validBefore,uint64 leadershipEpoch,uint64 authorizerEpoch)"
    );
    bytes32 internal constant ALLOWANCE_AUTHORIZATION = keccak256(
        "AllowanceAuthorization(bytes32 orgDomain,address safe,address module,bytes32 adminOperationId,address token,address spender,uint256 expectedAllowance,uint256 newAllowance,uint256 nonce,uint64 validAfter,uint64 validBefore,uint64 leadershipEpoch,uint64 authorizerEpoch)"
    );
    bytes32 internal constant ADMIN_ACTION_AUTHORIZATION = keccak256(
        "AdminActionAuthorization(bytes32 orgDomain,address contractAddress,uint256 chainId,bytes32 authorityRole,bytes4 functionSelector,bytes32 payloadHash,bytes32 adminOperationId,uint256 adminNonce,uint64 adminEpoch,uint64 validAfter,uint64 validBefore,bytes32 workflowId)"
    );
    bytes32 internal constant VERDICT_ATTESTATION = keccak256(
        "VerdictAttestation(bytes32 callId,bytes32 commitmentHash,address escrowContract,uint64 verifierEpoch,bytes32 verificationSpecHash,bytes32 verifierSoftwareHash,bytes32 deliveryHash,uint64 deliveredAt,bytes32 evidenceHash,uint8 verdict,uint256 verdictNonce,uint64 issuedAt,uint64 validUntil)"
    );

    function domainSeparator(uint256 chainId, address verifyingContract) internal pure returns (bytes32) {
        return keccak256(abi.encode(EIP712_DOMAIN, keccak256("ASCP"), keccak256("4"), chainId, verifyingContract));
    }

    function digest(bytes32 separator, bytes32 structHash) internal pure returns (bytes32) {
        return keccak256(abi.encodePacked("\x19\x01", separator, structHash));
    }
}
