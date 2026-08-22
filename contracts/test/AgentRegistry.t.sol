// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {StdInvariant} from "forge-std/StdInvariant.sol";
import {AgentRegistry} from "../src/AgentRegistry.sol";

contract AgentRegistryGovernorHarness {
    function setRegistryAdmin(AgentRegistry registry, address admin) external {
        registry.setRegistryAdmin(admin);
    }
}

contract AgentRegistryTest is Test {
    uint256 internal constant ADMIN_KEY = 0xA11CE;
    uint256 internal constant ADMIN_B_KEY = 0xB0B;
    bytes32 internal constant ORG_DOMAIN = keccak256("org-domain");
    bytes32 internal constant WORKFLOW_ID = keccak256("workflow");

    AgentRegistryGovernorHarness internal governor;
    AgentRegistry internal registry;
    address internal relayer;

    function setUp() public {
        vm.warp(1_800_000_000);
        governor = new AgentRegistryGovernorHarness();
        registry = new AgentRegistry(address(governor), vm.addr(ADMIN_KEY), ORG_DOMAIN);
        relayer = makeAddr("keeper-relayer");
    }

    function testKeeperRelaysRegistrationAndEveryBindingIsStored() public {
        bytes32 policyHash = keccak256("policy-v1");
        AgentRegistry.AdminActionAuthorization memory authorization =
            _registerAuthorization("agent-one", policyHash, WORKFLOW_ID, 1);
        bytes32 expectedAgentId = registry.deriveAgentId(authorization.adminOperationId);
        vm.prank(relayer);
        bytes32 agentId =
            registry.register("agent-one", policyHash, WORKFLOW_ID, authorization, _sign(authorization, ADMIN_KEY));

        assertEq(agentId, expectedAgentId);
        AgentRegistry.Agent memory agent = registry.getAgent(agentId);
        assertEq(agent.label, "agent-one");
        assertEq(agent.labelHash, keccak256("agent-one"));
        assertEq(agent.policyHash, policyHash);
        assertEq(uint8(agent.status), uint8(AgentRegistry.Status.Active));
        assertEq(agent.registeredAt, block.timestamp);
        assertEq(agent.updatedAt, block.timestamp);
        assertEq(registry.agentCount(), 1);
        assertTrue(registry.usedAdminOperationIds(authorization.adminOperationId));
        assertTrue(registry.usedAdminNonces(authorization.adminNonce));
        assertEq(relayer.balance, 0);
    }

    function testPolicyAndStatusLifecycleRetirementIsFinal() public {
        bytes32 agentId = _register("lifecycle", keccak256("policy-v1"), 2);

        bytes32 policyV2 = keccak256("policy-v2");
        AgentRegistry.AdminActionAuthorization memory update = _policyAuthorization(agentId, policyV2, WORKFLOW_ID, 3);
        vm.prank(relayer);
        registry.updatePolicyHash(agentId, policyV2, WORKFLOW_ID, update, _sign(update, ADMIN_KEY));
        assertEq(registry.getAgent(agentId).policyHash, policyV2);

        _setStatus(agentId, AgentRegistry.Status.Suspended, 4);
        assertEq(uint8(registry.getAgent(agentId).status), uint8(AgentRegistry.Status.Suspended));
        _setStatus(agentId, AgentRegistry.Status.Active, 5);
        _setStatus(agentId, AgentRegistry.Status.Retired, 6);
        assertEq(uint8(registry.getAgent(agentId).status), uint8(AgentRegistry.Status.Retired));

        AgentRegistry.AdminActionAuthorization memory afterRetire =
            _statusAuthorization(agentId, AgentRegistry.Status.Active, WORKFLOW_ID, 7);
        bytes memory signature = _sign(afterRetire, ADMIN_KEY);
        vm.expectRevert(abi.encodeWithSelector(AgentRegistry.AgentRetired.selector, agentId));
        registry.setStatus(agentId, AgentRegistry.Status.Active, WORKFLOW_ID, afterRetire, signature);
        assertFalse(registry.usedAdminOperationIds(afterRetire.adminOperationId));

        AgentRegistry.AdminActionAuthorization memory policyAfterRetire =
            _policyAuthorization(agentId, keccak256("policy-v3"), WORKFLOW_ID, 8);
        signature = _sign(policyAfterRetire, ADMIN_KEY);
        vm.expectRevert(abi.encodeWithSelector(AgentRegistry.AgentRetired.selector, agentId));
        registry.updatePolicyHash(agentId, keccak256("policy-v3"), WORKFLOW_ID, policyAfterRetire, signature);
    }

    function testRegisterRejectsPayloadWorkflowSelectorRoleAndCrossContractMutation() public {
        bytes32 policyHash = keccak256("policy");
        AgentRegistry.AdminActionAuthorization memory authorization =
            _registerAuthorization("agent", policyHash, WORKFLOW_ID, 9);

        bytes memory signature = _sign(authorization, ADMIN_KEY);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("changed", policyHash, WORKFLOW_ID, authorization, signature);

        AgentRegistry.AdminActionAuthorization memory changed =
            _registerAuthorization("agent", policyHash, WORKFLOW_ID, 10);
        changed.workflowId = keccak256("wrong-workflow");
        signature = _sign(changed, ADMIN_KEY);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent", policyHash, WORKFLOW_ID, changed, signature);

        changed = _registerAuthorization("agent", policyHash, WORKFLOW_ID, 11);
        changed.functionSelector = AgentRegistry.setStatus.selector;
        signature = _sign(changed, ADMIN_KEY);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent", policyHash, WORKFLOW_ID, changed, signature);

        changed = _registerAuthorization("agent", policyHash, WORKFLOW_ID, 12);
        changed.authorityRole = keccak256("wrong-role");
        signature = _sign(changed, ADMIN_KEY);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent", policyHash, WORKFLOW_ID, changed, signature);

        changed = _registerAuthorization("agent", policyHash, WORKFLOW_ID, 13);
        changed.contractAddress = address(governor);
        signature = _sign(changed, ADMIN_KEY);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent", policyHash, WORKFLOW_ID, changed, signature);
    }

    function testReplayNonceWrongSignerExpiryAndLongWindowFailWithoutState() public {
        AgentRegistry.AdminActionAuthorization memory authorization =
            _registerAuthorization("agent", keccak256("policy"), WORKFLOW_ID, 14);
        bytes memory signature = _sign(authorization, ADMIN_KEY);
        registry.register("agent", keccak256("policy"), WORKFLOW_ID, authorization, signature);
        vm.expectRevert(
            abi.encodeWithSelector(AgentRegistry.AuthorizationUsed.selector, authorization.adminOperationId)
        );
        registry.register("agent", keccak256("policy"), WORKFLOW_ID, authorization, signature);

        AgentRegistry.AdminActionAuthorization memory sameNonce =
            _registerAuthorization("agent-two", keccak256("policy"), WORKFLOW_ID, 15);
        sameNonce.adminNonce = authorization.adminNonce;
        signature = _sign(sameNonce, ADMIN_KEY);
        vm.expectRevert(abi.encodeWithSelector(AgentRegistry.AuthorizationNonceUsed.selector, authorization.adminNonce));
        registry.register("agent-two", keccak256("policy"), WORKFLOW_ID, sameNonce, signature);

        AgentRegistry.AdminActionAuthorization memory wrongSigner =
            _registerAuthorization("agent-three", keccak256("policy"), WORKFLOW_ID, 16);
        signature = _sign(wrongSigner, ADMIN_B_KEY);
        vm.expectRevert(
            abi.encodeWithSelector(AgentRegistry.UnauthorizedSigner.selector, vm.addr(ADMIN_KEY), vm.addr(ADMIN_B_KEY))
        );
        registry.register("agent-three", keccak256("policy"), WORKFLOW_ID, wrongSigner, signature);

        AgentRegistry.AdminActionAuthorization memory expired =
            _registerAuthorization("agent-four", keccak256("policy"), WORKFLOW_ID, 17);
        signature = _sign(expired, ADMIN_KEY);
        vm.warp(expired.validBefore);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent-four", keccak256("policy"), WORKFLOW_ID, expired, signature);

        vm.warp(1_800_000_000);
        AgentRegistry.AdminActionAuthorization memory longWindow =
            _registerAuthorization("agent-five", keccak256("policy"), WORKFLOW_ID, 18);
        longWindow.validBefore = longWindow.validAfter + 601;
        signature = _sign(longWindow, ADMIN_KEY);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent-five", keccak256("policy"), WORKFLOW_ID, longWindow, signature);
        assertEq(registry.agentCount(), 1);
    }

    function testAdminRotationAtoBtoAMakesOldSignaturePermanentlyInvalid() public {
        AgentRegistry.AdminActionAuthorization memory authorization =
            _registerAuthorization("agent", keccak256("policy"), WORKFLOW_ID, 19);
        bytes memory oldSignature = _sign(authorization, ADMIN_KEY);
        governor.setRegistryAdmin(registry, vm.addr(ADMIN_B_KEY));
        governor.setRegistryAdmin(registry, vm.addr(ADMIN_KEY));
        assertEq(registry.registryAdminEpoch(), 3);
        vm.expectRevert(AgentRegistry.InvalidAuthorization.selector);
        registry.register("agent", keccak256("policy"), WORKFLOW_ID, authorization, oldSignature);
    }

    function testOnlyGovernorRotatesAdminAndConstructorRequiresContractGovernor() public {
        vm.expectRevert(abi.encodeWithSelector(AgentRegistry.NotGovernor.selector, address(this)));
        registry.setRegistryAdmin(vm.addr(ADMIN_B_KEY));

        vm.expectRevert(AgentRegistry.ZeroAddress.selector);
        governor.setRegistryAdmin(registry, address(0));

        vm.expectRevert(abi.encodeWithSelector(AgentRegistry.GovernorMustBeContract.selector, address(0xBEEF)));
        new AgentRegistry(address(0xBEEF), vm.addr(ADMIN_KEY), ORG_DOMAIN);

        vm.expectRevert(AgentRegistry.ZeroAddress.selector);
        new AgentRegistry(address(governor), address(0), ORG_DOMAIN);

        vm.expectRevert(AgentRegistry.ZeroAddress.selector);
        new AgentRegistry(address(governor), vm.addr(ADMIN_KEY), bytes32(0));
    }

    function testFuzzLabelBoundary(uint8 rawLength) public {
        uint256 length = (uint256(rawLength) % 80) + 1;
        bytes memory raw = new bytes(length);
        for (uint256 i; i < length; ++i) {
            raw[i] = "a";
        }
        string memory label = string(raw);
        AgentRegistry.AdminActionAuthorization memory authorization =
            _registerAuthorization(label, keccak256("policy"), WORKFLOW_ID, uint256(rawLength) + 100);
        bytes memory signature = _sign(authorization, ADMIN_KEY);
        if (length > registry.MAX_LABEL_BYTES()) {
            vm.expectRevert(AgentRegistry.InvalidLabel.selector);
            registry.register(label, keccak256("policy"), WORKFLOW_ID, authorization, signature);
            return;
        }
        bytes32 agentId = registry.register(label, keccak256("policy"), WORKFLOW_ID, authorization, signature);
        assertEq(bytes(registry.getAgent(agentId).label).length, length);
    }

    function testGoSolidityAdminAuthorizationGoldenVector() public {
        assertEq(
            registry.TYPED_DATA_MANIFEST_SHA256(), 0x87eee19267c1684f91e10454a8f1a26880a2434e65f5609791c54b803154bff5
        );
        address fixedRegistry = 0x1111111111111111111111111111111111111111;
        vm.etch(fixedRegistry, address(registry).code);
        vm.chainId(8453);
        AgentRegistry.AdminActionAuthorization memory authorization = AgentRegistry.AdminActionAuthorization({
            orgDomain: bytes32(uint256(1)),
            contractAddress: fixedRegistry,
            chainId: 8453,
            authorityRole: bytes32(uint256(2)),
            functionSelector: bytes4(0x85045a95),
            payloadHash: bytes32(uint256(3)),
            adminOperationId: bytes32(uint256(4)),
            adminNonce: 42,
            adminEpoch: 7,
            validAfter: 1_800_000_000,
            validBefore: 1_800_000_600,
            workflowId: bytes32(uint256(5))
        });
        assertEq(
            AgentRegistry(fixedRegistry).adminAuthorizationDigest(authorization),
            0x7b7f08dd98d5de1302ff68757f58617706be5d90c7e36bbb0d42defb51838327
        );
    }

    function _register(string memory label, bytes32 policyHash, uint256 sequence) internal returns (bytes32) {
        AgentRegistry.AdminActionAuthorization memory authorization =
            _registerAuthorization(label, policyHash, WORKFLOW_ID, sequence);
        vm.prank(relayer);
        return registry.register(label, policyHash, WORKFLOW_ID, authorization, _sign(authorization, ADMIN_KEY));
    }

    function _setStatus(bytes32 agentId, AgentRegistry.Status status, uint256 sequence) internal {
        AgentRegistry.AdminActionAuthorization memory authorization =
            _statusAuthorization(agentId, status, WORKFLOW_ID, sequence);
        vm.prank(relayer);
        registry.setStatus(agentId, status, WORKFLOW_ID, authorization, _sign(authorization, ADMIN_KEY));
    }

    function _registerAuthorization(string memory label, bytes32 policyHash, bytes32 workflowId, uint256 sequence)
        internal
        view
        returns (AgentRegistry.AdminActionAuthorization memory)
    {
        return _authorization(
            AgentRegistry.register.selector, keccak256(abi.encode(label, policyHash, workflowId)), workflowId, sequence
        );
    }

    function _policyAuthorization(bytes32 agentId, bytes32 policyHash, bytes32 workflowId, uint256 sequence)
        internal
        view
        returns (AgentRegistry.AdminActionAuthorization memory)
    {
        return _authorization(
            AgentRegistry.updatePolicyHash.selector,
            keccak256(abi.encode(agentId, policyHash, workflowId)),
            workflowId,
            sequence
        );
    }

    function _statusAuthorization(bytes32 agentId, AgentRegistry.Status status, bytes32 workflowId, uint256 sequence)
        internal
        view
        returns (AgentRegistry.AdminActionAuthorization memory)
    {
        return _authorization(
            AgentRegistry.setStatus.selector, keccak256(abi.encode(agentId, status, workflowId)), workflowId, sequence
        );
    }

    function _authorization(bytes4 selector, bytes32 payloadHash, bytes32 workflowId, uint256 sequence)
        internal
        view
        returns (AgentRegistry.AdminActionAuthorization memory)
    {
        return AgentRegistry.AdminActionAuthorization({
            orgDomain: ORG_DOMAIN,
            contractAddress: address(registry),
            chainId: block.chainid,
            authorityRole: registry.REGISTRY_ADMIN_ROLE(),
            functionSelector: selector,
            payloadHash: payloadHash,
            adminOperationId: keccak256(abi.encode("admin-operation", sequence)),
            adminNonce: sequence,
            adminEpoch: registry.registryAdminEpoch(),
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 10 minutes),
            workflowId: workflowId
        });
    }

    function _sign(AgentRegistry.AdminActionAuthorization memory authorization, uint256 key)
        internal
        view
        returns (bytes memory)
    {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, registry.adminAuthorizationDigest(authorization));
        return abi.encodePacked(r, s, v);
    }
}

contract AgentRegistryInvariantHandler is Test {
    AgentRegistry internal immutable registry;
    bytes32 internal immutable agentId;
    uint256 internal immutable adminKey;
    bytes32 internal immutable orgDomain;
    bytes32 internal immutable workflowId;

    AgentRegistry.Status public ghostStatus = AgentRegistry.Status.Active;
    uint256 public attempts;
    uint256 public successfulTransitions;
    uint256 public successesAfterRetirement;

    constructor(AgentRegistry registry_, bytes32 agentId_, uint256 adminKey_, bytes32 orgDomain_, bytes32 workflowId_) {
        registry = registry_;
        agentId = agentId_;
        adminKey = adminKey_;
        orgDomain = orgDomain_;
        workflowId = workflowId_;
    }

    function transition(uint8 rawStatus) external {
        attempts += 1;
        AgentRegistry.Status next = AgentRegistry.Status((uint256(rawStatus) % 3) + 1);
        AgentRegistry.AdminActionAuthorization memory authorization = AgentRegistry.AdminActionAuthorization({
            orgDomain: orgDomain,
            contractAddress: address(registry),
            chainId: block.chainid,
            authorityRole: registry.REGISTRY_ADMIN_ROLE(),
            functionSelector: AgentRegistry.setStatus.selector,
            payloadHash: keccak256(abi.encode(agentId, next, workflowId)),
            adminOperationId: keccak256(abi.encode("invariant-operation", attempts)),
            adminNonce: attempts + 1_000,
            adminEpoch: registry.registryAdminEpoch(),
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 10 minutes),
            workflowId: workflowId
        });
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(adminKey, registry.adminAuthorizationDigest(authorization));
        bytes memory signature = abi.encodePacked(r, s, v);
        AgentRegistry.Status before = ghostStatus;
        try registry.setStatus(agentId, next, workflowId, authorization, signature) {
            successfulTransitions += 1;
            if (before == AgentRegistry.Status.Retired) successesAfterRetirement += 1;
            ghostStatus = next;
        } catch {}
    }
}

contract AgentRegistryInvariantTest is StdInvariant, Test {
    uint256 internal constant ADMIN_KEY = 0xA11CE;
    bytes32 internal constant ORG_DOMAIN = keccak256("org-domain");
    bytes32 internal constant WORKFLOW_ID = keccak256("workflow");

    AgentRegistry internal registry;
    bytes32 internal agentId;
    AgentRegistryInvariantHandler internal handler;

    function setUp() public {
        vm.warp(1_800_000_000);
        AgentRegistryGovernorHarness governor = new AgentRegistryGovernorHarness();
        registry = new AgentRegistry(address(governor), vm.addr(ADMIN_KEY), ORG_DOMAIN);
        AgentRegistry.AdminActionAuthorization memory authorization = AgentRegistry.AdminActionAuthorization({
            orgDomain: ORG_DOMAIN,
            contractAddress: address(registry),
            chainId: block.chainid,
            authorityRole: registry.REGISTRY_ADMIN_ROLE(),
            functionSelector: AgentRegistry.register.selector,
            payloadHash: keccak256(abi.encode("invariant-agent", keccak256("policy"), WORKFLOW_ID)),
            adminOperationId: keccak256("invariant-registration"),
            adminNonce: 1,
            adminEpoch: 1,
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 10 minutes),
            workflowId: WORKFLOW_ID
        });
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(ADMIN_KEY, registry.adminAuthorizationDigest(authorization));
        agentId = registry.register(
            "invariant-agent", keccak256("policy"), WORKFLOW_ID, authorization, abi.encodePacked(r, s, v)
        );
        handler = new AgentRegistryInvariantHandler(registry, agentId, ADMIN_KEY, ORG_DOMAIN, WORKFLOW_ID);
        targetContract(address(handler));
    }

    function invariantRetirementIsAbsorbingAndGhostMatchesChain() public view {
        assertEq(handler.successesAfterRetirement(), 0);
        assertEq(uint8(registry.getAgent(agentId).status), uint8(handler.ghostStatus()));
    }

    function invariantRegistrationIdentityAndCountNeverChange() public view {
        AgentRegistry.Agent memory agent = registry.getAgent(agentId);
        assertEq(agent.labelHash, keccak256("invariant-agent"));
        assertEq(agent.registeredAt, 1_800_000_000);
        assertEq(registry.agentCount(), 1);
    }
}
