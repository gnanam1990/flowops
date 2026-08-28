// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {DeployASCPBaseMainnet} from "./DeployASCPBaseMainnet.s.sol";

/// @notice Owner-authorized, explicitly unaudited deployment path for the
///         zero-fund ASCP contract graph used as Base builder evidence.
/// @dev This path cannot claim an external review. It deploys the same
///      write-inert graph as the production script, but does not enable the
///      Safe module, allowlist the escrow, approve USDC, or move funds.
contract DeployASCPBaseMainnetExperimental is DeployASCPBaseMainnet {
    string public constant EXPERIMENTAL_STATUS = "owner-authorized-unaudited-zero-fund";

    error ExternalReviewMustRemainUnset();
    error OwnerRiskAcceptanceNotRecorded();

    function _requireReleaseGates(
        address deployer,
        address safe,
        address publisher,
        address pauser,
        address registryAdmin,
        address spendAuthorizer,
        bytes32 orgDomain,
        bytes32 reviewDigest,
        bytes32 ownerRiskAcceptanceDigest,
        bool broadcastEnabled
    ) internal pure override {
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
        if (reviewDigest != bytes32(0)) revert ExternalReviewMustRemainUnset();
        if (ownerRiskAcceptanceDigest == bytes32(0)) revert OwnerRiskAcceptanceNotRecorded();
        if (!broadcastEnabled) revert MainnetBroadcastDisabled();
    }

    function _designatedDeployer() internal pure override returns (address) {
        return 0x3c1DAA7a6193848320e9477cBcfb7F512c0Fd74B;
    }

    function _expectedDeployerNonce() internal pure override returns (uint256) {
        return 1;
    }

    function _productionSafe() internal pure override returns (address) {
        return 0x13E9Fa8d49Ee3E3b456Db71d111Da9b78fABD518;
    }

    function _expectedSafeOwners() internal pure override returns (address[3] memory owners) {
        owners = [
            0x0f094eec6B569c3f33033102ad3ce33EAbFeb2fB,
            0xE8405844a45C209895afE2e49be6aA2C6C6202a6,
            0xe88872F94013E4584BCeafb5d5f87dA291d086D2
        ];
    }

    function _expectedSafeThreshold() internal pure override returns (uint256) {
        return 2;
    }

    function _expectedSafeNonce() internal pure override returns (uint256) {
        return 0;
    }

    function _expectedSafeRuntimeCodeHash() internal pure override returns (bytes32) {
        return 0xd7d408ebcd99b2b70be43e20253d6d92a8ea8fab29bd3be7f55b10032331fb4c;
    }

    function _expectedSafeImplementation() internal pure override returns (address) {
        return 0x29fcB43b46531BcA003ddC8FCB67FFE91900C762;
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
        return bytes32(0);
    }

    function _releasePlanDigest() internal view override returns (bytes32) {
        return vm.envBytes32("FLOWOPS_OWNER_RISK_ACCEPTANCE_DIGEST");
    }

    function _broadcastEnabled() internal view override returns (bool) {
        return vm.envBool("FLOWOPS_EXPERIMENTAL_MAINNET_BROADCAST_ENABLED");
    }

    function _deploymentMode() internal pure override returns (string memory) {
        return EXPERIMENTAL_STATUS;
    }
}
