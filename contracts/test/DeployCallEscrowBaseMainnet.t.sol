// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {CallEscrow} from "../src/CallEscrow.sol";
import {DeployCallEscrowBaseMainnet} from "../script/DeployCallEscrowBaseMainnet.s.sol";

contract ReadyMainnetDeploymentHarness is DeployCallEscrowBaseMainnet {
    address internal constant READY_DEPLOYER = address(0xBEEF);
    bytes32 internal constant READY_REVIEW_DIGEST = keccak256("independent-review-artifact");

    function _designatedDeployer() internal pure override returns (address) {
        return READY_DEPLOYER;
    }

    function _externalReviewDigest() internal pure override returns (bytes32) {
        return READY_REVIEW_DIGEST;
    }

    function _broadcastEnabled() internal pure override returns (bool) {
        return true;
    }
}

contract DeployCallEscrowBaseMainnetTest is Test {
    DeployCallEscrowBaseMainnet internal deployment;

    function setUp() public {
        deployment = new DeployCallEscrowBaseMainnet();
    }

    function test_committedPackagePinsCanonicalBaseConfigurationAndStaysBlocked() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8453);
        assertEq(deployment.BASE_MAINNET_USDC(), 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913);
        assertEq(deployment.OPTIMISTIC_RELEASE_WINDOW(), 1 hours);
        assertEq(deployment.DESIGNATED_DEPLOYER(), address(0));
        assertEq(deployment.EXTERNAL_REVIEW_DIGEST(), bytes32(0));
        assertFalse(deployment.MAINNET_BROADCAST_ENABLED());
    }

    function test_runRejectsEveryOtherChainBeforeAnyReleaseGate() public {
        vm.chainId(84532);
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployCallEscrowBaseMainnet.WrongChain.selector, deployment.BASE_MAINNET_CHAIN_ID(), 84532
            )
        );
        deployment.run();
    }

    function test_runRejectsMissingCanonicalUSDCBeforeAnyReleaseGate() public {
        vm.chainId(deployment.BASE_MAINNET_CHAIN_ID());
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployCallEscrowBaseMainnet.MissingCanonicalUSDC.selector, deployment.BASE_MAINNET_USDC()
            )
        );
        deployment.run();
    }

    function test_runCannotBroadcastFromCommittedPackage() public {
        vm.chainId(deployment.BASE_MAINNET_CHAIN_ID());
        vm.etch(deployment.BASE_MAINNET_USDC(), hex"00");

        vm.expectRevert(DeployCallEscrowBaseMainnet.MainnetDeployerNotDesignated.selector);
        deployment.run();
    }

    function test_releaseGateRejectsMissingExternalReview() public {
        vm.expectRevert(DeployCallEscrowBaseMainnet.ExternalReviewNotRecorded.selector);
        deployment.validateReleaseGates(address(0xBEEF), bytes32(0), true);
    }

    function test_releaseGateRejectsDisabledBroadcast() public {
        vm.expectRevert(DeployCallEscrowBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.validateReleaseGates(address(0xBEEF), keccak256("review"), false);
    }

    function test_releaseGateAcceptsOnlyFullySpecifiedPromotion() public view {
        deployment.validateReleaseGates(address(0xBEEF), keccak256("review"), true);
    }

    function test_promotedHarnessDeploysPinnedConstructorWithoutAdminRole() public {
        ReadyMainnetDeploymentHarness ready = new ReadyMainnetDeploymentHarness();
        vm.chainId(ready.BASE_MAINNET_CHAIN_ID());
        if (ready.BASE_MAINNET_USDC().code.length == 0) {
            vm.etch(ready.BASE_MAINNET_USDC(), hex"00");
        }

        CallEscrow escrow = ready.run();

        assertEq(address(escrow.asset()), ready.BASE_MAINNET_USDC());
        assertEq(escrow.optimisticReleaseWindow(), ready.OPTIMISTIC_RELEASE_WINDOW());
        assertEq(escrow.MAX_OPTIMISTIC_RELEASE_WINDOW(), 30 days);
    }
}
