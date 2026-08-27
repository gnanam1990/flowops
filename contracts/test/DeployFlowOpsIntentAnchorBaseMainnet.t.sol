// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {FlowOpsIntentAnchor} from "../src/FlowOpsIntentAnchor.sol";
import {DeployFlowOpsIntentAnchorBaseMainnet} from "../script/DeployFlowOpsIntentAnchorBaseMainnet.s.sol";

contract IntentAnchorDeploymentHarness is DeployFlowOpsIntentAnchorBaseMainnet {
    function _designatedDeployer() internal pure override returns (address) {
        return address(0xBEEF);
    }

    function _sourceCommit() internal pure override returns (bytes20) {
        return hex"0123456789abcdef0123456789abcdef01234567";
    }

    function _deploymentApprovalDigest() internal pure override returns (bytes32) {
        return keccak256("reviewed-deployment-approval");
    }

    function _expectedContractAddress() internal pure override returns (address) {
        return 0x29ced945BB6A5acc52d2A29C7c7e8E5f84Cf299d;
    }

    function _expectedInitcodeHash() internal pure override returns (bytes32) {
        return keccak256(type(FlowOpsIntentAnchor).creationCode);
    }

    function _expectedRuntimeCodeHash() internal pure override returns (bytes32) {
        return keccak256(type(FlowOpsIntentAnchor).runtimeCode);
    }

    function _broadcastEnabled() internal pure override returns (bool) {
        return true;
    }
}

contract DeployFlowOpsIntentAnchorBaseMainnetTest is Test {
    DeployFlowOpsIntentAnchorBaseMainnet internal deployment;

    function setUp() public {
        deployment = new DeployFlowOpsIntentAnchorBaseMainnet();
    }

    function test_attemptThreeApprovalPinsFreshDigestAndEnablesOneBroadcast() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(deployment.DESIGNATED_DEPLOYER(), 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B);
        assertEq(deployment.SOURCE_COMMIT(), hex"ea21fbaaa8c8cc3aecca17e910146911703507da");
        assertEq(
            deployment.DEPLOYMENT_APPROVAL_DIGEST(), 0x20ea55570d31230094be2e4217e9b070694a2e888408d2c044970fd3d9d699d5
        );
        assertEq(deployment.EXPECTED_DEPLOYER_NONCE(), 0);
        assertEq(deployment.EXPECTED_CONTRACT_ADDRESS(), 0xD109ec995d8fC1FFD2fd66f367288b3Bc3EC8AAA);
        assertEq(
            deployment.EXPECTED_INITCODE_HASH(), 0xefb111e5a3fd1eb31422a41d57a811f28d215e72b6f0cdf04d385fc83c06a863
        );
        assertEq(
            deployment.EXPECTED_RUNTIME_CODE_HASH(), 0x832a61ee74a1df09968706b4ffe3aacab23ad8ba463cc5407e8f795c499f4151
        );
        assertEq(deployment.MAX_GAS_LIMIT(), 650_000);
        assertEq(deployment.MAX_FEE_PER_GAS_WEI(), 20_000_000);
        assertEq(deployment.MAX_GAS_SPEND_WEI(), 13_000_000_000_000);
        assertTrue(deployment.MAINNET_BROADCAST_ENABLED());
    }

    function test_runRejectsWrongChainBeforeAnyReleaseGate() public {
        vm.chainId(84_532);
        vm.expectRevert(abi.encodeWithSelector(DeployFlowOpsIntentAnchorBaseMainnet.WrongChain.selector, 8_453, 84_532));
        deployment.run();
    }

    function test_releaseGateRejectsEveryMissingOrDisabledBinding() public {
        address deployer = address(0xBEEF);
        bytes20 sourceCommit = hex"0123456789abcdef0123456789abcdef01234567";
        bytes32 approval = keccak256("approval");
        address expectedContract = address(0xCAFE);
        bytes32 initcodeHash = keccak256("initcode");
        bytes32 runtimeCodeHash = keccak256("runtime");

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.MainnetDeployerNotDesignated.selector);
        deployment.validateReleaseGates(
            address(0), sourceCommit, approval, expectedContract, initcodeHash, runtimeCodeHash, true
        );

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.SourceCommitNotRecorded.selector);
        deployment.validateReleaseGates(
            deployer, bytes20(0), approval, expectedContract, initcodeHash, runtimeCodeHash, true
        );

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.DeploymentApprovalNotRecorded.selector);
        deployment.validateReleaseGates(
            deployer, sourceCommit, bytes32(0), expectedContract, initcodeHash, runtimeCodeHash, true
        );

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.ExpectedAddressNotRecorded.selector);
        deployment.validateReleaseGates(
            deployer, sourceCommit, approval, address(0), initcodeHash, runtimeCodeHash, true
        );

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.InitcodeHashNotRecorded.selector);
        deployment.validateReleaseGates(
            deployer, sourceCommit, approval, expectedContract, bytes32(0), runtimeCodeHash, true
        );

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.RuntimeCodeHashNotRecorded.selector);
        deployment.validateReleaseGates(
            deployer, sourceCommit, approval, expectedContract, initcodeHash, bytes32(0), true
        );

        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.validateReleaseGates(
            deployer, sourceCommit, approval, expectedContract, initcodeHash, runtimeCodeHash, false
        );
    }

    function test_promotedHarnessDeploysExactZeroFundContract() public {
        IntentAnchorDeploymentHarness harness = new IntentAnchorDeploymentHarness();
        vm.chainId(8_453);

        FlowOpsIntentAnchor anchor = harness.run();

        assertEq(address(anchor), 0x29ced945BB6A5acc52d2A29C7c7e8E5f84Cf299d);
        assertEq(address(anchor).codehash, keccak256(type(FlowOpsIntentAnchor).runtimeCode));
        assertEq(anchor.KIND(), keccak256("FLOWOPS_INTENT_ANCHOR_V1"));
        assertFalse(anchor.acceptsFunds());
        assertFalse(anchor.executesPayments());
    }

    function test_promotedHarnessRejectsNonceDriftBeforeBroadcast() public {
        IntentAnchorDeploymentHarness harness = new IntentAnchorDeploymentHarness();
        vm.chainId(8_453);
        vm.setNonce(address(0xBEEF), 1);

        vm.expectRevert(
            abi.encodeWithSelector(
                DeployFlowOpsIntentAnchorBaseMainnet.DeployerNonceMismatch.selector, uint64(0), uint64(1)
            )
        );
        harness.run();
    }
}
