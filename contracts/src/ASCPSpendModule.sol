// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {ASCPCallEscrow} from "./ASCPCallEscrow.sol";
import {ServiceDirectory} from "./ServiceDirectory.sol";

interface IModuleSafe {
    function execTransactionFromModule(address to, uint256 value, bytes memory data, uint8 operation)
        external
        returns (bool success);
}

/// @title ASCPSpendModule
/// @notice Non-upgradeable, single-Safe execution boundary for exact ASCP lock
///         and bounded USDC allowance authorizations.
contract ASCPSpendModule is EIP712, ReentrancyGuard {
    using ECDSA for bytes32;

    uint64 public constant MAX_AUTHORIZATION_WINDOW = 10 minutes;
    uint64 public constant CAP_ACTIVATION_DELAY = 1 hours;
    uint256 public constant MAX_GOVERNANCE_NONCE_INVALIDATIONS = 100;
    uint8 private constant SAFE_OPERATION_CALL = 0;
    bytes32 public constant GOVERNANCE_PAYLOAD_DOMAIN = keccak256("ASCP_SPEND_MODULE_GOVERNANCE_V1");

    bytes32 public constant LOCK_AUTHORIZATION_TYPEHASH = keccak256(
        "LockAuthorization(bytes32 orgDomain,address safe,bytes32 operationId,bytes32 commitmentHash,bytes32 calldataHash,address escrow,uint256 amount,uint64 validAfter,uint64 validBefore,bytes32 nonce,uint64 leadershipEpoch,uint64 authorizerEpoch)"
    );
    bytes32 public constant ALLOWANCE_AUTHORIZATION_TYPEHASH = keccak256(
        "AllowanceAuthorization(bytes32 orgDomain,address safe,bytes32 adminOperationId,address token,address spender,uint256 expectedCurrentAllowance,uint256 newAllowance,uint64 validAfter,uint64 validBefore,bytes32 nonce,uint64 authorizerEpoch)"
    );

    struct LockAuthorization {
        bytes32 orgDomain;
        address safe;
        bytes32 operationId;
        bytes32 commitmentHash;
        bytes32 calldataHash;
        address escrow;
        uint256 amount;
        uint64 validAfter;
        uint64 validBefore;
        bytes32 nonce;
        uint64 leadershipEpoch;
        uint64 authorizerEpoch;
    }

    struct AllowanceAuthorization {
        bytes32 orgDomain;
        address safe;
        bytes32 adminOperationId;
        address token;
        address spender;
        uint256 expectedCurrentAllowance;
        uint256 newAllowance;
        uint64 validAfter;
        uint64 validBefore;
        bytes32 nonce;
        uint64 authorizerEpoch;
    }

    struct Caps {
        uint256 perTransaction;
        uint256 perDay;
        uint256 allowanceCeiling;
    }

    struct PendingCaps {
        Caps values;
        uint64 activatesAt;
    }

    address public immutable safe;
    IERC20 public immutable token;
    address public spendAuthorizer;
    uint64 public authorizerEpoch;
    bool public emergencyPaused;
    Caps public caps;
    PendingCaps public pendingCaps;
    uint256 public executedPrincipal;

    mapping(address escrow => bytes32 runtimeCodeHash) public escrowAllowlist;
    mapping(bytes32 nonce => bool consumed) public usedNonces;
    mapping(uint256 utcDay => uint256 amount) public dayExecutedPrincipal;

    event LockExecuted(
        bytes32 indexed operationId,
        bytes32 indexed nonce,
        address indexed escrow,
        bytes32 commitmentHash,
        uint256 amount,
        uint256 utcDay
    );
    event AllowanceExecuted(
        bytes32 indexed adminOperationId,
        bytes32 indexed nonce,
        address indexed spender,
        uint256 previousAllowance,
        uint256 newAllowance
    );
    event SpendAuthorizerSet(address indexed authorizer, uint64 indexed authorizerEpoch);
    event EscrowAllowlistSet(address indexed escrow, bytes32 indexed runtimeCodeHash);
    event CapsScheduled(uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling, uint64 activatesAt);
    event CapsActivated(uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling);
    event EmergencyPauseSet(bool paused);
    event NonceInvalidated(bytes32 indexed nonce);
    event GovernanceWorkflowBound(
        bytes32 indexed workflowId, bytes32 indexed workflowPayloadHash, bytes4 indexed functionSelector
    );

    error NotSafe(address caller);
    error InvalidContract(address account);
    error InvalidAuthorizer();
    error EmergencyPaused();
    error InvalidAuthorization();
    error AuthorizationWindowInvalid();
    error NonceAlreadyUsed(bytes32 nonce);
    error InvalidSignature(address recovered);
    error CalldataMismatch();
    error EscrowNotAllowlisted(address escrow);
    error EscrowCodeHashMismatch(address escrow, bytes32 expected, bytes32 actual);
    error InvalidLockPayload();
    error InvalidAllowancePayload();
    error AllowanceMismatch(uint256 expected, uint256 actual);
    error PerTransactionCapExceeded(uint256 amount, uint256 limit);
    error DailyCapExceeded(uint256 attempted, uint256 limit);
    error AllowanceCeilingExceeded(uint256 amount, uint256 limit);
    error SafeExecutionFailed();
    error InvalidCaps();
    error CapsNotReady(uint64 activatesAt);
    error CapsAlreadyPending(uint64 activatesAt);
    error CapsUnchanged();
    error EscrowAllowlistUnchanged(address escrow, bytes32 runtimeCodeHash);
    error InvalidNonceInvalidationCount(uint256 count);
    error InvalidWorkflowBinding();
    error PauseUnchanged(bool paused);

    modifier onlySafe() {
        if (msg.sender != safe) revert NotSafe(msg.sender);
        _;
    }

    constructor(address safe_, IERC20 token_, address spendAuthorizer_, Caps memory initialCaps) EIP712("ASCP", "3") {
        if (safe_.code.length == 0) revert InvalidContract(safe_);
        if (address(token_).code.length == 0) revert InvalidContract(address(token_));
        if (spendAuthorizer_ == address(0)) revert InvalidAuthorizer();
        _validateCaps(initialCaps);
        safe = safe_;
        token = token_;
        spendAuthorizer = spendAuthorizer_;
        authorizerEpoch = 1;
        caps = initialCaps;
        emit SpendAuthorizerSet(spendAuthorizer_, 1);
        emit CapsActivated(initialCaps.perTransaction, initialCaps.perDay, initialCaps.allowanceCeiling);
    }

    function executeLock(bytes calldata payload, LockAuthorization calldata authorization, bytes calldata signature)
        external
        nonReentrant
    {
        _requireActiveAuthorization(
            authorization.safe,
            authorization.orgDomain,
            authorization.operationId,
            authorization.nonce,
            authorization.validAfter,
            authorization.validBefore,
            authorization.authorizerEpoch
        );
        if (keccak256(payload) != authorization.calldataHash) revert CalldataMismatch();
        _requireAllowlisted(authorization.escrow);
        _validateLockPayload(payload, authorization);
        address recovered = lockAuthorizationDigest(authorization).recover(signature);
        if (recovered != spendAuthorizer) revert InvalidSignature(recovered);

        Caps memory activeCaps = caps;
        if (authorization.amount > activeCaps.perTransaction) {
            revert PerTransactionCapExceeded(authorization.amount, activeCaps.perTransaction);
        }
        uint256 utcDay = block.timestamp / 1 days;
        uint256 nextDayTotal = dayExecutedPrincipal[utcDay] + authorization.amount;
        if (nextDayTotal > activeCaps.perDay) revert DailyCapExceeded(nextDayTotal, activeCaps.perDay);

        usedNonces[authorization.nonce] = true;
        dayExecutedPrincipal[utcDay] = nextDayTotal;
        executedPrincipal += authorization.amount;
        bool success =
            IModuleSafe(safe).execTransactionFromModule(authorization.escrow, 0, payload, SAFE_OPERATION_CALL);
        if (!success) revert SafeExecutionFailed();
        _emitLockExecuted(authorization, utcDay);
    }

    function executeAllowance(
        bytes calldata payload,
        AllowanceAuthorization calldata authorization,
        bytes calldata signature
    ) external nonReentrant {
        _requireActiveAuthorization(
            authorization.safe,
            authorization.orgDomain,
            authorization.adminOperationId,
            authorization.nonce,
            authorization.validAfter,
            authorization.validBefore,
            authorization.authorizerEpoch
        );
        if (authorization.token != address(token)) revert InvalidAuthorization();
        if (
            keccak256(payload)
                != keccak256(abi.encodeCall(IERC20.approve, (authorization.spender, authorization.newAllowance)))
        ) {
            revert InvalidAllowancePayload();
        }
        _requireAllowlisted(authorization.spender);
        address recovered = allowanceAuthorizationDigest(authorization).recover(signature);
        if (recovered != spendAuthorizer) revert InvalidSignature(recovered);
        uint256 currentAllowance = token.allowance(safe, authorization.spender);
        if (currentAllowance != authorization.expectedCurrentAllowance) {
            revert AllowanceMismatch(authorization.expectedCurrentAllowance, currentAllowance);
        }
        if (authorization.newAllowance > caps.allowanceCeiling) {
            revert AllowanceCeilingExceeded(authorization.newAllowance, caps.allowanceCeiling);
        }

        usedNonces[authorization.nonce] = true;
        bool success = IModuleSafe(safe).execTransactionFromModule(address(token), 0, payload, SAFE_OPERATION_CALL);
        if (!success) revert SafeExecutionFailed();
        emit AllowanceExecuted(
            authorization.adminOperationId,
            authorization.nonce,
            authorization.spender,
            currentAllowance,
            authorization.newAllowance
        );
    }

    function lockAuthorizationDigest(LockAuthorization calldata authorization) public view returns (bytes32) {
        return _hashTypedDataV4(
            keccak256(
                abi.encode(
                    LOCK_AUTHORIZATION_TYPEHASH,
                    authorization.orgDomain,
                    authorization.safe,
                    authorization.operationId,
                    authorization.commitmentHash,
                    authorization.calldataHash,
                    authorization.escrow,
                    authorization.amount,
                    authorization.validAfter,
                    authorization.validBefore,
                    authorization.nonce,
                    authorization.leadershipEpoch,
                    authorization.authorizerEpoch
                )
            )
        );
    }

    function allowanceAuthorizationDigest(AllowanceAuthorization calldata authorization) public view returns (bytes32) {
        return _hashTypedDataV4(
            keccak256(
                abi.encode(
                    ALLOWANCE_AUTHORIZATION_TYPEHASH,
                    authorization.orgDomain,
                    authorization.safe,
                    authorization.adminOperationId,
                    authorization.token,
                    authorization.spender,
                    authorization.expectedCurrentAllowance,
                    authorization.newAllowance,
                    authorization.validAfter,
                    authorization.validBefore,
                    authorization.nonce,
                    authorization.authorizerEpoch
                )
            )
        );
    }

    function setSpendAuthorizer(address newAuthorizer, bytes32 workflowId, bytes32 workflowPayloadHash)
        external
        onlySafe
    {
        if (newAuthorizer == address(0)) revert InvalidAuthorizer();
        _requireGovernanceWorkflow(
            this.setSpendAuthorizer.selector,
            keccak256(abi.encode(spendAuthorizer, authorizerEpoch, newAuthorizer)),
            workflowId,
            workflowPayloadHash
        );
        spendAuthorizer = newAuthorizer;
        authorizerEpoch += 1;
        emit SpendAuthorizerSet(newAuthorizer, authorizerEpoch);
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.setSpendAuthorizer.selector);
    }

    function setEscrowAllowlist(
        address escrow,
        bytes32 runtimeCodeHash,
        bytes32 workflowId,
        bytes32 workflowPayloadHash
    ) external onlySafe {
        bytes32 currentCodeHash = escrowAllowlist[escrow];
        if (currentCodeHash == runtimeCodeHash) revert EscrowAllowlistUnchanged(escrow, runtimeCodeHash);
        _requireGovernanceWorkflow(
            this.setEscrowAllowlist.selector,
            keccak256(abi.encode(escrow, currentCodeHash, runtimeCodeHash)),
            workflowId,
            workflowPayloadHash
        );
        if (runtimeCodeHash != bytes32(0)) {
            if (escrow.code.length == 0) revert InvalidContract(escrow);
            bytes32 actual = escrow.codehash;
            if (actual != runtimeCodeHash) revert EscrowCodeHashMismatch(escrow, runtimeCodeHash, actual);
        }
        escrowAllowlist[escrow] = runtimeCodeHash;
        emit EscrowAllowlistSet(escrow, runtimeCodeHash);
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.setEscrowAllowlist.selector);
    }

    function scheduleCaps(Caps calldata newCaps, bytes32 workflowId, bytes32 workflowPayloadHash) external onlySafe {
        _validateCaps(newCaps);
        if (pendingCaps.activatesAt != 0) revert CapsAlreadyPending(pendingCaps.activatesAt);
        if (
            newCaps.perTransaction == caps.perTransaction && newCaps.perDay == caps.perDay
                && newCaps.allowanceCeiling == caps.allowanceCeiling
        ) revert CapsUnchanged();
        _requireGovernanceWorkflow(
            this.scheduleCaps.selector,
            keccak256(
                abi.encode(
                    caps.perTransaction,
                    caps.perDay,
                    caps.allowanceCeiling,
                    newCaps.perTransaction,
                    newCaps.perDay,
                    newCaps.allowanceCeiling
                )
            ),
            workflowId,
            workflowPayloadHash
        );
        uint64 activatesAt = uint64(block.timestamp) + CAP_ACTIVATION_DELAY;
        pendingCaps = PendingCaps(newCaps, activatesAt);
        emit CapsScheduled(newCaps.perTransaction, newCaps.perDay, newCaps.allowanceCeiling, activatesAt);
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.scheduleCaps.selector);
    }

    function activateCaps() external {
        PendingCaps memory pending = pendingCaps;
        if (pending.activatesAt == 0 || block.timestamp < pending.activatesAt) {
            revert CapsNotReady(pending.activatesAt);
        }
        caps = pending.values;
        delete pendingCaps;
        emit CapsActivated(pending.values.perTransaction, pending.values.perDay, pending.values.allowanceCeiling);
    }

    function setEmergencyPause(bool paused, bytes32 workflowId, bytes32 workflowPayloadHash) external onlySafe {
        if (emergencyPaused == paused) revert PauseUnchanged(paused);
        _requireGovernanceWorkflow(
            this.setEmergencyPause.selector,
            keccak256(abi.encode(emergencyPaused, paused)),
            workflowId,
            workflowPayloadHash
        );
        emergencyPaused = paused;
        emit EmergencyPauseSet(paused);
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.setEmergencyPause.selector);
    }

    function invalidateNonces(bytes32[] calldata nonces, bytes32 workflowId, bytes32 workflowPayloadHash)
        external
        onlySafe
    {
        uint256 length = nonces.length;
        if (length == 0 || length > MAX_GOVERNANCE_NONCE_INVALIDATIONS) {
            revert InvalidNonceInvalidationCount(length);
        }
        _requireGovernanceWorkflow(
            this.invalidateNonces.selector, keccak256(abi.encode(nonces)), workflowId, workflowPayloadHash
        );
        for (uint256 i; i < length; ++i) {
            bytes32 nonce = nonces[i];
            if (nonce == bytes32(0) || usedNonces[nonce]) revert NonceAlreadyUsed(nonce);
            usedNonces[nonce] = true;
            emit NonceInvalidated(nonce);
        }
        emit GovernanceWorkflowBound(workflowId, workflowPayloadHash, this.invalidateNonces.selector);
    }

    function governancePayloadHash(bytes32 workflowId, bytes4 functionSelector, bytes32 argumentsHash)
        public
        view
        returns (bytes32)
    {
        return keccak256(
            abi.encode(
                GOVERNANCE_PAYLOAD_DOMAIN, block.chainid, address(this), workflowId, functionSelector, argumentsHash
            )
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
                || workflowPayloadHash != governancePayloadHash(workflowId, functionSelector, argumentsHash)
        ) revert InvalidWorkflowBinding();
    }

    function _requireActiveAuthorization(
        address authorizedSafe,
        bytes32 orgDomain,
        bytes32 actionId,
        bytes32 nonce,
        uint64 validAfter,
        uint64 validBefore,
        uint64 signedAuthorizerEpoch
    ) private view {
        if (emergencyPaused) revert EmergencyPaused();
        if (
            authorizedSafe != safe || orgDomain == bytes32(0) || actionId == bytes32(0) || nonce == bytes32(0)
                || signedAuthorizerEpoch != authorizerEpoch
        ) revert InvalidAuthorization();
        if (
            validBefore <= validAfter || validBefore - validAfter > MAX_AUTHORIZATION_WINDOW
                || block.timestamp < validAfter || block.timestamp > validBefore
        ) revert AuthorizationWindowInvalid();
        if (usedNonces[nonce]) revert NonceAlreadyUsed(nonce);
    }

    function _requireAllowlisted(address escrow) private view {
        bytes32 expected = escrowAllowlist[escrow];
        if (expected == bytes32(0)) revert EscrowNotAllowlisted(escrow);
        bytes32 actual = escrow.codehash;
        if (escrow.code.length == 0 || actual != expected) {
            revert EscrowCodeHashMismatch(escrow, expected, actual);
        }
    }

    function _decodeCanonicalLock(bytes calldata payload)
        private
        pure
        returns (
            ASCPCallEscrow.ExecutionCommitment memory commitment,
            ServiceDirectory.SellerLeaf memory seller,
            ServiceDirectory.ResourceLeaf memory resource,
            bytes32[] memory sellerProof,
            bytes32[] memory resourceProof
        )
    {
        if (payload.length < 4 || bytes4(payload[:4]) != ASCPCallEscrow.lockCall.selector) {
            revert InvalidLockPayload();
        }
        (commitment, seller, resource, sellerProof, resourceProof) = abi.decode(
            payload[4:],
            (
                ASCPCallEscrow.ExecutionCommitment,
                ServiceDirectory.SellerLeaf,
                ServiceDirectory.ResourceLeaf,
                bytes32[],
                bytes32[]
            )
        );
        if (
            keccak256(payload)
                != keccak256(
                    abi.encodeWithSelector(
                        ASCPCallEscrow.lockCall.selector, commitment, seller, resource, sellerProof, resourceProof
                    )
                )
        ) revert InvalidLockPayload();
    }

    function _validateLockPayload(bytes calldata payload, LockAuthorization calldata authorization) private view {
        (
            ASCPCallEscrow.ExecutionCommitment memory commitment,
            ServiceDirectory.SellerLeaf memory seller,
            ServiceDirectory.ResourceLeaf memory resource,
            bytes32[] memory sellerProof,
            bytes32[] memory resourceProof
        ) = _decodeCanonicalLock(payload);
        // Canonical re-encoding in _decodeCanonicalLock binds every decoded
        // leaf and proof byte even though only the commitment is inspected here.
        seller;
        resource;
        sellerProof;
        resourceProof;
        if (
            commitment.operationId != authorization.operationId || commitment.escrowContract != authorization.escrow
                || commitment.asset != address(token) || commitment.amount != authorization.amount
        ) revert InvalidLockPayload();
        bytes32 commitmentHash = ASCPCallEscrow(authorization.escrow)
            .executionCommitmentDigest(commitment, authorization.escrow, block.chainid);
        if (commitmentHash != authorization.commitmentHash) revert InvalidLockPayload();
    }

    function _emitLockExecuted(LockAuthorization calldata authorization, uint256 utcDay) private {
        emit LockExecuted(
            authorization.operationId,
            authorization.nonce,
            authorization.escrow,
            authorization.commitmentHash,
            authorization.amount,
            utcDay
        );
    }

    function _validateCaps(Caps memory values) private pure {
        if (
            values.perTransaction == 0 || values.perDay == 0 || values.perTransaction > values.perDay
                || values.allowanceCeiling == 0
        ) revert InvalidCaps();
    }
}
