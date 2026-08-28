// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {DeployASCPBaseMainnet} from "../script/DeployASCPBaseMainnet.s.sol";
import {DeployASCPBaseMainnetExperimental} from "../script/DeployASCPBaseMainnetExperimental.s.sol";

contract DeployASCPBaseMainnetExperimentalTest is Test {
    uint256 internal constant PREPARED_FORK_BLOCK = 50_548_759;
    address internal constant DEPLOYER = 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B;
    address internal constant SAFE = 0x13E9Fa8d49Ee3E3b456Db71d111Da9b78fABD518;
    address internal constant PUBLISHER = 0xb6e55668efB27a1571DBe14A9e388eed8A654fAC;
    address internal constant PAUSER = 0xec8757c4DC1184F3ECE812295D5aC5b570aB0Ce0;
    address internal constant REGISTRY_ADMIN = 0x0D9973D582B694E8Ce101BD17e5768c35BacbE60;
    address internal constant SPEND_AUTHORIZER = 0x90F4c2af31bCBf3f1e0A800bd606fc00BC0A446b;
    bytes32 internal constant ORG_DOMAIN = 0x36444de4c6f22f9f5ddaf7a7d993666631402ec5e038bf1032c455373f69bb93;
    bytes32 internal constant OWNER_ACCEPTANCE = keccak256("test-owner-risk-acceptance");

    DeployASCPBaseMainnetExperimental internal deployment;

    function setUp() public {
        deployment = new DeployASCPBaseMainnetExperimental();
    }

    function test_experimentalAdmissionRejectsAnyExternalReviewClaim() public {
        vm.expectRevert(DeployASCPBaseMainnetExperimental.ExternalReviewMustRemainUnset.selector);
        deployment.validateReleaseGates(
            DEPLOYER,
            SAFE,
            PUBLISHER,
            PAUSER,
            REGISTRY_ADMIN,
            SPEND_AUTHORIZER,
            ORG_DOMAIN,
            keccak256("not-an-external-review"),
            OWNER_ACCEPTANCE,
            true
        );
    }

    function test_experimentalAdmissionRequiresOwnerAcceptanceAndExplicitBroadcast() public {
        vm.expectRevert(DeployASCPBaseMainnetExperimental.OwnerRiskAcceptanceNotRecorded.selector);
        deployment.validateReleaseGates(
            DEPLOYER,
            SAFE,
            PUBLISHER,
            PAUSER,
            REGISTRY_ADMIN,
            SPEND_AUTHORIZER,
            ORG_DOMAIN,
            bytes32(0),
            bytes32(0),
            true
        );

        vm.expectRevert(DeployASCPBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.validateReleaseGates(
            DEPLOYER,
            SAFE,
            PUBLISHER,
            PAUSER,
            REGISTRY_ADMIN,
            SPEND_AUTHORIZER,
            ORG_DOMAIN,
            bytes32(0),
            OWNER_ACCEPTANCE,
            false
        );
    }

    function test_experimentalCandidateDeploysExactZeroFundGraphOnPinnedFork() public {
        string memory rpcURL = vm.envOr("BASE_MAINNET_FORK_RPC_URL", string(""));
        if (bytes(rpcURL).length == 0) {
            vm.skip(true, "set BASE_MAINNET_FORK_RPC_URL to run experimental Base mainnet evidence");
        }
        vm.setEnv("FLOWOPS_OWNER_RISK_ACCEPTANCE_DIGEST", vm.toString(OWNER_ACCEPTANCE));
        vm.setEnv("FLOWOPS_EXPERIMENTAL_MAINNET_BROADCAST_ENABLED", "true");
        vm.createSelectFork(rpcURL, PREPARED_FORK_BLOCK);
        deployment = new DeployASCPBaseMainnetExperimental();

        DeployASCPBaseMainnet.Deployment memory deployed = deployment.run();

        assertEq(address(deployed.serviceDirectory), 0x2bc89B98aDA8335FeaB04d5b7B5Af6A63EB95FD1);
        assertEq(address(deployed.agentRegistry), 0x15332E8C8e230E8A1C05095196DAC42BA8Cc6906);
        assertEq(address(deployed.callEscrow), 0x214CBBB2190075Ba43fA6518560d37C09720E0C4);
        assertEq(address(deployed.spendModule), 0x942b83421C3Ac4E1A04753e5e0208FD56CAd649e);
        assertEq(vm.getNonce(DEPLOYER), 5);

        assertEq(deployed.serviceDirectory.governor(), SAFE);
        assertEq(deployed.agentRegistry.governor(), SAFE);
        assertEq(deployed.callEscrow.safe(), SAFE);
        assertEq(deployed.spendModule.safe(), SAFE);
        assertEq(deployed.serviceDirectory.currentVersion(), 0);
        assertEq(deployed.agentRegistry.agentCount(), 0);
        assertEq(deployed.callEscrow.totalLocked(), 0);
        assertEq(deployed.spendModule.executedPrincipal(), 0);
        assertEq(deployed.spendModule.escrowAllowlist(address(deployed.callEscrow)), bytes32(0));
        assertFalse(deployed.spendModule.emergencyPaused());
        assertFalse(deployed.callEscrow.emergencyPaused());

        address[4] memory contracts_ = [
            address(deployed.serviceDirectory),
            address(deployed.agentRegistry),
            address(deployed.callEscrow),
            address(deployed.spendModule)
        ];
        for (uint256 index = 0; index < contracts_.length; ++index) {
            assertGt(contracts_[index].code.length, 0);
            assertEq(contracts_[index].balance, 0);
            assertEq(IERC20(deployment.BASE_MAINNET_USDC()).balanceOf(contracts_[index]), 0);
        }
    }
}
