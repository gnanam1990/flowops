export type ProposalAnchorDeployment = {
  status: "not-deployed" | "experimental-unaudited";
  network: "Base mainnet";
  address: string | null;
  explorerHref: string | null;
};

const EVM_ADDRESS = /^0x[0-9a-fA-F]{40}$/;
const ZERO_ADDRESS = /^0x0{40}$/i;

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
    };
  }

  return {
    status: "experimental-unaudited",
    network: "Base mainnet",
    address,
    explorerHref: `https://base.blockscout.com/address/${address}?tab=contract`,
  };
}
