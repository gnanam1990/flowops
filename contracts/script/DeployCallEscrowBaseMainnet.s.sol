// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {CallEscrow} from "../src/CallEscrow.sol";

/// @notice Base-mainnet deployment package for CallEscrow.
/// @dev This committed version is intentionally impossible to broadcast. A
///      separate reviewed PR must designate the deployer, bind the external
///      review digest, and enable broadcast before `run` can deploy anything.
contract DeployCallEscrowBaseMainnet is Script {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;
    address public constant BASE_MAINNET_USDC = 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913;
    uint256 public constant OPTIMISTIC_RELEASE_WINDOW = 1 hours;

    address public constant DESIGNATED_DEPLOYER = address(0);
    bytes32 public constant EXTERNAL_REVIEW_DIGEST = bytes32(0);
    bool public constant MAINNET_BROADCAST_ENABLED = false;

    error WrongChain(uint256 expected, uint256 actual);
    error MissingCanonicalUSDC(address asset);
    error MainnetDeployerNotDesignated();
    error ExternalReviewNotRecorded();
    error MainnetBroadcastDisabled();
    error DeploymentInvariantFailed();

    function run() external returns (CallEscrow escrow) {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }
        if (BASE_MAINNET_USDC.code.length == 0) {
            revert MissingCanonicalUSDC(BASE_MAINNET_USDC);
        }

        address deployer = _designatedDeployer();
        _requireReleaseGates(deployer, _externalReviewDigest(), _broadcastEnabled());

        vm.startBroadcast(deployer);
        escrow = new CallEscrow(IERC20(BASE_MAINNET_USDC), OPTIMISTIC_RELEASE_WINDOW);
        vm.stopBroadcast();

        if (
            address(escrow.asset()) != BASE_MAINNET_USDC
                || escrow.optimisticReleaseWindow() != OPTIMISTIC_RELEASE_WINDOW
        ) {
            revert DeploymentInvariantFailed();
        }

        console2.log("FlowOps CallEscrow Base mainnet deployment");
        console2.log("deployer", deployer);
        console2.log("contract", address(escrow));
        console2.log("asset", BASE_MAINNET_USDC);
        console2.log("optimistic release window", OPTIMISTIC_RELEASE_WINDOW);
        console2.logBytes32(_externalReviewDigest());
    }

    /// @notice Testable representation of the three independent release gates.
    function validateReleaseGates(address deployer, bytes32 reviewDigest, bool broadcastEnabled) external pure {
        _requireReleaseGates(deployer, reviewDigest, broadcastEnabled);
    }

    function _requireReleaseGates(address deployer, bytes32 reviewDigest, bool broadcastEnabled) internal pure {
        if (deployer == address(0)) revert MainnetDeployerNotDesignated();
        if (reviewDigest == bytes32(0)) revert ExternalReviewNotRecorded();
        if (!broadcastEnabled) revert MainnetBroadcastDisabled();
    }

    function _designatedDeployer() internal view virtual returns (address) {
        return DESIGNATED_DEPLOYER;
    }

    function _externalReviewDigest() internal view virtual returns (bytes32) {
        return EXTERNAL_REVIEW_DIGEST;
    }

    function _broadcastEnabled() internal view virtual returns (bool) {
        return MAINNET_BROADCAST_ENABLED;
    }
}
