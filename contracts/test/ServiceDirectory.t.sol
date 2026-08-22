// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";

contract DirectoryGovernor {
    function approve(ServiceDirectory directory, uint64 versionId, bytes32 proposalHash) external {
        directory.approveVersion(versionId, proposalHash);
    }

    function cancel(ServiceDirectory directory, uint64 versionId, bytes32 proposalHash) external {
        bytes32 workflowId = keccak256(abi.encode("cancel-workflow", versionId, proposalHash));
        bytes32 payloadHash = directory.governancePayloadHash(
            workflowId, directory.cancelVersion.selector, keccak256(abi.encode(versionId, proposalHash))
        );
        directory.cancelVersion(versionId, proposalHash, workflowId, payloadHash);
    }

    function setSellerPaused(ServiceDirectory directory, bytes32 sellerId, bool paused) external {
        ServiceDirectory.AdminActionAuthorization memory authorization;
        bytes32 workflowId = keccak256(abi.encode("seller-overlay", sellerId, paused));
        bytes32 payloadHash = directory.governancePayloadHash(
            workflowId,
            directory.pauseSeller.selector,
            keccak256(abi.encode(sellerId, directory.pausedSeller(sellerId), paused))
        );
        directory.pauseSeller(sellerId, paused, workflowId, payloadHash, authorization, "");
    }

    function setKeyRevoked(ServiceDirectory directory, address key, bool revoked) external {
        ServiceDirectory.AdminActionAuthorization memory authorization;
        bytes32 workflowId = keccak256(abi.encode("key-overlay", key, revoked));
        bytes32 payloadHash = directory.governancePayloadHash(
            workflowId,
            directory.setQuoteKeyRevoked.selector,
            keccak256(abi.encode(key, directory.quoteKeyRevoked(key), revoked))
        );
        directory.setQuoteKeyRevoked(key, revoked, workflowId, payloadHash, authorization, "");
    }

    function setPublisher(ServiceDirectory directory, address publisher) external {
        bytes32 workflowId = keccak256(abi.encode("publisher-rotation", publisher, directory.directoryPublisherEpoch()));
        bytes32 payloadHash = directory.governancePayloadHash(
            workflowId,
            directory.setDirectoryPublisher.selector,
            keccak256(abi.encode(directory.directoryPublisher(), directory.directoryPublisherEpoch(), publisher))
        );
        directory.setDirectoryPublisher(publisher, workflowId, payloadHash);
    }

    function setPauser(ServiceDirectory directory, address pauser) external {
        bytes32 workflowId = keccak256(abi.encode("pauser-rotation", pauser, directory.pauserEpoch()));
        bytes32 payloadHash = directory.governancePayloadHash(
            workflowId,
            directory.setPauser.selector,
            keccak256(abi.encode(directory.pauser(), directory.pauserEpoch(), pauser))
        );
        directory.setPauser(pauser, workflowId, payloadHash);
    }
}

contract ServiceDirectoryTest is Test {
    uint256 internal constant PUBLISHER_KEY = 0xA11CE;
    uint256 internal constant PAUSER_KEY = 0xB0B;
    bytes32 internal constant ORG_DOMAIN = keccak256("org-flowops-test");
    bytes32 internal constant SELLER_ID = keccak256("seller-a");
    bytes32 internal constant RESOURCE_ID = keccak256("resource-a");

    DirectoryGovernor internal governor;
    ServiceDirectory internal directory;
    address internal publisher;
    address internal pauser;
    address internal relayer = makeAddr("relayer");
    address internal stranger = makeAddr("stranger");

    function setUp() public {
        vm.warp(10_000);
        publisher = vm.addr(PUBLISHER_KEY);
        pauser = vm.addr(PAUSER_KEY);
        governor = new DirectoryGovernor();
        directory = new ServiceDirectory(address(governor), publisher, pauser, ORG_DOMAIN);
    }

    function testConstructorRequiresContractGovernorAndNonzeroAuthorities() public {
        vm.expectRevert(abi.encodeWithSelector(ServiceDirectory.GovernorMustBeContract.selector, stranger));
        new ServiceDirectory(stranger, publisher, pauser, ORG_DOMAIN);
        vm.expectRevert(ServiceDirectory.ZeroAddress.selector);
        new ServiceDirectory(address(governor), address(0), pauser, ORG_DOMAIN);
    }

    function testAuthorityRotationsRequireExactWorkflowBindingAndRejectNoOp() public {
        address nextPublisher = makeAddr("next-publisher");
        vm.expectRevert(ServiceDirectory.InvalidWorkflowBinding.selector);
        vm.prank(address(governor));
        directory.setDirectoryPublisher(nextPublisher, bytes32(0), bytes32(0));

        governor.setPublisher(directory, nextPublisher);
        assertEq(directory.directoryPublisher(), nextPublisher);
        assertEq(directory.directoryPublisherEpoch(), 2);
        vm.expectRevert(abi.encodeWithSelector(ServiceDirectory.AuthorityUnchanged.selector, nextPublisher));
        governor.setPublisher(directory, nextPublisher);

        address nextPauser = makeAddr("next-pauser");
        governor.setPauser(directory, nextPauser);
        assertEq(directory.pauser(), nextPauser);
        assertEq(directory.pauserEpoch(), 2);
    }

    function testProposalApprovalActivationAndMerkleLeafBinding() public {
        (ServiceDirectory.SellerLeaf memory seller, ServiceDirectory.ResourceLeaf memory resource, bytes32 root) =
            _leavesAndRoot();
        ServiceDirectory.DirectoryProposal memory proposal =
            _proposal(root, 1, 1, ServiceDirectory.ChangeClass.Ordinary, uint64(block.timestamp));
        bytes32 proposalHash = _propose(proposal, PUBLISHER_KEY, 1);

        assertEq(directory.currentVersion(), 0);
        assertEq(directory.currentRoot(), bytes32(0));
        assertFalse(directory.verifySeller(1, seller, _proof(directory.hashResourceLeaf(resource))));

        governor.approve(directory, 1, proposalHash);
        ServiceDirectory.ProposalRecord memory record = directory.getProposal(proposalHash);
        assertEq(uint8(record.state), uint8(ServiceDirectory.ProposalState.ApprovedPendingActivation));
        assertEq(record.effectiveActivatesAt, uint64(block.timestamp + directory.ORDINARY_DELAY()));

        vm.warp(record.effectiveActivatesAt);
        assertEq(directory.currentVersion(), 1);
        assertEq(directory.currentRoot(), root);
        assertTrue(directory.verifySeller(1, seller, _proof(directory.hashResourceLeaf(resource))));
        assertTrue(directory.verifyResource(1, resource, _proof(directory.hashSellerLeaf(seller))));
        assertFalse(directory.verifySeller(2, seller, _proof(directory.hashResourceLeaf(resource))));

        directory.activateVersion();
        record = directory.getProposal(proposalHash);
        assertEq(uint8(record.state), uint8(ServiceDirectory.ProposalState.Active));
    }

    function testProposalAuthorizationRejectsPayloadSubstitutionAndReplay() public {
        (,, bytes32 root) = _leavesAndRoot();
        ServiceDirectory.DirectoryProposal memory proposal =
            _proposal(root, 1, 11, ServiceDirectory.ChangeClass.Ordinary, uint64(block.timestamp));
        ServiceDirectory.AdminActionAuthorization memory authorization = _proposalAuthorization(proposal, 11);
        bytes memory signature = _sign(PUBLISHER_KEY, authorization);

        ServiceDirectory.DirectoryProposal memory substituted =
            abi.decode(abi.encode(proposal), (ServiceDirectory.DirectoryProposal));
        substituted.newRoot = keccak256("substituted-root");
        vm.expectRevert(ServiceDirectory.InvalidWorkflowBinding.selector);
        vm.prank(relayer);
        directory.proposeVersion(substituted, authorization, signature);

        substituted = abi.decode(abi.encode(proposal), (ServiceDirectory.DirectoryProposal));
        substituted.proposerNonce = proposal.proposerNonce + 1;
        vm.expectRevert(ServiceDirectory.InvalidAuthorization.selector);
        vm.prank(relayer);
        directory.proposeVersion(substituted, authorization, signature);

        proposal.newRoot = root;
        vm.prank(relayer);
        directory.proposeVersion(proposal, authorization, signature);
        bytes32 sellerId = keccak256("seller-pause");
        bytes32 pauseWorkflowId = _overlayWorkflow("seller-pause", 12);
        bytes32 pausePayloadHash = _pauseWorkflowPayload(sellerId, false, true, pauseWorkflowId);
        ServiceDirectory.AdminActionAuthorization memory pauseAuthorization =
            _pauseAuthorization(sellerId, false, true, pauseWorkflowId, 12);
        bytes memory pauseSignature = _sign(PAUSER_KEY, pauseAuthorization);
        vm.prank(relayer);
        directory.pauseSeller(sellerId, true, pauseWorkflowId, pausePayloadHash, pauseAuthorization, pauseSignature);
        vm.expectRevert(abi.encodeWithSelector(ServiceDirectory.SellerPauseUnchanged.selector, sellerId, true));
        vm.prank(relayer);
        directory.pauseSeller(sellerId, true, pauseWorkflowId, pausePayloadHash, pauseAuthorization, pauseSignature);
    }

    function testGovernorIsRequiredForApprovalAndCancellationReproposalIsBound() public {
        (,, bytes32 root) = _leavesAndRoot();
        ServiceDirectory.DirectoryProposal memory first =
            _proposal(root, 1, 21, ServiceDirectory.ChangeClass.Ordinary, uint64(block.timestamp));
        bytes32 firstHash = _propose(first, PUBLISHER_KEY, 21);

        vm.expectRevert(abi.encodeWithSelector(ServiceDirectory.NotGovernor.selector, stranger));
        vm.prank(stranger);
        directory.approveVersion(1, firstHash);

        governor.cancel(directory, 1, firstHash);
        assertEq(uint8(directory.getProposal(firstHash).state), uint8(ServiceDirectory.ProposalState.Cancelled));
        ServiceDirectory.DirectoryProposal memory second =
            _proposal(root, 1, 22, ServiceDirectory.ChangeClass.PayoutOrAuthorityAffecting, uint64(block.timestamp));
        bytes32 secondHash = _propose(second, PUBLISHER_KEY, 22);
        governor.approve(directory, 1, secondHash);
        ServiceDirectory.ProposalRecord memory record = directory.getProposal(secondHash);
        assertEq(record.effectiveActivatesAt, uint64(block.timestamp + directory.PAYOUT_OR_AUTHORITY_DELAY()));

        vm.warp(record.effectiveActivatesAt);
        vm.expectRevert(
            abi.encodeWithSelector(
                ServiceDirectory.ActivationAlreadyDue.selector, secondHash, record.effectiveActivatesAt, block.timestamp
            )
        );
        governor.cancel(directory, 1, secondHash);
    }

    function testDirectoryProposalAndCancellationRejectWorkflowPayloadSubstitution() public {
        (,, bytes32 root) = _leavesAndRoot();
        ServiceDirectory.DirectoryProposal memory proposal =
            _proposal(root, 1, 25, ServiceDirectory.ChangeClass.Ordinary, uint64(block.timestamp));
        proposal.workflowPayloadHash = keccak256("wrong-payload");
        ServiceDirectory.AdminActionAuthorization memory authorization = _proposalAuthorization(proposal, 25);
        bytes memory signature = _sign(PUBLISHER_KEY, authorization);
        vm.expectRevert(ServiceDirectory.InvalidWorkflowBinding.selector);
        vm.prank(relayer);
        directory.proposeVersion(proposal, authorization, signature);

        proposal.workflowPayloadHash = directory.directoryProposalWorkflowPayloadHash(proposal);
        bytes32 proposalHash = _propose(proposal, PUBLISHER_KEY, 26);
        bytes32 cancelWorkflowId = keccak256("cancel-workflow");
        bytes32 wrong = directory.governancePayloadHash(
            cancelWorkflowId, directory.cancelVersion.selector, keccak256(abi.encode(uint64(1), bytes32(uint256(1))))
        );
        vm.expectRevert(ServiceDirectory.InvalidWorkflowBinding.selector);
        vm.prank(address(governor));
        directory.cancelVersion(1, proposalHash, cancelWorkflowId, wrong);

        bytes32 exact = directory.governancePayloadHash(
            cancelWorkflowId, directory.cancelVersion.selector, keccak256(abi.encode(uint64(1), proposalHash))
        );
        vm.expectRevert(ServiceDirectory.InvalidWorkflowBinding.selector);
        vm.prank(address(governor));
        directory.cancelVersion(1, proposalHash, keccak256("wrong-workflow"), exact);
    }

    function testGovernancePayloadMatchesPublishedGoGoldenVector() public {
        address fixedDirectory = 0x1111111111111111111111111111111111111111;
        vm.etch(fixedDirectory, address(directory).code);
        vm.chainId(8453);
        ServiceDirectory.DirectoryProposal memory proposal = ServiceDirectory.DirectoryProposal({
            versionId: 2,
            previousVersion: 1,
            previousRoot: bytes32(uint256(5)),
            newRoot: bytes32(uint256(6)),
            blobContentHash: bytes32(uint256(7)),
            locationsHash: bytes32(uint256(8)),
            changeClass: ServiceDirectory.ChangeClass.PayoutOrAuthorityAffecting,
            requestedActivatesAt: 1_800_000_000,
            workflowId: bytes32(uint256(10)),
            workflowPayloadHash: bytes32(0),
            proposerNonce: 11
        });
        assertEq(
            ServiceDirectory(fixedDirectory).directoryProposalWorkflowPayloadHash(proposal),
            0xf577289b92b129c625813d0725e72da6c048a94651ad07508083ecc3a01f24b9
        );
        assertEq(
            ServiceDirectory(fixedDirectory)
                .governancePayloadHash(
                    proposal.workflowId,
                    ServiceDirectory(fixedDirectory).cancelVersion.selector,
                    keccak256(abi.encode(uint64(2), bytes32(uint256(9))))
                ),
            0xdffa3dea6724afbc06b8e60d4306cd37fd64c84cf20c506aa29f188791eb2b08
        );
    }

    function testProtectiveOverlaysOnlyAllowHotKeyToTighten() public {
        bytes32 sellerId = keccak256("seller-overlay");
        bytes32 pauseWorkflowId = _overlayWorkflow("seller-pause", 31);
        bytes32 pausePayloadHash = _pauseWorkflowPayload(sellerId, false, true, pauseWorkflowId);
        ServiceDirectory.AdminActionAuthorization memory pauseAuthorization =
            _pauseAuthorization(sellerId, false, true, pauseWorkflowId, 31);
        vm.prank(relayer);
        directory.pauseSeller(
            sellerId, true, pauseWorkflowId, pausePayloadHash, pauseAuthorization, _sign(PAUSER_KEY, pauseAuthorization)
        );
        assertTrue(directory.pausedSeller(sellerId));

        ServiceDirectory.AdminActionAuthorization memory unusedAuthorization;
        bytes32 unpauseWorkflowId = _overlayWorkflow("seller-unpause", 31);
        bytes32 unpausePayloadHash = _pauseWorkflowPayload(sellerId, true, false, unpauseWorkflowId);
        vm.expectRevert(abi.encodeWithSelector(ServiceDirectory.NotGovernor.selector, stranger));
        vm.prank(stranger);
        directory.pauseSeller(sellerId, false, unpauseWorkflowId, unpausePayloadHash, unusedAuthorization, "");
        governor.setSellerPaused(directory, sellerId, false);
        assertFalse(directory.pausedSeller(sellerId));

        address quoteKey = makeAddr("quote-key");
        bytes32 revokeWorkflowId = _overlayWorkflow("quote-revoke", 32);
        bytes32 revokePayloadHash = _revokeWorkflowPayload(quoteKey, false, true, revokeWorkflowId);
        ServiceDirectory.AdminActionAuthorization memory revokeAuthorization =
            _revokeAuthorization(quoteKey, false, true, revokeWorkflowId, 32);
        vm.prank(relayer);
        directory.setQuoteKeyRevoked(
            quoteKey,
            true,
            revokeWorkflowId,
            revokePayloadHash,
            revokeAuthorization,
            _sign(PAUSER_KEY, revokeAuthorization)
        );
        assertTrue(directory.quoteKeyRevoked(quoteKey));
        governor.setKeyRevoked(directory, quoteKey, false);
        assertFalse(directory.quoteKeyRevoked(quoteKey));
    }

    function testFuzzSortedPairVerification(bytes32 sellerSalt, bytes32 resourceSalt) public {
        vm.assume(sellerSalt != resourceSalt);
        ServiceDirectory.SellerLeaf memory seller = ServiceDirectory.SellerLeaf({
            sellerId: keccak256(abi.encode("seller", sellerSalt)),
            payoutAddress: makeAddr("payout"),
            ackAuthority: makeAddr("ack"),
            quoteSigningKey: makeAddr("quote"),
            keyEpoch: 1,
            baseURLOriginHash: keccak256("https://seller.example"),
            status: 1
        });
        ServiceDirectory.ResourceLeaf memory resource = ServiceDirectory.ResourceLeaf({
            sellerId: seller.sellerId,
            resourceId: keccak256(abi.encode("resource", resourceSalt)),
            price: 1,
            escrowSupported: true,
            verificationSpecHash: keccak256("spec"),
            declaredWorkTime: 1,
            verificationBudgetSeconds: 1
        });
        bytes32 sellerHash = directory.hashSellerLeaf(seller);
        bytes32 resourceHash = directory.hashResourceLeaf(resource);
        bytes32 root = sellerHash < resourceHash
            ? keccak256(abi.encodePacked(sellerHash, resourceHash))
            : keccak256(abi.encodePacked(resourceHash, sellerHash));
        ServiceDirectory.DirectoryProposal memory proposal =
            _proposal(root, 1, uint256(sellerSalt), ServiceDirectory.ChangeClass.Ordinary, uint64(block.timestamp));
        bytes32 proposalHash = _propose(proposal, PUBLISHER_KEY, uint256(sellerSalt));
        governor.approve(directory, 1, proposalHash);
        vm.warp(block.timestamp + directory.ORDINARY_DELAY());
        assertTrue(directory.verifySeller(1, seller, _proof(resourceHash)));
        assertTrue(directory.verifyResource(1, resource, _proof(sellerHash)));
    }

    function _leavesAndRoot()
        internal
        returns (ServiceDirectory.SellerLeaf memory seller, ServiceDirectory.ResourceLeaf memory resource, bytes32 root)
    {
        seller = ServiceDirectory.SellerLeaf({
            sellerId: SELLER_ID,
            payoutAddress: makeAddr("payout"),
            ackAuthority: makeAddr("ack"),
            quoteSigningKey: makeAddr("quote"),
            keyEpoch: 1,
            baseURLOriginHash: keccak256("https://seller.example"),
            status: 1
        });
        resource = ServiceDirectory.ResourceLeaf({
            sellerId: SELLER_ID,
            resourceId: RESOURCE_ID,
            price: 60_000,
            escrowSupported: true,
            verificationSpecHash: keccak256("verification-spec"),
            declaredWorkTime: 60,
            verificationBudgetSeconds: 30
        });
        bytes32 sellerHash = directory.hashSellerLeaf(seller);
        bytes32 resourceHash = directory.hashResourceLeaf(resource);
        root = sellerHash < resourceHash
            ? keccak256(abi.encodePacked(sellerHash, resourceHash))
            : keccak256(abi.encodePacked(resourceHash, sellerHash));
    }

    function _proposal(
        bytes32 root,
        uint64 version,
        uint256 nonce,
        ServiceDirectory.ChangeClass changeClass,
        uint64 requestedAt
    ) internal view returns (ServiceDirectory.DirectoryProposal memory proposal) {
        proposal = ServiceDirectory.DirectoryProposal({
            versionId: version,
            previousVersion: 0,
            previousRoot: bytes32(0),
            newRoot: root,
            blobContentHash: keccak256(abi.encode("blob", nonce)),
            locationsHash: keccak256(abi.encode("locations", nonce)),
            changeClass: changeClass,
            requestedActivatesAt: requestedAt,
            workflowId: keccak256(abi.encode("workflow", nonce)),
            workflowPayloadHash: bytes32(0),
            proposerNonce: nonce
        });
        proposal.workflowPayloadHash = directory.directoryProposalWorkflowPayloadHash(proposal);
    }

    function _proposalAuthorization(ServiceDirectory.DirectoryProposal memory proposal, uint256 nonce)
        internal
        view
        returns (ServiceDirectory.AdminActionAuthorization memory)
    {
        return ServiceDirectory.AdminActionAuthorization({
            orgDomain: ORG_DOMAIN,
            contractAddress: address(directory),
            chainId: block.chainid,
            authorityRole: directory.DIRECTORY_PUBLISHER_ROLE(),
            functionSelector: directory.proposeVersion.selector,
            payloadHash: keccak256(abi.encode(proposal)),
            adminOperationId: keccak256(abi.encode("proposal-operation", nonce)),
            adminNonce: nonce,
            adminEpoch: directory.directoryPublisherEpoch(),
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 60),
            workflowId: proposal.workflowId
        });
    }

    function _pauseAuthorization(bytes32 sellerId, bool current, bool paused, bytes32 workflowId, uint256 nonce)
        internal
        view
        returns (ServiceDirectory.AdminActionAuthorization memory)
    {
        return _overlayAuthorization(
            directory.pauseSeller.selector,
            keccak256(abi.encode(sellerId, current, paused, workflowId)),
            workflowId,
            nonce
        );
    }

    function _revokeAuthorization(address key, bool current, bool revoked, bytes32 workflowId, uint256 nonce)
        internal
        view
        returns (ServiceDirectory.AdminActionAuthorization memory)
    {
        return _overlayAuthorization(
            directory.setQuoteKeyRevoked.selector,
            keccak256(abi.encode(key, current, revoked, workflowId)),
            workflowId,
            nonce
        );
    }

    function _overlayAuthorization(bytes4 selector, bytes32 payloadHash, bytes32 workflowId, uint256 nonce)
        internal
        view
        returns (ServiceDirectory.AdminActionAuthorization memory)
    {
        return ServiceDirectory.AdminActionAuthorization({
            orgDomain: ORG_DOMAIN,
            contractAddress: address(directory),
            chainId: block.chainid,
            authorityRole: directory.PAUSER_ROLE(),
            functionSelector: selector,
            payloadHash: payloadHash,
            adminOperationId: keccak256(abi.encode("overlay-operation", nonce)),
            adminNonce: nonce,
            adminEpoch: directory.pauserEpoch(),
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 60),
            workflowId: workflowId
        });
    }

    function _overlayWorkflow(string memory kind, uint256 nonce) internal pure returns (bytes32) {
        return keccak256(abi.encode(kind, nonce));
    }

    function _pauseWorkflowPayload(bytes32 sellerId, bool current, bool paused, bytes32 workflowId)
        internal
        view
        returns (bytes32)
    {
        return directory.governancePayloadHash(
            workflowId, directory.pauseSeller.selector, keccak256(abi.encode(sellerId, current, paused))
        );
    }

    function _revokeWorkflowPayload(address key, bool current, bool revoked, bytes32 workflowId)
        internal
        view
        returns (bytes32)
    {
        return directory.governancePayloadHash(
            workflowId, directory.setQuoteKeyRevoked.selector, keccak256(abi.encode(key, current, revoked))
        );
    }

    function _propose(ServiceDirectory.DirectoryProposal memory proposal, uint256 signerKey, uint256 authorizationNonce)
        internal
        returns (bytes32)
    {
        ServiceDirectory.AdminActionAuthorization memory authorization =
            _proposalAuthorization(proposal, authorizationNonce);
        vm.prank(relayer);
        return directory.proposeVersion(proposal, authorization, _sign(signerKey, authorization));
    }

    function _sign(uint256 key, ServiceDirectory.AdminActionAuthorization memory authorization)
        internal
        view
        returns (bytes memory)
    {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, directory.adminAuthorizationDigest(authorization));
        return abi.encodePacked(r, s, v);
    }

    function _proof(bytes32 sibling) internal pure returns (bytes32[] memory proof) {
        proof = new bytes32[](1);
        proof[0] = sibling;
    }
}
