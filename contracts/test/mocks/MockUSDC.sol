// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

contract MockUSDC is ERC20 {
    constructor() ERC20("Mock USDC", "mUSDC") {}

    function decimals() public pure override returns (uint8) {
        return 6;
    }

    function mint(address account, uint256 amount) external {
        _mint(account, amount);
    }
}

contract FeeToken is ERC20 {
    address private constant FEE_SINK = address(0xFEE);

    constructor() ERC20("Fee Token", "FEE") {}

    function mint(address account, uint256 amount) external {
        _mint(account, amount);
    }

    function _update(address from, address to, uint256 value) internal override {
        if (from != address(0) && to != address(0) && value > 1) {
            super._update(from, FEE_SINK, 1);
            super._update(from, to, value - 1);
            return;
        }
        super._update(from, to, value);
    }
}

interface IRefundTarget {
    function refundExpired(bytes32 callId) external;
}

contract ReentrantToken is ERC20 {
    address public escrow;
    bytes32 public refundTarget;
    bool public attempted;
    bool public blocked;

    constructor() ERC20("Reentrant Token", "REENTRANT") {}

    function mint(address account, uint256 amount) external {
        _mint(account, amount);
    }

    function arm(address escrow_, bytes32 refundTarget_) external {
        escrow = escrow_;
        refundTarget = refundTarget_;
        attempted = false;
        blocked = false;
    }

    function _update(address from, address to, uint256 value) internal override {
        if (escrow != address(0) && from == escrow && !attempted) {
            attempted = true;
            try IRefundTarget(escrow).refundExpired(refundTarget) {}
            catch {
                blocked = true;
            }
        }
        super._update(from, to, value);
    }
}
