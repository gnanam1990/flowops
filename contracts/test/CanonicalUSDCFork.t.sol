// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ASCPCallEscrow, IServiceDirectory} from "../src/ASCPCallEscrow.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";

interface ICanonicalUSDC is IERC20 {
    function pauser() external view returns (address);
    function blacklister() external view returns (address);
    function paused() external view returns (bool);
    function isBlacklisted(address account) external view returns (bool);
    function pause() external;
    function unpause() external;
    function blacklist(address account) external;
    function unBlacklist(address account) external;
}

contract CanonicalUSDCForkDirectory is IServiceDirectory {
    uint64 public constant VERSION = 9;

    function currentVersion() external pure returns (uint64) {
        return VERSION;
    }

    function verifySeller(uint64, ServiceDirectory.SellerLeaf calldata, bytes32[] calldata)
        external
        pure
        returns (bool)
    {
        return true;
    }

    function verifyResource(uint64, ServiceDirectory.ResourceLeaf calldata, bytes32[] calldata)
        external
        pure
        returns (bool)
    {
        return true;
    }

    function pausedSeller(bytes32) external pure returns (bool) {
        return false;
    }

    function quoteKeyRevoked(address) external pure returns (bool) {
        return false;
    }
}

contract CanonicalUSDCForkSafe {
    function approve(IERC20 token, address spender, uint256 amount) external {
        token.approve(spender, amount);
    }

    function lock(
        ASCPCallEscrow escrow,
        ASCPCallEscrow.ExecutionCommitment calldata commitment,
        ServiceDirectory.SellerLeaf calldata seller,
        ServiceDirectory.ResourceLeaf calldata resource
    ) external returns (bytes32) {
        return escrow.lockCall(commitment, seller, resource, new bytes32[](0), new bytes32[](0));
    }
}

contract CanonicalUSDCForkGovernor {
    function addVerifier(ASCPCallEscrow escrow, address verifier, uint64 epoch) external {
        bytes32 workflowId = keccak256(abi.encode("canonical-usdc-fork-verifier", verifier, epoch));
        bytes32 payloadHash = escrow.governancePayloadHash(
            workflowId,
            escrow.addVerifier.selector,
            keccak256(abi.encode(verifier, uint64(0), uint64(0), uint64(0), false, epoch))
        );
        escrow.addVerifier(verifier, epoch, workflowId, payloadHash);
    }
}

/// @notice Opt-in, pinned Base-mainnet fork evidence. The configured RPC is
/// read-only from this process; pause and blacklist calls execute only in the
/// local fork VM through role impersonation and are never broadcast.
contract CanonicalUSDCForkTest is Test {
    uint256 internal constant BASE_MAINNET_BLOCK = 50_482_467;
    ICanonicalUSDC internal constant USDC = ICanonicalUSDC(0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913);
    uint256 internal constant AMOUNT = 42;
    uint256 internal constant VERIFIER_KEY = 0xBEEF;
    uint64 internal constant VERIFIER_EPOCH = 7;

    bool internal forkEnabled;
    CanonicalUSDCForkDirectory internal directory;
    CanonicalUSDCForkSafe internal buyer;
    CanonicalUSDCForkGovernor internal governor;
    ASCPCallEscrow internal escrow;
    ServiceDirectory.SellerLeaf internal seller;
    ServiceDirectory.ResourceLeaf internal resource;

    modifier onFork() {
        if (!forkEnabled) {
            vm.skip(true, "set BASE_MAINNET_FORK_RPC_URL to run pinned canonical-USDC evidence");
        }
        _;
    }

    function setUp() public {
        string memory rpcURL = vm.envOr("BASE_MAINNET_FORK_RPC_URL", string(""));
        if (bytes(rpcURL).length == 0) return;
        vm.createSelectFork(rpcURL, BASE_MAINNET_BLOCK);
        forkEnabled = true;

        directory = new CanonicalUSDCForkDirectory();
        buyer = new CanonicalUSDCForkSafe();
        governor = new CanonicalUSDCForkGovernor();
        escrow = new ASCPCallEscrow(
            IERC20(address(USDC)), IServiceDirectory(address(directory)), address(buyer), address(governor)
        );
        seller = ServiceDirectory.SellerLeaf({
            sellerId: keccak256("fork-seller"),
            payoutAddress: makeAddr("fork-payout"),
            ackAuthority: makeAddr("fork-ack-authority"),
            quoteSigningKey: makeAddr("fork-quote-key"),
            keyEpoch: 1,
            baseURLOriginHash: keccak256("https://fork-seller.example"),
            status: 1
        });
        resource = ServiceDirectory.ResourceLeaf({
            sellerId: seller.sellerId,
            resourceId: keccak256("fork-resource"),
            price: AMOUNT,
            escrowSupported: true,
            verificationSpecHash: keccak256("fork-verification-spec"),
            declaredWorkTime: 300,
            verificationBudgetSeconds: 120
        });
        deal(address(USDC), address(buyer), 1_000_000, true);
        buyer.approve(IERC20(address(USDC)), address(escrow), type(uint256).max);
    }

    function testCanonicalUSDCPauseBlocksLockAtomicallyAndRecovers() public onFork {
        ASCPCallEscrow.ExecutionCommitment memory commitment = _commitment("pause-lock");
        bytes32 callId = _callId(commitment);
        uint256 buyerBefore = USDC.balanceOf(address(buyer));

        _pause();
        vm.expectRevert();
        buyer.lock(escrow, commitment, seller, resource);
        _assertUnfunded(callId, buyerBefore);

        _unpause();
        assertEq(buyer.lock(escrow, commitment, seller, resource), callId);
        _assertLocked(callId, buyerBefore);
    }

    function testCanonicalUSDCBuyerBlacklistBlocksLockAtomicallyAndRecovers() public onFork {
        ASCPCallEscrow.ExecutionCommitment memory commitment = _commitment("buyer-blacklist-lock");
        bytes32 callId = _callId(commitment);
        uint256 buyerBefore = USDC.balanceOf(address(buyer));

        _blacklist(address(buyer));
        vm.expectRevert();
        buyer.lock(escrow, commitment, seller, resource);
        _assertUnfunded(callId, buyerBefore);

        _unBlacklist(address(buyer));
        assertEq(buyer.lock(escrow, commitment, seller, resource), callId);
        _assertLocked(callId, buyerBefore);
    }

    function testCanonicalUSDCPayoutBlacklistRollsBackReleaseAndNonceUntilRecovery() public onFork {
        _activateVerifier();
        ASCPCallEscrow.ExecutionCommitment memory commitment = _commitment("payout-blacklist-release");
        bytes32 callId = buyer.lock(escrow, commitment, seller, resource);
        (ASCPCallEscrow.VerdictAttestation memory attestation, bytes memory signature) =
            _releaseAttestation(callId, commitment, 71);

        _blacklist(seller.payoutAddress);
        vm.expectRevert();
        escrow.release(callId, attestation, signature);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Locked));
        assertEq(escrow.totalLocked(), AMOUNT);
        assertEq(USDC.balanceOf(address(escrow)), AMOUNT);
        assertEq(USDC.balanceOf(seller.payoutAddress), 0);
        assertFalse(escrow.usedVerdictNonces(attestation.verdictNonce));

        _unBlacklist(seller.payoutAddress);
        escrow.release(callId, attestation, signature);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Released));
        assertEq(escrow.totalLocked(), 0);
        assertEq(USDC.balanceOf(address(escrow)), 0);
        assertEq(USDC.balanceOf(seller.payoutAddress), AMOUNT);
        assertTrue(escrow.usedVerdictNonces(attestation.verdictNonce));
    }

    function testCanonicalUSDCBuyerBlacklistRollsBackExpiredRefundUntilRecovery() public onFork {
        ASCPCallEscrow.ExecutionCommitment memory commitment = _commitment("buyer-blacklist-refund");
        uint256 buyerBefore = USDC.balanceOf(address(buyer));
        bytes32 callId = buyer.lock(escrow, commitment, seller, resource);
        vm.warp(commitment.settleBy + 1);

        _blacklist(address(buyer));
        vm.expectRevert();
        escrow.claimExpired(callId);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Locked));
        assertEq(escrow.totalLocked(), AMOUNT);
        assertEq(USDC.balanceOf(address(escrow)), AMOUNT);
        assertEq(USDC.balanceOf(address(buyer)), buyerBefore - AMOUNT);

        _unBlacklist(address(buyer));
        escrow.claimExpired(callId);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Refunded));
        assertEq(escrow.totalLocked(), 0);
        assertEq(USDC.balanceOf(address(escrow)), 0);
        assertEq(USDC.balanceOf(address(buyer)), buyerBefore);
    }

    function _commitment(string memory operation) internal view returns (ASCPCallEscrow.ExecutionCommitment memory) {
        return ASCPCallEscrow.ExecutionCommitment({
            orgDomain: keccak256("fork-org"),
            operationId: keccak256(bytes(operation)),
            rail: 1,
            schemeVersion: 1,
            protection: 1,
            escrowContract: address(escrow),
            purchaseSpecHash: keccak256("fork-purchase-spec"),
            quoteHash: keccak256(bytes(operation)),
            verificationSpecHash: resource.verificationSpecHash,
            declaredWorkTime: resource.declaredWorkTime,
            verificationBudgetSeconds: resource.verificationBudgetSeconds,
            directoryVersion: directory.VERSION(),
            sellerId: seller.sellerId,
            resourceId: resource.resourceId,
            payTo: seller.payoutAddress,
            ackAuthority: seller.ackAuthority,
            amount: AMOUNT,
            chainId: block.chainid,
            asset: address(USDC),
            quoteExpiresAt: uint64(block.timestamp + 10 minutes),
            acceptBy: uint64(block.timestamp + 5 minutes),
            deliverBy: uint64(block.timestamp + 20 minutes),
            settleBy: uint64(block.timestamp + 1 hours)
        });
    }

    function _callId(ASCPCallEscrow.ExecutionCommitment memory commitment) internal view returns (bytes32) {
        return keccak256(abi.encodePacked(escrow.executionCommitmentDigest(commitment, address(escrow), block.chainid)));
    }

    function _releaseAttestation(bytes32 callId, ASCPCallEscrow.ExecutionCommitment memory commitment, uint256 nonce)
        internal
        view
        returns (ASCPCallEscrow.VerdictAttestation memory attestation, bytes memory signature)
    {
        attestation = ASCPCallEscrow.VerdictAttestation({
            callId: callId,
            commitmentHash: escrow.executionCommitmentDigest(commitment, address(escrow), block.chainid),
            escrowContract: address(escrow),
            verifierEpoch: VERIFIER_EPOCH,
            verificationSpecHash: commitment.verificationSpecHash,
            verifierSoftwareHash: keccak256("fork-verifier-v1"),
            deliveryHash: keccak256("fork-delivery"),
            deliveredAt: uint64(block.timestamp),
            evidenceHash: keccak256("fork-evidence"),
            verdict: escrow.VERDICT_RELEASE(),
            verdictNonce: nonce,
            issuedAt: uint64(block.timestamp),
            validUntil: uint64(block.timestamp + 5 minutes)
        });
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(VERIFIER_KEY, escrow.verdictAttestationDigest(attestation));
        signature = abi.encodePacked(r, s, v);
    }

    function _activateVerifier() internal {
        address verifier = vm.addr(VERIFIER_KEY);
        governor.addVerifier(escrow, verifier, VERIFIER_EPOCH);
        vm.warp(block.timestamp + escrow.VERIFIER_ACTIVATION_DELAY());
        escrow.activateVerifier(verifier);
    }

    function _assertUnfunded(bytes32 callId, uint256 buyerBefore) internal view {
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.None));
        assertEq(escrow.totalLocked(), 0);
        assertEq(USDC.balanceOf(address(escrow)), 0);
        assertEq(USDC.balanceOf(address(buyer)), buyerBefore);
    }

    function _assertLocked(bytes32 callId, uint256 buyerBefore) internal view {
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Locked));
        assertEq(escrow.totalLocked(), AMOUNT);
        assertEq(USDC.balanceOf(address(escrow)), AMOUNT);
        assertEq(USDC.balanceOf(address(buyer)), buyerBefore - AMOUNT);
    }

    function _pause() internal {
        address pauser = USDC.pauser();
        assertTrue(pauser != address(0));
        vm.prank(pauser);
        USDC.pause();
        assertTrue(USDC.paused());
    }

    function _unpause() internal {
        vm.prank(USDC.pauser());
        USDC.unpause();
        assertFalse(USDC.paused());
    }

    function _blacklist(address account) internal {
        address blacklister = USDC.blacklister();
        assertTrue(blacklister != address(0));
        vm.prank(blacklister);
        USDC.blacklist(account);
        assertTrue(USDC.isBlacklisted(account));
    }

    function _unBlacklist(address account) internal {
        vm.prank(USDC.blacklister());
        USDC.unBlacklist(account);
        assertFalse(USDC.isBlacklisted(account));
    }
}
