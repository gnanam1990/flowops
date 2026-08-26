// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";

contract DirectoryReleaseGovernorFixture {}

contract DirectoryReleaseCompilerParityTest is Test {
    ServiceDirectory private directory;

    function setUp() external {
        vm.chainId(84532);
        DirectoryReleaseGovernorFixture governor = new DirectoryReleaseGovernorFixture();
        ServiceDirectory implementation =
            new ServiceDirectory(address(governor), address(0x11), address(0x15), bytes32(uint256(1)));
        vm.etch(address(0x10), address(implementation).code);
        directory = ServiceDirectory(address(0x10));
    }

    function testGoCompilerMatchesSolidityLeavesMerkleProposalAndPublisherPayload() external view {
        ServiceDirectory.SellerLeaf memory seller = ServiceDirectory.SellerLeaf({
            sellerId: bytes32(uint256(3)),
            payoutAddress: address(0x12),
            ackAuthority: address(0x13),
            quoteSigningKey: address(0x14),
            keyEpoch: 1,
            baseURLOriginHash: keccak256("https://seller.testnet.flowopsagent.xyz"),
            status: 1
        });
        ServiceDirectory.ResourceLeaf memory firstResource = ServiceDirectory.ResourceLeaf({
            sellerId: bytes32(uint256(3)),
            resourceId: bytes32(uint256(5)),
            price: 1000,
            escrowSupported: true,
            verificationSpecHash: bytes32(uint256(6)),
            declaredWorkTime: 300,
            verificationBudgetSeconds: 120
        });
        ServiceDirectory.ResourceLeaf memory secondResource = ServiceDirectory.ResourceLeaf({
            sellerId: bytes32(uint256(3)),
            resourceId: bytes32(uint256(7)),
            price: 2000,
            escrowSupported: true,
            verificationSpecHash: bytes32(uint256(8)),
            declaredWorkTime: 600,
            verificationBudgetSeconds: 180
        });

        bytes32 sellerHash = directory.hashSellerLeaf(seller);
        bytes32 firstResourceHash = directory.hashResourceLeaf(firstResource);
        bytes32 secondResourceHash = directory.hashResourceLeaf(secondResource);
        assertEq(sellerHash, 0x88a72adf37498749e2ed8cfb315ae0361e3ce0110d0cd26de1c781a5d0e296cc);
        assertEq(firstResourceHash, 0x25e10978d4cbaf5a24c2e0e9da575cf86c643b24c3717a4c74ece695ecd44d1f);
        assertEq(secondResourceHash, 0x0516f574c50832dc78b8374e120338e98d90e6b1aa3821dae23236fa6edbc722);

        bytes32 merkleRoot = _hashPair(_hashPair(secondResourceHash, firstResourceHash), sellerHash);
        assertEq(merkleRoot, 0x1ecc06c3c00f64a7d77054f6d8131e03c74bf8d021b777f9c88de8fd3d710257);

        ServiceDirectory.DirectoryProposal memory proposal = ServiceDirectory.DirectoryProposal({
            versionId: 1,
            previousVersion: 0,
            previousRoot: bytes32(0),
            newRoot: merkleRoot,
            blobContentHash: 0x39d45c036716126c31d3747c96811eba0c29826acecf8d9d40ce7a3a7c1ab3be,
            locationsHash: 0xc0091241e31568f69766d46edaea56132172c8869a8098803c31c58bda8880f6,
            changeClass: ServiceDirectory.ChangeClass.PayoutOrAuthorityAffecting,
            requestedActivatesAt: 0,
            workflowId: bytes32(uint256(2)),
            workflowPayloadHash: bytes32(0),
            proposerNonce: 7
        });
        proposal.workflowPayloadHash = directory.directoryProposalWorkflowPayloadHash(proposal);
        assertEq(proposal.workflowPayloadHash, 0x55cf5f2f1243c758c246fa52357c21d5489ea21e854b5a81afa86c2e7de71bcf);
        assertEq(directory.hashProposal(proposal), 0x864f171371d53c8b9c48d46c789586119cba3f2a181fba2cdff01939388271e0);
        assertEq(keccak256(abi.encode(proposal)), 0xd3840894110cb6203f36cd34e5d5042933f88b9d9f5b6552552b68fc014b65d9);
        assertEq(directory.proposeVersion.selector, bytes4(0xfd0d35e6));
        assertEq(directory.approveVersion.selector, bytes4(0x0bf45ed9));

        ServiceDirectory.AdminActionAuthorization memory authorization = ServiceDirectory.AdminActionAuthorization({
            orgDomain: bytes32(uint256(1)),
            contractAddress: address(directory),
            chainId: 84532,
            authorityRole: directory.DIRECTORY_PUBLISHER_ROLE(),
            functionSelector: directory.proposeVersion.selector,
            payloadHash: keccak256(abi.encode(proposal)),
            adminOperationId: 0x4305b3a7d59ec5cb8e073a531c8596fbdd5b770ca615c44907376b210ee0d4fd,
            adminNonce: 41,
            adminEpoch: 1,
            validAfter: 1_787_745_570,
            validBefore: 1_787_746_170,
            workflowId: bytes32(uint256(2))
        });
        assertEq(
            directory.adminAuthorizationDigest(authorization),
            0x40849e12a6f903e1fbe06ce35bd6221718a22ff50d0e2201f3ece255f7847f58
        );
    }

    function _hashPair(bytes32 left, bytes32 right) private pure returns (bytes32) {
        return left < right ? keccak256(abi.encodePacked(left, right)) : keccak256(abi.encodePacked(right, left));
    }
}
