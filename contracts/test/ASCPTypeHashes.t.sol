// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {ASCPTypeHashes} from "../src/libraries/ASCPTypeHashes.sol";

contract ASCPTypeHashesTest is Test {
    struct SellerQuote {
        bytes32 purchaseSpecHash;
        bytes32 sellerId;
        bytes32 resourceId;
        uint64 directoryVersion;
        uint16 schemeVersion;
        uint256 chainId;
        address asset;
        uint256 amountBaseUnits;
        address payTo;
        address ackAuthority;
        bytes32 verificationSpecHash;
        uint64 declaredWorkTime;
        uint64 verificationBudgetSeconds;
        uint64 quoteExpiresAt;
        bytes32 quoteNonce;
    }

    function testRegistryPinsEveryNormativeTypeString() public pure {
        assertEq(
            ASCPTypeHashes.EIP712_DOMAIN,
            keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
        );
        assertEq(
            ASCPTypeHashes.EXECUTION_COMMITMENT,
            keccak256(
                "ExecutionCommitment(bytes32 orgDomain,bytes32 operationId,uint8 rail,uint16 schemeVersion,uint8 protection,address escrowContract,bytes32 purchaseSpecHash,bytes32 quoteHash,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 directoryVersion,bytes32 sellerId,bytes32 resourceId,address payTo,address ackAuthority,uint256 amount,uint256 chainId,address asset,uint64 quoteExpiresAt,uint64 acceptBy,uint64 deliverBy,uint64 settleBy)"
            )
        );
        assertEq(
            ASCPTypeHashes.SELLER_QUOTE,
            keccak256(
                "SellerQuote(bytes32 purchaseSpecHash,bytes32 sellerId,bytes32 resourceId,uint64 directoryVersion,uint16 schemeVersion,uint256 chainId,address asset,uint256 amountBaseUnits,address payTo,address ackAuthority,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 quoteExpiresAt,bytes32 quoteNonce)"
            )
        );
        assertEq(
            ASCPTypeHashes.LOCK_AUTHORIZATION,
            keccak256(
                "LockAuthorization(bytes32 orgDomain,address safe,address module,bytes32 operationId,bytes32 commitmentHash,bytes32 calldataHash,address escrow,uint256 amount,uint256 nonce,uint64 validAfter,uint64 validBefore,uint64 leadershipEpoch,uint64 authorizerEpoch)"
            )
        );
        assertEq(
            ASCPTypeHashes.ALLOWANCE_AUTHORIZATION,
            keccak256(
                "AllowanceAuthorization(bytes32 orgDomain,address safe,address module,bytes32 adminOperationId,address token,address spender,uint256 expectedAllowance,uint256 newAllowance,uint256 nonce,uint64 validAfter,uint64 validBefore,uint64 leadershipEpoch,uint64 authorizerEpoch)"
            )
        );
        assertEq(
            ASCPTypeHashes.ADMIN_ACTION_AUTHORIZATION,
            keccak256(
                "AdminActionAuthorization(bytes32 orgDomain,address contractAddress,uint256 chainId,bytes32 authorityRole,bytes4 functionSelector,bytes32 payloadHash,bytes32 adminOperationId,uint256 adminNonce,uint64 adminEpoch,uint64 validAfter,uint64 validBefore,bytes32 workflowId)"
            )
        );
        assertEq(
            ASCPTypeHashes.VERDICT_ATTESTATION,
            keccak256(
                "VerdictAttestation(bytes32 callId,bytes32 commitmentHash,address escrowContract,uint64 verifierEpoch,bytes32 verificationSpecHash,bytes32 verifierSoftwareHash,bytes32 deliveryHash,uint64 deliveredAt,bytes32 evidenceHash,uint8 verdict,uint256 verdictNonce,uint64 issuedAt,uint64 validUntil)"
            )
        );
    }

    function testSellerQuoteMatchesPublishedCrossLanguageVector() public pure {
        SellerQuote memory quote = SellerQuote({
            purchaseSpecHash: bytes32(uint256(1)),
            sellerId: bytes32(uint256(2)),
            resourceId: bytes32(uint256(3)),
            directoryVersion: 9,
            schemeVersion: 1,
            chainId: 84_532,
            asset: address(0x036CbD53842c5426634e7929541eC2318f3dCF7e),
            amountBaseUnits: 42,
            payTo: address(0x3333333333333333333333333333333333333333),
            ackAuthority: address(0x4444444444444444444444444444444444444444),
            verificationSpecHash: bytes32(uint256(4)),
            declaredWorkTime: 30,
            verificationBudgetSeconds: 10,
            quoteExpiresAt: 1_900_000_000,
            quoteNonce: bytes32(uint256(5))
        });
        bytes32 structHash = keccak256(abi.encode(ASCPTypeHashes.SELLER_QUOTE, quote));
        assertEq(structHash, 0x0152a523033e8ad13633a303e819b0c359174ac12574a86fec9ac22f9301462b);

        bytes32 separator = ASCPTypeHashes.domainSeparator(84_532, address(0x1111111111111111111111111111111111111111));
        assertEq(separator, 0x38fc76dc6879cd78bd1138e65f61e972f99be7612cd3661abc5d194886acc722);
        assertEq(
            ASCPTypeHashes.digest(separator, structHash),
            0x8617e94d747d3126b442bd4911992ef5418109ee0b3f127fc031a49c6fbd141a
        );
    }
}
