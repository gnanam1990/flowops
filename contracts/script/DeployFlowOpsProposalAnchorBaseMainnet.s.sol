// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {FlowOpsProposalAnchor} from "../src/FlowOpsProposalAnchor.sol";

/// @notice Structurally blocked Base-mainnet deployment package for the
///         evidence-only FlowOps proposal anchor.
/// @dev A separate reviewed promotion commit must bind all four zero/false
///      release fields before this package can broadcast.
contract DeployFlowOpsProposalAnchorBaseMainnet is Script {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;

    address public constant DESIGNATED_DEPLOYER = address(0);
    bytes32 public constant PROPOSAL_DIGEST = bytes32(0);
    bytes20 public constant SOURCE_COMMIT = bytes20(0);
    bytes32 public constant DEPLOYMENT_APPROVAL_DIGEST = bytes32(0);
    bool public constant MAINNET_BROADCAST_ENABLED = false;

    error WrongChain(uint256 expected, uint256 actual);
    error MainnetDeployerNotDesignated();
    error ProposalDigestNotRecorded();
    error SourceCommitNotRecorded();
    error DeploymentApprovalNotRecorded();
    error MainnetBroadcastDisabled();
    error DeploymentInvariantFailed();

    function run() external returns (FlowOpsProposalAnchor anchor) {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }

        address deployer = _designatedDeployer();
        bytes32 proposalDigest = _proposalDigest();
        bytes20 sourceCommit = _sourceCommit();
        _requireReleaseGates(deployer, proposalDigest, sourceCommit, _deploymentApprovalDigest(), _broadcastEnabled());

        vm.startBroadcast(deployer);
        anchor = new FlowOpsProposalAnchor(proposalDigest, sourceCommit);
        vm.stopBroadcast();

        if (
            anchor.proposalDigest() != proposalDigest || anchor.sourceCommit() != sourceCommit
                || anchor.deployer() != deployer || anchor.productionReady() || anchor.acceptsFunds()
                || anchor.vaultCreationEnabled()
        ) {
            revert DeploymentInvariantFailed();
        }

        console2.log("FlowOps experimental proposal anchor on Base mainnet");
        console2.log("deployer", deployer);
        console2.log("contract", address(anchor));
        console2.log("status", anchor.DEPLOYMENT_STATUS());
        console2.logBytes32(proposalDigest);
        console2.logBytes20(sourceCommit);
        console2.logBytes32(_deploymentApprovalDigest());
    }

    function validateReleaseGates(
        address deployer,
        bytes32 proposalDigest,
        bytes20 sourceCommit,
        bytes32 deploymentApprovalDigest,
        bool broadcastEnabled
    ) external pure {
        _requireReleaseGates(deployer, proposalDigest, sourceCommit, deploymentApprovalDigest, broadcastEnabled);
    }

    function _requireReleaseGates(
        address deployer,
        bytes32 proposalDigest,
        bytes20 sourceCommit,
        bytes32 deploymentApprovalDigest,
        bool broadcastEnabled
    ) internal pure {
        if (deployer == address(0)) revert MainnetDeployerNotDesignated();
        if (proposalDigest == bytes32(0)) revert ProposalDigestNotRecorded();
        if (sourceCommit == bytes20(0)) revert SourceCommitNotRecorded();
        if (deploymentApprovalDigest == bytes32(0)) revert DeploymentApprovalNotRecorded();
        if (!broadcastEnabled) revert MainnetBroadcastDisabled();
    }

    function _designatedDeployer() internal view virtual returns (address) {
        return DESIGNATED_DEPLOYER;
    }

    function _proposalDigest() internal view virtual returns (bytes32) {
        return PROPOSAL_DIGEST;
    }

    function _sourceCommit() internal view virtual returns (bytes20) {
        return SOURCE_COMMIT;
    }

    function _deploymentApprovalDigest() internal view virtual returns (bytes32) {
        return DEPLOYMENT_APPROVAL_DIGEST;
    }

    function _broadcastEnabled() internal view virtual returns (bool) {
        return MAINNET_BROADCAST_ENABLED;
    }
}
