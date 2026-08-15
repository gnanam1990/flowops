// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {FlowOpsProposalAnchor} from "../src/FlowOpsProposalAnchor.sol";

/// @notice One-time Base-mainnet deployment package for the evidence-only
///         FlowOps proposal anchor.
/// @dev The reviewed package pins the immutable deployment inputs and the
///      exact human broadcast approval. It authorizes no funding or production
///      action.
contract DeployFlowOpsProposalAnchorBaseMainnet is Script {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;

    address public constant DESIGNATED_DEPLOYER = 0xEEC526F6555dD43536F712D5c978CbC13CB4517f;
    bytes32 public constant PROPOSAL_DIGEST = 0x35476d70f7c33d19bb8fc1fa3484e289f0a42aac43e2beca7f941f5340132362;
    bytes20 public constant SOURCE_COMMIT = hex"bd9292d0f916b1e3d828443b41e31a8e635b2b3e";
    bytes32 public constant PROMOTION_PACKAGE_APPROVAL_DIGEST =
        0xbfc1cd20d1f05885029683100e8c0a5387948597db5de68ea13eb1043223a726;
    uint64 public constant EXPECTED_DEPLOYER_NONCE = 0;
    address public constant EXPECTED_ANCHOR_ADDRESS = 0x149D03Ec527Ad8667d47e7b6a2d316Dd54033250;
    bytes32 public constant INITCODE_HASH = 0x41d3a9c08503394daca600ba7520c6818d7f373c08ecff3c916e2eceef93d35e;
    bytes32 public constant EXPECTED_RUNTIME_CODE_HASH =
        0xe5b5b63f37bfd5b6627f48cedd8c0fdcc841f130fd1d5259058374e7a543ed86;
    uint256 public constant ESTIMATED_GAS = 188_437;
    uint256 public constant MAX_GAS_LIMIT = 250_000;
    uint256 public constant MAX_FEE_PER_GAS_WEI = 20_000_000;
    uint256 public constant MAX_GAS_SPEND_WEI = MAX_GAS_LIMIT * MAX_FEE_PER_GAS_WEI;

    bytes32 public constant DEPLOYMENT_APPROVAL_DIGEST =
        0x19b2ec0dad4ae81c0ec838d04285301618f670aa581bda4f218c52dbbd8b5377;
    bool public constant MAINNET_BROADCAST_ENABLED = true;

    error WrongChain(uint256 expected, uint256 actual);
    error MainnetDeployerNotDesignated();
    error ProposalDigestNotRecorded();
    error SourceCommitNotRecorded();
    error DeploymentApprovalNotRecorded();
    error MainnetBroadcastDisabled();
    error DeployerNonceMismatch(uint64 expected, uint64 actual);
    error PredictedAddressMismatch(address expected, address actual);
    error InitcodeHashMismatch(bytes32 expected, bytes32 actual);
    error RuntimeCodeHashMismatch(bytes32 expected, bytes32 actual);
    error DeploymentInvariantFailed();

    function run() external returns (FlowOpsProposalAnchor anchor) {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }

        address deployer = _designatedDeployer();
        bytes32 proposalDigest = _proposalDigest();
        bytes20 sourceCommit = _sourceCommit();
        _requireReleaseGates(deployer, proposalDigest, sourceCommit, _deploymentApprovalDigest(), _broadcastEnabled());

        uint64 expectedNonce = _expectedDeployerNonce();
        uint64 actualNonce = vm.getNonce(deployer);
        if (actualNonce != expectedNonce) revert DeployerNonceMismatch(expectedNonce, actualNonce);

        address expectedAnchor = _expectedAnchorAddress();
        address predictedAnchor = vm.computeCreateAddress(deployer, expectedNonce);
        if (predictedAnchor != expectedAnchor) revert PredictedAddressMismatch(expectedAnchor, predictedAnchor);

        bytes32 actualInitcodeHash = keccak256(
            abi.encodePacked(type(FlowOpsProposalAnchor).creationCode, abi.encode(proposalDigest, sourceCommit))
        );
        if (actualInitcodeHash != INITCODE_HASH) revert InitcodeHashMismatch(INITCODE_HASH, actualInitcodeHash);

        vm.startBroadcast(deployer);
        anchor = new FlowOpsProposalAnchor(proposalDigest, sourceCommit);
        vm.stopBroadcast();

        bytes32 actualRuntimeCodeHash = address(anchor).codehash;
        if (actualRuntimeCodeHash != EXPECTED_RUNTIME_CODE_HASH) {
            revert RuntimeCodeHashMismatch(EXPECTED_RUNTIME_CODE_HASH, actualRuntimeCodeHash);
        }

        if (
            address(anchor) != expectedAnchor || anchor.proposalDigest() != proposalDigest
                || anchor.sourceCommit() != sourceCommit || anchor.deployer() != deployer || anchor.productionReady()
                || anchor.acceptsFunds() || anchor.vaultCreationEnabled()
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

    function _expectedDeployerNonce() internal view virtual returns (uint64) {
        return EXPECTED_DEPLOYER_NONCE;
    }

    function _expectedAnchorAddress() internal view virtual returns (address) {
        return EXPECTED_ANCHOR_ADDRESS;
    }

    function _broadcastEnabled() internal view virtual returns (bool) {
        return MAINNET_BROADCAST_ENABLED;
    }
}
