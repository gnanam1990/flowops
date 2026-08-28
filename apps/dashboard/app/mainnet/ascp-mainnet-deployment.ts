import deploymentRecord from "./ascp-mainnet-deployment.json";

export type ASCPMainnetContract = {
  name: string;
  binding: string;
  address: string;
  blockNumber: number;
  runtimeCodeKeccak: string;
  sourceVerified: boolean;
};

export type ASCPMainnetDeployment = {
  schemaVersion: 1;
  releaseId: string;
  network: "Base mainnet";
  chainId: 8453;
  status: "deployed-inactive";
  firstDeploymentBlock: number;
  finalizedThroughBlock: number;
  safe: string;
  asset: { address: string; symbol: "USDC"; decimals: 6 };
  contracts: ASCPMainnetContract[];
  activation: {
    runtimeEnabled: false;
    externalReviewCompleted: false;
    safeModuleEnabled: false;
    escrowAllowlisted: false;
    fundingAuthorized: false;
    allContractNativeBalancesWei: "0";
    allContractUsdcBalancesAtomic: "0";
  };
};

export function loadASCPMainnetDeployment(): ASCPMainnetDeployment {
  return deploymentRecord as ASCPMainnetDeployment;
}
