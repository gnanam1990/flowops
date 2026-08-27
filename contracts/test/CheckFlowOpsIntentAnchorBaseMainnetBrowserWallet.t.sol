// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {CheckFlowOpsBaseMainnetBrowserWallet} from "../script/CheckFlowOpsBaseMainnetBrowserWallet.s.sol";

contract CheckFlowOpsIntentAnchorBaseMainnetBrowserWalletTest is Test {
    CheckFlowOpsBaseMainnetBrowserWallet internal preflight;

    function setUp() public {
        preflight = new CheckFlowOpsBaseMainnetBrowserWallet();
    }

    function test_preflightPinsExpectedBaseWalletAndCreatesNoTransaction() public {
        vm.chainId(8_453);

        assertEq(preflight.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(preflight.DESIGNATED_DEPLOYER(), 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B);
        preflight.run();
    }

    function test_preflightRejectsWrongSimulationChain() public {
        vm.chainId(1);
        vm.expectRevert(
            abi.encodeWithSelector(CheckFlowOpsBaseMainnetBrowserWallet.WrongSimulationChain.selector, 8_453, 1)
        );
        preflight.run();
    }
}
