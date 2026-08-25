// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";
import {AgentRegistry} from "../src/AgentRegistry.sol";
import {ASCPCallEscrow, IServiceDirectory} from "../src/ASCPCallEscrow.sol";
import {ASCPSpendModule} from "../src/ASCPSpendModule.sol";

interface ISafeDeploymentTarget {
    function getOwners() external view returns (address[] memory);
    function getThreshold() external view returns (uint256);
    function isModuleEnabled(address module) external view returns (bool);
    function execTransactionFromModule(address to, uint256 value, bytes memory data, uint8 operation)
        external
        returns (bool success);
}

/// @notice Fail-closed deployment package for the complete ASCP v4 graph on
///         Base Sepolia. Deployment creates a write-inert graph: it does not
///         enable the Safe module, publish a directory root, allowlist escrow,
///         activate a verifier, approve USDC, or move funds.
contract DeployASCPBaseSepolia is Script {
    uint256 public constant BASE_SEPOLIA_CHAIN_ID = 84_532;
    address public constant BASE_SEPOLIA_USDC = 0x036CbD53842c5426634e7929541eC2318f3dCF7e;
    bytes32 public constant BASE_SEPOLIA_USDC_RUNTIME_CODE_HASH =
        0xedc5281a85c0efecd49999a1ef668390c59b88702f2d4a07029d7f5d63059d6c;
    uint256 public constant INITIAL_PER_TRANSACTION_CAP = 1_000_000;
    uint256 public constant INITIAL_DAILY_CAP = 10_000_000;
    uint256 public constant INITIAL_ALLOWANCE_CEILING = 10_000_000;
    bytes32 public constant REQUIRED_BROADCAST_GUARD = keccak256("FLOWOPS_ASCP_BASE_SEPOLIA_BROADCAST_V1");

    struct Config {
        address deployer;
        uint256 expectedDeployerNonce;
        address safe;
        address directoryPublisher;
        address directoryPauser;
        address registryAdmin;
        address spendAuthorizer;
        bytes32 organizationDomain;
        bytes32 deploymentPlanDigest;
        bytes32 broadcastGuard;
    }

    struct Deployment {
        ServiceDirectory serviceDirectory;
        AgentRegistry agentRegistry;
        ASCPCallEscrow callEscrow;
        ASCPSpendModule spendModule;
    }

    error WrongChain(uint256 expected, uint256 actual);
    error MissingCanonicalUSDC(address asset);
    error UnexpectedCanonicalUSDCCodeHash(bytes32 expected, bytes32 actual);
    error DeployerNotDesignated();
    error UnexpectedDeployerNonce(uint256 expected, uint256 actual);
    error SafeNotDesignated();
    error SafeNotContract(address safe);
    error SafeInterfaceInvalid(address safe);
    error AuthorityNotDesignated();
    error AuthoritySeparationInvalid();
    error OrganizationDomainNotDesignated();
    error DeploymentPlanNotRecorded();
    error BroadcastGuardInvalid();
    error PredictedDeploymentAddressDirty(address predicted);
    error DeploymentInvariantFailed();

    function run() external returns (Deployment memory deployed) {
        if (block.chainid != BASE_SEPOLIA_CHAIN_ID) revert WrongChain(BASE_SEPOLIA_CHAIN_ID, block.chainid);
        _requireCanonicalUSDC();

        Config memory config = _config();
        _requireDeploymentConfig(config);
        _requireSafe(config.safe);
        uint256 actualNonce = vm.getNonce(config.deployer);
        if (actualNonce != config.expectedDeployerNonce) {
            revert UnexpectedDeployerNonce(config.expectedDeployerNonce, actualNonce);
        }
        _requireCleanPredictedAddresses(config.deployer, config.expectedDeployerNonce);

        vm.startBroadcast(config.deployer);
        deployed.serviceDirectory = new ServiceDirectory(
            config.safe, config.directoryPublisher, config.directoryPauser, config.organizationDomain
        );
        deployed.agentRegistry = new AgentRegistry(config.safe, config.registryAdmin, config.organizationDomain);
        deployed.callEscrow = new ASCPCallEscrow(
            IERC20(BASE_SEPOLIA_USDC), IServiceDirectory(address(deployed.serviceDirectory)), config.safe, config.safe
        );
        deployed.spendModule = new ASCPSpendModule(
            config.safe,
            IERC20(BASE_SEPOLIA_USDC),
            config.spendAuthorizer,
            ASCPSpendModule.Caps({
                perTransaction: INITIAL_PER_TRANSACTION_CAP,
                perDay: INITIAL_DAILY_CAP,
                allowanceCeiling: INITIAL_ALLOWANCE_CEILING
            })
        );
        vm.stopBroadcast();

        _verifyDeployment(deployed, config);
        console2.log("FlowOps ASCP v4 Base Sepolia deployment");
        console2.log("deployer", config.deployer);
        console2.log("safe", config.safe);
        console2.log("serviceDirectory", address(deployed.serviceDirectory));
        console2.log("agentRegistry", address(deployed.agentRegistry));
        console2.log("ascpCallEscrow", address(deployed.callEscrow));
        console2.log("ascpSpendModule", address(deployed.spendModule));
        console2.logBytes32(config.organizationDomain);
        console2.logBytes32(config.deploymentPlanDigest);
    }

    function validateDeploymentConfig(Config calldata config) external pure {
        _requireDeploymentConfig(config);
    }

    function _requireDeploymentConfig(Config memory config) internal pure {
        if (config.deployer == address(0)) revert DeployerNotDesignated();
        if (config.safe == address(0)) revert SafeNotDesignated();
        if (
            config.directoryPublisher == address(0) || config.directoryPauser == address(0)
                || config.registryAdmin == address(0) || config.spendAuthorizer == address(0)
        ) revert AuthorityNotDesignated();
        if (
            config.deployer == config.safe || config.directoryPublisher == config.safe
                || config.directoryPauser == config.safe || config.registryAdmin == config.safe
                || config.spendAuthorizer == config.safe || config.deployer == config.directoryPublisher
                || config.deployer == config.directoryPauser || config.deployer == config.registryAdmin
                || config.deployer == config.spendAuthorizer || config.directoryPublisher == config.directoryPauser
                || config.directoryPublisher == config.registryAdmin
                || config.directoryPublisher == config.spendAuthorizer || config.directoryPauser == config.registryAdmin
                || config.directoryPauser == config.spendAuthorizer || config.registryAdmin == config.spendAuthorizer
        ) revert AuthoritySeparationInvalid();
        if (config.organizationDomain == bytes32(0)) revert OrganizationDomainNotDesignated();
        if (config.deploymentPlanDigest == bytes32(0)) revert DeploymentPlanNotRecorded();
        if (config.broadcastGuard != REQUIRED_BROADCAST_GUARD) revert BroadcastGuardInvalid();
    }

    function _requireCanonicalUSDC() internal view virtual {
        if (BASE_SEPOLIA_USDC.code.length == 0) revert MissingCanonicalUSDC(BASE_SEPOLIA_USDC);
        bytes32 actualCodeHash = BASE_SEPOLIA_USDC.codehash;
        bytes32 expectedCodeHash = _expectedCanonicalUSDCCodeHash();
        if (actualCodeHash != expectedCodeHash) {
            revert UnexpectedCanonicalUSDCCodeHash(expectedCodeHash, actualCodeHash);
        }
    }

    function _expectedCanonicalUSDCCodeHash() internal view virtual returns (bytes32) {
        return BASE_SEPOLIA_USDC_RUNTIME_CODE_HASH;
    }

    function _requireCleanPredictedAddresses(address deployer, uint256 startingNonce) private view {
        for (uint256 offset = 0; offset < 4; ++offset) {
            address predicted = vm.computeCreateAddress(deployer, startingNonce + offset);
            if (
                predicted.code.length != 0 || vm.getNonce(predicted) != 0 || predicted.balance != 0
                    || IERC20(BASE_SEPOLIA_USDC).balanceOf(predicted) != 0
            ) revert PredictedDeploymentAddressDirty(predicted);
        }
    }

    function _requireSafe(address safe) private {
        if (safe.code.length == 0) revert SafeNotContract(safe);
        address[] memory owners;
        uint256 threshold;
        bool zeroModuleEnabled;
        try ISafeDeploymentTarget(safe).getOwners() returns (address[] memory values) {
            owners = values;
        } catch {
            revert SafeInterfaceInvalid(safe);
        }
        try ISafeDeploymentTarget(safe).getThreshold() returns (uint256 value) {
            threshold = value;
        } catch {
            revert SafeInterfaceInvalid(safe);
        }
        try ISafeDeploymentTarget(safe).isModuleEnabled(address(0)) returns (bool enabled) {
            zeroModuleEnabled = enabled;
        } catch {
            revert SafeInterfaceInvalid(safe);
        }
        if (owners.length == 0 || threshold == 0 || threshold > owners.length || zeroModuleEnabled) {
            revert SafeInterfaceInvalid(safe);
        }
        for (uint256 index = 0; index < owners.length; ++index) {
            if (owners[index] == address(0)) revert SafeInterfaceInvalid(safe);
            for (uint256 prior = 0; prior < index; ++prior) {
                if (owners[index] == owners[prior]) revert SafeInterfaceInvalid(safe);
            }
        }
        (bool moduleCallSucceeded, bytes memory moduleCallResult) = safe.call(
            abi.encodeCall(ISafeDeploymentTarget.execTransactionFromModule, (address(0), 0, bytes(""), uint8(0)))
        );
        if (
            moduleCallSucceeded
                || keccak256(moduleCallResult) != keccak256(abi.encodeWithSignature("Error(string)", "GS104"))
        ) revert SafeInterfaceInvalid(safe);
    }

    function _verifyDeployment(Deployment memory deployed, Config memory config) private view {
        (uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling) = deployed.spendModule.caps();
        if (
            deployed.serviceDirectory.governor() != config.safe
                || deployed.serviceDirectory.directoryPublisher() != config.directoryPublisher
                || deployed.serviceDirectory.pauser() != config.directoryPauser
                || deployed.serviceDirectory.orgDomain() != config.organizationDomain
                || deployed.agentRegistry.governor() != config.safe
                || deployed.agentRegistry.registryAdmin() != config.registryAdmin
                || deployed.agentRegistry.orgDomain() != config.organizationDomain
                || address(deployed.callEscrow.usdc()) != BASE_SEPOLIA_USDC
                || address(deployed.callEscrow.serviceDirectory()) != address(deployed.serviceDirectory)
                || deployed.callEscrow.safe() != config.safe || deployed.callEscrow.governor() != config.safe
                || deployed.spendModule.safe() != config.safe
                || address(deployed.spendModule.token()) != BASE_SEPOLIA_USDC
                || deployed.spendModule.spendAuthorizer() != config.spendAuthorizer
                || perTransaction != INITIAL_PER_TRANSACTION_CAP || perDay != INITIAL_DAILY_CAP
                || allowanceCeiling != INITIAL_ALLOWANCE_CEILING || deployed.spendModule.emergencyPaused()
                || deployed.callEscrow.emergencyPaused() || deployed.serviceDirectory.currentVersion() != 0
                || deployed.serviceDirectory.currentRoot() != bytes32(0) || deployed.agentRegistry.agentCount() != 0
                || deployed.callEscrow.totalLocked() != 0 || deployed.spendModule.executedPrincipal() != 0
                || deployed.spendModule.escrowAllowlist(address(deployed.callEscrow)) != bytes32(0)
                || address(deployed.serviceDirectory).balance != 0 || address(deployed.agentRegistry).balance != 0
                || address(deployed.callEscrow).balance != 0 || address(deployed.spendModule).balance != 0
                || IERC20(BASE_SEPOLIA_USDC).balanceOf(address(deployed.serviceDirectory)) != 0
                || IERC20(BASE_SEPOLIA_USDC).balanceOf(address(deployed.agentRegistry)) != 0
                || IERC20(BASE_SEPOLIA_USDC).balanceOf(address(deployed.callEscrow)) != 0
                || IERC20(BASE_SEPOLIA_USDC).balanceOf(address(deployed.spendModule)) != 0
                || ISafeDeploymentTarget(config.safe).isModuleEnabled(address(deployed.spendModule))
        ) revert DeploymentInvariantFailed();
    }

    function _config() internal view virtual returns (Config memory) {
        return Config({
            deployer: vm.envAddress("FLOWOPS_ASCP_SEPOLIA_DEPLOYER"),
            expectedDeployerNonce: vm.envUint("FLOWOPS_ASCP_SEPOLIA_EXPECTED_DEPLOYER_NONCE"),
            safe: vm.envAddress("FLOWOPS_ASCP_SEPOLIA_SAFE"),
            directoryPublisher: vm.envAddress("FLOWOPS_ASCP_SEPOLIA_DIRECTORY_PUBLISHER"),
            directoryPauser: vm.envAddress("FLOWOPS_ASCP_SEPOLIA_DIRECTORY_PAUSER"),
            registryAdmin: vm.envAddress("FLOWOPS_ASCP_SEPOLIA_REGISTRY_ADMIN"),
            spendAuthorizer: vm.envAddress("FLOWOPS_ASCP_SEPOLIA_SPEND_AUTHORIZER"),
            organizationDomain: vm.envBytes32("FLOWOPS_ASCP_SEPOLIA_ORGANIZATION_DOMAIN"),
            deploymentPlanDigest: vm.envBytes32("FLOWOPS_ASCP_SEPOLIA_DEPLOYMENT_PLAN_DIGEST"),
            broadcastGuard: vm.envBytes32("FLOWOPS_ASCP_SEPOLIA_BROADCAST_GUARD")
        });
    }
}
