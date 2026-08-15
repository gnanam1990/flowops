// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {FlowOpsProposalAnchor} from "../src/FlowOpsProposalAnchor.sol";

contract FlowOpsProposalAnchorTest is Test {
    bytes32 internal constant PROPOSAL_DIGEST = keccak256("flowops-proposal-v1");
    bytes20 internal constant SOURCE_COMMIT = hex"0123456789abcdef0123456789abcdef01234567";

    FlowOpsProposalAnchor internal anchor;

    function setUp() public {
        vm.chainId(8_453);
        anchor = new FlowOpsProposalAnchor(PROPOSAL_DIGEST, SOURCE_COMMIT);
    }

    function test_constructorPinsEvidenceAndPermanentExperimentalStatus() public view {
        assertEq(anchor.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(anchor.KIND(), keccak256("FLOWOPS_PROPOSAL_ANCHOR_V1"));
        assertEq(anchor.DEPLOYMENT_STATUS(), "EXPERIMENTAL_UNAUDITED_NO_FUNDS");
        assertEq(anchor.proposalDigest(), PROPOSAL_DIGEST);
        assertEq(anchor.sourceCommit(), SOURCE_COMMIT);
        assertEq(anchor.deployer(), address(this));
        assertFalse(anchor.productionReady());
        assertFalse(anchor.acceptsFunds());
        assertFalse(anchor.vaultCreationEnabled());
    }

    function test_constructorRejectsEveryOtherChain() public {
        vm.chainId(84_532);
        vm.expectRevert(abi.encodeWithSelector(FlowOpsProposalAnchor.WrongChain.selector, 8_453, 84_532));
        new FlowOpsProposalAnchor(PROPOSAL_DIGEST, SOURCE_COMMIT);
    }

    function test_constructorRejectsMissingEvidenceBindings() public {
        vm.expectRevert(FlowOpsProposalAnchor.ProposalDigestZero.selector);
        new FlowOpsProposalAnchor(bytes32(0), SOURCE_COMMIT);

        vm.expectRevert(FlowOpsProposalAnchor.SourceCommitZero.selector);
        new FlowOpsProposalAnchor(PROPOSAL_DIGEST, bytes20(0));
    }

    function test_emptyCalldataAndDirectEthAreRejected() public {
        address sender = makeAddr("sender");
        vm.deal(sender, 1 ether);

        vm.prank(sender);
        (bool emptyCallAccepted,) = address(anchor).call("");
        assertFalse(emptyCallAccepted);

        vm.prank(sender);
        (bool ethAccepted,) = address(anchor).call{value: 1 wei}("");
        assertFalse(ethAccepted);
        assertEq(address(anchor).balance, 0);
    }

    function test_fundVaultUpgradeAndAdministrativeSelectorsAreAbsent() public {
        bytes[] memory forbiddenCalls = new bytes[](6);
        forbiddenCalls[0] = abi.encodeWithSignature("fund(bytes32,address,uint256,bytes32,bytes32,uint64,uint64)");
        forbiddenCalls[1] = abi.encodeWithSignature("deposit(uint256)", 1);
        forbiddenCalls[2] = abi.encodeWithSignature("createVault(address)", address(this));
        forbiddenCalls[3] = abi.encodeWithSignature("upgradeToAndCall(address,bytes)", address(this), bytes(""));
        forbiddenCalls[4] = abi.encodeWithSignature("setProductionReady(bool)", true);
        forbiddenCalls[5] = abi.encodeWithSignature("withdraw(address,uint256)", address(this), 1);

        for (uint256 i = 0; i < forbiddenCalls.length; ++i) {
            (bool accepted,) = address(anchor).call(forbiddenCalls[i]);
            assertFalse(accepted);
        }

        assertFalse(anchor.productionReady());
        assertFalse(anchor.acceptsFunds());
        assertFalse(anchor.vaultCreationEnabled());
    }
}
