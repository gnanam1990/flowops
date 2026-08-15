// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {FlowOpsProposalAnchor} from "../src/FlowOpsProposalAnchor.sol";
import {DeployFlowOpsProposalAnchorBaseMainnet} from "../script/DeployFlowOpsProposalAnchorBaseMainnet.s.sol";

contract ReadyProposalAnchorDeploymentHarness is DeployFlowOpsProposalAnchorBaseMainnet {
    address public constant READY_DEPLOYER = address(0xBEEF);
    bytes32 public constant READY_PROPOSAL_DIGEST = keccak256("flowops-proposal-v1");
    bytes20 public constant READY_SOURCE_COMMIT = hex"0123456789abcdef0123456789abcdef01234567";
    bytes32 public constant READY_APPROVAL_DIGEST = keccak256("fresh-mainnet-approval");

    function _designatedDeployer() internal pure override returns (address) {
        return READY_DEPLOYER;
    }

    function _proposalDigest() internal pure override returns (bytes32) {
        return READY_PROPOSAL_DIGEST;
    }

    function _sourceCommit() internal pure override returns (bytes20) {
        return READY_SOURCE_COMMIT;
    }

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

    function test_committedPackageStaysStructurallyBlocked() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(deployment.DESIGNATED_DEPLOYER(), address(0));
        assertEq(deployment.PROPOSAL_DIGEST(), bytes32(0));
        assertEq(deployment.SOURCE_COMMIT(), bytes20(0));
        assertEq(deployment.DEPLOYMENT_APPROVAL_DIGEST(), bytes32(0));
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
        vm.expectRevert(DeployFlowOpsProposalAnchorBaseMainnet.MainnetDeployerNotDesignated.selector);
        deployment.run();
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
        ReadyProposalAnchorDeploymentHarness ready = new ReadyProposalAnchorDeploymentHarness();
        vm.chainId(8_453);

        FlowOpsProposalAnchor anchor = ready.run();

        assertEq(anchor.deployer(), ready.READY_DEPLOYER());
        assertEq(anchor.proposalDigest(), ready.READY_PROPOSAL_DIGEST());
        assertEq(anchor.sourceCommit(), ready.READY_SOURCE_COMMIT());
        assertEq(anchor.DEPLOYMENT_STATUS(), "EXPERIMENTAL_UNAUDITED_NO_FUNDS");
        assertFalse(anchor.productionReady());
        assertFalse(anchor.acceptsFunds());
        assertFalse(anchor.vaultCreationEnabled());
    }
}
