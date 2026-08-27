import type { MainnetIntentAnchorDeployment } from "./mainnet-types";

const EVM_ADDRESS = /^0x[0-9a-fA-F]{40}$/;
const ZERO_ADDRESS = /^0x0{40}$/i;

export function loadMainnetIntentAnchorDeployment(
  configuredAddress = process.env.FLOWOPS_MAINNET_INTENT_ANCHOR_ADDRESS,
): MainnetIntentAnchorDeployment {
  const address = configuredAddress?.trim() ?? "";
  if (!EVM_ADDRESS.test(address) || ZERO_ADDRESS.test(address)) {
    return {
      status: "deployment-pending",
      network: "Base mainnet",
      chainId: 8453,
      address: null,
      explorerHref: null,
    };
  }

  return {
    status: "limited-mainnet-live",
    network: "Base mainnet",
    chainId: 8453,
    address,
    explorerHref: `https://base.blockscout.com/address/${address}?tab=contract`,
  };
}
