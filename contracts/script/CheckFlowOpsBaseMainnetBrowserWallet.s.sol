// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";

/// @notice Connects Foundry's browser wallet without creating a transaction.
/// @dev The caller must still compare Foundry's connected-wallet chain output
///      with BASE_MAINNET_CHAIN_ID before preparing a deployment approval.
contract CheckFlowOpsBaseMainnetBrowserWallet is Script {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;
    address public constant DESIGNATED_DEPLOYER = 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B;

    error WrongSimulationChain(uint256 expected, uint256 actual);

    function run() external view {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongSimulationChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }

        console2.log("FlowOps browser-wallet preflight only; no transaction is created");
        console2.log("required chain", BASE_MAINNET_CHAIN_ID);
        console2.log("required account", DESIGNATED_DEPLOYER);
    }
}
