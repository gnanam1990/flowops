// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {DeployASCPBaseMainnet, IMainnetSafeDeploymentTarget} from "../script/DeployASCPBaseMainnet.s.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract ProductionSafeFixture {
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

contract InvalidProductionSafeFixture {
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

contract ViewOnlyProductionSafeFixture {
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

contract ReadyASCPMainnetDeploymentHarness is DeployASCPBaseMainnet {
    address internal immutable readySafe;

    constructor(address safe_) {
        readySafe = safe_;
    }

    function _designatedDeployer() internal pure override returns (address) {
        return address(0xBEEF);
    }

    function _productionSafe() internal view override returns (address) {
        return readySafe;
    }

    function _expectedCanonicalUSDCCodeHash() internal view override returns (bytes32) {
        return BASE_MAINNET_USDC.codehash;
    }

    function _directoryPublisher() internal pure override returns (address) {
        return address(0x1001);
    }

    function _directoryPauser() internal pure override returns (address) {
        return address(0x1002);
    }

    function _registryAdmin() internal pure override returns (address) {
        return address(0x1003);
    }

    function _spendAuthorizer() internal pure override returns (address) {
        return address(0x1004);
    }

    function _organizationDomain() internal pure override returns (bytes32) {
        return keccak256("flowops-production-org");
    }

    function _externalReviewDigest() internal pure override returns (bytes32) {
        return keccak256("external-review");
    }

    function _releasePlanDigest() internal pure override returns (bytes32) {
        return keccak256("signed-release");
    }

    function _broadcastEnabled() internal pure override returns (bool) {
        return true;
    }
}

/// @dev Preparation-only harness for a pinned read-only Base mainnet fork. It
///      cannot change the committed deployment package or authorize broadcast.
contract PreparedASCPMainnetCandidateHarness is DeployASCPBaseMainnet {
    function _designatedDeployer() internal pure override returns (address) {
        return 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B;
    }

    function _expectedDeployerNonce() internal pure override returns (uint256) {
        return 1;
    }

    function _productionSafe() internal pure override returns (address) {
        return 0x13E9Fa8d49Ee3E3b456Db71d111Da9b78fABD518;
    }

    function _directoryPublisher() internal pure override returns (address) {
        return 0xb6e55668efB27a1571DBe14A9e388eed8A654fAC;
    }

    function _directoryPauser() internal pure override returns (address) {
        return 0xec8757c4DC1184F3ECE812295D5aC5b570aB0Ce0;
    }

    function _registryAdmin() internal pure override returns (address) {
        return 0x0D9973D582B694E8Ce101BD17e5768c35BacbE60;
    }

    function _spendAuthorizer() internal pure override returns (address) {
        return 0x90F4c2af31bCBf3f1e0A800bd606fc00BC0A446b;
    }

    function _organizationDomain() internal pure override returns (bytes32) {
        return 0x36444de4c6f22f9f5ddaf7a7d993666631402ec5e038bf1032c455373f69bb93;
    }

    function _externalReviewDigest() internal pure override returns (bytes32) {
        return keccak256("PREPARATION_ONLY_EXTERNAL_REVIEW_PLACEHOLDER");
    }

    function _releasePlanDigest() internal pure override returns (bytes32) {
        return keccak256("PREPARATION_ONLY_RELEASE_PLAN_PLACEHOLDER");
    }

    function _broadcastEnabled() internal pure override returns (bool) {
        return true;
    }
}

contract DeployASCPBaseMainnetTest is Test {
    uint256 internal constant PREPARED_FORK_BLOCK = 50_548_759;
    address internal constant PREPARED_DEPLOYER = 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B;
    address internal constant PREPARED_SAFE = 0x13E9Fa8d49Ee3E3b456Db71d111Da9b78fABD518;
    bytes32 internal constant PREPARED_ORG_DOMAIN = 0x36444de4c6f22f9f5ddaf7a7d993666631402ec5e038bf1032c455373f69bb93;
    DeployASCPBaseMainnet internal deployment;

    function setUp() public {
        deployment = new DeployASCPBaseMainnet();
    }

    function test_committedFullASCPPackagePinsCanonicalConfigurationAndStaysBlocked() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8453);
        assertEq(deployment.BASE_MAINNET_USDC(), 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913);
        assertEq(
            deployment.BASE_MAINNET_USDC_RUNTIME_CODE_HASH(),
            0xa6705a10bb756b5dea144591118be77d7af0c3eee3bf2dfe2583dcb0364fefab
        );
        assertEq(deployment.INITIAL_PER_TRANSACTION_CAP(), 1_000_000);
        assertEq(deployment.INITIAL_DAILY_CAP(), 10_000_000);
        assertEq(deployment.INITIAL_ALLOWANCE_CEILING(), 10_000_000);
        assertEq(deployment.DESIGNATED_DEPLOYER(), address(0));
        assertEq(deployment.EXPECTED_DEPLOYER_NONCE(), 0);
        assertEq(deployment.PRODUCTION_SAFE(), address(0));
        assertEq(deployment.EXTERNAL_REVIEW_DIGEST(), bytes32(0));
        assertEq(deployment.RELEASE_PLAN_DIGEST(), bytes32(0));
        assertFalse(deployment.MAINNET_BROADCAST_ENABLED());
    }

    function test_runRejectsSubstitutedNonemptyCanonicalUSDCCodeBeforeReadingReleaseConfig() public {
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(8453);
        vm.etch(deployment.BASE_MAINNET_USDC(), address(usdcFixture).code);
        bytes32 actualCodeHash = deployment.BASE_MAINNET_USDC().codehash;
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployASCPBaseMainnet.UnexpectedCanonicalUSDCCodeHash.selector,
                deployment.BASE_MAINNET_USDC_RUNTIME_CODE_HASH(),
                actualCodeHash
            )
        );
        deployment.run();
    }

    function test_runRejectsWrongChainAndMissingCanonicalUSDC() public {
        vm.chainId(84532);
        vm.expectRevert(abi.encodeWithSelector(DeployASCPBaseMainnet.WrongChain.selector, 8453, 84532));
        deployment.run();

        vm.chainId(8453);
        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseMainnet.MissingCanonicalUSDC.selector, deployment.BASE_MAINNET_USDC())
        );
        deployment.run();
    }

    function test_releaseGateRejectsMissingAndOverlappingAuthorities() public {
        bytes32 org = keccak256("org");
        bytes32 review = keccak256("review");
        bytes32 release = keccak256("release");
        vm.expectRevert(DeployASCPBaseMainnet.MainnetDeployerNotDesignated.selector);
        deployment.validateReleaseGates(
            address(0), address(1), address(2), address(3), address(4), address(5), org, review, release, true
        );
        vm.expectRevert(DeployASCPBaseMainnet.ProductionSafeNotDesignated.selector);
        deployment.validateReleaseGates(
            address(1), address(0), address(2), address(3), address(4), address(5), org, review, release, true
        );
        vm.expectRevert(DeployASCPBaseMainnet.AuthorityNotDesignated.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(0), address(3), address(4), address(5), org, review, release, true
        );
        vm.expectRevert(DeployASCPBaseMainnet.AuthoritySeparationInvalid.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(3), address(3), address(4), address(5), org, review, release, true
        );
    }

    function test_releaseGateRejectsMissingProofsAndDisabledBroadcast() public {
        bytes32 org = keccak256("org");
        bytes32 review = keccak256("review");
        bytes32 release = keccak256("release");
        vm.expectRevert(DeployASCPBaseMainnet.OrganizationDomainNotDesignated.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(3), address(4), address(5), address(6), bytes32(0), review, release, true
        );
        vm.expectRevert(DeployASCPBaseMainnet.ExternalReviewNotRecorded.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(3), address(4), address(5), address(6), org, bytes32(0), release, true
        );
        vm.expectRevert(DeployASCPBaseMainnet.ReleasePlanNotRecorded.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(3), address(4), address(5), address(6), org, review, bytes32(0), true
        );
        vm.expectRevert(DeployASCPBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(3), address(4), address(5), address(6), org, review, release, false
        );
    }

    function testFuzz_releaseGateRejectsEveryZeroAuthority(uint8 selectedRole) public {
        address[6] memory roles = [address(1), address(2), address(3), address(4), address(5), address(6)];
        selectedRole = uint8(bound(selectedRole, 0, 5));
        roles[selectedRole] = address(0);
        if (selectedRole == 0) {
            vm.expectRevert(DeployASCPBaseMainnet.MainnetDeployerNotDesignated.selector);
        } else if (selectedRole == 1) {
            vm.expectRevert(DeployASCPBaseMainnet.ProductionSafeNotDesignated.selector);
        } else {
            vm.expectRevert(DeployASCPBaseMainnet.AuthorityNotDesignated.selector);
        }
        deployment.validateReleaseGates(
            roles[0],
            roles[1],
            roles[2],
            roles[3],
            roles[4],
            roles[5],
            bytes32(uint256(1)),
            bytes32(uint256(2)),
            bytes32(uint256(3)),
            true
        );
    }

    function testFuzz_releaseGateRejectsEveryPairwiseAuthorityCollision(uint8 firstRole, uint8 secondRole) public {
        address[6] memory roles = [address(1), address(2), address(3), address(4), address(5), address(6)];
        firstRole = uint8(bound(firstRole, 0, 5));
        secondRole = uint8(bound(secondRole, 0, 4));
        if (secondRole >= firstRole) secondRole += 1;
        roles[secondRole] = roles[firstRole];
        vm.expectRevert(DeployASCPBaseMainnet.AuthoritySeparationInvalid.selector);
        deployment.validateReleaseGates(
            roles[0],
            roles[1],
            roles[2],
            roles[3],
            roles[4],
            roles[5],
            bytes32(uint256(1)),
            bytes32(uint256(2)),
            bytes32(uint256(3)),
            true
        );
    }

    function testFuzz_releaseGateRejectsEveryMissingDigest(uint8 selectedDigest) public {
        bytes32[3] memory digests = [bytes32(uint256(1)), bytes32(uint256(2)), bytes32(uint256(3))];
        selectedDigest = uint8(bound(selectedDigest, 0, 2));
        digests[selectedDigest] = bytes32(0);
        if (selectedDigest == 0) {
            vm.expectRevert(DeployASCPBaseMainnet.OrganizationDomainNotDesignated.selector);
        } else if (selectedDigest == 1) {
            vm.expectRevert(DeployASCPBaseMainnet.ExternalReviewNotRecorded.selector);
        } else {
            vm.expectRevert(DeployASCPBaseMainnet.ReleasePlanNotRecorded.selector);
        }
        deployment.validateReleaseGates(
            address(1),
            address(2),
            address(3),
            address(4),
            address(5),
            address(6),
            digests[0],
            digests[1],
            digests[2],
            true
        );
    }

    function testFuzz_releaseGateRejectsDisabledBroadcast(bytes32 org, bytes32 review, bytes32 release) public {
        org = bytes32(uint256(org) | 1);
        review = bytes32(uint256(review) | 1);
        release = bytes32(uint256(release) | 1);
        vm.expectRevert(DeployASCPBaseMainnet.MainnetBroadcastDisabled.selector);
        deployment.validateReleaseGates(
            address(1), address(2), address(3), address(4), address(5), address(6), org, review, release, false
        );
    }

    function test_promotedHarnessDeploysCompleteButWriteInertASCPGraph() public {
        ProductionSafeFixture safe = new ProductionSafeFixture();
        ReadyASCPMainnetDeploymentHarness ready = new ReadyASCPMainnetDeploymentHarness(address(safe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(8453);
        vm.etch(ready.BASE_MAINNET_USDC(), address(usdcFixture).code);
        assertEq(MockUSDC(ready.BASE_MAINNET_USDC()).decimals(), 6);

        DeployASCPBaseMainnet.Deployment memory deployed = ready.run();

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
        assertEq(deployed.callEscrow.totalLocked(), 0);
        assertEq(deployed.spendModule.executedPrincipal(), 0);
        assertEq(MockUSDC(ready.BASE_MAINNET_USDC()).balanceOf(address(deployed.serviceDirectory)), 0);
        assertEq(MockUSDC(ready.BASE_MAINNET_USDC()).balanceOf(address(deployed.agentRegistry)), 0);
        assertEq(MockUSDC(ready.BASE_MAINNET_USDC()).balanceOf(address(deployed.callEscrow)), 0);
        assertEq(MockUSDC(ready.BASE_MAINNET_USDC()).balanceOf(address(deployed.spendModule)), 0);
        assertEq(address(deployed.serviceDirectory).balance, 0);
        assertEq(address(deployed.agentRegistry).balance, 0);
        assertEq(address(deployed.callEscrow).balance, 0);
        assertEq(address(deployed.spendModule).balance, 0);
    }

    function test_preparedProductionCandidateDeploysExactWriteInertGraphOnPinnedFork() public {
        string memory rpcURL = vm.envOr("BASE_MAINNET_FORK_RPC_URL", string(""));
        if (bytes(rpcURL).length == 0) {
            vm.skip(true, "set BASE_MAINNET_FORK_RPC_URL to run prepared Base mainnet candidate evidence");
        }
        vm.createSelectFork(rpcURL, PREPARED_FORK_BLOCK);
        assertEq(block.chainid, 8453);
        assertEq(vm.getNonce(PREPARED_DEPLOYER), 1);

        PreparedASCPMainnetCandidateHarness candidate = new PreparedASCPMainnetCandidateHarness();
        DeployASCPBaseMainnet.Deployment memory deployed = candidate.run();

        assertEq(address(deployed.serviceDirectory), 0x2bc89B98aDA8335FeaB04d5b7B5Af6A63EB95FD1);
        assertEq(address(deployed.agentRegistry), 0x15332E8C8e230E8A1C05095196DAC42BA8Cc6906);
        assertEq(address(deployed.callEscrow), 0x214CBBB2190075Ba43fA6518560d37C09720E0C4);
        assertEq(address(deployed.spendModule), 0x942b83421C3Ac4E1A04753e5e0208FD56CAd649e);
        assertEq(vm.getNonce(PREPARED_DEPLOYER), 5);

        assertEq(deployed.serviceDirectory.governor(), PREPARED_SAFE);
        assertEq(deployed.serviceDirectory.directoryPublisher(), 0xb6e55668efB27a1571DBe14A9e388eed8A654fAC);
        assertEq(deployed.serviceDirectory.pauser(), 0xec8757c4DC1184F3ECE812295D5aC5b570aB0Ce0);
        assertEq(deployed.serviceDirectory.orgDomain(), PREPARED_ORG_DOMAIN);
        assertEq(deployed.serviceDirectory.currentVersion(), 0);
        assertEq(deployed.serviceDirectory.currentRoot(), bytes32(0));

        assertEq(deployed.agentRegistry.governor(), PREPARED_SAFE);
        assertEq(deployed.agentRegistry.registryAdmin(), 0x0D9973D582B694E8Ce101BD17e5768c35BacbE60);
        assertEq(deployed.agentRegistry.orgDomain(), PREPARED_ORG_DOMAIN);
        assertEq(deployed.agentRegistry.agentCount(), 0);

        assertEq(address(deployed.callEscrow.usdc()), candidate.BASE_MAINNET_USDC());
        assertEq(address(deployed.callEscrow.serviceDirectory()), address(deployed.serviceDirectory));
        assertEq(deployed.callEscrow.safe(), PREPARED_SAFE);
        assertEq(deployed.callEscrow.governor(), PREPARED_SAFE);
        assertEq(deployed.callEscrow.totalLocked(), 0);
        assertFalse(deployed.callEscrow.emergencyPaused());

        assertEq(deployed.spendModule.safe(), PREPARED_SAFE);
        assertEq(address(deployed.spendModule.token()), candidate.BASE_MAINNET_USDC());
        assertEq(deployed.spendModule.spendAuthorizer(), 0x90F4c2af31bCBf3f1e0A800bd606fc00BC0A446b);
        (uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling) = deployed.spendModule.caps();
        assertEq(perTransaction, 1_000_000);
        assertEq(perDay, 10_000_000);
        assertEq(allowanceCeiling, 10_000_000);
        assertEq(deployed.spendModule.executedPrincipal(), 0);
        assertEq(deployed.spendModule.escrowAllowlist(address(deployed.callEscrow)), bytes32(0));
        assertFalse(deployed.spendModule.emergencyPaused());

        assertFalse(IMainnetSafeDeploymentTarget(PREPARED_SAFE).isModuleEnabled(address(deployed.spendModule)));
        assertEq(IMainnetSafeDeploymentTarget(PREPARED_SAFE).getThreshold(), 2);
        assertEq(IMainnetSafeDeploymentTarget(PREPARED_SAFE).getOwners().length, 3);
        assertEq(IERC20(candidate.BASE_MAINNET_USDC()).balanceOf(PREPARED_SAFE), 0);
        assertEq(PREPARED_SAFE.balance, 0);

        address[4] memory contracts_ = [
            address(deployed.serviceDirectory),
            address(deployed.agentRegistry),
            address(deployed.callEscrow),
            address(deployed.spendModule)
        ];
        for (uint256 index = 0; index < contracts_.length; ++index) {
            assertGt(contracts_[index].code.length, 0);
            assertEq(contracts_[index].balance, 0);
            assertEq(IERC20(candidate.BASE_MAINNET_USDC()).balanceOf(contracts_[index]), 0);
        }
    }

    function test_promotedHarnessRejectsContractWithoutValidSafeState() public {
        InvalidProductionSafeFixture invalidSafe = new InvalidProductionSafeFixture();
        ReadyASCPMainnetDeploymentHarness ready = new ReadyASCPMainnetDeploymentHarness(address(invalidSafe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(8453);
        vm.etch(ready.BASE_MAINNET_USDC(), address(usdcFixture).code);
        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseMainnet.ProductionSafeInterfaceInvalid.selector, address(invalidSafe))
        );
        ready.run();
    }

    function test_promotedHarnessRejectsViewCompatibleContractWithoutSafeModuleExecution() public {
        ViewOnlyProductionSafeFixture viewOnlySafe = new ViewOnlyProductionSafeFixture();
        ReadyASCPMainnetDeploymentHarness ready = new ReadyASCPMainnetDeploymentHarness(address(viewOnlySafe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(8453);
        vm.etch(ready.BASE_MAINNET_USDC(), address(usdcFixture).code);
        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseMainnet.ProductionSafeInterfaceInvalid.selector, address(viewOnlySafe))
        );
        ready.run();
    }

    function test_promotedHarnessRejectsStaleDeployerNonceBeforeAnyCreation() public {
        ProductionSafeFixture safe = new ProductionSafeFixture();
        ReadyASCPMainnetDeploymentHarness ready = new ReadyASCPMainnetDeploymentHarness(address(safe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(8453);
        vm.etch(ready.BASE_MAINNET_USDC(), address(usdcFixture).code);
        vm.setNonce(address(0xBEEF), 1);
        vm.expectRevert(abi.encodeWithSelector(DeployASCPBaseMainnet.UnexpectedDeployerNonce.selector, 0, 1));
        ready.run();
    }

    function testFuzz_promotedHarnessRejectsEveryDirtyPredictedDeploymentAddressBeforeAnyCreation(
        uint8 selectedOffset,
        uint8 dirtyState
    ) public {
        ProductionSafeFixture safe = new ProductionSafeFixture();
        ReadyASCPMainnetDeploymentHarness ready = new ReadyASCPMainnetDeploymentHarness(address(safe));
        MockUSDC usdcFixture = new MockUSDC();
        vm.chainId(8453);
        vm.etch(ready.BASE_MAINNET_USDC(), address(usdcFixture).code);
        selectedOffset = uint8(bound(selectedOffset, 0, 3));
        dirtyState = uint8(bound(dirtyState, 0, 3));
        address predicted = vm.computeCreateAddress(address(0xBEEF), selectedOffset);
        if (dirtyState == 0) {
            vm.deal(predicted, 1);
        } else if (dirtyState == 1) {
            MockUSDC(ready.BASE_MAINNET_USDC()).mint(predicted, 1);
        } else if (dirtyState == 2) {
            vm.etch(predicted, hex"00");
        } else {
            vm.setNonce(predicted, 1);
        }

        vm.expectRevert(
            abi.encodeWithSelector(DeployASCPBaseMainnet.PredictedDeploymentAddressDirty.selector, predicted)
        );
        ready.run();
        assertEq(vm.getNonce(address(0xBEEF)), 0);
    }
}
