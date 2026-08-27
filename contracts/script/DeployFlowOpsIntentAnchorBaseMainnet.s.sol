// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {FlowOpsIntentAnchor} from "../src/FlowOpsIntentAnchor.sol";

/// @notice Fail-closed deployment ceremony for the limited, zero-fund Base
///         mainnet intent anchor.
/// @dev A separate reviewed promotion commit must replace every zero binding
///      and explicitly enable one broadcast. The committed preparation state
///      cannot reach a wallet prompt.
contract DeployFlowOpsIntentAnchorBaseMainnet is Script {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;

    address public constant DESIGNATED_DEPLOYER = address(0);
    bytes20 public constant SOURCE_COMMIT = bytes20(0);
    bytes32 public constant DEPLOYMENT_APPROVAL_DIGEST = bytes32(0);
    uint64 public constant EXPECTED_DEPLOYER_NONCE = 0;
    address public constant EXPECTED_CONTRACT_ADDRESS = address(0);
    bytes32 public constant EXPECTED_INITCODE_HASH = bytes32(0);
    bytes32 public constant EXPECTED_RUNTIME_CODE_HASH = bytes32(0);
    uint256 public constant MAX_GAS_LIMIT = 650_000;
    uint256 public constant MAX_FEE_PER_GAS_WEI = 20_000_000;
    uint256 public constant MAX_GAS_SPEND_WEI = MAX_GAS_LIMIT * MAX_FEE_PER_GAS_WEI;
    bool public constant MAINNET_BROADCAST_ENABLED = false;

    error WrongChain(uint256 expected, uint256 actual);
    error MainnetDeployerNotDesignated();
    error SourceCommitNotRecorded();
    error DeploymentApprovalNotRecorded();
    error ExpectedAddressNotRecorded();
    error InitcodeHashNotRecorded();
    error RuntimeCodeHashNotRecorded();
    error MainnetBroadcastDisabled();
    error DeployerNonceMismatch(uint64 expected, uint64 actual);
    error PredictedAddressMismatch(address expected, address actual);
    error InitcodeHashMismatch(bytes32 expected, bytes32 actual);
    error RuntimeCodeHashMismatch(bytes32 expected, bytes32 actual);
    error DeploymentInvariantFailed();

    function run() external returns (FlowOpsIntentAnchor anchor) {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }

        address deployer = _designatedDeployer();
        _requireReleaseGates(
            deployer,
            _sourceCommit(),
            _deploymentApprovalDigest(),
            _expectedContractAddress(),
            _expectedInitcodeHash(),
            _expectedRuntimeCodeHash(),
            _broadcastEnabled()
        );

        uint64 expectedNonce = _expectedDeployerNonce();
        uint64 actualNonce = vm.getNonce(deployer);
        if (actualNonce != expectedNonce) revert DeployerNonceMismatch(expectedNonce, actualNonce);

        address expectedContract = _expectedContractAddress();
        address predictedContract = vm.computeCreateAddress(deployer, expectedNonce);
        if (predictedContract != expectedContract) {
            revert PredictedAddressMismatch(expectedContract, predictedContract);
        }

        bytes32 actualInitcodeHash = keccak256(type(FlowOpsIntentAnchor).creationCode);
        if (actualInitcodeHash != _expectedInitcodeHash()) {
            revert InitcodeHashMismatch(_expectedInitcodeHash(), actualInitcodeHash);
        }

        vm.startBroadcast(deployer);
        anchor = new FlowOpsIntentAnchor();
        vm.stopBroadcast();

        bytes32 actualRuntimeCodeHash = address(anchor).codehash;
        if (actualRuntimeCodeHash != _expectedRuntimeCodeHash()) {
            revert RuntimeCodeHashMismatch(_expectedRuntimeCodeHash(), actualRuntimeCodeHash);
        }
        if (
            address(anchor) != expectedContract || anchor.BASE_MAINNET_CHAIN_ID() != BASE_MAINNET_CHAIN_ID
                || anchor.KIND() != keccak256("FLOWOPS_INTENT_ANCHOR_V1") || anchor.acceptsFunds()
                || anchor.executesPayments()
        ) {
            revert DeploymentInvariantFailed();
        }

        console2.log("FlowOps limited mainnet intent anchor");
        console2.log("deployer", deployer);
        console2.log("contract", address(anchor));
        console2.log("status", anchor.DEPLOYMENT_STATUS());
        console2.logBytes20(_sourceCommit());
        console2.logBytes32(_deploymentApprovalDigest());
    }

    function validateReleaseGates(
        address deployer,
        bytes20 sourceCommit,
        bytes32 approvalDigest,
        address expectedContract,
        bytes32 initcodeHash,
        bytes32 runtimeCodeHash,
        bool broadcastEnabled
    ) external pure {
        _requireReleaseGates(
            deployer, sourceCommit, approvalDigest, expectedContract, initcodeHash, runtimeCodeHash, broadcastEnabled
        );
    }

    function _requireReleaseGates(
        address deployer,
        bytes20 sourceCommit,
        bytes32 approvalDigest,
        address expectedContract,
        bytes32 initcodeHash,
        bytes32 runtimeCodeHash,
        bool broadcastEnabled
    ) internal pure {
        if (deployer == address(0)) revert MainnetDeployerNotDesignated();
        if (sourceCommit == bytes20(0)) revert SourceCommitNotRecorded();
        if (approvalDigest == bytes32(0)) revert DeploymentApprovalNotRecorded();
        if (expectedContract == address(0)) revert ExpectedAddressNotRecorded();
        if (initcodeHash == bytes32(0)) revert InitcodeHashNotRecorded();
        if (runtimeCodeHash == bytes32(0)) revert RuntimeCodeHashNotRecorded();
        if (!broadcastEnabled) revert MainnetBroadcastDisabled();
    }

    function _designatedDeployer() internal view virtual returns (address) {
        return DESIGNATED_DEPLOYER;
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

    function _expectedContractAddress() internal view virtual returns (address) {
        return EXPECTED_CONTRACT_ADDRESS;
    }

    function _expectedInitcodeHash() internal view virtual returns (bytes32) {
        return EXPECTED_INITCODE_HASH;
    }

    function _expectedRuntimeCodeHash() internal view virtual returns (bytes32) {
        return EXPECTED_RUNTIME_CODE_HASH;
    }

    function _broadcastEnabled() internal view virtual returns (bool) {
        return MAINNET_BROADCAST_ENABLED;
    }
}
