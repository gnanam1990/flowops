// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {Vm} from "forge-std/Vm.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {CallEscrow} from "../src/CallEscrow.sol";
import {MockUSDC, FeeToken, ReentrantToken} from "./mocks/MockUSDC.sol";

contract CallEscrowTest is Test {
    MockUSDC internal usdc;
    CallEscrow internal escrow;

    address internal buyer = makeAddr("buyer");
    address internal provider = makeAddr("provider");
    address internal stranger = makeAddr("stranger");

    uint256 internal constant AMOUNT = 20_000;
    uint256 internal constant RELEASE_WINDOW = 30;
    bytes32 internal CALL_ID;
    bytes32 internal constant TASK = keccak256("task-1");
    bytes32 internal constant REQUEST = keccak256("request-1");
    bytes32 internal constant RESPONSE = keccak256("response-1");
    bytes32 internal constant EVIDENCE = keccak256("evidence-1");

    function setUp() public {
        vm.warp(10_000);
        usdc = new MockUSDC();
        escrow = new CallEscrow(IERC20(address(usdc)), RELEASE_WINDOW);
        CALL_ID = escrow.deriveCallId(buyer, TASK, REQUEST);
        usdc.mint(buyer, 1_000_000);
        vm.prank(buyer);
        usdc.approve(address(escrow), type(uint256).max);
    }

    function _fund(bytes32 callId) internal returns (uint64 acknowledgeBy, uint64 deliverBy) {
        acknowledgeBy = uint64(block.timestamp + 60);
        deliverBy = uint64(block.timestamp + 180);
        vm.prank(buyer);
        escrow.fund(callId, provider, AMOUNT, TASK, REQUEST, acknowledgeBy, deliverBy);
    }

    function _deliver(bytes32 callId) internal {
        vm.prank(provider);
        escrow.acknowledge(callId);
        vm.prank(provider);
        escrow.submitDelivery(callId, RESPONSE, EVIDENCE);
    }

    function test_constructorPinsAssetAndWindowAndHasNoOwner() public view {
        assertEq(address(escrow.asset()), address(usdc));
        assertEq(escrow.optimisticReleaseWindow(), RELEASE_WINDOW);
    }

    function test_constructorRejectsEOAAndUnsafeWindows() public {
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.AssetNotContract.selector, stranger));
        new CallEscrow(IERC20(stranger), 1);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.BadOptimisticReleaseWindow.selector, 0));
        new CallEscrow(IERC20(address(usdc)), 0);
        uint256 tooLong = escrow.MAX_OPTIMISTIC_RELEASE_WINDOW() + 1;
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.BadOptimisticReleaseWindow.selector, tooLong));
        new CallEscrow(IERC20(address(usdc)), tooLong);
    }

    function test_fundLocksExactAmountAndSnapshotsAllAuthorityFields() public {
        (uint64 acknowledgeBy, uint64 deliverBy) = _fund(CALL_ID);
        CallEscrow.Call memory call_ = escrow.getCall(CALL_ID);
        assertEq(call_.buyer, buyer);
        assertEq(call_.provider, provider);
        assertEq(call_.amount, AMOUNT);
        assertEq(call_.taskDigest, TASK);
        assertEq(call_.requestDigest, REQUEST);
        assertEq(call_.acknowledgeBy, acknowledgeBy);
        assertEq(call_.deliverBy, deliverBy);
        assertEq(uint8(call_.state), uint8(CallEscrow.State.Funded));
        assertEq(usdc.balanceOf(address(escrow)), AMOUNT);
        assertEq(escrow.totalLocked(), AMOUNT);
    }

    function test_fundRejectsDuplicateAndCrossTaskReuse() public {
        _fund(CALL_ID);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.CallExists.selector, CALL_ID));
        vm.prank(buyer);
        escrow.fund(
            CALL_ID, provider, AMOUNT, TASK, REQUEST, uint64(block.timestamp + 60), uint64(block.timestamp + 180)
        );

        bytes32 otherTask = keccak256("other-task");
        bytes32 otherRequest = keccak256("other-request");
        bytes32 otherExpected = escrow.deriveCallId(buyer, otherTask, otherRequest);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.BadCallId.selector, otherExpected, CALL_ID));
        vm.prank(buyer);
        escrow.fund(
            CALL_ID,
            stranger,
            AMOUNT + 1,
            otherTask,
            otherRequest,
            uint64(block.timestamp + 60),
            uint64(block.timestamp + 180)
        );
    }

    function test_fundCallIdCannotBeSquattedByAnotherBuyer() public {
        usdc.mint(stranger, AMOUNT);
        vm.startPrank(stranger);
        usdc.approve(address(escrow), AMOUNT);
        bytes32 attackerExpected = escrow.deriveCallId(stranger, TASK, REQUEST);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.BadCallId.selector, attackerExpected, CALL_ID));
        escrow.fund(
            CALL_ID, provider, AMOUNT, TASK, REQUEST, uint64(block.timestamp + 60), uint64(block.timestamp + 180)
        );
        vm.stopPrank();

        _fund(CALL_ID);
        assertEq(escrow.getCall(CALL_ID).buyer, buyer);
    }

    function test_callIdBindsChainAndExactContractVersion() public {
        bytes32 localId = escrow.deriveCallId(buyer, TASK, REQUEST);
        vm.chainId(84532);
        bytes32 baseSepoliaId = escrow.deriveCallId(buyer, TASK, REQUEST);
        assertNotEq(localId, baseSepoliaId);

        CallEscrow nextVersion = new CallEscrow(IERC20(address(usdc)), RELEASE_WINDOW);
        assertNotEq(baseSepoliaId, nextVersion.deriveCallId(buyer, TASK, REQUEST));
    }

    function test_fundRejectsInvalidIdentityAmountDigestsAndDeadlines() public {
        uint64 acknowledgeBy = uint64(block.timestamp + 60);
        uint64 deliverBy = uint64(block.timestamp + 180);

        vm.startPrank(buyer);
        vm.expectRevert(CallEscrow.CallIdZero.selector);
        escrow.fund(bytes32(0), provider, AMOUNT, TASK, REQUEST, acknowledgeBy, deliverBy);
        bytes32 wrongCallId = keccak256("not-domain-separated");
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.BadCallId.selector, CALL_ID, wrongCallId));
        escrow.fund(wrongCallId, provider, AMOUNT, TASK, REQUEST, acknowledgeBy, deliverBy);
        vm.expectRevert(CallEscrow.ProviderZero.selector);
        escrow.fund(CALL_ID, address(0), AMOUNT, TASK, REQUEST, acknowledgeBy, deliverBy);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.BuyerIsProvider.selector, buyer));
        escrow.fund(CALL_ID, buyer, AMOUNT, TASK, REQUEST, acknowledgeBy, deliverBy);
        vm.expectRevert(CallEscrow.AmountZero.selector);
        escrow.fund(CALL_ID, provider, 0, TASK, REQUEST, acknowledgeBy, deliverBy);
        vm.expectRevert(CallEscrow.DigestZero.selector);
        escrow.fund(CALL_ID, provider, AMOUNT, bytes32(0), REQUEST, acknowledgeBy, deliverBy);
        vm.expectRevert(CallEscrow.DigestZero.selector);
        escrow.fund(CALL_ID, provider, AMOUNT, TASK, bytes32(0), acknowledgeBy, deliverBy);
        vm.expectPartialRevert(CallEscrow.BadDeadlines.selector);
        escrow.fund(CALL_ID, provider, AMOUNT, TASK, REQUEST, uint64(block.timestamp), deliverBy);
        vm.expectPartialRevert(CallEscrow.BadDeadlines.selector);
        escrow.fund(CALL_ID, provider, AMOUNT, TASK, REQUEST, acknowledgeBy, acknowledgeBy);
        vm.stopPrank();
    }

    function test_fundRejectsFeeOnTransferAssetAtomically() public {
        FeeToken feeToken = new FeeToken();
        CallEscrow feeEscrow = new CallEscrow(IERC20(address(feeToken)), RELEASE_WINDOW);
        feeToken.mint(buyer, AMOUNT);
        vm.startPrank(buyer);
        feeToken.approve(address(feeEscrow), AMOUNT);
        bytes32 feeCallId = feeEscrow.deriveCallId(buyer, TASK, REQUEST);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.InexactFunding.selector, AMOUNT, AMOUNT - 1));
        feeEscrow.fund(
            feeCallId, provider, AMOUNT, TASK, REQUEST, uint64(block.timestamp + 60), uint64(block.timestamp + 180)
        );
        vm.stopPrank();
        assertEq(feeToken.balanceOf(address(feeEscrow)), 0);
        assertEq(feeEscrow.totalLocked(), 0);
        assertEq(uint8(feeEscrow.stateOf(feeCallId)), uint8(CallEscrow.State.None));
    }

    function test_acknowledgeOnlySnapshottedProviderAtInclusiveDeadline() public {
        (uint64 acknowledgeBy,) = _fund(CALL_ID);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.NotProvider.selector, CALL_ID, stranger));
        vm.prank(stranger);
        escrow.acknowledge(CALL_ID);

        vm.warp(acknowledgeBy);
        vm.prank(provider);
        escrow.acknowledge(CALL_ID);
        assertEq(uint8(escrow.stateOf(CALL_ID)), uint8(CallEscrow.State.Acknowledged));
    }

    function test_acknowledgeAfterDeadlineCannotStealRefund() public {
        (uint64 acknowledgeBy,) = _fund(CALL_ID);
        vm.warp(uint256(acknowledgeBy) + 1);
        vm.expectRevert(
            abi.encodeWithSelector(
                CallEscrow.AcknowledgementWindowClosed.selector, CALL_ID, acknowledgeBy, block.timestamp
            )
        );
        vm.prank(provider);
        escrow.acknowledge(CALL_ID);
        escrow.refundExpired(CALL_ID);
        assertEq(usdc.balanceOf(buyer), 1_000_000);
    }

    function test_refundUnacknowledgedOnlyAfterDeadlineAndOnlyToBuyer() public {
        (uint64 acknowledgeBy,) = _fund(CALL_ID);
        vm.warp(acknowledgeBy);
        vm.expectPartialRevert(CallEscrow.RefundNotAvailable.selector);
        vm.prank(stranger);
        escrow.refundExpired(CALL_ID);

        vm.warp(uint256(acknowledgeBy) + 1);
        vm.prank(stranger);
        escrow.refundExpired(CALL_ID);
        assertEq(usdc.balanceOf(buyer), 1_000_000);
        assertEq(usdc.balanceOf(stranger), 0);
        assertEq(uint8(escrow.stateOf(CALL_ID)), uint8(CallEscrow.State.Refunded));
        assertEq(escrow.totalLocked(), 0);
    }

    function test_refundAcknowledgedButUndeliveredOnlyAfterDeliveryDeadline() public {
        (, uint64 deliverBy) = _fund(CALL_ID);
        vm.prank(provider);
        escrow.acknowledge(CALL_ID);
        vm.warp(uint256(deliverBy) + 1);
        escrow.refundExpired(CALL_ID);
        assertEq(usdc.balanceOf(buyer), 1_000_000);
        assertEq(uint8(escrow.stateOf(CALL_ID)), uint8(CallEscrow.State.Refunded));
    }

    function test_submitDeliveryBindsBothDigestsAtInclusiveDeadline() public {
        (, uint64 deliverBy) = _fund(CALL_ID);
        vm.prank(provider);
        escrow.acknowledge(CALL_ID);
        vm.warp(deliverBy);
        vm.prank(provider);
        escrow.submitDelivery(CALL_ID, RESPONSE, EVIDENCE);
        CallEscrow.Call memory call_ = escrow.getCall(CALL_ID);
        assertEq(call_.responseDigest, RESPONSE);
        assertEq(call_.evidenceDigest, EVIDENCE);
        assertEq(call_.deliveredAt, deliverBy);
        assertEq(escrow.releasableAt(CALL_ID), uint256(deliverBy) + RELEASE_WINDOW);
    }

    function test_submitDeliveryRejectsWrongProviderEmptyProofLateAndReplay() public {
        (, uint64 deliverBy) = _fund(CALL_ID);
        vm.prank(provider);
        escrow.acknowledge(CALL_ID);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.NotProvider.selector, CALL_ID, stranger));
        vm.prank(stranger);
        escrow.submitDelivery(CALL_ID, RESPONSE, EVIDENCE);
        vm.expectRevert(CallEscrow.DigestZero.selector);
        vm.prank(provider);
        escrow.submitDelivery(CALL_ID, bytes32(0), EVIDENCE);
        vm.warp(uint256(deliverBy) + 1);
        vm.expectRevert(
            abi.encodeWithSelector(CallEscrow.DeliveryWindowClosed.selector, CALL_ID, deliverBy, block.timestamp)
        );
        vm.prank(provider);
        escrow.submitDelivery(CALL_ID, RESPONSE, EVIDENCE);
    }

    function test_acceptDeliveryOnlyBuyerAndPaysOnlyProvider() public {
        _fund(CALL_ID);
        _deliver(CALL_ID);
        vm.expectRevert(abi.encodeWithSelector(CallEscrow.NotBuyer.selector, CALL_ID, stranger));
        vm.prank(stranger);
        escrow.acceptDelivery(CALL_ID);

        vm.prank(buyer);
        escrow.acceptDelivery(CALL_ID);
        assertEq(usdc.balanceOf(provider), AMOUNT);
        assertEq(usdc.balanceOf(stranger), 0);
        assertEq(uint8(escrow.stateOf(CALL_ID)), uint8(CallEscrow.State.Released));
        assertEq(escrow.totalLocked(), 0);
    }

    function test_releaseEventFollowsAssetTransferLog() public {
        _fund(CALL_ID);
        _deliver(CALL_ID);
        vm.recordLogs();
        vm.prank(buyer);
        escrow.acceptDelivery(CALL_ID);

        Vm.Log[] memory logs = vm.getRecordedLogs();
        bytes32 transferSignature = keccak256("Transfer(address,address,uint256)");
        bytes32 releasedSignature = keccak256("Released(bytes32,address,uint256,bool)");
        uint256 transferIndex = type(uint256).max;
        uint256 releasedIndex = type(uint256).max;
        for (uint256 i = 0; i < logs.length; i++) {
            if (logs[i].topics[0] == transferSignature) transferIndex = i;
            if (logs[i].topics[0] == releasedSignature) releasedIndex = i;
        }
        assertLt(transferIndex, releasedIndex);
    }

    function test_refundEventFollowsAssetTransferLog() public {
        (uint64 acknowledgeBy,) = _fund(CALL_ID);
        vm.warp(uint256(acknowledgeBy) + 1);
        vm.recordLogs();
        escrow.refundExpired(CALL_ID);

        Vm.Log[] memory logs = vm.getRecordedLogs();
        bytes32 transferSignature = keccak256("Transfer(address,address,uint256)");
        bytes32 refundedSignature = keccak256("Refunded(bytes32,address,uint256,uint8)");
        uint256 transferIndex = type(uint256).max;
        uint256 refundedIndex = type(uint256).max;
        for (uint256 i = 0; i < logs.length; i++) {
            if (logs[i].topics[0] == transferSignature) transferIndex = i;
            if (logs[i].topics[0] == refundedSignature) refundedIndex = i;
        }
        assertLt(transferIndex, refundedIndex);
    }

    function test_reentrancyCannotFinalizeAnotherExpiredPosition() public {
        ReentrantToken token = new ReentrantToken();
        CallEscrow guarded = new CallEscrow(IERC20(address(token)), RELEASE_WINDOW);
        token.mint(buyer, 2 * AMOUNT);
        vm.prank(buyer);
        token.approve(address(guarded), type(uint256).max);

        bytes32 firstRequest = keccak256("reentrant-first");
        bytes32 first = guarded.deriveCallId(buyer, TASK, firstRequest);
        vm.prank(buyer);
        guarded.fund(
            first, provider, AMOUNT, TASK, firstRequest, uint64(block.timestamp + 60), uint64(block.timestamp + 180)
        );
        bytes32 secondRequest = keccak256("reentrant-second");
        bytes32 second = guarded.deriveCallId(buyer, TASK, secondRequest);
        vm.prank(buyer);
        guarded.fund(
            second, provider, AMOUNT, TASK, secondRequest, uint64(block.timestamp + 5), uint64(block.timestamp + 180)
        );
        vm.prank(provider);
        guarded.acknowledge(first);
        vm.prank(provider);
        guarded.submitDelivery(first, RESPONSE, EVIDENCE);
        token.arm(address(guarded), second);

        vm.warp(guarded.releasableAt(first));
        guarded.optimisticRelease(first);
        assertTrue(token.attempted());
        assertTrue(token.blocked());
        assertEq(uint8(guarded.stateOf(second)), uint8(CallEscrow.State.Funded));

        guarded.refundExpired(second);
        assertEq(uint8(guarded.stateOf(second)), uint8(CallEscrow.State.Refunded));
        assertEq(guarded.totalLocked(), 0);
    }

    function test_optimisticReleaseIsPermissionlessAtExactBoundary() public {
        _fund(CALL_ID);
        _deliver(CALL_ID);
        uint256 releaseAt = escrow.releasableAt(CALL_ID);
        vm.warp(releaseAt - 1);
        vm.expectRevert(
            abi.encodeWithSelector(CallEscrow.ReleaseWindowOpen.selector, CALL_ID, releaseAt, block.timestamp)
        );
        escrow.optimisticRelease(CALL_ID);

        vm.warp(releaseAt);
        vm.prank(stranger);
        escrow.optimisticRelease(CALL_ID);
        assertEq(usdc.balanceOf(provider), AMOUNT);
        assertEq(uint8(escrow.stateOf(CALL_ID)), uint8(CallEscrow.State.Released));
    }

    function test_releaseAndRefundAreMutuallyExclusiveTerminalStates() public {
        _fund(CALL_ID);
        _deliver(CALL_ID);
        vm.prank(buyer);
        escrow.acceptDelivery(CALL_ID);
        vm.warp(block.timestamp + 1_000_000);
        vm.expectPartialRevert(CallEscrow.RefundNotAvailable.selector);
        escrow.refundExpired(CALL_ID);
        vm.expectPartialRevert(CallEscrow.WrongState.selector);
        escrow.optimisticRelease(CALL_ID);

        bytes32 secondRequest = keccak256("request-2");
        bytes32 second = escrow.deriveCallId(buyer, TASK, secondRequest);
        uint64 secondAcknowledgeBy = uint64(block.timestamp + 60);
        uint64 deliverBy = uint64(block.timestamp + 180);
        vm.prank(buyer);
        escrow.fund(second, provider, AMOUNT, TASK, secondRequest, secondAcknowledgeBy, deliverBy);
        vm.prank(provider);
        escrow.acknowledge(second);
        vm.warp(uint256(deliverBy) + 1);
        escrow.refundExpired(second);
        vm.expectPartialRevert(CallEscrow.WrongState.selector);
        vm.prank(buyer);
        escrow.acceptDelivery(second);
    }

    function testFuzz_fundAndRefundConserveExactAtomicAmount(uint96 rawAmount, uint32 ackDelay, uint32 deliveryGap)
        public
    {
        uint256 amount = bound(uint256(rawAmount), 1, 1_000_000);
        uint64 acknowledgeBy = uint64(block.timestamp + bound(uint256(ackDelay), 1, 7 days));
        uint64 deliverBy = uint64(uint256(acknowledgeBy) + bound(uint256(deliveryGap), 1, 7 days));
        usdc.mint(buyer, amount);
        bytes32 requestDigest = keccak256(abi.encode(rawAmount, ackDelay, deliveryGap));
        bytes32 callId = escrow.deriveCallId(buyer, TASK, requestDigest);
        vm.prank(buyer);
        escrow.fund(callId, provider, amount, TASK, requestDigest, acknowledgeBy, deliverBy);
        assertEq(usdc.balanceOf(address(escrow)), amount);
        vm.warp(uint256(acknowledgeBy) + 1);
        escrow.refundExpired(callId);
        assertEq(usdc.balanceOf(address(escrow)), 0);
        assertEq(escrow.totalLocked(), 0);
    }
}
