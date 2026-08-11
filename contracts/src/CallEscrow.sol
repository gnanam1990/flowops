// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @title CallEscrow
/// @notice Non-upgradeable, task-bound escrow for providers that implement the
///         FlowOps acknowledgement and objective-delivery protocol.
/// @dev The contract deliberately has no owner, pause, fee, arbitrary payout,
///      dispute, or rescue role. It governs delivery lifecycle conditions, not
///      subjective output quality. UNAUDITED: Base mainnet use is prohibited
///      until the repository's external-review gate is complete.
contract CallEscrow is ReentrancyGuard {
    using SafeERC20 for IERC20;

    bytes32 public constant CALL_ID_DOMAIN = keccak256("FLOWOPS_CALL_ESCROW_V1");

    enum State {
        None,
        Funded,
        Acknowledged,
        Delivered,
        Released,
        Refunded
    }

    struct Call {
        address buyer;
        address provider;
        uint256 amount;
        uint64 acknowledgeBy;
        uint64 deliverBy;
        uint64 deliveredAt;
        bytes32 taskDigest;
        bytes32 requestDigest;
        bytes32 responseDigest;
        bytes32 evidenceDigest;
        State state;
    }

    uint256 public constant MAX_OPTIMISTIC_RELEASE_WINDOW = 30 days;

    IERC20 public immutable asset;
    uint256 public immutable optimisticReleaseWindow;
    uint256 public totalLocked;

    mapping(bytes32 callId => Call call_) private _calls;

    event CallFunded(
        bytes32 indexed callId,
        address indexed buyer,
        address indexed provider,
        uint256 amount,
        bytes32 taskDigest,
        bytes32 requestDigest,
        uint64 acknowledgeBy,
        uint64 deliverBy
    );
    event CallAcknowledged(bytes32 indexed callId, address indexed provider);
    event DeliverySubmitted(
        bytes32 indexed callId,
        address indexed provider,
        bytes32 responseDigest,
        bytes32 evidenceDigest,
        uint64 deliveredAt,
        uint256 releasableAt
    );
    event Released(bytes32 indexed callId, address indexed provider, uint256 amount, bool buyerAccepted);
    event Refunded(bytes32 indexed callId, address indexed buyer, uint256 amount, State expiredFrom);

    error AssetNotContract(address asset);
    error BadOptimisticReleaseWindow(uint256 window);
    error CallIdZero();
    error BadCallId(bytes32 expected, bytes32 actual);
    error CallExists(bytes32 callId);
    error ProviderZero();
    error BuyerIsProvider(address account);
    error AmountZero();
    error DigestZero();
    error BadDeadlines(uint64 acknowledgeBy, uint64 deliverBy, uint256 nowTs);
    error WrongState(bytes32 callId, State have, State want);
    error NotBuyer(bytes32 callId, address caller);
    error NotProvider(bytes32 callId, address caller);
    error AcknowledgementWindowClosed(bytes32 callId, uint64 acknowledgeBy, uint256 nowTs);
    error DeliveryWindowClosed(bytes32 callId, uint64 deliverBy, uint256 nowTs);
    error ReleaseWindowOpen(bytes32 callId, uint256 releasableAt, uint256 nowTs);
    error RefundNotAvailable(bytes32 callId, State state, uint256 eligibleAt, uint256 nowTs);
    error InexactFunding(uint256 expected, uint256 received);

    constructor(IERC20 asset_, uint256 optimisticReleaseWindow_) {
        if (address(asset_).code.length == 0) revert AssetNotContract(address(asset_));
        if (optimisticReleaseWindow_ == 0 || optimisticReleaseWindow_ > MAX_OPTIMISTIC_RELEASE_WINDOW) {
            revert BadOptimisticReleaseWindow(optimisticReleaseWindow_);
        }
        asset = asset_;
        optimisticReleaseWindow = optimisticReleaseWindow_;
    }

    /// @notice Locks one immutable call snapshot. The customer's signer must
    ///         authorize this exact calldata; no field can later be substituted.
    function fund(
        bytes32 callId,
        address provider,
        uint256 amount,
        bytes32 taskDigest,
        bytes32 requestDigest,
        uint64 acknowledgeBy,
        uint64 deliverBy
    ) external nonReentrant {
        if (callId == bytes32(0)) revert CallIdZero();
        if (provider == address(0)) revert ProviderZero();
        if (provider == msg.sender) revert BuyerIsProvider(msg.sender);
        if (amount == 0) revert AmountZero();
        if (taskDigest == bytes32(0) || requestDigest == bytes32(0)) revert DigestZero();
        // forge-lint: disable-next-line(block-timestamp)
        if (acknowledgeBy <= block.timestamp || deliverBy <= acknowledgeBy) {
            revert BadDeadlines(acknowledgeBy, deliverBy, block.timestamp);
        }
        bytes32 expectedCallId = deriveCallId(msg.sender, taskDigest, requestDigest);
        if (callId != expectedCallId) revert BadCallId(expectedCallId, callId);
        if (_calls[callId].state != State.None) revert CallExists(callId);

        _calls[callId] = Call({
            buyer: msg.sender,
            provider: provider,
            amount: amount,
            acknowledgeBy: acknowledgeBy,
            deliverBy: deliverBy,
            deliveredAt: 0,
            taskDigest: taskDigest,
            requestDigest: requestDigest,
            responseDigest: bytes32(0),
            evidenceDigest: bytes32(0),
            state: State.Funded
        });
        totalLocked += amount;

        uint256 balanceBefore = asset.balanceOf(address(this));
        asset.safeTransferFrom(msg.sender, address(this), amount);
        uint256 received = asset.balanceOf(address(this)) - balanceBefore;
        if (received != amount) revert InexactFunding(amount, received);

        emit CallFunded(callId, msg.sender, provider, amount, taskDigest, requestDigest, acknowledgeBy, deliverBy);
    }

    /// @notice Provider accepts the immutable call terms before the inclusive
    ///         acknowledgement deadline.
    function acknowledge(bytes32 callId) external nonReentrant {
        Call storage call_ = _calls[callId];
        _requireState(callId, call_, State.Funded);
        if (msg.sender != call_.provider) revert NotProvider(callId, msg.sender);
        // forge-lint: disable-next-line(block-timestamp)
        if (block.timestamp > call_.acknowledgeBy) {
            revert AcknowledgementWindowClosed(callId, call_.acknowledgeBy, block.timestamp);
        }
        call_.state = State.Acknowledged;
        emit CallAcknowledged(callId, msg.sender);
    }

    /// @notice Records objective response and delivery-evidence digests. Empty
    ///         or failed output must never be represented by non-zero fake data.
    function submitDelivery(bytes32 callId, bytes32 responseDigest, bytes32 evidenceDigest) external nonReentrant {
        Call storage call_ = _calls[callId];
        _requireState(callId, call_, State.Acknowledged);
        if (msg.sender != call_.provider) revert NotProvider(callId, msg.sender);
        // forge-lint: disable-next-line(block-timestamp)
        if (block.timestamp > call_.deliverBy) {
            revert DeliveryWindowClosed(callId, call_.deliverBy, block.timestamp);
        }
        if (responseDigest == bytes32(0) || evidenceDigest == bytes32(0)) revert DigestZero();

        call_.responseDigest = responseDigest;
        call_.evidenceDigest = evidenceDigest;
        call_.deliveredAt = uint64(block.timestamp);
        call_.state = State.Delivered;

        emit DeliverySubmitted(
            callId,
            msg.sender,
            responseDigest,
            evidenceDigest,
            call_.deliveredAt,
            block.timestamp + optimisticReleaseWindow
        );
    }

    /// @notice Buyer confirms objective receipt and releases immediately.
    function acceptDelivery(bytes32 callId) external nonReentrant {
        Call storage call_ = _calls[callId];
        _requireState(callId, call_, State.Delivered);
        if (msg.sender != call_.buyer) revert NotBuyer(callId, msg.sender);
        _release(callId, call_, true);
    }

    /// @notice Anyone may finalize a delivered call at or after the disclosed
    ///         optimistic deadline. Funds can only go to the snapshotted provider.
    function optimisticRelease(bytes32 callId) external nonReentrant {
        Call storage call_ = _calls[callId];
        _requireState(callId, call_, State.Delivered);
        uint256 releaseAt = uint256(call_.deliveredAt) + optimisticReleaseWindow;
        // forge-lint: disable-next-line(block-timestamp)
        if (block.timestamp < releaseAt) {
            revert ReleaseWindowOpen(callId, releaseAt, block.timestamp);
        }
        _release(callId, call_, false);
    }

    /// @notice Anyone may return funds to the original buyer after a missed
    ///         acknowledgement or delivery deadline. No caller chooses a recipient.
    function refundExpired(bytes32 callId) external nonReentrant {
        Call storage call_ = _calls[callId];
        State expiredFrom = call_.state;
        uint256 eligibleAt;
        if (expiredFrom == State.Funded) {
            eligibleAt = call_.acknowledgeBy;
        } else if (expiredFrom == State.Acknowledged) {
            eligibleAt = call_.deliverBy;
        } else {
            revert RefundNotAvailable(callId, expiredFrom, 0, block.timestamp);
        }
        // forge-lint: disable-next-line(block-timestamp)
        if (block.timestamp <= eligibleAt) {
            revert RefundNotAvailable(callId, expiredFrom, eligibleAt + 1, block.timestamp);
        }

        call_.state = State.Refunded;
        totalLocked -= call_.amount;
        asset.safeTransfer(call_.buyer, call_.amount);
        emit Refunded(callId, call_.buyer, call_.amount, expiredFrom);
    }

    function getCall(bytes32 callId) external view returns (Call memory) {
        return _calls[callId];
    }

    /// @notice A domain-separated identity prevents another buyer from
    ///         front-running and occupying a visible call ID, and prevents the
    ///         same ID from being replayed on another chain or contract version.
    function deriveCallId(address buyer, bytes32 taskDigest, bytes32 requestDigest) public view returns (bytes32) {
        return keccak256(abi.encode(CALL_ID_DOMAIN, block.chainid, address(this), buyer, taskDigest, requestDigest));
    }

    function stateOf(bytes32 callId) external view returns (State) {
        return _calls[callId].state;
    }

    function releasableAt(bytes32 callId) external view returns (uint256) {
        Call storage call_ = _calls[callId];
        if (call_.state != State.Delivered) return 0;
        return uint256(call_.deliveredAt) + optimisticReleaseWindow;
    }

    function _release(bytes32 callId, Call storage call_, bool buyerAccepted) private {
        call_.state = State.Released;
        totalLocked -= call_.amount;
        asset.safeTransfer(call_.provider, call_.amount);
        emit Released(callId, call_.provider, call_.amount, buyerAccepted);
    }

    function _requireState(bytes32 callId, Call storage call_, State want) private view {
        if (call_.state != want) revert WrongState(callId, call_.state, want);
    }
}
