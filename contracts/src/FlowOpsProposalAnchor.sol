// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

/// @title FlowOpsProposalAnchor
/// @notice Permanent, read-only evidence that a specific FlowOps proposal and
///         source revision were anchored on Base mainnet.
/// @dev This is not a factory, vault, escrow, proxy, or production release. It
///      has no payable entry point, token operation, administrative role,
///      upgrade path, arbitrary call, or contract-creation function. Anyone can
///      still transfer an ERC-20 directly to any address; users must not send
///      ETH or tokens to this evidence-only address because they cannot be
///      recovered through this contract.
contract FlowOpsProposalAnchor {
    uint256 public constant BASE_MAINNET_CHAIN_ID = 8_453;
    bytes32 public constant KIND = keccak256("FLOWOPS_PROPOSAL_ANCHOR_V1");
    string public constant DEPLOYMENT_STATUS = "EXPERIMENTAL_UNAUDITED_NO_FUNDS";

    bytes32 public immutable proposalDigest;
    bytes20 public immutable sourceCommit;
    address public immutable deployer;

    event ProposalAnchored(address indexed deployer, bytes32 indexed proposalDigest, bytes20 sourceCommit);

    error WrongChain(uint256 expected, uint256 actual);
    error ProposalDigestZero();
    error SourceCommitZero();

    constructor(bytes32 proposalDigest_, bytes20 sourceCommit_) {
        if (block.chainid != BASE_MAINNET_CHAIN_ID) {
            revert WrongChain(BASE_MAINNET_CHAIN_ID, block.chainid);
        }
        if (proposalDigest_ == bytes32(0)) revert ProposalDigestZero();
        if (sourceCommit_ == bytes20(0)) revert SourceCommitZero();

        proposalDigest = proposalDigest_;
        sourceCommit = sourceCommit_;
        deployer = msg.sender;

        emit ProposalAnchored(msg.sender, proposalDigest_, sourceCommit_);
    }

    function productionReady() external pure returns (bool) {
        return false;
    }

    function acceptsFunds() external pure returns (bool) {
        return false;
    }

    function vaultCreationEnabled() external pure returns (bool) {
        return false;
    }
}
