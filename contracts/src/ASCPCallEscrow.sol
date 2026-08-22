// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {ServiceDirectory} from "./ServiceDirectory.sol";

interface IServiceDirectory {
    function currentVersion() external view returns (uint64);
    function verifySeller(uint64 version, ServiceDirectory.SellerLeaf calldata leaf, bytes32[] calldata proof)
        external
        view
        returns (bool);
    function verifyResource(uint64 version, ServiceDirectory.ResourceLeaf calldata leaf, bytes32[] calldata proof)
        external
        view
        returns (bool);
    function pausedSeller(bytes32 sellerId) external view returns (bool);
    function quoteKeyRevoked(address key) external view returns (bool);
}

/// @title ASCPCallEscrow
/// @notice ASCP v4 immutable lock boundary. It intentionally implements only
///         lock storage; acknowledgement and verdict settlement are separate
///         lifecycle modules and cannot alter a stored commitment.
contract ASCPCallEscrow is ReentrancyGuard {
    using SafeERC20 for IERC20;

    bytes32 public constant EXECUTION_COMMITMENT_TYPEHASH = keccak256(
        "ExecutionCommitment(bytes32 orgDomain,bytes32 operationId,uint8 rail,uint16 schemeVersion,uint8 protection,address escrowContract,bytes32 purchaseSpecHash,bytes32 quoteHash,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 directoryVersion,bytes32 sellerId,bytes32 resourceId,address payTo,address ackAuthority,uint256 amount,uint256 chainId,address asset,uint64 quoteExpiresAt,uint64 acceptBy,uint64 deliverBy,uint64 settleBy)"
    );
    bytes32 public constant EIP712_DOMAIN_TYPEHASH =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");
    bytes32 private constant NAME_HASH = keccak256("ASCP");
    bytes32 private constant VERSION_HASH = keccak256("4");
    bytes32 public constant GOVERNANCE_PAYLOAD_DOMAIN = keccak256("ASCP_CALL_ESCROW_GOVERNANCE_V1");

    uint8 public constant RAIL_ESCROW = 1;
    uint16 public constant SCHEME_VERSION_V1 = 1;
    uint8 public constant PROTECTION_ESCROW = 1;
    uint64 public constant MIN_SETTLEMENT_MARGIN = 30 minutes;
    uint64 public constant MIN_ONCHAIN_VERIFICATION_BUFFER = 120 seconds;
    uint64 public constant VERIFIER_ACTIVATION_DELAY = 24 hours;
    uint64 public constant MAX_ATTESTATION_WINDOW = 15 minutes;
    uint8 public constant VERDICT_RELEASE = 1;
    uint8 public constant VERDICT_EARLY_REFUND = 2;
    bytes32 public constant VERDICT_ATTESTATION_TYPEHASH = keccak256(
        "VerdictAttestation(bytes32 callId,bytes32 commitmentHash,address escrowContract,uint64 verifierEpoch,bytes32 verificationSpecHash,bytes32 verifierSoftwareHash,bytes32 deliveryHash,uint64 deliveredAt,bytes32 evidenceHash,uint8 verdict,uint256 verdictNonce,uint64 issuedAt,uint64 validUntil)"
    );

    enum State {
        None,
        Locked,
        Acked,
        Released,
        Refunded
    }

    struct VerdictAttestation {
        bytes32 callId;
        bytes32 commitmentHash;
        address escrowContract;
        uint64 verifierEpoch;
        bytes32 verificationSpecHash;
        bytes32 verifierSoftwareHash;
        bytes32 deliveryHash;
        uint64 deliveredAt;
        bytes32 evidenceHash;
        uint8 verdict;
        uint256 verdictNonce;
        uint64 issuedAt;
        uint64 validUntil;
    }

    struct PendingVerifier {
        uint64 epoch;
        uint64 activatesAt;
    }

    struct ExecutionCommitment {
        bytes32 orgDomain;
        bytes32 operationId;
        uint8 rail;
        uint16 schemeVersion;
        uint8 protection;
        address escrowContract;
        bytes32 purchaseSpecHash;
        bytes32 quoteHash;
        bytes32 verificationSpecHash;
        uint64 declaredWorkTime;
        uint64 verificationBudgetSeconds;
        uint64 directoryVersion;
        bytes32 sellerId;
        bytes32 resourceId;
        address payTo;
        address ackAuthority;
        uint256 amount;
        uint256 chainId;
        address asset;
        uint64 quoteExpiresAt;
        uint64 acceptBy;
        uint64 deliverBy;
        uint64 settleBy;
    }

    struct Call {
        address buyer;
        address payTo;
        address ackAuthority;
        uint256 amount;
        uint64 acceptBy;
        uint64 deliverBy;
        uint64 settleBy;
        bytes32 operationId;
        bytes32 commitmentHash;
        bytes32 verificationSpecHash;
        State state;
    }

    IERC20 public immutable usdc;
    IServiceDirectory public immutable serviceDirectory;
    address public immutable safe;
    address public immutable governor;
    uint256 public totalLocked;
    mapping(bytes32 callId => Call call_) private _calls;
    mapping(address key => uint64 epoch) public activeVerifierEpoch;
    mapping(address key => PendingVerifier pending) public pendingVerifier;
    mapping(address key => bool revoked) public verifierRevoked;
    mapping(uint256 nonce => bool used) public usedVerdictNonces;
    bool public emergencyPaused;

    event CallLocked(
        bytes32 indexed callId,
        bytes32 indexed operationId,
        bytes32 indexed commitmentHash,
        address buyer,
        address payTo,
        uint256 amount,
        uint64 settleBy
    );
    event CallRefunded(bytes32 indexed callId, bytes32 indexed operationId, address indexed buyer, uint256 amount);
    event CallAcked(bytes32 indexed callId, bytes32 indexed operationId);
    event CallReleased(
        bytes32 indexed callId,
        bytes32 indexed operationId,
        bytes32 indexed commitmentHash,
        bytes32 deliveryHash,
        bytes32 evidenceHash,
        address payTo,
        uint256 amount
    );
    event VerifierAdded(address indexed key, uint64 epoch, uint64 activatesAt);
    event VerifierActivated(address indexed key, uint64 epoch);
    event VerifierRevoked(address indexed key, uint64 epoch);
    event EmergencyPauseSet();
    event GovernanceWorkflowBound(
        bytes32 indexed workflowId, bytes32 indexed workflowPayloadHash, bytes4 indexed functionSelector
    );

    error AssetNotContract(address asset);
    error DirectoryNotContract(address directory);
    error SafeNotContract(address safe);
    error GovernorNotContract(address governor);
    error NotGovernor(address caller);
    error NotAckAuthority(address caller);
    error WrongState(bytes32 callId, State have);
    error AckWindowClosed();
    error EmergencyPaused();
    error InvalidVerdict();
    error VerifierNotActive(address signer, uint64 epoch);
    error VerdictNonceUsed(uint256 nonce);
    error VerifierActivationPending(address key);
    error InvalidVerifier();
    error InvalidWorkflowBinding();
    error NotSafe(address caller);
    error InvalidCommitment();
    error ChainMismatch(uint256 expected, uint256 actual);
    error EscrowMismatch(address expected, address actual);
    error AlreadyLocked(bytes32 callId);
    error DirectoryProofInvalid();
    error DirectoryTermsMismatch();
    error SellerUnavailable();
    error InvalidDeadlines();
    error InsufficientDeliveryWindow();
    error InsufficientSettlementMargin();
    error InexactFunding(uint256 expected, uint256 received);
    error RefundNotAvailable(bytes32 callId, State state, uint64 settleBy, uint256 nowTs);

    constructor(IERC20 usdc_, IServiceDirectory serviceDirectory_, address safe_, address governor_) {
        if (address(usdc_).code.length == 0) revert AssetNotContract(address(usdc_));
        if (address(serviceDirectory_).code.length == 0) revert DirectoryNotContract(address(serviceDirectory_));
        if (safe_.code.length == 0) revert SafeNotContract(safe_);
        if (governor_.code.length == 0) revert GovernorNotContract(governor_);
        usdc = usdc_;
        serviceDirectory = serviceDirectory_;
        safe = safe_;
        governor = governor_;
    }

    /// @notice Locks the exact EIP-712 commitment. The caller is stored as the
    /// buyer, so later settlement code has no recipient-selection authority.
    function lockCall(
        ExecutionCommitment calldata c,
        ServiceDirectory.SellerLeaf calldata seller,
        ServiceDirectory.ResourceLeaf calldata resource,
        bytes32[] calldata sellerProof,
        bytes32[] calldata resourceProof
    ) external nonReentrant returns (bytes32 callId) {
        if (msg.sender != safe) revert NotSafe(msg.sender);
        if (c.chainId != block.chainid) revert ChainMismatch(block.chainid, c.chainId);
        if (c.escrowContract != address(this)) revert EscrowMismatch(address(this), c.escrowContract);
        if (
            c.rail != RAIL_ESCROW || c.schemeVersion != SCHEME_VERSION_V1 || c.protection != PROTECTION_ESCROW
                || c.orgDomain == bytes32(0) || c.operationId == bytes32(0) || c.purchaseSpecHash == bytes32(0)
                || c.quoteHash == bytes32(0) || c.verificationSpecHash == bytes32(0) || c.sellerId == bytes32(0)
                || c.resourceId == bytes32(0) || c.payTo == address(0) || c.ackAuthority == address(0) || c.amount == 0
                || c.asset != address(usdc) || c.directoryVersion == 0 || c.declaredWorkTime == 0
                || c.verificationBudgetSeconds == 0
        ) revert InvalidCommitment();
        // forge-lint: disable-next-line(block-timestamp)
        if (
            block.timestamp >= c.acceptBy || block.timestamp >= c.quoteExpiresAt || c.acceptBy >= c.deliverBy
                || c.deliverBy >= c.settleBy
        ) {
            revert InvalidDeadlines();
        }
        // forge-lint: disable-next-line(block-timestamp)
        if (
            c.deliverBy - block.timestamp
                < c.declaredWorkTime + _max(c.verificationBudgetSeconds, MIN_ONCHAIN_VERIFICATION_BUFFER)
        ) {
            revert InsufficientDeliveryWindow();
        }
        // forge-lint: disable-next-line(block-timestamp)
        if (c.settleBy - block.timestamp < MIN_SETTLEMENT_MARGIN) revert InsufficientSettlementMargin();
        if (c.directoryVersion != serviceDirectory.currentVersion()) revert DirectoryProofInvalid();
        if (!serviceDirectory.verifySeller(c.directoryVersion, seller, sellerProof)) revert DirectoryProofInvalid();
        if (!serviceDirectory.verifyResource(c.directoryVersion, resource, resourceProof)) {
            revert DirectoryProofInvalid();
        }
        if (
            seller.status != 1 || serviceDirectory.pausedSeller(c.sellerId)
                || serviceDirectory.quoteKeyRevoked(seller.quoteSigningKey)
        ) {
            revert SellerUnavailable();
        }
        if (
            seller.sellerId != c.sellerId || resource.sellerId != seller.sellerId || resource.resourceId != c.resourceId
                || seller.payoutAddress != c.payTo || seller.ackAuthority != c.ackAuthority
                || resource.price != c.amount || !resource.escrowSupported
                || resource.declaredWorkTime != c.declaredWorkTime
                || resource.verificationBudgetSeconds != c.verificationBudgetSeconds
                || resource.verificationSpecHash != c.verificationSpecHash
        ) revert DirectoryTermsMismatch();

        bytes32 commitmentHash = executionCommitmentDigest(c, address(this), block.chainid);
        return _storeAndFund(c, commitmentHash);
    }

    function executionCommitmentDigest(ExecutionCommitment calldata c, address escrow, uint256 domainChainId)
        public
        pure
        returns (bytes32)
    {
        if (c.chainId != domainChainId || c.escrowContract != escrow) revert InvalidCommitment();
        bytes32 structHash = keccak256(abi.encode(EXECUTION_COMMITMENT_TYPEHASH, c));
        bytes32 domainSeparator =
            keccak256(abi.encode(EIP712_DOMAIN_TYPEHASH, NAME_HASH, VERSION_HASH, domainChainId, escrow));
        return keccak256(abi.encodePacked("\x19\x01", domainSeparator, structHash));
    }

    function getCall(bytes32 callId) external view returns (Call memory) {
        return _calls[callId];
    }

    function addVerifier(address key, uint64 epoch, bytes32 workflowId, bytes32 workflowPayloadHash) external {
        if (msg.sender != governor) revert NotGovernor(msg.sender);
        PendingVerifier memory currentPending = pendingVerifier[key];
        if (
            key == address(0) || epoch == 0 || epoch <= activeVerifierEpoch[key]
                || (currentPending.epoch != 0 && epoch <= currentPending.epoch)
        ) revert InvalidVerifier();
        _requireGovernanceWorkflow(
            this.addVerifier.selector,
            keccak256(
                abi.encode(key, activeVerifierEpoch[key], currentPending.epoch, currentPending.activatesAt, epoch)
            ),
            workflowId,
            workflowPayloadHash
        );
        pendingVerifier[key] = PendingVerifier(epoch, uint64(block.timestamp) + VERIFIER_ACTIVATION_DELAY);
        verifierRevoked[key] = false;
        emit VerifierAdded(key, epoch, uint64(block.timestamp) + VERIFIER_ACTIVATION_DELAY);
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.addVerifier.selector);
    }

    function activateVerifier(address key) external {
        PendingVerifier memory p = pendingVerifier[key];
        if (p.epoch == 0 || block.timestamp < p.activatesAt) revert VerifierActivationPending(key);
        activeVerifierEpoch[key] = p.epoch;
        delete pendingVerifier[key];
        emit VerifierActivated(key, p.epoch);
    }

    function revokeVerifier(address key, bytes32 workflowId, bytes32 workflowPayloadHash) external {
        if (msg.sender != governor) revert NotGovernor(msg.sender);
        uint64 epoch = activeVerifierEpoch[key];
        if (key == address(0) || epoch == 0 || verifierRevoked[key]) revert InvalidVerifier();
        _requireGovernanceWorkflow(
            this.revokeVerifier.selector,
            keccak256(abi.encode(key, epoch, false, true)),
            workflowId,
            workflowPayloadHash
        );
        verifierRevoked[key] = true;
        emit VerifierRevoked(key, epoch);
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.revokeVerifier.selector);
    }

    function setEmergencyPause(bytes32 workflowId, bytes32 workflowPayloadHash) external {
        if (msg.sender != governor) revert NotGovernor(msg.sender);
        if (emergencyPaused) revert EmergencyPaused();
        _requireGovernanceWorkflow(
            this.setEmergencyPause.selector, keccak256(abi.encode(false, true)), workflowId, workflowPayloadHash
        );
        emergencyPaused = true;
        emit EmergencyPauseSet();
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.setEmergencyPause.selector);
    }

    function governancePayloadHash(bytes4 functionSelector, bytes32 argumentsHash) public view returns (bytes32) {
        return
            keccak256(
                abi.encode(GOVERNANCE_PAYLOAD_DOMAIN, block.chainid, address(this), functionSelector, argumentsHash)
            );
    }

    function _requireGovernanceWorkflow(
        bytes4 functionSelector,
        bytes32 argumentsHash,
        bytes32 workflowId,
        bytes32 workflowPayloadHash
    ) private view {
        if (
            workflowId == bytes32(0) || workflowPayloadHash == bytes32(0)
                || workflowPayloadHash != governancePayloadHash(functionSelector, argumentsHash)
        ) revert InvalidWorkflowBinding();
    }

    function ack(bytes32 callId) external nonReentrant {
        Call storage call_ = _calls[callId];
        if (call_.state != State.Locked) revert WrongState(callId, call_.state);
        if (msg.sender != call_.ackAuthority) revert NotAckAuthority(msg.sender);
        if (block.timestamp > call_.acceptBy) revert AckWindowClosed();
        call_.state = State.Acked;
        emit CallAcked(callId, call_.operationId);
    }

    function release(bytes32 callId, VerdictAttestation calldata a, bytes calldata signature) external nonReentrant {
        Call storage call_ = _consumeVerdict(callId, a, signature, VERDICT_RELEASE);
        call_.state = State.Released;
        totalLocked -= call_.amount;
        usdc.safeTransfer(call_.payTo, call_.amount);
        emit CallReleased(
            callId, call_.operationId, call_.commitmentHash, a.deliveryHash, a.evidenceHash, call_.payTo, call_.amount
        );
    }

    function refundWithVerdict(bytes32 callId, VerdictAttestation calldata a, bytes calldata signature)
        external
        nonReentrant
    {
        Call storage call_ = _consumeVerdict(callId, a, signature, VERDICT_EARLY_REFUND);
        call_.state = State.Refunded;
        totalLocked -= call_.amount;
        usdc.safeTransfer(call_.buyer, call_.amount);
        emit CallRefunded(callId, call_.operationId, call_.buyer, call_.amount);
    }

    function verdictAttestationDigest(VerdictAttestation calldata a) public view returns (bytes32) {
        return keccak256(
            abi.encodePacked(
                "\x19\x01",
                keccak256(abi.encode(EIP712_DOMAIN_TYPEHASH, NAME_HASH, VERSION_HASH, block.chainid, address(this))),
                keccak256(abi.encode(VERDICT_ATTESTATION_TYPEHASH, a))
            )
        );
    }

    function _consumeVerdict(bytes32 callId, VerdictAttestation calldata a, bytes calldata signature, uint8 verdict)
        private
        returns (Call storage call_)
    {
        if (emergencyPaused) revert EmergencyPaused();
        call_ = _calls[callId];
        if (call_.state != State.Locked && call_.state != State.Acked) revert WrongState(callId, call_.state);
        if (
            a.verdict != verdict || a.callId != callId || a.commitmentHash != call_.commitmentHash
                || a.escrowContract != address(this) || a.verificationSpecHash != call_.verificationSpecHash
                || a.verifierSoftwareHash == bytes32(0) || a.deliveryHash == bytes32(0) || a.evidenceHash == bytes32(0)
                || a.deliveredAt == 0 || a.deliveredAt > call_.deliverBy || a.deliveredAt > a.issuedAt
                || a.issuedAt > block.timestamp || a.validUntil <= a.issuedAt
                || a.validUntil - a.issuedAt > MAX_ATTESTATION_WINDOW || a.validUntil > call_.settleBy
                || block.timestamp > a.validUntil
        ) revert InvalidVerdict();
        if (usedVerdictNonces[a.verdictNonce]) revert VerdictNonceUsed(a.verdictNonce);
        address signer = ECDSA.recover(verdictAttestationDigest(a), signature);
        if (verifierRevoked[signer] || activeVerifierEpoch[signer] != a.verifierEpoch || a.verifierEpoch == 0) {
            revert VerifierNotActive(signer, a.verifierEpoch);
        }
        usedVerdictNonces[a.verdictNonce] = true;
    }

    /// @notice The non-bypassable recovery path for a lock that has not yet
    /// received a verdict settlement. It pays only the snapshotted buyer.
    function claimExpired(bytes32 callId) external nonReentrant {
        Call storage call_ = _calls[callId];
        // forge-lint: disable-next-line(block-timestamp)
        if ((call_.state != State.Locked && call_.state != State.Acked) || block.timestamp <= call_.settleBy) {
            revert RefundNotAvailable(callId, call_.state, call_.settleBy, block.timestamp);
        }
        call_.state = State.Refunded;
        totalLocked -= call_.amount;
        usdc.safeTransfer(call_.buyer, call_.amount);
        emit CallRefunded(callId, call_.operationId, call_.buyer, call_.amount);
    }

    function _storeAndFund(ExecutionCommitment calldata c, bytes32 commitmentHash) private returns (bytes32 callId) {
        callId = keccak256(abi.encodePacked(commitmentHash));
        if (_calls[callId].state != State.None) revert AlreadyLocked(callId);
        _calls[callId] = Call({
            buyer: msg.sender,
            payTo: c.payTo,
            ackAuthority: c.ackAuthority,
            amount: c.amount,
            acceptBy: c.acceptBy,
            deliverBy: c.deliverBy,
            settleBy: c.settleBy,
            operationId: c.operationId,
            commitmentHash: commitmentHash,
            verificationSpecHash: c.verificationSpecHash,
            state: State.Locked
        });
        totalLocked += c.amount;
        uint256 beforeBalance = usdc.balanceOf(address(this));
        usdc.safeTransferFrom(msg.sender, address(this), c.amount);
        uint256 received = usdc.balanceOf(address(this)) - beforeBalance;
        if (received != c.amount) revert InexactFunding(c.amount, received);
        emit CallLocked(callId, c.operationId, commitmentHash, msg.sender, c.payTo, c.amount, c.settleBy);
    }

    function _max(uint64 left, uint64 right) private pure returns (uint64) {
        return left > right ? left : right;
    }
}
