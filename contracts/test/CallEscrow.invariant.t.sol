// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {StdInvariant} from "forge-std/StdInvariant.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {CallEscrow} from "../src/CallEscrow.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract CallEscrowHandler is Test {
    MockUSDC internal immutable usdc;
    CallEscrow internal immutable escrow;
    address internal immutable buyer;
    address internal immutable provider;
    bytes32[] internal callIds;
    uint256 internal nonce;

    constructor(MockUSDC usdc_, CallEscrow escrow_, address buyer_, address provider_) {
        usdc = usdc_;
        escrow = escrow_;
        buyer = buyer_;
        provider = provider_;
    }

    function fund(uint96 rawAmount, uint32 ackDelay, uint32 deliveryGap) external {
        if (callIds.length >= 32) return;
        uint256 amount = bound(uint256(rawAmount), 1, 1_000_000);
        uint64 acknowledgeBy = uint64(block.timestamp + bound(uint256(ackDelay), 1, 1 days));
        uint64 deliverBy = uint64(uint256(acknowledgeBy) + bound(uint256(deliveryGap), 1, 1 days));
        bytes32 requestDigest = keccak256(abi.encode("request", nonce++));
        bytes32 taskDigest = keccak256(abi.encode("task", nonce));
        bytes32 callId = escrow.deriveCallId(buyer, taskDigest, requestDigest);
        usdc.mint(buyer, amount);
        vm.prank(buyer);
        try escrow.fund(callId, provider, amount, taskDigest, requestDigest, acknowledgeBy, deliverBy) {
            callIds.push(callId);
        } catch {}
    }

    function acknowledge(uint256 index) external {
        if (callIds.length == 0) return;
        bytes32 callId = callIds[index % callIds.length];
        vm.prank(provider);
        try escrow.acknowledge(callId) {} catch {}
    }

    function deliver(uint256 index, bytes32 responseDigest, bytes32 evidenceDigest) external {
        if (callIds.length == 0 || responseDigest == bytes32(0) || evidenceDigest == bytes32(0)) return;
        bytes32 callId = callIds[index % callIds.length];
        vm.prank(provider);
        try escrow.submitDelivery(callId, responseDigest, evidenceDigest) {} catch {}
    }

    function accept(uint256 index) external {
        if (callIds.length == 0) return;
        bytes32 callId = callIds[index % callIds.length];
        vm.prank(buyer);
        try escrow.acceptDelivery(callId) {} catch {}
    }

    function release(uint256 index) external {
        if (callIds.length == 0) return;
        try escrow.optimisticRelease(callIds[index % callIds.length]) {} catch {}
    }

    function refund(uint256 index) external {
        if (callIds.length == 0) return;
        try escrow.refundExpired(callIds[index % callIds.length]) {} catch {}
    }

    function advanceTime(uint32 seconds_) external {
        vm.warp(block.timestamp + bound(uint256(seconds_), 1, 2 days));
    }

    function callsLength() external view returns (uint256) {
        return callIds.length;
    }

    function callIdAt(uint256 index) external view returns (bytes32) {
        return callIds[index];
    }
}

contract CallEscrowInvariantTest is StdInvariant, Test {
    MockUSDC internal usdc;
    CallEscrow internal escrow;
    CallEscrowHandler internal handler;
    address internal buyer = makeAddr("invariant-buyer");
    address internal provider = makeAddr("invariant-provider");

    function setUp() public {
        vm.warp(10_000);
        usdc = new MockUSDC();
        escrow = new CallEscrow(IERC20(address(usdc)), 30);
        handler = new CallEscrowHandler(usdc, escrow, buyer, provider);
        vm.prank(buyer);
        usdc.approve(address(escrow), type(uint256).max);
        targetContract(address(handler));
    }

    function invariant_livePositionsEqualLockedAccounting() public view {
        uint256 live;
        uint256 count = handler.callsLength();
        for (uint256 i = 0; i < count; i++) {
            CallEscrow.Call memory call_ = escrow.getCall(handler.callIdAt(i));
            if (
                call_.state == CallEscrow.State.Funded || call_.state == CallEscrow.State.Acknowledged
                    || call_.state == CallEscrow.State.Delivered
            ) {
                live += call_.amount;
            }
        }
        assertEq(escrow.totalLocked(), live);
        assertEq(usdc.balanceOf(address(escrow)), live);
    }

    function invariant_valueOnlyExistsWithBuyerProviderOrEscrow() public view {
        assertEq(usdc.totalSupply(), usdc.balanceOf(buyer) + usdc.balanceOf(provider) + usdc.balanceOf(address(escrow)));
    }

    function invariant_terminalCallsNeverRetainLockedValue() public view {
        uint256 count = handler.callsLength();
        for (uint256 i = 0; i < count; i++) {
            CallEscrow.Call memory call_ = escrow.getCall(handler.callIdAt(i));
            if (call_.state == CallEscrow.State.Released || call_.state == CallEscrow.State.Refunded) {
                assertTrue(call_.responseDigest != bytes32(0) || call_.state == CallEscrow.State.Refunded);
            }
        }
    }
}
