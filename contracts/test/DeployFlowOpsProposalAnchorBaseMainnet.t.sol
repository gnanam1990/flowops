// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {FlowOpsProposalAnchor} from "../src/FlowOpsProposalAnchor.sol";
import {DeployFlowOpsProposalAnchorBaseMainnet} from "../script/DeployFlowOpsProposalAnchorBaseMainnet.s.sol";

contract ApprovedProposalAnchorDeploymentHarness is DeployFlowOpsProposalAnchorBaseMainnet {
    bytes32 public constant READY_APPROVAL_DIGEST = keccak256("fresh-mainnet-approval");

    function _deploymentApprovalDigest() internal pure override returns (bytes32) {
        return READY_APPROVAL_DIGEST;
    }

    function _broadcastEnabled() internal pure override returns (bool) {
        return true;
    }
}

contract DeployFlowOpsProposalAnchorBaseMainnetTest is Test {
    DeployFlowOpsProposalAnchorBaseMainnet internal deployment;

    function setUp() public {
        deployment = new DeployFlowOpsProposalAnchorBaseMainnet();
    }

    function test_committedPackagePinsActivationApprovalAndStaysStructurallyBlocked() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(deployment.DESIGNATED_DEPLOYER(), 0xEEC526F6555dD43536F712D5c978CbC13CB4517f);
        assertEq(deployment.PROPOSAL_DIGEST(), 0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362);
        assertEq(deployment.SOURCE_COMMIT(), hex"bd9292d0f916b1e3d828443b41e31a8e635b2b3e");
        assertEq(
            deployment.PROMOTION_PACKAGE_APPROVAL_DIGEST(),
            0xbfc1cd20d1f05885029683100e8c0a5387948597db5de68ea13eb1043223a726
        );
        assertEq(deployment.EXPECTED_DEPLOYER_NONCE(), 0);
        assertEq(deployment.EXPECTED_ANCHOR_ADDRESS(), 0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250);
        assertEq(deployment.INITCODE_HASH(), 0x41d3a9c08503394daca600ba7520c6818d7f373c08ecff3c916e2eceef93d35e);
        assertEq(
            deployment.EXPECTED_RUNTIME_CODE_HASH(), 0xe5b5b63f37bfd5b6627f48cedd8c0fdcc841f130fd1d5259058374e7a543ed86
        );
        assertEq(deployment.ESTIMATED_GAS(), 188_437);
        assertEq(deployment.MAX_GAS_LIMIT(), 250_000);
        assertEq(deployment.MAX_FEE_PER_GAS_WEI(), 20_000_000);
        assertEq(deployment.MAX_GAS_SPEND_WEI(), 5_000_000_000_000);
        assertEq(
            deployment.DEPLOYMENT_APPROVAL_DIGEST(), 0x5f7b7a92e649df58f7df8afd468e514c8ac5d0f7ff7c5a8108150d25f2cefd17
        );
        assertFalse(deployment.MAINNET_BROADCAST_ENABLED());
    }

    function test_runRejectsEveryOtherChainBeforeReleaseGates() public {
        vm.chainId(84_532);
        vm.expectRevert(
            abi.encodeWithSelector(DeployFlowOpsProposalAnchorBaseMainnet.WrongChain.selector, 8_453, 84_532)
        );
        deployment.run();
    }

    function test_runCannotBroadcastFromCommittedPackage() public {
        vm.chainId(8_453);
        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.run();
    }

    function test_promotedHarnessRejectsNonceDriftBeforeBroadcast() public {
        ApprovedProposalAnchorDeploymentHarness ready = new ApprovedProposalAnchorDeploymentHarness();
        vm.chainId(8_453);
        vm.setNonce(ready.DESIGNATED_DEPLOYER(), 1);
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployFlowOpsProposalAnchorBaseMainnet.DeployerNonceMismatch.selector, uint64(0), uint64(1)
            )
        );
        ready.run();
    }

    function test_releaseGatesRejectEveryMissingBinding() public {
        address deployer = address(0xBEEF);
        bytes32 proposalDigest = keccak256("proposal");
        bytes20 sourceCommit = hex"0123456789abcdef0123456789abcdef01234567";
        bytes32 approvalDigest = keccak256("approval");

        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.MainnetDeployerNotDesignated.selector);
        deployment.validateReleaseGates(address(0), proposalDigest, sourceCommit, approvalDigest, true);

        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.ProposalDigestNotRecorded.selector);
        deployment.validateReleaseGates(deployer, bytes32(0), sourceCommit, approvalDigest, true);

        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.SourceCommitNotRecorded.selector);
        deployment.validateReleaseGates(deployer, proposalDigest, bytes20(0), approvalDigest, true);

        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.DeploymentApprovalNotRecorded.selector);
        deployment.validateReleaseGates(deployer, proposalDigest, sourceCommit, bytes32(0), true);

        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.validateReleaseGates(deployer, proposalDigest, sourceCommit, approvalDigest, false);
    }

    function test_promotedHarnessDeploysPermanentNoFundsAnchor() public {
        ApprovedProposalAnchorDeploymentHarness ready = new ApprovedProposalAnchorDeploymentHarness();
        vm.chainId(8_453);

        FlowOpsProposalAnchor anchor = ready.run();

        assertEq(address(anchor), ready.EXPECTED_ANCHOR_ADDRESS());
        assertEq(address(anchor).codehash, ready.EXPECTED_RUNTIME_CODE_HASH());
        assertEq(anchor.deployer(), ready.DESIGNATED_DEPLOYER());
        assertEq(anchor.proposalDigest(), ready.PROPOSAL_DIGEST());
        assertEq(anchor.sourceCommit(), ready.SOURCE_COMMIT());
        assertEq(anchor.DEPLOYMENT_STATUS(), "EXPERIMENTAL_UNAUDITED_NO_FUNDS");
        assertFalse(anchor.productionReady());
        assertFalse(anchor.acceptsFunds());
        assertFalse(anchor.vaultCreationEnabled());
    }
}
