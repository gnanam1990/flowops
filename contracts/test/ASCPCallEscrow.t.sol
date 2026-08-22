// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ASCPCallEscrow, IServiceDirectory} from "../src/ASCPCallEscrow.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";
import {MockUSDC, FeeToken} from "./mocks/MockUSDC.sol";

contract DirectoryLockHarness is IServiceDirectory {
    uint64 public version = 9;
    bool public sellerProofValid = true;
    bool public resourceProofValid = true;
    mapping(bytes32 sellerId => bool) public pausedSeller;
    mapping(address key => bool) public quoteKeyRevoked;

    function currentVersion() external view returns (uint64) {
        return version;
    }

    function verifySeller(uint64, ServiceDirectory.SellerLeaf calldata, bytes32[] calldata)
        external
        view
        returns (bool)
    {
        return sellerProofValid;
    }

    function verifyResource(uint64, ServiceDirectory.ResourceLeaf calldata, bytes32[] calldata)
        external
        view
        returns (bool)
    {
        return resourceProofValid;
    }

    function setVersion(uint64 value) external {
        version = value;
    }

    function setSellerProofValid(bool value) external {
        sellerProofValid = value;
    }

    function setResourceProofValid(bool value) external {
        resourceProofValid = value;
    }

    function setPaused(bytes32 sellerId, bool value) external {
        pausedSeller[sellerId] = value;
    }

    function setKeyRevoked(address key, bool value) external {
        quoteKeyRevoked[key] = value;
    }
}

contract EscrowDirectoryGovernor {
    function approve(ServiceDirectory directory, uint64 versionId, bytes32 proposalHash) external {
        directory.approveVersion(versionId, proposalHash);
    }

    function addVerifier(ASCPCallEscrow escrow, address key, uint64 epoch) external {
        bytes32 workflowId = keccak256(abi.encode("add-verifier", key, epoch));
        (uint64 pendingEpoch, uint64 pendingActivatesAt) = escrow.pendingVerifier(key);
        bytes32 payloadHash = escrow.governancePayloadHash(
            escrow.addVerifier.selector,
            keccak256(abi.encode(key, escrow.activeVerifierEpoch(key), pendingEpoch, pendingActivatesAt, epoch))
        );
        escrow.addVerifier(key, epoch, workflowId, payloadHash);
    }

    function revokeVerifier(ASCPCallEscrow escrow, address key) external {
        uint64 epoch = escrow.activeVerifierEpoch(key);
        bytes32 workflowId = keccak256(abi.encode("revoke-verifier", key, epoch));
        bytes32 payloadHash = escrow.governancePayloadHash(
            escrow.revokeVerifier.selector, keccak256(abi.encode(key, epoch, false, true))
        );
        escrow.revokeVerifier(key, workflowId, payloadHash);
    }

    function pause(ASCPCallEscrow escrow) external {
        bytes32 workflowId = keccak256("pause-verifier");
        bytes32 payloadHash =
            escrow.governancePayloadHash(escrow.setEmergencyPause.selector, keccak256(abi.encode(false, true)));
        escrow.setEmergencyPause(workflowId, payloadHash);
    }
}

contract EscrowSafeHarness {
    function approve(IERC20 token, address spender, uint256 amount) external {
        token.approve(spender, amount);
    }

    function lock(
        ASCPCallEscrow escrow,
        ASCPCallEscrow.ExecutionCommitment calldata c,
        ServiceDirectory.SellerLeaf calldata seller,
        ServiceDirectory.ResourceLeaf calldata resource,
        bytes32[] calldata sellerProof,
        bytes32[] calldata resourceProof
    ) external returns (bytes32) {
        return escrow.lockCall(c, seller, resource, sellerProof, resourceProof);
    }
}

contract ASCPCallEscrowTest is Test {
    MockUSDC internal usdc;
    DirectoryLockHarness internal directory;
    ASCPCallEscrow internal escrow;
    EscrowDirectoryGovernor internal settlementGovernor;
    EscrowSafeHarness internal buyer;
    ServiceDirectory.SellerLeaf internal seller;
    ServiceDirectory.ResourceLeaf internal resource;

    function setUp() public {
        vm.warp(1_800_000_000);
        usdc = new MockUSDC();
        directory = new DirectoryLockHarness();
        buyer = new EscrowSafeHarness();
        settlementGovernor = new EscrowDirectoryGovernor();
        escrow = new ASCPCallEscrow(
            IERC20(address(usdc)), IServiceDirectory(address(directory)), address(buyer), address(settlementGovernor)
        );
        seller = ServiceDirectory.SellerLeaf({
            sellerId: keccak256("seller"),
            payoutAddress: makeAddr("pay-to"),
            ackAuthority: makeAddr("ack-authority"),
            quoteSigningKey: makeAddr("quote-key"),
            keyEpoch: 1,
            baseURLOriginHash: keccak256("https://seller.example"),
            status: 1
        });
        resource = ServiceDirectory.ResourceLeaf({
            sellerId: seller.sellerId,
            resourceId: keccak256("resource"),
            price: 42,
            escrowSupported: true,
            verificationSpecHash: keccak256("verification-spec"),
            declaredWorkTime: 300,
            verificationBudgetSeconds: 120
        });
        usdc.mint(address(buyer), 1_000_000);
        buyer.approve(IERC20(address(usdc)), address(escrow), type(uint256).max);
    }

    function testLockStoresOnlyExactCommitmentAndCallID() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        bytes32 digest = escrow.executionCommitmentDigest(c, address(escrow), block.chainid);
        bytes32 callId = keccak256(abi.encodePacked(digest));
        vm.expectEmit(true, true, true, true);
        emit ASCPCallEscrow.CallLocked(callId, c.operationId, digest, address(buyer), c.payTo, c.amount, c.settleBy);
        assertEq(buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0)), callId);
        ASCPCallEscrow.Call memory call_ = escrow.getCall(callId);
        assertEq(call_.buyer, address(buyer));
        assertEq(call_.payTo, seller.payoutAddress);
        assertEq(call_.ackAuthority, seller.ackAuthority);
        assertEq(call_.amount, resource.price);
        assertEq(call_.commitmentHash, digest);
        assertEq(call_.verificationSpecHash, resource.verificationSpecHash);
        assertEq(uint8(call_.state), uint8(ASCPCallEscrow.State.Locked));
        assertEq(escrow.totalLocked(), resource.price);
        assertEq(usdc.balanceOf(address(escrow)), resource.price);
    }

    function testExecutionCommitmentMatchesPublishedGoGoldenVector() public view {
        ASCPCallEscrow.ExecutionCommitment memory c = ASCPCallEscrow.ExecutionCommitment({
            orgDomain: bytes32(uint256(1)),
            operationId: bytes32(uint256(2)),
            rail: 1,
            schemeVersion: 1,
            protection: 1,
            escrowContract: 0x1111111111111111111111111111111111111111,
            purchaseSpecHash: bytes32(uint256(3)),
            quoteHash: bytes32(uint256(4)),
            verificationSpecHash: bytes32(uint256(5)),
            declaredWorkTime: 300,
            verificationBudgetSeconds: 120,
            directoryVersion: 9,
            sellerId: bytes32(uint256(6)),
            resourceId: bytes32(uint256(7)),
            payTo: 0x3333333333333333333333333333333333333333,
            ackAuthority: 0x4444444444444444444444444444444444444444,
            amount: 42,
            chainId: 84532,
            asset: 0x036CbD53842c5426634e7929541eC2318f3dCF7e,
            quoteExpiresAt: 1_900_000_000,
            acceptBy: 1_900_000_100,
            deliverBy: 1_900_000_500,
            settleBy: 1_900_002_400
        });
        assertEq(
            escrow.executionCommitmentDigest(c, c.escrowContract, c.chainId),
            0xa12a57a1ebc376e573b7ebaaee4bec4ca7dfcdec16fbef024852e9028882337c
        );
    }

    function testVerdictAttestationMatchesPublishedGoGoldenVector() public {
        address fixedEscrow = 0x1111111111111111111111111111111111111111;
        vm.etch(fixedEscrow, address(escrow).code);
        vm.chainId(8453);
        ASCPCallEscrow.VerdictAttestation memory a = ASCPCallEscrow.VerdictAttestation({
            callId: bytes32(uint256(1)),
            commitmentHash: bytes32(uint256(2)),
            escrowContract: fixedEscrow,
            verifierEpoch: 7,
            verificationSpecHash: bytes32(uint256(3)),
            verifierSoftwareHash: bytes32(uint256(4)),
            deliveryHash: bytes32(uint256(5)),
            deliveredAt: 1_800_000_000,
            evidenceHash: bytes32(uint256(6)),
            verdict: 1,
            verdictNonce: 42,
            issuedAt: 1_800_000_010,
            validUntil: 1_800_000_610
        });
        assertEq(
            ASCPCallEscrow(fixedEscrow).verdictAttestationDigest(a),
            0xb5bd196d91f7d0069c355204391ebf4929c51064bb1fbac9213ea810ccbe56dc
        );
    }

    function testLockAcceptsActualCurrentServiceDirectoryProofs() public {
        uint256 publisherKey = 0xA11CE;
        address publisher = vm.addr(publisherKey);
        EscrowDirectoryGovernor governor = new EscrowDirectoryGovernor();
        ServiceDirectory realDirectory =
            new ServiceDirectory(address(governor), publisher, makeAddr("pauser"), keccak256("org"));
        bytes32 sellerHash = realDirectory.hashSellerLeaf(seller);
        bytes32 resourceHash = realDirectory.hashResourceLeaf(resource);
        bytes32 root = sellerHash < resourceHash
            ? keccak256(abi.encodePacked(sellerHash, resourceHash))
            : keccak256(abi.encodePacked(resourceHash, sellerHash));
        ServiceDirectory.DirectoryProposal memory proposal = ServiceDirectory.DirectoryProposal({
            versionId: 1,
            previousVersion: 0,
            previousRoot: bytes32(0),
            newRoot: root,
            blobContentHash: keccak256("blob"),
            locationsHash: keccak256("locations"),
            changeClass: ServiceDirectory.ChangeClass.Ordinary,
            requestedActivatesAt: uint64(block.timestamp),
            workflowId: keccak256("workflow"),
            workflowPayloadHash: bytes32(0),
            proposerNonce: 1
        });
        proposal.workflowPayloadHash = realDirectory.directoryProposalWorkflowPayloadHash(proposal);
        ServiceDirectory.AdminActionAuthorization memory authorization = ServiceDirectory.AdminActionAuthorization({
            orgDomain: keccak256("org"),
            contractAddress: address(realDirectory),
            chainId: block.chainid,
            authorityRole: realDirectory.DIRECTORY_PUBLISHER_ROLE(),
            functionSelector: realDirectory.proposeVersion.selector,
            payloadHash: keccak256(abi.encode(proposal)),
            adminOperationId: keccak256("proposal-operation"),
            adminNonce: 1,
            adminEpoch: realDirectory.directoryPublisherEpoch(),
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 60),
            workflowId: proposal.workflowId
        });
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(publisherKey, realDirectory.adminAuthorizationDigest(authorization));
        bytes32 proposalHash = realDirectory.proposeVersion(proposal, authorization, abi.encodePacked(r, s, v));
        governor.approve(realDirectory, 1, proposalHash);
        uint64 activatesAt = realDirectory.getProposal(proposalHash).effectiveActivatesAt;
        vm.warp(activatesAt);

        ASCPCallEscrow realEscrow = new ASCPCallEscrow(
            IERC20(address(usdc)),
            IServiceDirectory(address(realDirectory)),
            address(buyer),
            address(settlementGovernor)
        );
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        c.escrowContract = address(realEscrow);
        c.directoryVersion = 1;
        c.quoteExpiresAt = uint64(block.timestamp + 10 minutes);
        c.acceptBy = uint64(block.timestamp + 5 minutes);
        c.deliverBy = uint64(block.timestamp + 20 minutes);
        c.settleBy = uint64(block.timestamp + 1 hours);
        bytes32[] memory sellerProof = _proof(resourceHash);
        bytes32[] memory resourceProof = _proof(sellerHash);
        buyer.approve(IERC20(address(usdc)), address(realEscrow), type(uint256).max);
        bytes32 callId = buyer.lock(realEscrow, c, seller, resource, sellerProof, resourceProof);
        assertEq(uint8(realEscrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Locked));
    }

    function testLockRejectsDomainAndImmutableTermSubstitution() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        c.chainId += 1;
        vm.expectRevert(abi.encodeWithSelector(ASCPCallEscrow.ChainMismatch.selector, block.chainid, c.chainId));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        c.escrowContract = makeAddr("other-escrow");
        vm.expectRevert(
            abi.encodeWithSelector(ASCPCallEscrow.EscrowMismatch.selector, address(escrow), c.escrowContract)
        );
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        c.payTo = makeAddr("substituted-payee");
        vm.expectRevert(ASCPCallEscrow.DirectoryTermsMismatch.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        c.amount += 1;
        vm.expectRevert(ASCPCallEscrow.DirectoryTermsMismatch.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        c.verificationSpecHash = keccak256("substituted-spec");
        vm.expectRevert(ASCPCallEscrow.DirectoryTermsMismatch.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
    }

    function testLockRequiresCurrentVerifiedActiveUnrevokedDirectoryTerms() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        directory.setVersion(c.directoryVersion + 1);
        vm.expectRevert(ASCPCallEscrow.DirectoryProofInvalid.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        directory.setVersion(c.directoryVersion);
        directory.setSellerProofValid(false);
        vm.expectRevert(ASCPCallEscrow.DirectoryProofInvalid.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        directory.setSellerProofValid(true);
        directory.setPaused(seller.sellerId, true);
        vm.expectRevert(ASCPCallEscrow.SellerUnavailable.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        directory.setPaused(seller.sellerId, false);
        directory.setKeyRevoked(seller.quoteSigningKey, true);
        vm.expectRevert(ASCPCallEscrow.SellerUnavailable.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
    }

    function testLockRejectsExpiredOrUnsafeTimingAndDuplicate() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        c.acceptBy = uint64(block.timestamp);
        vm.expectRevert(ASCPCallEscrow.InvalidDeadlines.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        c.deliverBy = uint64(block.timestamp + c.declaredWorkTime + 119);
        c.settleBy = c.deliverBy + 1 hours;
        vm.expectRevert(ASCPCallEscrow.InsufficientDeliveryWindow.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        c.settleBy = uint64(block.timestamp + 29 minutes);
        c.deliverBy = c.settleBy - 1;
        vm.expectRevert(ASCPCallEscrow.InsufficientSettlementMargin.selector);
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));

        c = _commitment();
        bytes32 callId =
            keccak256(abi.encodePacked(escrow.executionCommitmentDigest(c, address(escrow), block.chainid)));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
        vm.expectRevert(abi.encodeWithSelector(ASCPCallEscrow.AlreadyLocked.selector, callId));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
    }

    function testLockRejectsEveryCallerOtherThanPinnedSafe() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        address attacker = makeAddr("attacker");
        vm.expectRevert(abi.encodeWithSelector(ASCPCallEscrow.NotSafe.selector, attacker));
        vm.prank(attacker);
        escrow.lockCall(c, seller, resource, new bytes32[](0), new bytes32[](0));
    }

    function testLockRejectsFeeOnTransferAssetAtomically() public {
        FeeToken feeToken = new FeeToken();
        ASCPCallEscrow feeEscrow = new ASCPCallEscrow(
            IERC20(address(feeToken)),
            IServiceDirectory(address(directory)),
            address(buyer),
            address(settlementGovernor)
        );
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        c.asset = address(feeToken);
        c.escrowContract = address(feeEscrow);
        feeToken.mint(address(buyer), c.amount);
        buyer.approve(IERC20(address(feeToken)), address(feeEscrow), c.amount);
        vm.expectRevert(abi.encodeWithSelector(ASCPCallEscrow.InexactFunding.selector, c.amount, c.amount - 1));
        buyer.lock(feeEscrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
        assertEq(feeEscrow.totalLocked(), 0);
    }

    function testAnyoneCanRefundOnlyTheStoredBuyerAfterSettlementDeadline() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        bytes32 callId =
            keccak256(abi.encodePacked(escrow.executionCommitmentDigest(c, address(escrow), block.chainid)));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
        vm.expectRevert(
            abi.encodeWithSelector(
                ASCPCallEscrow.RefundNotAvailable.selector,
                callId,
                ASCPCallEscrow.State.Locked,
                c.settleBy,
                block.timestamp
            )
        );
        escrow.claimExpired(callId);
        vm.warp(c.settleBy + 1);
        escrow.claimExpired(callId);
        assertEq(usdc.balanceOf(address(buyer)), 1_000_000);
        assertEq(escrow.totalLocked(), 0);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Refunded));
    }

    function testAckMovesNoFundsAndStillExpiresToBuyerRefund() public {
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        bytes32 callId =
            keccak256(abi.encodePacked(escrow.executionCommitmentDigest(c, address(escrow), block.chainid)));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
        vm.prank(seller.ackAuthority);
        escrow.ack(callId);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Acked));
        assertEq(usdc.balanceOf(seller.payoutAddress), 0);
        vm.warp(c.settleBy + 1);
        escrow.claimExpired(callId);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Refunded));
    }

    function testVerifierReleaseIsEpochBoundSingleUseAndRevocable() public {
        uint256 verifierKey = 0xBEEF;
        address verifier = vm.addr(verifierKey);
        settlementGovernor.addVerifier(escrow, verifier, 7);
        vm.expectRevert(abi.encodeWithSelector(ASCPCallEscrow.VerifierActivationPending.selector, verifier));
        escrow.activateVerifier(verifier);
        vm.warp(block.timestamp + escrow.VERIFIER_ACTIVATION_DELAY());
        escrow.activateVerifier(verifier);
        ASCPCallEscrow.ExecutionCommitment memory c = _commitment();
        bytes32 callId =
            keccak256(abi.encodePacked(escrow.executionCommitmentDigest(c, address(escrow), block.chainid)));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
        ASCPCallEscrow.VerdictAttestation memory a = _releaseAttestation(callId, c, 7, 11);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(verifierKey, escrow.verdictAttestationDigest(a));
        escrow.release(callId, a, abi.encodePacked(r, s, v));
        assertEq(usdc.balanceOf(seller.payoutAddress), c.amount);
        assertEq(uint8(escrow.getCall(callId).state), uint8(ASCPCallEscrow.State.Released));
        vm.expectRevert(
            abi.encodeWithSelector(ASCPCallEscrow.WrongState.selector, callId, ASCPCallEscrow.State.Released)
        );
        escrow.release(callId, a, abi.encodePacked(r, s, v));

        c.operationId = keccak256("revoked-operation");
        bytes32 second =
            keccak256(abi.encodePacked(escrow.executionCommitmentDigest(c, address(escrow), block.chainid)));
        buyer.lock(escrow, c, seller, resource, new bytes32[](0), new bytes32[](0));
        a = _releaseAttestation(second, c, 7, 12);
        (v, r, s) = vm.sign(verifierKey, escrow.verdictAttestationDigest(a));
        settlementGovernor.revokeVerifier(escrow, verifier);
        vm.expectRevert(abi.encodeWithSelector(ASCPCallEscrow.VerifierNotActive.selector, verifier, 7));
        escrow.release(second, a, abi.encodePacked(r, s, v));
    }

    function testVerifierGovernanceRequiresExactWorkflowPayloadBinding() public {
        address verifier = makeAddr("bound-verifier");
        bytes32 workflowId = keccak256("verifier-workflow");
        bytes32 payloadHash = escrow.governancePayloadHash(
            escrow.addVerifier.selector, keccak256(abi.encode(verifier, uint64(0), uint64(0), uint64(0), uint64(9)))
        );
        bytes32 staleNextHash = escrow.governancePayloadHash(
            escrow.addVerifier.selector, keccak256(abi.encode(verifier, uint64(0), uint64(0), uint64(0), uint64(10)))
        );

        vm.expectRevert(ASCPCallEscrow.InvalidWorkflowBinding.selector);
        vm.prank(address(settlementGovernor));
        escrow.addVerifier(verifier, 10, workflowId, payloadHash);

        vm.expectEmit(true, true, true, true);
        emit ASCPCallEscrow.GovernanceWorkflowBound(workflowId, payloadHash, escrow.addVerifier.selector);
        vm.prank(address(settlementGovernor));
        escrow.addVerifier(verifier, 9, workflowId, payloadHash);

        vm.expectRevert(ASCPCallEscrow.InvalidWorkflowBinding.selector);
        vm.prank(address(settlementGovernor));
        escrow.addVerifier(verifier, 10, keccak256("stale-workflow"), staleNextHash);
    }

    function testGovernancePayloadMatchesPublishedGoGoldenVector() public {
        address fixedEscrow = 0x1111111111111111111111111111111111111111;
        vm.etch(fixedEscrow, address(escrow).code);
        vm.chainId(8453);
        ASCPCallEscrow target = ASCPCallEscrow(fixedEscrow);
        assertEq(
            target.governancePayloadHash(
                target.addVerifier.selector,
                keccak256(
                    abi.encode(0x2222222222222222222222222222222222222222, uint64(0), uint64(0), uint64(0), uint64(7))
                )
            ),
            0x0f19082ff9903ba033a08ccbb3b8528aecf73085f137c7525e75edfd3eb82ae5
        );
        assertEq(
            target.governancePayloadHash(
                target.revokeVerifier.selector,
                keccak256(abi.encode(0x2222222222222222222222222222222222222222, uint64(7), false, true))
            ),
            0xc495ef0524c5118be2b3f5212e30d052b96b53e213fe6ad459612e0acff0837b
        );
        assertEq(
            target.governancePayloadHash(target.setEmergencyPause.selector, keccak256(abi.encode(false, true))),
            0xd392524af9b81440a6267a4b8b84939e061dc4df4a9d1133258dc88dc857001a
        );
    }

    function _commitment() internal view returns (ASCPCallEscrow.ExecutionCommitment memory c) {
        c = ASCPCallEscrow.ExecutionCommitment({
            orgDomain: keccak256("org"),
            operationId: keccak256("operation"),
            rail: 1,
            schemeVersion: 1,
            protection: 1,
            escrowContract: address(escrow),
            purchaseSpecHash: keccak256("purchase-spec"),
            quoteHash: keccak256("quote"),
            verificationSpecHash: resource.verificationSpecHash,
            declaredWorkTime: resource.declaredWorkTime,
            verificationBudgetSeconds: resource.verificationBudgetSeconds,
            directoryVersion: directory.version(),
            sellerId: seller.sellerId,
            resourceId: resource.resourceId,
            payTo: seller.payoutAddress,
            ackAuthority: seller.ackAuthority,
            amount: resource.price,
            chainId: block.chainid,
            asset: address(usdc),
            quoteExpiresAt: uint64(block.timestamp + 10 minutes),
            acceptBy: uint64(block.timestamp + 5 minutes),
            deliverBy: uint64(block.timestamp + 20 minutes),
            settleBy: uint64(block.timestamp + 1 hours)
        });
    }

    function _proof(bytes32 sibling) internal pure returns (bytes32[] memory proof) {
        proof = new bytes32[](1);
        proof[0] = sibling;
    }

    function _releaseAttestation(
        bytes32 callId,
        ASCPCallEscrow.ExecutionCommitment memory c,
        uint64 epoch,
        uint256 nonce
    ) internal view returns (ASCPCallEscrow.VerdictAttestation memory) {
        return ASCPCallEscrow.VerdictAttestation({
            callId: callId,
            commitmentHash: escrow.executionCommitmentDigest(c, address(escrow), block.chainid),
            escrowContract: address(escrow),
            verifierEpoch: epoch,
            verificationSpecHash: c.verificationSpecHash,
            verifierSoftwareHash: keccak256("verifier-v1"),
            deliveryHash: keccak256("delivery"),
            deliveredAt: uint64(block.timestamp),
            evidenceHash: keccak256("evidence"),
            verdict: escrow.VERDICT_RELEASE(),
            verdictNonce: nonce,
            issuedAt: uint64(block.timestamp),
            validUntil: uint64(block.timestamp + 5 minutes)
        });
    }
}
