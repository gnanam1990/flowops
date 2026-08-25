// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {DeployASCPBaseMainnet} from "../script/DeployASCPBaseMainnet.s.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract ProductionSafeFixture {}

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

contract DeployASCPBaseMainnetTest is Test {
    DeployASCPBaseMainnet internal deployment;

    function setUp() public {
        deployment = new DeployASCPBaseMainnet();
    }

    function test_committedFullASCPPackagePinsCanonicalConfigurationAndStaysBlocked() public view {
        assertEq(deployment.BASE_MAINNET_CHAIN_ID(), 8453);
        assertEq(deployment.BASE_MAINNET_USDC(), 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913);
        assertEq(deployment.INITIAL_PER_TRANSACTION_CAP(), 1_000_000);
        assertEq(deployment.INITIAL_DAILY_CAP(), 10_000_000);
        assertEq(deployment.INITIAL_ALLOWANCE_CEILING(), 10_000_000);
        assertEq(deployment.DESIGNATED_DEPLOYER(), address(0));
        assertEq(deployment.PRODUCTION_SAFE(), address(0));
        assertEq(deployment.EXTERNAL_REVIEW_DIGEST(), bytes32(0));
        assertEq(deployment.RELEASE_PLAN_DIGEST(), bytes32(0));
        assertFalse(deployment.MAINNET_BROADCAST_ENABLED());
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

        assertEq(deployed.serviceDirectory.governor(), address(safe));
        assertEq(deployed.agentRegistry.governor(), address(safe));
        assertEq(deployed.callEscrow.safe(), address(safe));
        assertEq(deployed.spendModule.safe(), address(safe));
        assertEq(deployed.spendModule.escrowAllowlist(address(deployed.callEscrow)), bytes32(0));
        assertEq(deployed.serviceDirectory.currentVersion(), 0);
        assertEq(deployed.callEscrow.totalLocked(), 0);
        assertEq(deployed.spendModule.executedPrincipal(), 0);
    }
}
