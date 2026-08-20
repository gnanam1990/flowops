// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";

contract DirectoryGovernor {
    function approve(ServiceDirectory directory, uint64 versionId, bytes32 proposalHash) external {
        directory.approveVersion(versionId, proposalHash);
    }

    function cancel(ServiceDirectory directory, uint64 versionId, bytes32 proposalHash) external {
        directory.cancelVersion(versionId, proposalHash);
    }

    function setSellerPaused(ServiceDirectory directory, bytes32 sellerId, bool paused) external {
        ServiceDirectory.AdminActionAuthorization memory authorization;
        directory.pauseSeller(sellerId, paused, authorization, "");
    }

    function setKeyRevoked(ServiceDirectory directory, address key, bool revoked) external {
        ServiceDirectory.AdminActionAuthorization memory authorization;
        directory.setQuoteKeyRevoked(key, revoked, authorization, "");
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

        ServiceDirectory.DirectoryProposal memory substituted = proposal;
        substituted.newRoot = keccak256("substituted-root");
        vm.expectRevert(ServiceDirectory.InvalidAuthorization.selector);
        vm.prank(relayer);
        directory.proposeVersion(substituted, authorization, signature);

        proposal.newRoot = root;
        vm.prank(relayer);
        directory.proposeVersion(proposal, authorization, signature);
        bytes32 sellerId = keccak256("seller-pause");
        ServiceDirectory.AdminActionAuthorization memory pauseAuthorization = _pauseAuthorization(sellerId, true, 12);
        bytes memory pauseSignature = _sign(PAUSER_KEY, pauseAuthorization);
        vm.prank(relayer);
        directory.pauseSeller(sellerId, true, pauseAuthorization, pauseSignature);
        vm.expectRevert(
            abi.encodeWithSelector(ServiceDirectory.AuthorizationUsed.selector, pauseAuthorization.adminOperationId)
        );
        vm.prank(relayer);
        directory.pauseSeller(sellerId, true, pauseAuthorization, pauseSignature);
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

    function testProtectiveOverlaysOnlyAllowHotKeyToTighten() public {
        bytes32 sellerId = keccak256("seller-overlay");
        ServiceDirectory.AdminActionAuthorization memory pauseAuthorization = _pauseAuthorization(sellerId, true, 31);
        vm.prank(relayer);
        directory.pauseSeller(sellerId, true, pauseAuthorization, _sign(PAUSER_KEY, pauseAuthorization));
        assertTrue(directory.pausedSeller(sellerId));

        ServiceDirectory.AdminActionAuthorization memory unusedAuthorization;
        vm.expectRevert(abi.encodeWithSelector(ServiceDirectory.NotGovernor.selector, stranger));
        vm.prank(stranger);
        directory.pauseSeller(sellerId, false, unusedAuthorization, "");
        governor.setSellerPaused(directory, sellerId, false);
        assertFalse(directory.pausedSeller(sellerId));

        address quoteKey = makeAddr("quote-key");
        ServiceDirectory.AdminActionAuthorization memory revokeAuthorization = _revokeAuthorization(quoteKey, true, 32);
        vm.prank(relayer);
        directory.setQuoteKeyRevoked(quoteKey, true, revokeAuthorization, _sign(PAUSER_KEY, revokeAuthorization));
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
    ) internal pure returns (ServiceDirectory.DirectoryProposal memory) {
        return ServiceDirectory.DirectoryProposal({
            versionId: version,
            previousVersion: 0,
            previousRoot: bytes32(0),
            newRoot: root,
            blobContentHash: keccak256(abi.encode("blob", nonce)),
            locationsHash: keccak256(abi.encode("locations", nonce)),
            changeClass: changeClass,
            requestedActivatesAt: requestedAt,
            workflowId: keccak256(abi.encode("workflow", nonce)),
            workflowPayloadHash: keccak256(abi.encode("workflow-payload", nonce)),
            proposerNonce: nonce
        });
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

    function _pauseAuthorization(bytes32 sellerId, bool paused, uint256 nonce)
        internal
        view
        returns (ServiceDirectory.AdminActionAuthorization memory)
    {
        return _overlayAuthorization(directory.pauseSeller.selector, keccak256(abi.encode(sellerId, paused)), nonce);
    }

    function _revokeAuthorization(address key, bool revoked, uint256 nonce)
        internal
        view
        returns (ServiceDirectory.AdminActionAuthorization memory)
    {
        return _overlayAuthorization(directory.setQuoteKeyRevoked.selector, keccak256(abi.encode(key, revoked)), nonce);
    }

    function _overlayAuthorization(bytes4 selector, bytes32 payloadHash, uint256 nonce)
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
            workflowId: bytes32(0)
        });
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
