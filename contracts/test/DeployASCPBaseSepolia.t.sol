// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {DeployASCPBaseSepolia} from "../script/DeployASCPBaseSepolia.s.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract SepoliaSafeFixture {
    function getOwners() external pure returns (address[] memory owners) {
        owners = new address[](1);
        owners[0] = address(0xA11CE);
    }

    function getThreshold() external pure returns (uint256) {
        return 1;
    }

    function isModuleEnabled(address) external pure returns (bool) {
        return false;
    }

    function execTransactionFromModule(address, uint256, bytes memory, uint8) external pure returns (bool) {
        revert("GS104");
    }
}

contract InvalidSepoliaSafeFixture {
    function getOwners() external pure returns (address[] memory owners) {
        owners = new address[](1);
        owners[0] = address(0xA11CE);
    }

    function getThreshold() external pure returns (uint256) {
        return 2;
    }

    function isModuleEnabled(address) external pure returns (bool) {
        return false;
    }
}

contract ViewOnlySepoliaSafeFixture {
    function getOwners() external pure returns (address[] memory owners) {
        owners = new address[](1);
        owners[0] = address(0xA11CE);
    }

    function getThreshold() external pure returns (uint256) {
        return 1;
    }

    function isModuleEnabled(address) external pure returns (bool) {
        return false;
    }
}

contract ReadyASCPBaseSepoliaDeploymentHarness is DeployASCPBaseSepolia {
    address internal immutable readySafe;

    constructor(address safe_) {
        readySafe = safe_;
    }

    function _config() internal view override returns (Config memory) {
        return Config({
            deployer: address(0xBEEF),
            expectedDeployerNonce: 0,
            safe: readySafe,
            directoryPublisher: address(0x1001),
            directoryPauser: address(0x1002),
            registryAdmin: address(0x1003),
            spendAuthorizer: address(0x1004),
            organizationDomain: keccak256("flowops-sepolia-org"),
            deploymentPlanDigest: keccak256("reviewed-sepolia-plan"),
            broadcastGuard: REQUIRED_BROADCAST_GUARD
        });
    }

    function _expectedCanonicalUSDCCodeHash() internal view override returns (bytes32) {
        return BASE_SEPOLIA_USDC.codehash;
    }
}

contract DeployASCPBaseSepoliaTest is Test {
    DeployASCPBaseSepolia internal deployment;

    function setUp() public {
        deployment = new DeployASCPBaseSepolia();
    }

    function test_packagePinsCanonicalSepoliaConfiguration() public view {
        assertEq(deployment.BASE_SEPOLIA_CHAIN_ID(), 84532);
        assertEq(deployment.BASE_SEPOLIA_USDC(), 0x036CbD53842c5426634e7929541eC2318f3dCF7e);
        assertEq(
            deployment.BASE_SEPOLIA_USDC_RUNTIME_CODE_HASH(),
            0xedc5281a85c0efecd49999a1ef668390c59b88702f2d4a07029d7f5d63059d6c
        );
        assertEq(deployment.INITIAL_PER_TRANSACTION_CAP(), 1_000_000);
        assertEq(deployment.INITIAL_DAILY_CAP(), 10_000_000);
        assertEq(deployment.INITIAL_ALLOWANCE_CEILING(), 10_000_000);
        assertEq(deployment.REQUIRED_BROADCAST_GUARD(), keccak256("FLOWOPS_ASCP_BASE_SEPOLIA_BROADCAST_V1"));
    }

    function test_runRejectsSubstitutedNonemptyCanonicalUSDCCodeBeforeReadingEnvironment() public {
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(deployment.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        bytes32 actualCodeHash = deployment.BASE_SEPOLIA_USDC().codehash;
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployASCPBaseSepolia.UnexpectedCanonicalUSDCCodeHash.selector,
                deployment.BASE_SEPOLIA_USDC_RUNTIME_CODE_HASH(),
                actualCodeHash
            )
        );
        deployment.run();
    }

    function test_runRejectsWrongChainAndMissingCanonicalUSDCBeforeReadingEnvironment() public {
        vm.chainId(8453);
        vm.expectRevert(abi.encodeWithSelector(DeployASCPBaseSepolia.WrongChain.selector, 84532, 8453));
        deployment.run();

        vm.chainId(84532);
        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseSepolia.MissingCanonicalUSDC.selector, deployment.BASE_SEPOLIA_USDC())
        );
        deployment.run();
    }

    function test_configRejectsMissingAuthoritiesProofAndGuard() public {
        DeployASCPBaseSepolia.Config memory config = validConfig();
        config.deployer = address(0);
        vm.expectRevert(DeployASCPBaseSepolia.DeployerNotDesignated.selector);
        deployment.validateDeploymentConfig(config);

        config = validConfig();
        config.safe = address(0);
        vm.expectRevert(DeployASCPBaseSepolia.SafeNotDesignated.selector);
        deployment.validateDeploymentConfig(config);

        config = validConfig();
        config.directoryPublisher = address(0);
        vm.expectRevert(DeployASCPBaseSepolia.AuthorityNotDesignated.selector);
        deployment.validateDeploymentConfig(config);

        config = validConfig();
        config.organizationDomain = bytes32(0);
        vm.expectRevert(DeployASCPBaseSepolia.OrganizationDomainNotDesignated.selector);
        deployment.validateDeploymentConfig(config);

        config = validConfig();
        config.deploymentPlanDigest = bytes32(0);
        vm.expectRevert(DeployASCPBaseSepolia.DeploymentPlanNotRecorded.selector);
        deployment.validateDeploymentConfig(config);

        config = validConfig();
        config.broadcastGuard = bytes32(0);
        vm.expectRevert(DeployASCPBaseSepolia.BroadcastGuardInvalid.selector);
        deployment.validateDeploymentConfig(config);
    }

    function testFuzz_configRejectsEveryPairwiseAuthorityCollision(uint8 firstRole, uint8 secondRole) public {
        DeployASCPBaseSepolia.Config memory config = validConfig();
        address[6] memory roles = [
            config.deployer,
            config.safe,
            config.directoryPublisher,
            config.directoryPauser,
            config.registryAdmin,
            config.spendAuthorizer
        ];
        firstRole = uint8(bound(firstRole, 0, 5));
        secondRole = uint8(bound(secondRole, 0, 4));
        if (secondRole >= firstRole) secondRole += 1;
        roles[secondRole] = roles[firstRole];
        config.deployer = roles[0];
        config.safe = roles[1];
        config.directoryPublisher = roles[2];
        config.directoryPauser = roles[3];
        config.registryAdmin = roles[4];
        config.spendAuthorizer = roles[5];
        vm.expectRevert(DeployASCPBaseSepolia.AuthoritySeparationInvalid.selector);
        deployment.validateDeploymentConfig(config);
    }

    function testFuzz_configRejectsEveryZeroAuthority(uint8 selectedRole) public {
        DeployASCPBaseSepolia.Config memory config = validConfig();
        address[6] memory roles = [
            config.deployer,
            config.safe,
            config.directoryPublisher,
            config.directoryPauser,
            config.registryAdmin,
            config.spendAuthorizer
        ];
        selectedRole = uint8(bound(selectedRole, 0, 5));
        roles[selectedRole] = address(0);
        config.deployer = roles[0];
        config.safe = roles[1];
        config.directoryPublisher = roles[2];
        config.directoryPauser = roles[3];
        config.registryAdmin = roles[4];
        config.spendAuthorizer = roles[5];
        if (selectedRole == 0) {
            vm.expectRevert(DeployASCPBaseSepolia.DeployerNotDesignated.selector);
        } else if (selectedRole == 1) {
            vm.expectRevert(DeployASCPBaseSepolia.SafeNotDesignated.selector);
        } else {
            vm.expectRevert(DeployASCPBaseSepolia.AuthorityNotDesignated.selector);
        }
        deployment.validateDeploymentConfig(config);
    }

    function testFuzz_configRejectsEverySubstitutedBroadcastGuard(bytes32 guard) public {
        vm.assume(guard != deployment.REQUIRED_BROADCAST_GUARD());
        DeployASCPBaseSepolia.Config memory config = validConfig();
        config.broadcastGuard = guard;
        vm.expectRevert(DeployASCPBaseSepolia.BroadcastGuardInvalid.selector);
        deployment.validateDeploymentConfig(config);
    }

    function test_readyHarnessRequiresSafeContract() public {
        ReadyASCPBaseSepoliaDeploymentHarness ready = new ReadyASCPBaseSepoliaDeploymentHarness(address(0xCAFE));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(ready.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        vm.expectRevert(abi.encodeWithSelector(DeployASCPBaseSepolia.SafeNotContract.selector, address(0xCAFE)));
        ready.run();
    }

    function test_readyHarnessRejectsContractWithoutValidSafeState() public {
        InvalidSepoliaSafeFixture invalidSafe = new InvalidSepoliaSafeFixture();
        ReadyASCPBaseSepoliaDeploymentHarness ready = new ReadyASCPBaseSepoliaDeploymentHarness(address(invalidSafe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(ready.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseSepolia.SafeInterfaceInvalid.selector, address(invalidSafe))
        );
        ready.run();
    }

    function test_readyHarnessRejectsViewCompatibleContractWithoutSafeModuleExecution() public {
        ViewOnlySepoliaSafeFixture viewOnlySafe = new ViewOnlySepoliaSafeFixture();
        ReadyASCPBaseSepoliaDeploymentHarness ready = new ReadyASCPBaseSepoliaDeploymentHarness(address(viewOnlySafe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(ready.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseSepolia.SafeInterfaceInvalid.selector, address(viewOnlySafe))
        );
        ready.run();
    }

    function test_readyHarnessRejectsStaleDeployerNonceBeforeAnyCreation() public {
        SepoliaSafeFixture safe = new SepoliaSafeFixture();
        ReadyASCPBaseSepoliaDeploymentHarness ready = new ReadyASCPBaseSepoliaDeploymentHarness(address(safe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(ready.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        vm.setNonce(address(0xBEEF), 1);
        vm.expectRevert(abi.encodeWithSelector(DeployASCPBaseSepolia.UnexpectedDeployerNonce.selector, 0, 1));
        ready.run();
    }

    function testFuzz_readyHarnessRejectsEveryDirtyPredictedDeploymentAddressBeforeAnyCreation(
        uint8 selectedOffset,
        uint8 dirtyState
    ) public {
        SepoliaSafeFixture safe = new SepoliaSafeFixture();
        ReadyASCPBaseSepoliaDeploymentHarness ready = new ReadyASCPBaseSepoliaDeploymentHarness(address(safe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(ready.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        selectedOffset = uint8(bound(selectedOffset, 0, 3));
        dirtyState = uint8(bound(dirtyState, 0, 3));
        address predicted = vm.computeCreateAddress(address(0xBEEF), selectedOffset);
        if (dirtyState == 0) {
            vm.deal(predicted, 1);
        } else if (dirtyState == 1) {
            MockUSDC(ready.BASE_SEPOLIA_USDC()).mint(predicted, 1);
        } else {
            if (dirtyState == 2) {
                vm.etch(predicted, hex"00");
            } else {
                vm.setNonce(predicted, 1);
            }
        }

        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseSepolia.PredictedDeploymentAddressDirty.selector, predicted)
        );
        ready.run();
        assertEq(vm.getNonce(address(0xBEEF)), 0);
    }

    function test_readyHarnessDeploysCompleteButWriteInertASCPGraph() public {
        SepoliaSafeFixture safe = new SepoliaSafeFixture();
        ReadyASCPBaseSepoliaDeploymentHarness ready = new ReadyASCPBaseSepoliaDeploymentHarness(address(safe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(84532);
        vm.etch(ready.BASE_SEPOLIA_USDC(), address(usdcFixture).code);
        assertEq(MockUSDC(ready.BASE_SEPOLIA_USDC()).decimals(), 6);

        DeployASCPBaseSepolia.Deployment memory deployed = ready.run();

        assertEq(address(deployed.serviceDirectory), vm.computeCreateAddress(address(0xBEEF), 0));
        assertEq(address(deployed.agentRegistry), vm.computeCreateAddress(address(0xBEEF), 1));
        assertEq(address(deployed.callEscrow), vm.computeCreateAddress(address(0xBEEF), 2));
        assertEq(address(deployed.spendModule), vm.computeCreateAddress(address(0xBEEF), 3));
        assertEq(deployed.serviceDirectory.governor(), address(safe));
        assertEq(deployed.agentRegistry.governor(), address(safe));
        assertEq(deployed.callEscrow.safe(), address(safe));
        assertEq(deployed.spendModule.safe(), address(safe));
        assertEq(deployed.spendModule.escrowAllowlist(address(deployed.callEscrow)), bytes32(0));
        assertEq(deployed.serviceDirectory.currentVersion(), 0);
        assertEq(deployed.serviceDirectory.currentRoot(), bytes32(0));
        assertEq(deployed.agentRegistry.agentCount(), 0);
        assertEq(deployed.callEscrow.totalLocked(), 0);
        assertEq(deployed.spendModule.executedPrincipal(), 0);
        assertEq(MockUSDC(ready.BASE_SEPOLIA_USDC()).balanceOf(address(deployed.serviceDirectory)), 0);
        assertEq(MockUSDC(ready.BASE_SEPOLIA_USDC()).balanceOf(address(deployed.agentRegistry)), 0);
        assertEq(MockUSDC(ready.BASE_SEPOLIA_USDC()).balanceOf(address(deployed.callEscrow)), 0);
        assertEq(MockUSDC(ready.BASE_SEPOLIA_USDC()).balanceOf(address(deployed.spendModule)), 0);
        assertEq(address(deployed.serviceDirectory).balance, 0);
        assertEq(address(deployed.agentRegistry).balance, 0);
        assertEq(address(deployed.callEscrow).balance, 0);
        assertEq(address(deployed.spendModule).balance, 0);
    }

    function validConfig() private view returns (DeployASCPBaseSepolia.Config memory) {
        return DeployASCPBaseSepolia.Config({
            deployer: address(1),
            expectedDeployerNonce: 0,
            safe: address(2),
            directoryPublisher: address(3),
            directoryPauser: address(4),
            registryAdmin: address(5),
            spendAuthorizer: address(6),
            organizationDomain: bytes32(uint256(1)),
            deploymentPlanDigest: bytes32(uint256(2)),
            broadcastGuard: deployment.REQUIRED_BROADCAST_GUARD()
        });
    }
}
