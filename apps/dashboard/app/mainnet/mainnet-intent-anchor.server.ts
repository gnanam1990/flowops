import type { MainnetIntentAnchorDeployment } from "./mainnet-types";

const EVM_ADDRESS = /^0x[0-9a-fA-F]{40}$/;
const ZERO_ADDRESS = /^0x0{40}$/i;
const VERIFIED_MAINNET_INTENT_ANCHOR = "0xD109ec995d8fC1FFD2fd66f367288b3Bc3EC8AAA";

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
      sourceVerification: "unavailable",
      sourceVerificationHref: null,
    };
  }

  const explorerHref = `https://base.blockscout.com/address/${address}?tab=contract`;
  const sourceVerified = address.toLowerCase() === VERIFIED_MAINNET_INTENT_ANCHOR.toLowerCase();

  return {
    status: "limited-mainnet-live",
    network: "Base mainnet",
    chainId: 8453,
    address,
    explorerHref,
    sourceVerification: sourceVerified ? "verified" : "unavailable",
    sourceVerificationHref: sourceVerified ? explorerHref : null,
  };
}
