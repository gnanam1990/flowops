// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {CallEscrow} from "../src/CallEscrow.sol";

/// @notice Deterministic, Base-Sepolia-only deployment for the first FlowOps
///         CallEscrow rehearsal. Mainnet intentionally requires another script
///         and review gate.
contract DeployCallEscrowBaseSepolia is Script {
    uint256 public constant BASE_SEPOLIA_CHAIN_ID = 84_532;
    address public constant BASE_SEPOLIA_USDC = 0x036CbD53842c5426634e7929541eC2318f3dCF7e;
    address public constant EXPECTED_DEPLOYER = 0x079bDde909e28E437768A06d7001eb40896668d4;
    uint256 public constant OPTIMISTIC_RELEASE_WINDOW = 1 hours;

    error WrongChain(uint256 expected, uint256 actual);
    error MissingCanonicalUSDC(address asset);
    error DeploymentInvariantFailed();

    function run() external returns (CallEscrow escrow) {
        if (block.chainid != BASE_SEPOLIA_CHAIN_ID) {
            revert WrongChain(BASE_SEPOLIA_CHAIN_ID, block.chainid);
        }
        if (BASE_SEPOLIA_USDC.code.length == 0) {
            revert MissingCanonicalUSDC(BASE_SEPOLIA_USDC);
        }

        vm.startBroadcast(EXPECTED_DEPLOYER);
        escrow = new CallEscrow(IERC20(BASE_SEPOLIA_USDC), OPTIMISTIC_RELEASE_WINDOW);
        vm.stopBroadcast();

        if (
            address(escrow.asset()) != BASE_SEPOLIA_USDC
                || escrow.optimisticReleaseWindow() != OPTIMISTIC_RELEASE_WINDOW
        ) {
            revert DeploymentInvariantFailed();
        }

        console2.log("FlowOps CallEscrow Base Sepolia deployment");
        console2.log("deployer", EXPECTED_DEPLOYER);
        console2.log("contract", address(escrow));
        console2.log("asset", BASE_SEPOLIA_USDC);
        console2.log("optimistic release window", OPTIMISTIC_RELEASE_WINDOW);
    }
}
