// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {CallEscrow} from "../src/CallEscrow.sol";
import {DeployCallEscrowBaseSepolia} from "../script/DeployCallEscrowBaseSepolia.s.sol";

contract DeployCallEscrowBaseSepoliaTest is Test {
    DeployCallEscrowBaseSepolia internal deployment;

    function setUp() public {
        deployment = new DeployCallEscrowBaseSepolia();
    }

    function test_runPinsReviewedBaseSepoliaConfiguration() public {
        vm.chainId(deployment.BASE_SEPOLIA_CHAIN_ID());
        vm.etch(deployment.BASE_SEPOLIA_USDC(), hex"00");

        CallEscrow escrow = deployment.run();

        assertEq(address(escrow.asset()), deployment.BASE_SEPOLIA_USDC());
        assertEq(escrow.optimisticReleaseWindow(), deployment.OPTIMISTIC_RELEASE_WINDOW());
        assertEq(escrow.MAX_OPTIMISTIC_RELEASE_WINDOW(), 30 days);
    }

    function test_runRejectsEveryOtherChainBeforeBroadcast() public {
        vm.chainId(8453);
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployCallEscrowBaseSepolia.WrongChain.selector, deployment.BASE_SEPOLIA_CHAIN_ID(), 8453
            )
        );
        deployment.run();
    }

    function test_runRejectsMissingCanonicalUSDCCode() public {
        vm.chainId(deployment.BASE_SEPOLIA_CHAIN_ID());
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployCallEscrowBaseSepolia.MissingCanonicalUSDC.selector, deployment.BASE_SEPOLIA_USDC()
            )
        );
        deployment.run();
    }
}
