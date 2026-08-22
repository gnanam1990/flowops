// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";

/// @title AgentRegistry
/// @notice Single-organization registry whose hot administrator signs exact,
///         short-lived actions while a relayer pays gas.
contract AgentRegistry is EIP712 {
    using ECDSA for bytes32;

    bytes32 public constant TYPED_DATA_MANIFEST_SHA256 =
        0x87eee19267c1684f91e10454a8f1a26880a2434e65f5609791c54b803154bff5;
    bytes32 public constant REGISTRY_ADMIN_ROLE = keccak256("ASCP_REGISTRY_ADMIN");
    bytes32 public constant AGENT_ID_DOMAIN = keccak256("ASCP_AGENT_ID_V1");
    bytes32 public constant ADMIN_ACTION_TYPEHASH = keccak256(
        "AdminActionAuthorization(bytes32 orgDomain,address contractAddress,uint256 chainId,bytes32 authorityRole,bytes4 functionSelector,bytes32 payloadHash,bytes32 adminOperationId,uint256 adminNonce,uint64 adminEpoch,uint64 validAfter,uint64 validBefore,bytes32 workflowId)"
    );
    uint64 public constant MAX_AUTHORIZATION_WINDOW = 10 minutes;
    uint256 public constant MAX_LABEL_BYTES = 64;

    enum Status {
        None,
        Active,
        Suspended,
        Retired
    }

    struct AdminActionAuthorization {
        bytes32 orgDomain;
        address contractAddress;
        uint256 chainId;
        bytes32 authorityRole;
        bytes4 functionSelector;
        bytes32 payloadHash;
        bytes32 adminOperationId;
        uint256 adminNonce;
        uint64 adminEpoch;
        uint64 validAfter;
        uint64 validBefore;
        bytes32 workflowId;
    }

    struct Agent {
        string label;
        bytes32 labelHash;
        bytes32 policyHash;
        Status status;
        uint64 registeredAt;
        uint64 updatedAt;
    }

    address public immutable governor;
    bytes32 public immutable orgDomain;
    address public registryAdmin;
    uint64 public registryAdminEpoch = 1;
    uint256 public agentCount;

    mapping(bytes32 agentId => Agent agent) private _agents;
    mapping(bytes32 operationId => bool used) public usedAdminOperationIds;
    mapping(uint256 nonce => bool used) public usedAdminNonces;

    event RegistryAdminSet(address indexed previousAdmin, address indexed admin, uint64 indexed epoch);
    event AgentRegistered(
        bytes32 indexed agentId,
        bytes32 indexed policyHash,
        bytes32 indexed adminOperationId,
        string label,
        bytes32 workflowId,
        address registryAdmin,
        address relayer
    );
    event AgentPolicyUpdated(
        bytes32 indexed agentId,
        bytes32 indexed previousPolicyHash,
        bytes32 indexed policyHash,
        bytes32 adminOperationId,
        bytes32 workflowId,
        address registryAdmin,
        address relayer
    );
    event AgentStatusSet(
        bytes32 indexed agentId,
        Status indexed previousStatus,
        Status indexed status,
        bytes32 adminOperationId,
        bytes32 workflowId,
        address registryAdmin,
        address relayer
    );

    error GovernorMustBeContract(address governor);
    error ZeroAddress();
    error NotGovernor(address caller);
    error InvalidLabel();
    error InvalidPolicyHash();
    error InvalidAuthorization();
    error UnauthorizedSigner(address expected, address actual);
    error AuthorizationUsed(bytes32 operationId);
    error AuthorizationNonceUsed(uint256 nonce);
    error AgentUnknown(bytes32 agentId);
    error AgentAlreadyExists(bytes32 agentId);
    error AgentRetired(bytes32 agentId);
    error InvalidStatus(Status status);
    error StatusUnchanged(Status status);

    modifier onlyGovernor() {
        if (msg.sender != governor) revert NotGovernor(msg.sender);
        _;
    }

    constructor(address governor_, address registryAdmin_, bytes32 orgDomain_) EIP712("ASCP", "4") {
        if (governor_.code.length == 0) revert GovernorMustBeContract(governor_);
        if (registryAdmin_ == address(0) || orgDomain_ == bytes32(0)) revert ZeroAddress();
        governor = governor_;
        registryAdmin = registryAdmin_;
        orgDomain = orgDomain_;
        emit RegistryAdminSet(address(0), registryAdmin_, 1);
    }

    function register(
        string calldata label,
        bytes32 policyHash,
        bytes32 workflowId,
        AdminActionAuthorization calldata authorization,
        bytes calldata signature
    ) external returns (bytes32 agentId) {
        uint256 labelLength = bytes(label).length;
        if (labelLength == 0 || labelLength > MAX_LABEL_BYTES) revert InvalidLabel();
        if (policyHash == bytes32(0)) revert InvalidPolicyHash();
        _consumeAdminAuthorization(
            authorization,
            signature,
            this.register.selector,
            keccak256(abi.encode(label, policyHash, workflowId)),
            workflowId
        );
        agentId = deriveAgentId(authorization.adminOperationId);
        if (_agents[agentId].status != Status.None) revert AgentAlreadyExists(agentId);
        uint64 nowTs = uint64(block.timestamp);
        _agents[agentId] = Agent({
            label: label,
            labelHash: keccak256(bytes(label)),
            policyHash: policyHash,
            status: Status.Active,
            registeredAt: nowTs,
            updatedAt: nowTs
        });
        agentCount += 1;
        emit AgentRegistered(
            agentId, policyHash, authorization.adminOperationId, label, workflowId, registryAdmin, msg.sender
        );
    }

    function updatePolicyHash(
        bytes32 agentId,
        bytes32 policyHash,
        bytes32 workflowId,
        AdminActionAuthorization calldata authorization,
        bytes calldata signature
    ) external {
        Agent storage agent = _requireMutableAgent(agentId);
        if (policyHash == bytes32(0) || policyHash == agent.policyHash) revert InvalidPolicyHash();
        _consumeAdminAuthorization(
            authorization,
            signature,
            this.updatePolicyHash.selector,
            keccak256(abi.encode(agentId, policyHash, workflowId)),
            workflowId
        );
        bytes32 previous = agent.policyHash;
        agent.policyHash = policyHash;
        agent.updatedAt = uint64(block.timestamp);
        emit AgentPolicyUpdated(
            agentId, previous, policyHash, authorization.adminOperationId, workflowId, registryAdmin, msg.sender
        );
    }

    function setStatus(
        bytes32 agentId,
        Status status,
        bytes32 workflowId,
        AdminActionAuthorization calldata authorization,
        bytes calldata signature
    ) external {
        Agent storage agent = _requireMutableAgent(agentId);
        if (status == Status.None) revert InvalidStatus(status);
        if (status == agent.status) revert StatusUnchanged(status);
        _consumeAdminAuthorization(
            authorization,
            signature,
            this.setStatus.selector,
            keccak256(abi.encode(agentId, status, workflowId)),
            workflowId
        );
        Status previous = agent.status;
        agent.status = status;
        agent.updatedAt = uint64(block.timestamp);
        emit AgentStatusSet(
            agentId, previous, status, authorization.adminOperationId, workflowId, registryAdmin, msg.sender
        );
    }

    function setRegistryAdmin(address newAdmin) external onlyGovernor {
        if (newAdmin == address(0)) revert ZeroAddress();
        address previous = registryAdmin;
        registryAdmin = newAdmin;
        registryAdminEpoch += 1;
        emit RegistryAdminSet(previous, newAdmin, registryAdminEpoch);
    }

    function getAgent(bytes32 agentId) external view returns (Agent memory) {
        Agent memory agent = _agents[agentId];
        if (agent.status == Status.None) revert AgentUnknown(agentId);
        return agent;
    }

    function deriveAgentId(bytes32 adminOperationId) public view returns (bytes32) {
        if (adminOperationId == bytes32(0)) revert InvalidAuthorization();
        return keccak256(abi.encode(AGENT_ID_DOMAIN, block.chainid, address(this), orgDomain, adminOperationId));
    }

    function adminAuthorizationDigest(AdminActionAuthorization calldata authorization) public view returns (bytes32) {
        return _hashTypedDataV4(keccak256(abi.encode(ADMIN_ACTION_TYPEHASH, authorization)));
    }

    function _consumeAdminAuthorization(
        AdminActionAuthorization calldata authorization,
        bytes calldata signature,
        bytes4 selector,
        bytes32 payloadHash,
        bytes32 workflowId
    ) private {
        if (
            authorization.orgDomain != orgDomain || authorization.contractAddress != address(this)
                || authorization.chainId != block.chainid || authorization.authorityRole != REGISTRY_ADMIN_ROLE
                || authorization.functionSelector != selector || authorization.payloadHash != payloadHash
                || authorization.workflowId != workflowId || authorization.adminOperationId == bytes32(0)
                || authorization.adminEpoch != registryAdminEpoch || authorization.validAfter > block.timestamp
                || block.timestamp >= authorization.validBefore || authorization.validBefore <= authorization.validAfter
                || authorization.validBefore - authorization.validAfter > MAX_AUTHORIZATION_WINDOW
        ) revert InvalidAuthorization();
        if (usedAdminOperationIds[authorization.adminOperationId]) {
            revert AuthorizationUsed(authorization.adminOperationId);
        }
        if (usedAdminNonces[authorization.adminNonce]) revert AuthorizationNonceUsed(authorization.adminNonce);
        address actualSigner = adminAuthorizationDigest(authorization).recover(signature);
        if (actualSigner != registryAdmin) revert UnauthorizedSigner(registryAdmin, actualSigner);
        usedAdminOperationIds[authorization.adminOperationId] = true;
        usedAdminNonces[authorization.adminNonce] = true;
    }

    function _requireMutableAgent(bytes32 agentId) private view returns (Agent storage agent) {
        agent = _agents[agentId];
        if (agent.status == Status.None) revert AgentUnknown(agentId);
        if (agent.status == Status.Retired) revert AgentRetired(agentId);
    }
}
