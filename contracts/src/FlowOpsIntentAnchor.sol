// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

/// @title FlowOpsIntentAnchor
/// @notice A non-custodial Base mainnet registry for immutable FlowOps intent
///         and policy digests.
/// @dev Each record is scoped to msg.sender so another account cannot reserve
///      or front-run a controller's digest. The contract has no token, payment,
///      arbitrary-call, administrative, upgrade, or recovery surface. Users
///      must not send ETH or tokens to this address.
contract FlowOpsIntentAnchor {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;
    uint64 public constant MAX_INTENT_LIFETIME = 30 days;
    bytes32 public constant KIND = keccak256("FLOWOPS_INTENT_ANCHOR_V1");
    string public constant DEPLOYMENT_STATUS = "LIMITED_MAINNET_INTENT_EVIDENCE_NO_FUNDS";

    struct IntentRecord {
        bytes32 policyDigest;
        uint64 anchoredAt;
        uint64 expiresAt;
    }

    mapping(address controller => mapping(bytes32 intentDigest => IntentRecord record)) private _intents;

    event IntentAnchored(
        address indexed controller,
        bytes32 indexed intentDigest,
        bytes32 indexed policyDigest,
        uint64 anchoredAt,
        uint64 expiresAt
    );

    error WrongChain(uint256 expected, uint256 actual);
    error IntentDigestZero();
    error PolicyDigestZero();
    error IntentAlreadyAnchored(address controller, bytes32 intentDigest);
    error IntentExpired(uint64 expiresAt, uint64 currentTime);
    error IntentLifetimeTooLong(uint64 maximum, uint64 requested);

    constructor() {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }
    }

    /// @notice Permanently binds an exact intent digest to its policy digest
    ///         for the calling controller.
    function anchorIntent(bytes32 intentDigest, bytes32 policyDigest, uint64 expiresAt) external {
        if (intentDigest == bytes32(0)) revert IntentDigestZero();
        if (policyDigest == bytes32(0)) revert PolicyDigestZero();

        uint64 currentTime = uint64(block.timestamp);
        if (expiresAt <= currentTime) revert IntentExpired(expiresAt, currentTime);

        uint64 requestedLifetime = expiresAt - currentTime;
        if (requestedLifetime > MAX_INTENT_LIFETIME) {
            revert IntentLifetimeTooLong(MAX_INTENT_LIFETIME, requestedLifetime);
        }

        IntentRecord storage record = _intents[msg.sender][intentDigest];
        if (record.anchoredAt != 0) revert IntentAlreadyAnchored(msg.sender, intentDigest);

        record.policyDigest = policyDigest;
        record.anchoredAt = currentTime;
        record.expiresAt = expiresAt;

        emit IntentAnchored(msg.sender, intentDigest, policyDigest, currentTime, expiresAt);
    }

    /// @notice Returns the immutable record and whether it is currently active.
    function getIntent(address controller, bytes32 intentDigest)
        external
        view
        returns (bytes32 policyDigest, uint64 anchoredAt, uint64 expiresAt, bool active)
    {
        IntentRecord memory record = _intents[controller][intentDigest];
        return (
            record.policyDigest,
            record.anchoredAt,
            record.expiresAt,
            record.anchoredAt != 0 && block.timestamp < record.expiresAt
        );
    }

    function acceptsFunds() external pure returns (bool) {
        return false;
    }

    function executesPayments() external pure returns (bool) {
        return false;
    }
}
