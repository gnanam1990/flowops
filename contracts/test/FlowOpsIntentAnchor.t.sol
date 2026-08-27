// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {FlowOpsIntentAnchor} from "../src/FlowOpsIntentAnchor.sol";

contract FlowOpsIntentAnchorTest is Test {
    address internal controller = makeAddr("controller");
    address internal otherController = makeAddr("other-controller");
    bytes32 internal constant INTENT_DIGEST = keccak256("intent");
    bytes32 internal constant POLICY_DIGEST = keccak256("policy");

    FlowOpsIntentAnchor internal anchor;

    function setUp() public {
        vm.chainId(8_453);
        vm.warp(2_000_000_000);
        anchor = new FlowOpsIntentAnchor();
    }

    function test_constructorPinsBaseMainnetAndNoFundsStatus() public view {
        assertEq(anchor.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(anchor.MAX_INTENT_LIFETIME(), 30 days);
        assertEq(anchor.KIND(), keccak256("FLOWOPS_INTENT_ANCHOR_V1"));
        assertEq(anchor.DEPLOYMENT_STATUS(), "LIMITED_MAINNET_INTENT_EVIDENCE_NO_FUNDS");
        assertFalse(anchor.acceptsFunds());
        assertFalse(anchor.executesPayments());
    }

    function test_constructorRejectsEveryOtherChain() public {
        vm.chainId(84_532);
        vm.expectRevert(abi.encodeWithSelector(FlowOpsIntentAnchor.WrongChain.selector, 8_453, 84_532));
        new FlowOpsIntentAnchor();
    }

    function test_anchorRecordsExactControllerScopedIntent() public {
        uint64 expiresAt = uint64(block.timestamp + 1 days);

        vm.expectEmit(true, true, true, true, address(anchor));
        emit FlowOpsIntentAnchor.IntentAnchored(
            controller, INTENT_DIGEST, POLICY_DIGEST, uint64(block.timestamp), expiresAt
        );
        vm.prank(controller);
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, expiresAt);

        (bytes32 policyDigest, uint64 anchoredAt, uint64 recordedExpiry, bool active) =
            anchor.getIntent(controller, INTENT_DIGEST);
        assertEq(policyDigest, POLICY_DIGEST);
        assertEq(anchoredAt, uint64(block.timestamp));
        assertEq(recordedExpiry, expiresAt);
        assertTrue(active);

        (policyDigest, anchoredAt, recordedExpiry, active) = anchor.getIntent(otherController, INTENT_DIGEST);
        assertEq(policyDigest, bytes32(0));
        assertEq(anchoredAt, 0);
        assertEq(recordedExpiry, 0);
        assertFalse(active);
    }

    function test_sameDigestCanBeAnchoredByDifferentControllersWithoutCollision() public {
        uint64 expiresAt = uint64(block.timestamp + 1 days);

        vm.prank(controller);
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, expiresAt);

        vm.prank(otherController);
        anchor.anchorIntent(INTENT_DIGEST, keccak256("other-policy"), expiresAt);

        (bytes32 otherPolicy,,, bool active) = anchor.getIntent(otherController, INTENT_DIGEST);
        assertEq(otherPolicy, keccak256("other-policy"));
        assertTrue(active);
    }

    function test_replayAndPolicySubstitutionCannotReplaceExistingRecord() public {
        uint64 expiresAt = uint64(block.timestamp + 1 days);
        vm.prank(controller);
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, expiresAt);

        vm.expectRevert(
            abi.encodeWithSelector(FlowOpsIntentAnchor.IntentAlreadyAnchored.selector, controller, INTENT_DIGEST)
        );
        vm.prank(controller);
        anchor.anchorIntent(INTENT_DIGEST, keccak256("substituted-policy"), expiresAt);

        (bytes32 policyDigest,,, bool active) = anchor.getIntent(controller, INTENT_DIGEST);
        assertEq(policyDigest, POLICY_DIGEST);
        assertTrue(active);
    }

    function test_rejectsMissingExpiredAndOverlongBindings() public {
        uint64 currentTime = uint64(block.timestamp);

        vm.expectRevert(FlowOpsIntentAnchor.IntentDigestZero.selector);
        anchor.anchorIntent(bytes32(0), POLICY_DIGEST, currentTime + 1);

        vm.expectRevert(FlowOpsIntentAnchor.PolicyDigestZero.selector);
        anchor.anchorIntent(INTENT_DIGEST, bytes32(0), currentTime + 1);

        vm.expectRevert(abi.encodeWithSelector(FlowOpsIntentAnchor.IntentExpired.selector, currentTime, currentTime));
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, currentTime);

        vm.expectRevert(
            abi.encodeWithSelector(
                FlowOpsIntentAnchor.IntentLifetimeTooLong.selector, uint64(30 days), uint64(30 days + 1)
            )
        );
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, currentTime + uint64(30 days + 1));
    }

    function test_activeTurnsFalseAtExpiryWithoutMutatingEvidence() public {
        uint64 expiresAt = uint64(block.timestamp + 1 hours);
        vm.prank(controller);
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, expiresAt);

        vm.warp(expiresAt);
        (bytes32 policyDigest, uint64 anchoredAt, uint64 recordedExpiry, bool active) =
            anchor.getIntent(controller, INTENT_DIGEST);
        assertEq(policyDigest, POLICY_DIGEST);
        assertEq(anchoredAt, 2_000_000_000);
        assertEq(recordedExpiry, expiresAt);
        assertFalse(active);
    }

    function test_emptyCalldataEthAndForbiddenFinancialSelectorsAreRejected() public {
        vm.deal(controller, 1 ether);

        vm.prank(controller);
        (bool emptyCallAccepted,) = address(anchor).call("");
        assertFalse(emptyCallAccepted);

        vm.prank(controller);
        (bool ethAccepted,) = address(anchor).call{value: 1 wei}("");
        assertFalse(ethAccepted);
        assertEq(address(anchor).balance, 0);

        bytes[] memory forbiddenCalls = new bytes[](5);
        forbiddenCalls[0] = abi.encodeWithSignature("approve(address,uint256)", controller, 1);
        forbiddenCalls[1] = abi.encodeWithSignature("transfer(address,uint256)", controller, 1);
        forbiddenCalls[2] = abi.encodeWithSignature("execute(address,bytes)", controller, bytes(""));
        forbiddenCalls[3] = abi.encodeWithSignature("upgradeToAndCall(address,bytes)", controller, bytes(""));
        forbiddenCalls[4] = abi.encodeWithSignature("withdraw(address,uint256)", controller, 1);

        for (uint256 i = 0; i < forbiddenCalls.length; ++i) {
            (bool accepted,) = address(anchor).call(forbiddenCalls[i]);
            assertFalse(accepted);
        }
    }

    function testFuzz_anchorAcceptsEveryBoundedFutureLifetime(uint32 lifetimeSeed) public {
        uint64 lifetime = uint64(bound(lifetimeSeed, 1, 30 days));
        uint64 expiresAt = uint64(block.timestamp) + lifetime;

        vm.prank(controller);
        anchor.anchorIntent(INTENT_DIGEST, POLICY_DIGEST, expiresAt);

        (bytes32 policyDigest,, uint64 recordedExpiry, bool active) = anchor.getIntent(controller, INTENT_DIGEST);
        assertEq(policyDigest, POLICY_DIGEST);
        assertEq(recordedExpiry, expiresAt);
        assertTrue(active);
    }
}
