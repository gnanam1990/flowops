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

    function test_committedPreparationCannotBroadcast() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8_453);
        assertEq(deployment.DESIGNATED_DEPLOYER(), address(0));
        assertEq(deployment.SOURCE_COMMIT(), bytes20(0));
        assertEq(deployment.DEPLOYMENT_APPROVAL_DIGEST(), bytes32(0));
        assertEq(deployment.EXPECTED_CONTRACT_ADDRESS(), address(0));
        assertEq(deployment.EXPECTED_INITCODE_HASH(), bytes32(0));
        assertEq(deployment.EXPECTED_RUNTIME_CODE_HASH(), bytes32(0));
        assertEq(deployment.MAX_GAS_LIMIT(), 650_000);
        assertEq(deployment.MAX_FEE_PER_GAS_WEI(), 20_000_000);
        assertEq(deployment.MAX_GAS_SPEND_WEI(), 13_000_000_000_000);
        assertFalse(deployment.MAINNET_BROADCAST_ENABLED());
    }

    function test_runRejectsWrongChainBeforeAnyReleaseGate() public {
        vm.chainId(84_532);
        vm.expectRevert(abi.encodeWithSelector(DeployFlowOpsIntentAnchorBaseMainnet.WrongChain.selector, 8_453, 84_532));
        deployment.run();
    }

    function test_runRejectsUnassignedPreparationOnMainnet() public {
        vm.chainId(8_453);
        vm.expectRevert(DeployFlowOpsIntentAnchorBaseMainnet.MainnetDeployerNotDesignated.selector);
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
