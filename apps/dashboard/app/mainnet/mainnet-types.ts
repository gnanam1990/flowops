export type MainnetIntentAnchorDeployment = {
  status: "deployment-pending" | "limited-mainnet-live";
  network: "Base mainnet";
  chainId: 8453;
  address: string | null;
  explorerHref: string | null;
};
