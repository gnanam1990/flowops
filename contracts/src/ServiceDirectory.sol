// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";

/// @title ServiceDirectory
/// @notice Governed, append-only directory roots for FlowOps seller and
///         resource metadata. It never holds assets or chooses escrow payout
///         recipients; consumers must prove a leaf against currentRoot().
/// @dev UNAUDITED. This contract is intentionally not wired into CallEscrow
///      until its successor escrow integration and independent review exist.
contract ServiceDirectory {
    bytes32 public constant SELLER_LEAF_DOMAIN = keccak256("ASCP_SELLER_LEAF_V1");
    bytes32 public constant RESOURCE_LEAF_DOMAIN = keccak256("ASCP_RESOURCE_LEAF_V1");
    bytes32 public constant PROPOSAL_DOMAIN = keccak256("ASCP_DIRECTORY_PROPOSAL_V1");
    bytes32 public constant EIP712_DOMAIN_TYPEHASH =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");
    bytes32 public constant ADMIN_ACTION_TYPEHASH = keccak256(
        "AdminActionAuthorization(bytes32 orgDomain,address contractAddress,uint256 chainId,bytes32 authorityRole,bytes4 functionSelector,bytes32 payloadHash,bytes32 adminOperationId,uint256 adminNonce,uint64 adminEpoch,uint64 validAfter,uint64 validBefore,bytes32 workflowId)"
    );
    bytes32 public constant DIRECTORY_PUBLISHER_ROLE = keccak256("ASCP_DIRECTORY_PUBLISHER");
    bytes32 public constant PAUSER_ROLE = keccak256("ASCP_DIRECTORY_PAUSER");
    bytes32 private constant NAME_HASH = keccak256("ASCP");
    bytes32 private constant VERSION_HASH = keccak256("4");

    uint64 public constant ORDINARY_DELAY = 1 hours;
    uint64 public constant PAYOUT_OR_AUTHORITY_DELAY = 24 hours;
    uint64 public constant MAX_AUTHORIZATION_WINDOW = 10 minutes;

    enum ChangeClass {
        None,
        Ordinary,
        PayoutOrAuthorityAffecting
    }
    enum ProposalState {
        None,
        Proposed,
        ApprovedPendingActivation,
        Active,
        Cancelled
    }

    struct SellerLeaf {
        bytes32 sellerId;
        address payoutAddress;
        address ackAuthority;
        address quoteSigningKey;
        uint64 keyEpoch;
        bytes32 baseURLOriginHash;
        uint8 status;
    }

    struct ResourceLeaf {
        bytes32 sellerId;
        bytes32 resourceId;
        uint256 price;
        bool escrowSupported;
        bytes32 verificationSpecHash;
        uint64 declaredWorkTime;
        uint64 verificationBudgetSeconds;
    }

    struct DirectoryProposal {
        uint64 versionId;
        uint64 previousVersion;
        bytes32 previousRoot;
        bytes32 newRoot;
        bytes32 blobContentHash;
        bytes32 locationsHash;
        ChangeClass changeClass;
        uint64 requestedActivatesAt;
        bytes32 workflowId;
        bytes32 workflowPayloadHash;
        uint256 proposerNonce;
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

    struct ProposalRecord {
        DirectoryProposal proposal;
        bytes32 proposalHash;
        ProposalState state;
        address proposer;
        address approver;
        uint64 proposedAt;
        uint64 approvedAt;
        uint64 effectiveActivatesAt;
    }

    address public immutable governor;
    bytes32 public immutable orgDomain;
    address public directoryPublisher;
    address public pauser;
    uint64 public directoryPublisherEpoch = 1;
    uint64 public pauserEpoch = 1;
    uint64 private _activeVersion;
    bytes32 private _activeRoot;
    bytes32 private _liveSuccessorHash;

    mapping(bytes32 proposalHash => ProposalRecord record) private _proposals;
    mapping(uint64 versionId => bytes32 proposalHash) public latestProposalHash;
    mapping(uint256 nonce => bool used) public usedProposerNonces;
    mapping(bytes32 operationId => bool used) public usedAdminOperationIds;
    mapping(bytes32 role => mapping(uint256 nonce => bool used)) public usedAdminNonces;
    mapping(bytes32 sellerId => bool) public pausedSeller;
    mapping(address key => bool) public quoteKeyRevoked;

    event VersionProposed(
        bytes32 indexed proposalHash,
        uint64 indexed versionId,
        uint64 previousVersion,
        bytes32 previousRoot,
        bytes32 newRoot,
        bytes32 blobContentHash,
        bytes32 locationsHash,
        ChangeClass changeClass,
        bytes32 workflowId,
        bytes32 workflowPayloadHash,
        address proposer,
        uint64 proposedAt
    );
    event VersionApproved(
        bytes32 indexed proposalHash,
        uint64 indexed versionId,
        address indexed approver,
        uint64 approvedAt,
        uint64 effectiveActivatesAt
    );
    event VersionActivated(bytes32 indexed proposalHash, uint64 indexed versionId, bytes32 root, uint64 activatedAt);
    event VersionCancelled(bytes32 indexed proposalHash, uint64 indexed versionId, address indexed canceller);
    event DirectoryPublisherSet(address indexed previousPublisher, address indexed publisher, uint64 epoch);
    event PauserSet(address indexed previousPauser, address indexed pauser, uint64 epoch);
    event SellerPaused(bytes32 indexed sellerId, bool paused, address indexed actor);
    event QuoteKeyRevoked(address indexed key, bool revoked, address indexed actor);

    error GovernorMustBeContract(address governor);
    error ZeroAddress();
    error NotGovernor(address caller);
    error InvalidChangeClass(ChangeClass changeClass);
    error InvalidProposal();
    error VersionConflict(uint64 expected, uint64 actual);
    error PreviousVersionMismatch(uint64 expectedVersion, uint64 actualVersion);
    error PreviousRootMismatch(bytes32 expectedRoot, bytes32 actualRoot);
    error LiveSuccessorExists(bytes32 proposalHash);
    error UnknownProposal(bytes32 proposalHash);
    error WrongProposalState(bytes32 proposalHash, ProposalState expected, ProposalState actual);
    error ProposalHashMismatch(bytes32 expected, bytes32 actual);
    error ActivationAlreadyDue(bytes32 proposalHash, uint64 activationTime, uint256 nowTs);
    error InvalidAuthorization();
    error UnauthorizedSigner(address expected, address actual);
    error AuthorizationUsed(bytes32 operationId);
    error AuthorizationNonceUsed(bytes32 role, uint256 nonce);
    error SellerIdZero();
    error QuoteKeyZero();

    constructor(address governor_, address directoryPublisher_, address pauser_, bytes32 orgDomain_) {
        if (governor_.code.length == 0) revert GovernorMustBeContract(governor_);
        if (directoryPublisher_ == address(0) || pauser_ == address(0) || orgDomain_ == bytes32(0)) {
            revert ZeroAddress();
        }
        governor = governor_;
        directoryPublisher = directoryPublisher_;
        pauser = pauser_;
        orgDomain = orgDomain_;
    }

    function proposeVersion(
        DirectoryProposal calldata proposal,
        AdminActionAuthorization calldata authorization,
        bytes calldata signature
    ) external returns (bytes32 proposalHash) {
        _activateIfEligible();
        uint64 expectedVersion = _activeVersion + 1;
        if (proposal.versionId != expectedVersion) revert VersionConflict(expectedVersion, proposal.versionId);
        if (proposal.previousVersion != _activeVersion) {
            revert PreviousVersionMismatch(_activeVersion, proposal.previousVersion);
        }
        if (proposal.previousRoot != _activeRoot) revert PreviousRootMismatch(_activeRoot, proposal.previousRoot);
        if (_liveSuccessorHash != bytes32(0)) revert LiveSuccessorExists(_liveSuccessorHash);
        if (
            proposal.newRoot == bytes32(0) || proposal.blobContentHash == bytes32(0)
                || proposal.locationsHash == bytes32(0) || proposal.workflowId == bytes32(0)
                || proposal.workflowPayloadHash == bytes32(0)
        ) revert InvalidProposal();
        if (
            proposal.changeClass != ChangeClass.Ordinary
                && proposal.changeClass != ChangeClass.PayoutOrAuthorityAffecting
        ) revert InvalidChangeClass(proposal.changeClass);
        if (usedProposerNonces[proposal.proposerNonce]) revert InvalidProposal();

        proposalHash = hashProposal(proposal);
        _consumeAdminAuthorization(
            authorization,
            signature,
            DIRECTORY_PUBLISHER_ROLE,
            this.proposeVersion.selector,
            keccak256(abi.encode(proposal)),
            proposal.workflowId,
            directoryPublisher,
            directoryPublisherEpoch
        );

        usedProposerNonces[proposal.proposerNonce] = true;
        latestProposalHash[proposal.versionId] = proposalHash;
        _liveSuccessorHash = proposalHash;
        _proposals[proposalHash] = ProposalRecord({
            proposal: proposal,
            proposalHash: proposalHash,
            state: ProposalState.Proposed,
            proposer: msg.sender,
            approver: address(0),
            proposedAt: uint64(block.timestamp),
            approvedAt: 0,
            effectiveActivatesAt: 0
        });
        emit VersionProposed(
            proposalHash,
            proposal.versionId,
            proposal.previousVersion,
            proposal.previousRoot,
            proposal.newRoot,
            proposal.blobContentHash,
            proposal.locationsHash,
            proposal.changeClass,
            proposal.workflowId,
            proposal.workflowPayloadHash,
            msg.sender,
            uint64(block.timestamp)
        );
    }

    function approveVersion(uint64 versionId, bytes32 expectedProposalHash) external onlyGovernor {
        _activateIfEligible();
        bytes32 proposalHash = latestProposalHash[versionId];
        if (proposalHash == bytes32(0)) revert UnknownProposal(expectedProposalHash);
        if (proposalHash != expectedProposalHash) revert ProposalHashMismatch(proposalHash, expectedProposalHash);
        ProposalRecord storage record = _proposals[proposalHash];
        if (record.state != ProposalState.Proposed) {
            revert WrongProposalState(proposalHash, ProposalState.Proposed, record.state);
        }
        uint64 delay = record.proposal.changeClass == ChangeClass.Ordinary ? ORDINARY_DELAY : PAYOUT_OR_AUTHORITY_DELAY;
        uint64 requested = record.proposal.requestedActivatesAt;
        uint64 minimum = uint64(block.timestamp) + delay;
        record.state = ProposalState.ApprovedPendingActivation;
        record.approver = msg.sender;
        record.approvedAt = uint64(block.timestamp);
        record.effectiveActivatesAt = requested > minimum ? requested : minimum;
        emit VersionApproved(proposalHash, versionId, msg.sender, record.approvedAt, record.effectiveActivatesAt);
    }

    function cancelVersion(uint64 versionId, bytes32 expectedProposalHash) external onlyGovernor {
        bytes32 proposalHash = latestProposalHash[versionId];
        if (proposalHash == bytes32(0)) revert UnknownProposal(expectedProposalHash);
        if (proposalHash != expectedProposalHash) revert ProposalHashMismatch(proposalHash, expectedProposalHash);
        ProposalRecord storage record = _proposals[proposalHash];
        if (record.state != ProposalState.Proposed && record.state != ProposalState.ApprovedPendingActivation) {
            revert WrongProposalState(proposalHash, ProposalState.Proposed, record.state);
        }
        if (record.state == ProposalState.ApprovedPendingActivation && block.timestamp >= record.effectiveActivatesAt) {
            revert ActivationAlreadyDue(proposalHash, record.effectiveActivatesAt, block.timestamp);
        }
        record.state = ProposalState.Cancelled;
        _liveSuccessorHash = bytes32(0);
        emit VersionCancelled(proposalHash, versionId, msg.sender);
    }

    function activateVersion() external {
        _activateIfEligible();
    }

    function setDirectoryPublisher(address publisher) external onlyGovernor {
        if (publisher == address(0)) revert ZeroAddress();
        address previous = directoryPublisher;
        directoryPublisher = publisher;
        unchecked {
            ++directoryPublisherEpoch;
        }
        emit DirectoryPublisherSet(previous, publisher, directoryPublisherEpoch);
    }

    function setPauser(address pauser_) external onlyGovernor {
        if (pauser_ == address(0)) revert ZeroAddress();
        address previous = pauser;
        pauser = pauser_;
        unchecked {
            ++pauserEpoch;
        }
        emit PauserSet(previous, pauser_, pauserEpoch);
    }

    function pauseSeller(
        bytes32 sellerId,
        bool paused,
        AdminActionAuthorization calldata authorization,
        bytes calldata signature
    ) external {
        if (sellerId == bytes32(0)) revert SellerIdZero();
        if (!paused) {
            if (msg.sender != governor) revert NotGovernor(msg.sender);
        } else {
            _consumeAdminAuthorization(
                authorization,
                signature,
                PAUSER_ROLE,
                this.pauseSeller.selector,
                keccak256(abi.encode(sellerId, paused)),
                bytes32(0),
                pauser,
                pauserEpoch
            );
        }
        pausedSeller[sellerId] = paused;
        emit SellerPaused(sellerId, paused, msg.sender);
    }

    function setQuoteKeyRevoked(
        address key,
        bool revoked,
        AdminActionAuthorization calldata authorization,
        bytes calldata signature
    ) external {
        if (key == address(0)) revert QuoteKeyZero();
        if (!revoked) {
            if (msg.sender != governor) revert NotGovernor(msg.sender);
        } else {
            _consumeAdminAuthorization(
                authorization,
                signature,
                PAUSER_ROLE,
                this.setQuoteKeyRevoked.selector,
                keccak256(abi.encode(key, revoked)),
                bytes32(0),
                pauser,
                pauserEpoch
            );
        }
        quoteKeyRevoked[key] = revoked;
        emit QuoteKeyRevoked(key, revoked, msg.sender);
    }

    function currentVersion() public view returns (uint64) {
        ProposalRecord storage pending = _proposals[_liveSuccessorHash];
        if (
            _liveSuccessorHash != bytes32(0) && pending.state == ProposalState.ApprovedPendingActivation
                && block.timestamp >= pending.effectiveActivatesAt
        ) return pending.proposal.versionId;
        return _activeVersion;
    }

    function currentRoot() public view returns (bytes32) {
        ProposalRecord storage pending = _proposals[_liveSuccessorHash];
        if (
            _liveSuccessorHash != bytes32(0) && pending.state == ProposalState.ApprovedPendingActivation
                && block.timestamp >= pending.effectiveActivatesAt
        ) return pending.proposal.newRoot;
        return _activeRoot;
    }

    function getProposal(bytes32 proposalHash) external view returns (ProposalRecord memory) {
        return _proposals[proposalHash];
    }

    function hashSellerLeaf(SellerLeaf memory leaf) public pure returns (bytes32) {
        return keccak256(
            abi.encode(
                SELLER_LEAF_DOMAIN,
                leaf.sellerId,
                leaf.payoutAddress,
                leaf.ackAuthority,
                leaf.quoteSigningKey,
                leaf.keyEpoch,
                leaf.baseURLOriginHash,
                leaf.status
            )
        );
    }

    function hashResourceLeaf(ResourceLeaf memory leaf) public pure returns (bytes32) {
        return keccak256(
            abi.encode(
                RESOURCE_LEAF_DOMAIN,
                leaf.sellerId,
                leaf.resourceId,
                leaf.price,
                leaf.escrowSupported,
                leaf.verificationSpecHash,
                leaf.declaredWorkTime,
                leaf.verificationBudgetSeconds
            )
        );
    }

    function verifySeller(uint64 version, SellerLeaf calldata leaf, bytes32[] calldata proof)
        external
        view
        returns (bool)
    {
        return version == currentVersion() && _verify(currentRoot(), hashSellerLeaf(leaf), proof);
    }

    function verifyResource(uint64 version, ResourceLeaf calldata leaf, bytes32[] calldata proof)
        external
        view
        returns (bool)
    {
        return version == currentVersion() && _verify(currentRoot(), hashResourceLeaf(leaf), proof);
    }

    function hashProposal(DirectoryProposal memory proposal) public view returns (bytes32) {
        return keccak256(abi.encode(PROPOSAL_DOMAIN, block.chainid, address(this), proposal));
    }

    function adminAuthorizationDigest(AdminActionAuthorization memory authorization) public view returns (bytes32) {
        bytes32 structHash = keccak256(abi.encode(ADMIN_ACTION_TYPEHASH, authorization));
        return keccak256(abi.encodePacked("\x19\x01", _domainSeparator(), structHash));
    }

    modifier onlyGovernor() {
        if (msg.sender != governor) revert NotGovernor(msg.sender);
        _;
    }

    function _activateIfEligible() private {
        bytes32 proposalHash = _liveSuccessorHash;
        if (proposalHash == bytes32(0)) return;
        ProposalRecord storage record = _proposals[proposalHash];
        if (record.state != ProposalState.ApprovedPendingActivation || block.timestamp < record.effectiveActivatesAt) {
            return;
        }
        _activeVersion = record.proposal.versionId;
        _activeRoot = record.proposal.newRoot;
        record.state = ProposalState.Active;
        _liveSuccessorHash = bytes32(0);
        emit VersionActivated(proposalHash, _activeVersion, _activeRoot, uint64(block.timestamp));
    }

    function _consumeAdminAuthorization(
        AdminActionAuthorization calldata authorization,
        bytes calldata signature,
        bytes32 role,
        bytes4 selector,
        bytes32 payloadHash,
        bytes32 workflowId,
        address expectedSigner,
        uint64 expectedEpoch
    ) private {
        if (
            authorization.orgDomain != orgDomain || authorization.contractAddress != address(this)
                || authorization.chainId != block.chainid || authorization.authorityRole != role
                || authorization.functionSelector != selector || authorization.payloadHash != payloadHash
                || authorization.workflowId != workflowId || authorization.adminOperationId == bytes32(0)
                || authorization.adminEpoch != expectedEpoch || authorization.validAfter > block.timestamp
                || block.timestamp >= authorization.validBefore || authorization.validBefore <= authorization.validAfter
                || authorization.validBefore - authorization.validAfter > MAX_AUTHORIZATION_WINDOW
        ) revert InvalidAuthorization();
        if (usedAdminOperationIds[authorization.adminOperationId]) {
            revert AuthorizationUsed(authorization.adminOperationId);
        }
        if (usedAdminNonces[role][authorization.adminNonce]) {
            revert AuthorizationNonceUsed(role, authorization.adminNonce);
        }
        address actualSigner = ECDSA.recover(adminAuthorizationDigest(authorization), signature);
        if (actualSigner != expectedSigner) revert UnauthorizedSigner(expectedSigner, actualSigner);
        usedAdminOperationIds[authorization.adminOperationId] = true;
        usedAdminNonces[role][authorization.adminNonce] = true;
    }

    function _domainSeparator() private view returns (bytes32) {
        return keccak256(abi.encode(EIP712_DOMAIN_TYPEHASH, NAME_HASH, VERSION_HASH, block.chainid, address(this)));
    }

    function _verify(bytes32 root, bytes32 leaf, bytes32[] calldata proof) private pure returns (bool) {
        bytes32 computed = leaf;
        for (uint256 index; index < proof.length; ++index) {
            bytes32 sibling = proof[index];
            computed = computed < sibling
                ? keccak256(abi.encodePacked(computed, sibling))
                : keccak256(abi.encodePacked(sibling, computed));
        }
        return computed == root;
    }
}
