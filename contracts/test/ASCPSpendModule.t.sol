// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {Test} from "forge-std/Test.sol";
import {StdInvariant} from "forge-std/StdInvariant.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ASCPSpendModule, IModuleSafe} from "../src/ASCPSpendModule.sol";
import {ASCPCallEscrow, IServiceDirectory} from "../src/ASCPCallEscrow.sol";
import {ServiceDirectory} from "../src/ServiceDirectory.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract SpendModuleDirectoryHarness is IServiceDirectory {
    uint64 public constant VERSION = 9;
    mapping(bytes32 sellerId => bool) public pausedSeller;
    mapping(address key => bool) public quoteKeyRevoked;

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
}

contract SpendModuleSafeHarness is IModuleSafe {
    address public module;
    bool public returnFalse;
    address public lastTarget;
    uint256 public lastValue;
    uint8 public lastOperation;
    uint256 public moduleExecutions;

    function setModule(address module_) external {
        module = module_;
    }

    function setReturnFalse(bool value) external {
        returnFalse = value;
    }

    function ownerCall(address target, bytes calldata data) external returns (bytes memory result) {
        (bool success, bytes memory returned) = target.call(data);
        require(success, "OWNER_CALL_FAILED");
        return returned;
    }

    function execTransactionFromModule(address to, uint256 value, bytes memory data, uint8 operation)
        external
        returns (bool success)
    {
        require(msg.sender == module, "NOT_MODULE");
        lastTarget = to;
        lastValue = value;
        lastOperation = operation;
        if (returnFalse) return false;
        require(operation == 0, "NOT_CALL");
        (success,) = to.call{value: value}(data);
        if (success) moduleExecutions += 1;
    }
}

contract AllowlistedSpenderHarness {}

contract ASCPSpendModuleTest is Test {
    uint256 internal constant AUTHOR_KEY = 0xA11CE;
    uint256 internal constant AUTHOR_B_KEY = 0xB0B;
    bytes32 internal constant ORG_DOMAIN = keccak256("org-domain");

    MockUSDC internal usdc;
    SpendModuleDirectoryHarness internal directory;
    SpendModuleSafeHarness internal safe;
    ASCPCallEscrow internal escrow;
    ASCPSpendModule internal module;
    AllowlistedSpenderHarness internal allowanceSpender;
    ServiceDirectory.SellerLeaf internal seller;
    ServiceDirectory.ResourceLeaf internal resource;

    function setUp() public {
        vm.warp(1_800_000_000);
        usdc = new MockUSDC();
        directory = new SpendModuleDirectoryHarness();
        safe = new SpendModuleSafeHarness();
        escrow = new ASCPCallEscrow(IERC20(address(usdc)), directory, address(safe), address(safe));
        module = new ASCPSpendModule(
            address(safe),
            IERC20(address(usdc)),
            vm.addr(AUTHOR_KEY),
            ASCPSpendModule.Caps({perTransaction: 1_000, perDay: 2_000, allowanceCeiling: 10_000})
        );
        allowanceSpender = new AllowlistedSpenderHarness();
        safe.setModule(address(module));
        _setAllowlist(address(escrow), address(escrow).codehash, 1);
        _setAllowlist(address(allowanceSpender), address(allowanceSpender).codehash, 2);
        _ownerCall(address(usdc), abi.encodeCall(IERC20.approve, (address(escrow), type(uint256).max)));
        usdc.mint(address(safe), 1_000_000);

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
            price: 400,
            escrowSupported: true,
            verificationSpecHash: keccak256("verification-spec"),
            declaredWorkTime: 300,
            verificationBudgetSeconds: 120
        });
    }

    function testExecuteLockUsesOnlyCallZeroValueAndConsumesPermanentPrincipal() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 1);
        module.executeLock(payload, authorization, _signLock(authorization, AUTHOR_KEY));

        assertTrue(module.usedNonces(authorization.nonce));
        assertEq(module.executedPrincipal(), 400);
        assertEq(module.dayExecutedPrincipal(block.timestamp / 1 days), 400);
        assertEq(safe.lastTarget(), address(escrow));
        assertEq(safe.lastValue(), 0);
        assertEq(safe.lastOperation(), 0);
        assertEq(safe.moduleExecutions(), 1);
        assertEq(usdc.balanceOf(address(escrow)), 400);
    }

    function testLockReplayAndCrossSafeReplayFail() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 2);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        module.executeLock(payload, authorization, signature);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.NonceAlreadyUsed.selector, authorization.nonce));
        module.executeLock(payload, authorization, signature);

        SpendModuleSafeHarness otherSafe = new SpendModuleSafeHarness();
        ASCPSpendModule otherModule = new ASCPSpendModule(
            address(otherSafe),
            IERC20(address(usdc)),
            vm.addr(AUTHOR_KEY),
            ASCPSpendModule.Caps({perTransaction: 1_000, perDay: 2_000, allowanceCeiling: 10_000})
        );
        otherSafe.setModule(address(otherModule));
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        otherModule.executeLock(payload, authorization, signature);
    }

    function testLockRejectsMutatedCalldataAmountCommitmentAndEpoch() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 3);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);

        bytes memory changedPayload = bytes.concat(payload, hex"00");
        vm.expectRevert(ASCPSpendModule.CalldataMismatch.selector);
        module.executeLock(changedPayload, authorization, signature);

        ASCPSpendModule.LockAuthorization memory changed = authorization;
        changed.calldataHash = keccak256(changedPayload);
        signature = _signLock(changed, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidLockPayload.selector);
        module.executeLock(changedPayload, changed, signature);

        (, changed) = _lockAuthorization(400, 3);
        changed.amount += 1;
        signature = _signLock(changed, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidLockPayload.selector);
        module.executeLock(payload, changed, signature);

        (, changed) = _lockAuthorization(400, 3);
        changed.commitmentHash = keccak256("wrong-commitment");
        signature = _signLock(changed, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidLockPayload.selector);
        module.executeLock(payload, changed, signature);

        (, changed) = _lockAuthorization(400, 3);
        changed.authorizerEpoch += 1;
        signature = _signLock(changed, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeLock(payload, changed, signature);

        (, changed) = _lockAuthorization(400, 3);
        changed.module = address(0xBEEF);
        signature = _signLock(changed, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeLock(payload, changed, signature);

        (, changed) = _lockAuthorization(400, 3);
        changed.leadershipEpoch = 0;
        signature = _signLock(changed, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeLock(payload, changed, signature);
    }

    function testWrongSelectorAndUnallowlistedTargetFailClosed() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 4);
        payload[0] = bytes1(uint8(payload[0]) ^ 1);
        authorization.calldataHash = keccak256(payload);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidLockPayload.selector);
        module.executeLock(payload, authorization, signature);

        (, authorization) = _lockAuthorization(400, 5);
        authorization.escrow = address(0xBEEF);
        payload = _lockPayload(400, 5);
        signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.EscrowNotAllowlisted.selector, address(0xBEEF)));
        module.executeLock(payload, authorization, signature);
    }

    function testDownstreamFalseRollsBackNonceAndCounters() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 6);
        safe.setReturnFalse(true);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.SafeExecutionFailed.selector);
        module.executeLock(payload, authorization, signature);
        assertFalse(module.usedNonces(authorization.nonce));
        assertEq(module.executedPrincipal(), 0);
        assertEq(module.dayExecutedPrincipal(block.timestamp / 1 days), 0);
    }

    function testAuthorizerRotationAtoBtoAMakesOldSignaturePermanentlyInvalid() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 7);
        bytes memory oldSignature = _signLock(authorization, AUTHOR_KEY);
        _setAuthorizer(vm.addr(AUTHOR_B_KEY), 3);
        _setAuthorizer(vm.addr(AUTHOR_KEY), 4);
        assertEq(module.authorizerEpoch(), 3);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeLock(payload, authorization, oldSignature);
    }

    function testPauseDoesNotConsumeAndSafeInvalidationIsPermanent() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 8);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        _setPause(true, 5);
        vm.expectRevert(ASCPSpendModule.EmergencyPaused.selector);
        module.executeLock(payload, authorization, signature);
        assertFalse(module.usedNonces(authorization.nonce));

        _setPause(false, 6);
        uint256[] memory nonces = new uint256[](1);
        nonces[0] = authorization.nonce;
        _invalidateNonces(nonces, 7);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.NonceAlreadyUsed.selector, authorization.nonce));
        module.executeLock(payload, authorization, signature);
    }

    function testAuthorizationWindowAndCapsAreEnforced() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(1_001, 9);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.PerTransactionCapExceeded.selector, 1_001, 1_000));
        module.executeLock(payload, authorization, signature);

        (payload, authorization) = _lockAuthorization(400, 10);
        authorization.validBefore = authorization.validAfter + 601;
        signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.AuthorizationWindowInvalid.selector);
        module.executeLock(payload, authorization, signature);

        ASCPSpendModule.Caps memory newCaps =
            ASCPSpendModule.Caps({perTransaction: 2_000, perDay: 4_000, allowanceCeiling: 20_000});
        _scheduleCaps(newCaps, 8);
        vm.expectRevert(
            abi.encodeWithSelector(ASCPSpendModule.CapsAlreadyPending.selector, uint64(block.timestamp + 1 hours))
        );
        vm.prank(address(safe));
        module.scheduleCaps(newCaps, keccak256("other-caps-workflow"), keccak256("other-caps-payload"));
        vm.expectRevert(
            abi.encodeWithSelector(ASCPSpendModule.CapsNotReady.selector, uint64(block.timestamp + 1 hours))
        );
        module.activateCaps();
        vm.warp(block.timestamp + 1 hours);
        module.activateCaps();
        (uint256 perTransaction, uint256 perDay, uint256 ceiling) = module.caps();
        assertEq(perTransaction, 2_000);
        assertEq(perDay, 4_000);
        assertEq(ceiling, 20_000);
    }

    function testAllowanceRequiresExactStateAndDoesNotConsumeSpendCaps() public {
        (bytes memory payload, ASCPSpendModule.AllowanceAuthorization memory authorization) =
            _allowanceAuthorization(0, 8_000, 11);
        module.executeAllowance(payload, authorization, _signAllowance(authorization, AUTHOR_KEY));
        assertEq(usdc.allowance(address(safe), address(allowanceSpender)), 8_000);
        assertTrue(module.usedNonces(authorization.nonce));
        assertEq(module.executedPrincipal(), 0);
        assertEq(module.dayExecutedPrincipal(block.timestamp / 1 days), 0);
    }

    function testAllowanceDriftCeilingSuffixAndFalseReturnRollback() public {
        (bytes memory payload, ASCPSpendModule.AllowanceAuthorization memory authorization) =
            _allowanceAuthorization(1, 8_000, 12);
        bytes memory signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.AllowanceMismatch.selector, 1, 0));
        module.executeAllowance(payload, authorization, signature);

        (payload, authorization) = _allowanceAuthorization(0, 10_001, 13);
        signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.AllowanceCeilingExceeded.selector, 10_001, 10_000));
        module.executeAllowance(payload, authorization, signature);

        (payload, authorization) = _allowanceAuthorization(0, 8_000, 14);
        payload = bytes.concat(payload, hex"00");
        signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAllowancePayload.selector);
        module.executeAllowance(payload, authorization, signature);

        (payload, authorization) = _allowanceAuthorization(0, 8_000, 15);
        safe.setReturnFalse(true);
        signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.SafeExecutionFailed.selector);
        module.executeAllowance(payload, authorization, signature);
        assertFalse(module.usedNonces(authorization.nonce));
        assertEq(usdc.allowance(address(safe), address(allowanceSpender)), 0);
    }

    function testFuzzLockAmountCannotCrossPerTransactionCap(uint96 rawAmount) public {
        uint256 amount = 1_001 + (uint256(rawAmount) % (uint256(type(uint32).max) - 1_000));
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(amount, 16);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(
            abi.encodeWithSelector(ASCPSpendModule.PerTransactionCapExceeded.selector, amount, uint256(1_000))
        );
        module.executeLock(payload, authorization, signature);
    }

    function testExpiredWrongSignerAndMutatedPayeeFailClosed() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(400, 17);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        vm.warp(authorization.validBefore);
        vm.expectRevert(ASCPSpendModule.AuthorizationWindowInvalid.selector);
        module.executeLock(payload, authorization, signature);

        vm.warp(1_800_000_000);
        (payload, authorization) = _lockAuthorization(400, 18);
        signature = _signLock(authorization, AUTHOR_B_KEY);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.InvalidSignature.selector, vm.addr(AUTHOR_B_KEY)));
        module.executeLock(payload, authorization, signature);

        (payload, authorization) = _lockAuthorization(400, 19);
        ASCPCallEscrow.ExecutionCommitment memory commitment = _commitment(400, 19);
        ServiceDirectory.SellerLeaf memory changedSeller = seller;
        changedSeller.payoutAddress = makeAddr("attacker-payee");
        commitment.payTo = changedSeller.payoutAddress;
        payload = abi.encodeCall(
            ASCPCallEscrow.lockCall, (commitment, changedSeller, _resource(400), new bytes32[](0), new bytes32[](0))
        );
        authorization.calldataHash = keccak256(payload);
        signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidLockPayload.selector);
        module.executeLock(payload, authorization, signature);
    }

    function testDailyCapRejectsThirdLockWithoutConsumingNonce() public {
        (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization) = _lockAuthorization(1_000, 20);
        module.executeLock(payload, authorization, _signLock(authorization, AUTHOR_KEY));
        (payload, authorization) = _lockAuthorization(1_000, 21);
        module.executeLock(payload, authorization, _signLock(authorization, AUTHOR_KEY));
        (payload, authorization) = _lockAuthorization(1, 22);
        bytes memory signature = _signLock(authorization, AUTHOR_KEY);
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.DailyCapExceeded.selector, 2_001, 2_000));
        module.executeLock(payload, authorization, signature);
        assertFalse(module.usedNonces(authorization.nonce));
        assertEq(module.executedPrincipal(), 2_000);
    }

    function testWrongAllowanceTokenSpenderAndUnauthorizedGovernanceFail() public {
        (bytes memory payload, ASCPSpendModule.AllowanceAuthorization memory authorization) =
            _allowanceAuthorization(0, 800, 23);
        authorization.token = address(0xBEEF);
        bytes memory signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeAllowance(payload, authorization, signature);

        (payload, authorization) = _allowanceAuthorization(0, 800, 24);
        authorization.spender = address(escrow);
        signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAllowancePayload.selector);
        module.executeAllowance(payload, authorization, signature);

        (payload, authorization) = _allowanceAuthorization(0, 800, 25);
        authorization.module = address(0xBEEF);
        signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeAllowance(payload, authorization, signature);

        (payload, authorization) = _allowanceAuthorization(0, 800, 26);
        authorization.leadershipEpoch = 0;
        signature = _signAllowance(authorization, AUTHOR_KEY);
        vm.expectRevert(ASCPSpendModule.InvalidAuthorization.selector);
        module.executeAllowance(payload, authorization, signature);

        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.NotSafe.selector, address(this)));
        module.setEmergencyPause(true, bytes32(uint256(1)), bytes32(uint256(2)));
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.NotSafe.selector, address(this)));
        module.setSpendAuthorizer(vm.addr(AUTHOR_B_KEY), bytes32(uint256(1)), bytes32(uint256(2)));
    }

    function testGovernanceRejectsPayloadSubstitutionAndEmitsWorkflowBinding() public {
        address next = vm.addr(AUTHOR_B_KEY);
        bytes32 workflowId = keccak256("module-workflow");
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId,
            module.setSpendAuthorizer.selector,
            keccak256(abi.encode(module.spendAuthorizer(), module.authorizerEpoch(), next))
        );

        vm.expectRevert(ASCPSpendModule.InvalidWorkflowBinding.selector);
        vm.prank(address(safe));
        module.setSpendAuthorizer(vm.addr(0xC0FFEE), workflowId, payloadHash);

        vm.expectRevert(ASCPSpendModule.InvalidWorkflowBinding.selector);
        vm.prank(address(safe));
        module.setSpendAuthorizer(next, keccak256("wrong-workflow"), payloadHash);

        vm.expectEmit(true, true, true, true);
        emit ASCPSpendModule.GovernanceWorkflowBound(workflowId, payloadHash, module.setSpendAuthorizer.selector);
        _ownerCall(address(module), abi.encodeCall(ASCPSpendModule.setSpendAuthorizer, (next, workflowId, payloadHash)));
    }

    function testGovernanceRejectsNoopAllowlistAndInvalidNonceBatch() public {
        address target = address(escrow);
        bytes32 currentCodeHash = module.escrowAllowlist(target);
        bytes32 workflowId = keccak256("noop-allowlist-workflow");
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId,
            module.setEscrowAllowlist.selector,
            keccak256(abi.encode(target, currentCodeHash, currentCodeHash))
        );
        vm.expectRevert(
            abi.encodeWithSelector(ASCPSpendModule.EscrowAllowlistUnchanged.selector, target, currentCodeHash)
        );
        vm.prank(address(safe));
        module.setEscrowAllowlist(target, currentCodeHash, workflowId, payloadHash);

        uint256[] memory empty = new uint256[](0);
        payloadHash =
            module.governancePayloadHash(workflowId, module.invalidateNonces.selector, keccak256(abi.encode(empty)));
        vm.expectRevert(abi.encodeWithSelector(ASCPSpendModule.InvalidNonceInvalidationCount.selector, 0));
        vm.prank(address(safe));
        module.invalidateNonces(empty, workflowId, payloadHash);

        uint256[] memory oversized = new uint256[](module.MAX_GOVERNANCE_NONCE_INVALIDATIONS() + 1);
        payloadHash = module.governancePayloadHash(
            workflowId, module.invalidateNonces.selector, keccak256(abi.encode(oversized))
        );
        vm.expectRevert(
            abi.encodeWithSelector(ASCPSpendModule.InvalidNonceInvalidationCount.selector, oversized.length)
        );
        vm.prank(address(safe));
        module.invalidateNonces(oversized, workflowId, payloadHash);

        (uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling) = module.caps();
        ASCPSpendModule.Caps memory unchangedCaps =
            ASCPSpendModule.Caps({perTransaction: perTransaction, perDay: perDay, allowanceCeiling: allowanceCeiling});
        payloadHash = module.governancePayloadHash(
            workflowId,
            module.scheduleCaps.selector,
            keccak256(abi.encode(perTransaction, perDay, allowanceCeiling, perTransaction, perDay, allowanceCeiling))
        );
        vm.expectRevert(ASCPSpendModule.CapsUnchanged.selector);
        vm.prank(address(safe));
        module.scheduleCaps(unchangedCaps, workflowId, payloadHash);
    }

    function testGovernancePayloadMatchesPublishedGoGoldenVector() public {
        address fixedModule = 0x1111111111111111111111111111111111111111;
        vm.etch(fixedModule, address(module).code);
        vm.chainId(8453);
        ASCPSpendModule target = ASCPSpendModule(fixedModule);
        bytes32 workflowId = bytes32(uint256(10));
        assertEq(
            target.governancePayloadHash(
                workflowId,
                target.setSpendAuthorizer.selector,
                keccak256(
                    abi.encode(
                        0x2222222222222222222222222222222222222222,
                        uint64(9),
                        0x3333333333333333333333333333333333333333
                    )
                )
            ),
            0x3c4e6de0140852b75b2db79942976748a0d8b0cee3249a5c39044a9675ab720c
        );
        assertEq(
            target.governancePayloadHash(
                workflowId,
                target.setEscrowAllowlist.selector,
                keccak256(
                    abi.encode(0x3333333333333333333333333333333333333333, bytes32(uint256(1)), bytes32(uint256(2)))
                )
            ),
            0x584236800edc934197628ebfb3f2148ea00694291c6a6e74565208bbd3533544
        );
        assertEq(
            target.governancePayloadHash(
                workflowId, target.scheduleCaps.selector, keccak256(abi.encode(uint256(400), 800, 1000, 500, 900, 1200))
            ),
            0x0064db44b287bf57c4b40240ea6588aa2bd230d6349aae90da6f03f0c59833d4
        );
        assertEq(
            target.governancePayloadHash(
                workflowId, target.setEmergencyPause.selector, keccak256(abi.encode(false, true))
            ),
            0x92c84c2ef61ba58353d2bbf13cec02f789e014c8a076923dc2b22011c106e890
        );
        uint256[] memory nonces = new uint256[](2);
        nonces[0] = 3;
        nonces[1] = 4;
        assertEq(
            target.governancePayloadHash(workflowId, target.invalidateNonces.selector, keccak256(abi.encode(nonces))),
            0x9401eab51918e1bfd4c27edcded7ddcd71080d4d959796c42f0d8d730c6d24ac
        );
    }

    function testGoAndSolidityAuthorizationGoldenVectors() public {
        assertEq(
            module.TYPED_DATA_MANIFEST_SHA256(), 0x87eee19267c1684f91e10454a8f1a26880a2434e65f5609791c54b803154bff5
        );
        address fixedModule = 0x1111111111111111111111111111111111111111;
        vm.etch(fixedModule, address(module).code);
        vm.chainId(8453);
        ASCPSpendModule.LockAuthorization memory lockAuthorization = ASCPSpendModule.LockAuthorization({
            orgDomain: bytes32(uint256(1)),
            safe: 0x2222222222222222222222222222222222222222,
            module: fixedModule,
            operationId: bytes32(uint256(2)),
            commitmentHash: bytes32(uint256(3)),
            calldataHash: bytes32(uint256(4)),
            escrow: 0x3333333333333333333333333333333333333333,
            amount: 400,
            validAfter: 1_800_000_000,
            validBefore: 1_800_000_600,
            nonce: 5,
            leadershipEpoch: 7,
            authorizerEpoch: 9
        });
        bytes32 lockDigest = ASCPSpendModule(fixedModule).lockAuthorizationDigest(lockAuthorization);
        assertEq(lockDigest, 0xba4ea42568fd8e82f9586900a88e4ede6bde0a7c8b3f3293c51e75fad1d7f37e);
        assertEq(
            ecrecover(
                lockDigest,
                28,
                0xed1fe8f5ddfbff8e97023b53c23afb74c703012de97f1e3b3a41fb1405ceee0c,
                0x4310815481eff607c2d5d0064dfe9a47672a2084ddfeab30dabdd8531893bb5c
            ),
            0xe05fcC23807536bEe418f142D19fa0d21BB0cfF7
        );
        ASCPSpendModule.AllowanceAuthorization memory allowanceAuthorization = ASCPSpendModule.AllowanceAuthorization({
            orgDomain: bytes32(uint256(1)),
            safe: 0x2222222222222222222222222222222222222222,
            module: fixedModule,
            adminOperationId: bytes32(uint256(6)),
            token: 0x4444444444444444444444444444444444444444,
            spender: 0x5555555555555555555555555555555555555555,
            expectedAllowance: 400,
            newAllowance: 800,
            nonce: 7,
            validAfter: 1_800_000_000,
            validBefore: 1_800_000_600,
            leadershipEpoch: 7,
            authorizerEpoch: 9
        });
        bytes32 allowanceDigest = ASCPSpendModule(fixedModule).allowanceAuthorizationDigest(allowanceAuthorization);
        assertEq(allowanceDigest, 0x5f174440bb7236f3922420bd1142502b22cee71975c211b44f888249a7eeaf30);
        assertEq(
            ecrecover(
                allowanceDigest,
                28,
                0x9f649d3e714619743a4cd7cde10e7d1540f3864338a4cdba37481030bdc62183,
                0x5bd6f636c50f45cf22b381ff867258660d2256a08ce63f2028d81f01af12b545
            ),
            0xe05fcC23807536bEe418f142D19fa0d21BB0cfF7
        );
    }

    function _lockAuthorization(uint256 amount, uint256 sequence)
        internal
        view
        returns (bytes memory payload, ASCPSpendModule.LockAuthorization memory authorization)
    {
        payload = _lockPayload(amount, sequence);
        ASCPCallEscrow.ExecutionCommitment memory commitment = _commitment(amount, sequence);
        authorization = ASCPSpendModule.LockAuthorization({
            orgDomain: ORG_DOMAIN,
            safe: address(safe),
            module: address(module),
            operationId: commitment.operationId,
            commitmentHash: escrow.executionCommitmentDigest(commitment, address(escrow), block.chainid),
            calldataHash: keccak256(payload),
            escrow: address(escrow),
            amount: amount,
            nonce: sequence,
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 10 minutes),
            leadershipEpoch: 1,
            authorizerEpoch: module.authorizerEpoch()
        });
    }

    function _lockPayload(uint256 amount, uint256 sequence) internal view returns (bytes memory) {
        return abi.encodeCall(
            ASCPCallEscrow.lockCall,
            (_commitment(amount, sequence), seller, _resource(amount), new bytes32[](0), new bytes32[](0))
        );
    }

    function _commitment(uint256 amount, uint256 sequence)
        internal
        view
        returns (ASCPCallEscrow.ExecutionCommitment memory)
    {
        return ASCPCallEscrow.ExecutionCommitment({
            orgDomain: ORG_DOMAIN,
            operationId: keccak256(abi.encode("operation", sequence)),
            rail: escrow.RAIL_ESCROW(),
            schemeVersion: escrow.SCHEME_VERSION_V1(),
            protection: escrow.PROTECTION_ESCROW(),
            escrowContract: address(escrow),
            purchaseSpecHash: keccak256("purchase-spec"),
            quoteHash: keccak256("quote"),
            verificationSpecHash: resource.verificationSpecHash,
            declaredWorkTime: resource.declaredWorkTime,
            verificationBudgetSeconds: resource.verificationBudgetSeconds,
            directoryVersion: directory.VERSION(),
            sellerId: seller.sellerId,
            resourceId: resource.resourceId,
            payTo: seller.payoutAddress,
            ackAuthority: seller.ackAuthority,
            amount: amount,
            chainId: block.chainid,
            asset: address(usdc),
            quoteExpiresAt: uint64(block.timestamp + 20 minutes),
            acceptBy: uint64(block.timestamp + 20 minutes),
            deliverBy: uint64(block.timestamp + 40 minutes),
            settleBy: uint64(block.timestamp + 60 minutes)
        });
    }

    function _resource(uint256 amount) internal view returns (ServiceDirectory.ResourceLeaf memory value) {
        value = resource;
        value.price = amount;
    }

    function _allowanceAuthorization(uint256 expected, uint256 next, uint256 sequence)
        internal
        view
        returns (bytes memory payload, ASCPSpendModule.AllowanceAuthorization memory authorization)
    {
        payload = abi.encodeCall(IERC20.approve, (address(allowanceSpender), next));
        authorization = ASCPSpendModule.AllowanceAuthorization({
            orgDomain: ORG_DOMAIN,
            safe: address(safe),
            module: address(module),
            adminOperationId: keccak256(abi.encode("allowance", sequence)),
            token: address(usdc),
            spender: address(allowanceSpender),
            expectedAllowance: expected,
            newAllowance: next,
            nonce: sequence,
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 10 minutes),
            leadershipEpoch: 1,
            authorizerEpoch: module.authorizerEpoch()
        });
    }

    function _signLock(ASCPSpendModule.LockAuthorization memory authorization, uint256 key)
        internal
        view
        returns (bytes memory)
    {
        return _signature(module.lockAuthorizationDigest(authorization), key);
    }

    function _signAllowance(ASCPSpendModule.AllowanceAuthorization memory authorization, uint256 key)
        internal
        view
        returns (bytes memory)
    {
        return _signature(module.allowanceAuthorizationDigest(authorization), key);
    }

    function _signature(bytes32 digest, uint256 key) internal pure returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, digest);
        return abi.encodePacked(r, s, v);
    }

    function _setAuthorizer(address next, uint256 sequence) internal {
        bytes32 workflowId = keccak256(abi.encode("authorizer-workflow", sequence));
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId,
            module.setSpendAuthorizer.selector,
            keccak256(abi.encode(module.spendAuthorizer(), module.authorizerEpoch(), next))
        );
        _ownerCall(address(module), abi.encodeCall(ASCPSpendModule.setSpendAuthorizer, (next, workflowId, payloadHash)));
    }

    function _setAllowlist(address target, bytes32 nextCodeHash, uint256 sequence) internal {
        bytes32 workflowId = keccak256(abi.encode("allowlist-workflow", sequence));
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId,
            module.setEscrowAllowlist.selector,
            keccak256(abi.encode(target, module.escrowAllowlist(target), nextCodeHash))
        );
        _ownerCall(
            address(module),
            abi.encodeCall(ASCPSpendModule.setEscrowAllowlist, (target, nextCodeHash, workflowId, payloadHash))
        );
    }

    function _scheduleCaps(ASCPSpendModule.Caps memory next, uint256 sequence) internal {
        (uint256 perTransaction, uint256 perDay, uint256 allowanceCeiling) = module.caps();
        bytes32 workflowId = keccak256(abi.encode("caps-workflow", sequence));
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId,
            module.scheduleCaps.selector,
            keccak256(
                abi.encode(
                    perTransaction, perDay, allowanceCeiling, next.perTransaction, next.perDay, next.allowanceCeiling
                )
            )
        );
        _ownerCall(address(module), abi.encodeCall(ASCPSpendModule.scheduleCaps, (next, workflowId, payloadHash)));
    }

    function _setPause(bool paused, uint256 sequence) internal {
        bytes32 workflowId = keccak256(abi.encode("pause-workflow", sequence));
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId, module.setEmergencyPause.selector, keccak256(abi.encode(module.emergencyPaused(), paused))
        );
        _ownerCall(
            address(module), abi.encodeCall(ASCPSpendModule.setEmergencyPause, (paused, workflowId, payloadHash))
        );
    }

    function _invalidateNonces(uint256[] memory nonces, uint256 sequence) internal {
        bytes32 workflowId = keccak256(abi.encode("invalidate-workflow", sequence));
        bytes32 payloadHash =
            module.governancePayloadHash(workflowId, module.invalidateNonces.selector, keccak256(abi.encode(nonces)));
        _ownerCall(address(module), abi.encodeCall(ASCPSpendModule.invalidateNonces, (nonces, workflowId, payloadHash)));
    }

    function _ownerCall(address target, bytes memory data) internal {
        safe.ownerCall(target, data);
    }
}

contract ASCPSpendModuleInvariantHandler is Test {
    ASCPSpendModule internal immutable module;
    ASCPCallEscrow internal immutable escrow;
    MockUSDC internal immutable usdc;
    SpendModuleDirectoryHarness internal immutable directory;
    SpendModuleSafeHarness internal immutable safe;
    uint256 internal immutable authorKey;
    ServiceDirectory.SellerLeaf internal seller;
    ServiceDirectory.ResourceLeaf internal resource;

    uint256 public attempts;
    uint256 public successfulPrincipal;
    uint256 public successfulLocks;

    constructor(
        ASCPSpendModule module_,
        ASCPCallEscrow escrow_,
        MockUSDC usdc_,
        SpendModuleDirectoryHarness directory_,
        SpendModuleSafeHarness safe_,
        uint256 authorKey_,
        ServiceDirectory.SellerLeaf memory seller_,
        ServiceDirectory.ResourceLeaf memory resource_
    ) {
        module = module_;
        escrow = escrow_;
        usdc = usdc_;
        directory = directory_;
        safe = safe_;
        authorKey = authorKey_;
        seller = seller_;
        resource = resource_;
    }

    function executeBoundedLock(uint96 rawAmount, bytes32 seed) external {
        attempts += 1;
        uint256 amount = (uint256(rawAmount) % 1_000) + 1;
        uint256 nonce = uint256(keccak256(abi.encode("invariant-nonce", attempts, seed)));
        ASCPCallEscrow.ExecutionCommitment memory commitment = ASCPCallEscrow.ExecutionCommitment({
            orgDomain: keccak256("org-domain"),
            operationId: keccak256(abi.encode("invariant-operation", attempts, seed)),
            rail: escrow.RAIL_ESCROW(),
            schemeVersion: escrow.SCHEME_VERSION_V1(),
            protection: escrow.PROTECTION_ESCROW(),
            escrowContract: address(escrow),
            purchaseSpecHash: keccak256(abi.encode("purchase", attempts, seed)),
            quoteHash: keccak256(abi.encode("quote", attempts, seed)),
            verificationSpecHash: resource.verificationSpecHash,
            declaredWorkTime: resource.declaredWorkTime,
            verificationBudgetSeconds: resource.verificationBudgetSeconds,
            directoryVersion: directory.VERSION(),
            sellerId: seller.sellerId,
            resourceId: resource.resourceId,
            payTo: seller.payoutAddress,
            ackAuthority: seller.ackAuthority,
            amount: amount,
            chainId: block.chainid,
            asset: address(usdc),
            quoteExpiresAt: uint64(block.timestamp + 20 minutes),
            acceptBy: uint64(block.timestamp + 20 minutes),
            deliverBy: uint64(block.timestamp + 40 minutes),
            settleBy: uint64(block.timestamp + 60 minutes)
        });
        ServiceDirectory.ResourceLeaf memory selectedResource = resource;
        selectedResource.price = amount;
        bytes memory payload = abi.encodeCall(
            ASCPCallEscrow.lockCall, (commitment, seller, selectedResource, new bytes32[](0), new bytes32[](0))
        );
        ASCPSpendModule.LockAuthorization memory authorization = ASCPSpendModule.LockAuthorization({
            orgDomain: keccak256("org-domain"),
            safe: address(safe),
            module: address(module),
            operationId: commitment.operationId,
            commitmentHash: escrow.executionCommitmentDigest(commitment, address(escrow), block.chainid),
            calldataHash: keccak256(payload),
            escrow: address(escrow),
            amount: amount,
            nonce: nonce,
            validAfter: uint64(block.timestamp),
            validBefore: uint64(block.timestamp + 10 minutes),
            leadershipEpoch: 1,
            authorizerEpoch: module.authorizerEpoch()
        });
        bytes memory signature = _signature(module.lockAuthorizationDigest(authorization), authorKey);
        try module.executeLock(payload, authorization, signature) {
            successfulPrincipal += amount;
            successfulLocks += 1;
        } catch {}
    }

    function _signature(bytes32 digest, uint256 key) private pure returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, digest);
        return abi.encodePacked(r, s, v);
    }
}

contract ASCPSpendModuleInvariantTest is StdInvariant, Test {
    uint256 internal constant AUTHOR_KEY = 0xA11CE;
    MockUSDC internal usdc;
    SpendModuleDirectoryHarness internal directory;
    SpendModuleSafeHarness internal safe;
    ASCPCallEscrow internal escrow;
    ASCPSpendModule internal module;
    ASCPSpendModuleInvariantHandler internal handler;

    function setUp() public {
        vm.warp(1_800_000_000);
        usdc = new MockUSDC();
        directory = new SpendModuleDirectoryHarness();
        safe = new SpendModuleSafeHarness();
        escrow = new ASCPCallEscrow(IERC20(address(usdc)), directory, address(safe), address(safe));
        module = new ASCPSpendModule(
            address(safe),
            IERC20(address(usdc)),
            vm.addr(AUTHOR_KEY),
            ASCPSpendModule.Caps({perTransaction: 1_000, perDay: 2_000, allowanceCeiling: 10_000})
        );
        safe.setModule(address(module));
        bytes32 workflowId = keccak256("invariant-allowlist-workflow");
        bytes32 payloadHash = module.governancePayloadHash(
            workflowId,
            module.setEscrowAllowlist.selector,
            keccak256(abi.encode(address(escrow), bytes32(0), address(escrow).codehash))
        );
        safe.ownerCall(
            address(module),
            abi.encodeCall(
                ASCPSpendModule.setEscrowAllowlist, (address(escrow), address(escrow).codehash, workflowId, payloadHash)
            )
        );
        safe.ownerCall(address(usdc), abi.encodeCall(IERC20.approve, (address(escrow), type(uint256).max)));
        usdc.mint(address(safe), 1_000_000);

        ServiceDirectory.SellerLeaf memory seller = ServiceDirectory.SellerLeaf({
            sellerId: keccak256("seller"),
            payoutAddress: makeAddr("invariant-pay-to"),
            ackAuthority: makeAddr("invariant-ack-authority"),
            quoteSigningKey: makeAddr("invariant-quote-key"),
            keyEpoch: 1,
            baseURLOriginHash: keccak256("https://seller.example"),
            status: 1
        });
        ServiceDirectory.ResourceLeaf memory resource = ServiceDirectory.ResourceLeaf({
            sellerId: seller.sellerId,
            resourceId: keccak256("resource"),
            price: 1,
            escrowSupported: true,
            verificationSpecHash: keccak256("verification-spec"),
            declaredWorkTime: 300,
            verificationBudgetSeconds: 120
        });
        handler =
            new ASCPSpendModuleInvariantHandler(module, escrow, usdc, directory, safe, AUTHOR_KEY, seller, resource);
        handler.executeBoundedLock(1, keccak256("invariant-seed"));
        targetContract(address(handler));
    }

    function invariantPrincipalEqualsSuccessfulCallsAndDayCounter() public view {
        uint256 principal = module.executedPrincipal();
        assertGt(handler.successfulLocks(), 0);
        assertEq(safe.moduleExecutions(), handler.successfulLocks());
        assertEq(principal, handler.successfulPrincipal());
        assertEq(principal, module.dayExecutedPrincipal(block.timestamp / 1 days));
        assertLe(principal, 2_000);
    }

    function invariantModuleNeverAsksSafeForDelegatecallOrValue() public view {
        if (safe.moduleExecutions() > 0) {
            assertEq(safe.lastOperation(), 0);
            assertEq(safe.lastValue(), 0);
            assertEq(safe.lastTarget(), address(escrow));
        }
    }
}
