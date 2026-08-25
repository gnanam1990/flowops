export type ProposalAnchorDeployment = {
  status: "not-deployed" | "experimental-unaudited";
  network: "Base mainnet";
  address: string | null;
  explorerHref: string | null;
  sourceVerified: boolean;
};

const EVM_ADDRESS = /^0x[0-9a-fA-F]{40}$/;
const ZERO_ADDRESS = /^0x0{40}$/i;
const VERIFIED_PROPOSAL_ANCHOR = "0x149d03ec527ad8667d47e7b6a2d316dd54033250";

export function loadProposalAnchorDeployment(
  configuredAddress = process.env.FLOWOPS_PROPOSAL_ANCHOR_ADDRESS,
): ProposalAnchorDeployment {
  const address = configuredAddress?.trim() ?? "";
  if (!EVM_ADDRESS.test(address) || ZERO_ADDRESS.test(address)) {
    return {
      status: "not-deployed",
      network: "Base mainnet",
      address: null,
      explorerHref: null,
      sourceVerified: false,
    };
  }

  return {
    status: "experimental-unaudited",
    network: "Base mainnet",
    address,
    explorerHref: `https://base.blockscout.com/address/${address}?tab=contract`,
    sourceVerified: address.toLowerCase() === VERIFIED_PROPOSAL_ANCHOR,
  };
}
