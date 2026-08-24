// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";
import {AgentRegistry} from "../src/AgentRegistry.sol";
import {ASCPCallEscrow, IServiceDirectory} from "../src/ASCPCallEscrow.sol";
import {ASCPSpendModule} from "../src/ASCPSpendModule.sol";

/// @notice Fail-closed deployment package for the complete ASCP v4 Base
///         mainnet contract graph. The committed package cannot broadcast.
/// @dev Deployment does not enable the Safe module, seed a directory root,
///      allowlist the escrow, add a verifier, or move funds. Those are separate
///      dual-control Safe actions whose receipts must be bound into the signed
///      production release manifest.
contract DeployASCPBaseMainnet is Script {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;
    address public constant BASE_MAINNET_USDC = 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913;
    uint256 public constant INITIAL_PER_TRANSACTION_CAP = 1_000_000;
    uint256 public constant INITIAL_DAILY_CAP = 10_000_000;
    uint256 public constant INITIAL_ALLOWANCE_CEILING = 10_000_000;

    address public constant DESIGNATED_DEPLOYER = address(0);
    address public constant PRODUCTION_SAFE = address(0);
    address public constant DIRECTORY_PUBLISHER = address(0);
    address public constant DIRECTORY_PAUSER = address(0);
    address public constant REGISTRY_ADMIN = address(0);
    address public constant SPEND_AUTHORIZER = address(0);
    bytes32 public constant ORGANIZATION_DOMAIN = bytes32(0);
    bytes32 public constant EXTERNAL_REVIEW_DIGEST = bytes32(0);
    bytes32 public constant RELEASE_PLAN_DIGEST = bytes32(0);
    bool public constant MAINNET_BROADCAST_ENABLED = false;

    struct Deployment {
        ServiceDirectory serviceDirectory;
        AgentRegistry agentRegistry;
        ASCPCallEscrow callEscrow;
        ASCPSpendModule spendModule;
    }

    error WrongChain(uint256 expected, uint256 actual);
    error MissingCanonicalUSDC(address asset);
    error MainnetDeployerNotDesignated();
    error ProductionSafeNotDesignated();
    error ProductionSafeNotContract(address safe);
    error AuthorityNotDesignated();
    error AuthoritySeparationInvalid();
    error OrganizationDomainNotDesignated();
    error ExternalReviewNotRecorded();
    error ReleasePlanNotRecorded();
    error MainnetBroadcastDisabled();
    error DeploymentInvariantFailed();

    function run() external returns (Deployment memory deployed) {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        if (BASE_MAINNET_USDC.code.length == 0) revert MissingCanonicalUSDC(BASE_MAINNET_USDC);

        address deployer = _designatedDeployer();
        address safe = _productionSafe();
        address publisher = _directoryPublisher();
        address pauser = _directoryPauser();
        address registryAdmin = _registryAdmin();
        address spendAuthorizer = _spendAuthorizer();
        bytes32 orgDomain = _organizationDomain();
        _requireReleaseGates(
            deployer,
            safe,
            publisher,
            pauser,
            registryAdmin,
            spendAuthorizer,
            orgDomain,
            _externalReviewDigest(),
            _releasePlanDigest(),
            _broadcastEnabled()
        );
        if (safe.code.length == 0) revert ProductionSafeNotContract(safe);

        vm.startBroadcast(deployer);
        deployed.serviceDirectory = new ServiceDirectory(safe, publisher, pauser, orgDomain);
        deployed.agentRegistry = new AgentRegistry(safe, registryAdmin, orgDomain);
        deployed.callEscrow = new ASCPCallEscrow(
            IERC20(BASE_MAINNET_USDC), IServiceDirectory(address(deployed.serviceDirectory)), safe, safe
        );
        deployed.spendModule = new ASCPSpendModule(
            safe,
            IERC20(BASE_MAINNET_USDC),
            spendAuthorizer,
            ASCPSpendModule.Caps({
                perTransaction: INITIAL_PER_TRANSACTION_CAP,
                perDay: INITIAL_DAILY_CAP,
                allowanceCeiling: INITIAL_ALLOWANCE_CEILING
            })
        );
        vm.stopBroadcast();

        _verifyDeployment(deployed, safe, publisher, pauser, registryAdmin, spendAuthorizer, orgDomain);
        console2.log("FlowOps ASCP v4 Base mainnet deployment");
        console2.log("deployer", deployer);
        console2.log("safe", safe);
        console2.log("serviceDirectory", address(deployed.serviceDirectory));
        console2.log("agentRegistry", address(deployed.agentRegistry));
        console2.log("ascpCallEscrow", address(deployed.callEscrow));
        console2.log("ascpSpendModule", address(deployed.spendModule));
        console2.logBytes32(_externalReviewDigest());
        console2.logBytes32(_releasePlanDigest());
    }

    function validateReleaseGates(
        address deployer,
        address safe,
        address publisher,
        address pauser,
        address registryAdmin,
        address spendAuthorizer,
        bytes32 orgDomain,
        bytes32 reviewDigest,
        bytes32 releasePlanDigest,
        bool broadcastEnabled
    ) external pure {
        _requireReleaseGates(
            deployer,
            safe,
            publisher,
            pauser,
            registryAdmin,
            spendAuthorizer,
            orgDomain,
            reviewDigest,
            releasePlanDigest,
            broadcastEnabled
        );
    }

    function _requireReleaseGates(
        address deployer,
        address safe,
        address publisher,
        address pauser,
        address registryAdmin,
        address spendAuthorizer,
        bytes32 orgDomain,
        bytes32 reviewDigest,
        bytes32 releasePlanDigest,
        bool broadcastEnabled
    ) internal pure {
        if (deployer == address(0)) revert MainnetDeployerNotDesignated();
        if (safe == address(0)) revert ProductionSafeNotDesignated();
        if (
            publisher == address(0) || pauser == address(0) || registryAdmin == address(0)
                || spendAuthorizer == address(0)
        ) {
            revert AuthorityNotDesignated();
        }
        if (
            deployer == safe || publisher == safe || pauser == safe || registryAdmin == safe || spendAuthorizer == safe
                || deployer == publisher || deployer == pauser || deployer == registryAdmin
                || deployer == spendAuthorizer || publisher == pauser || publisher == registryAdmin
                || publisher == spendAuthorizer || pauser == registryAdmin || pauser == spendAuthorizer
                || registryAdmin == spendAuthorizer
        ) revert AuthoritySeparationInvalid();
        if (orgDomain == bytes32(0)) revert OrganizationDomainNotDesignated();
        if (reviewDigest == bytes32(0)) revert ExternalReviewNotRecorded();
        if (releasePlanDigest == bytes32(0)) revert ReleasePlanNotRecorded();
        if (!broadcastEnabled) revert MainnetBroadcastDisabled();
    }

    function _verifyDeployment(
        Deployment memory deployed,
        address safe,
        address publisher,
        address pauser,
        address registryAdmin,
        address spendAuthorizer,
        bytes32 orgDomain
    ) private view {
        (uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling) = deployed.spendModule.caps();
        if (
            deployed.serviceDirectory.governor() != safe || deployed.serviceDirectory.directoryPublisher() != publisher
                || deployed.serviceDirectory.pauser() != pauser || deployed.serviceDirectory.orgDomain() != orgDomain
                || deployed.agentRegistry.governor() != safe || deployed.agentRegistry.registryAdmin() != registryAdmin
                || deployed.agentRegistry.orgDomain() != orgDomain
                || address(deployed.callEscrow.usdc()) != BASE_MAINNET_USDC
                || address(deployed.callEscrow.serviceDirectory()) != address(deployed.serviceDirectory)
                || deployed.callEscrow.safe() != safe || deployed.callEscrow.governor() != safe
                || deployed.spendModule.safe() != safe || address(deployed.spendModule.token()) != BASE_MAINNET_USDC
                || deployed.spendModule.spendAuthorizer() != spendAuthorizer
                || perTransaction != INITIAL_PER_TRANSACTION_CAP || perDay != INITIAL_DAILY_CAP
                || allowanceCeiling != INITIAL_ALLOWANCE_CEILING || deployed.spendModule.emergencyPaused()
                || deployed.callEscrow.emergencyPaused()
        ) revert DeploymentInvariantFailed();
    }

    function _designatedDeployer() internal view virtual returns (address) {
        return DESIGNATED_DEPLOYER;
    }

    function _productionSafe() internal view virtual returns (address) {
        return PRODUCTION_SAFE;
    }

    function _directoryPublisher() internal view virtual returns (address) {
        return DIRECTORY_PUBLISHER;
    }

    function _directoryPauser() internal view virtual returns (address) {
        return DIRECTORY_PAUSER;
    }

    function _registryAdmin() internal view virtual returns (address) {
        return REGISTRY_ADMIN;
    }

    function _spendAuthorizer() internal view virtual returns (address) {
        return SPEND_AUTHORIZER;
    }

    function _organizationDomain() internal view virtual returns (bytes32) {
        return ORGANIZATION_DOMAIN;
    }

    function _externalReviewDigest() internal view virtual returns (bytes32) {
        return EXTERNAL_REVIEW_DIGEST;
    }

    function _releasePlanDigest() internal view virtual returns (bytes32) {
        return RELEASE_PLAN_DIGEST;
    }

    function _broadcastEnabled() internal view virtual returns (bool) {
        return MAINNET_BROADCAST_ENABLED;
    }
}
