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
  status: "activated-zero-fund";
  firstDeploymentBlock: number;
  finalizedThroughBlock: number;
  safe: string;
  asset: { address: string; symbol: "USDC"; decimals: 6 };
  contracts: ASCPMainnetContract[];
  activation: {
    runtimeEnabled: true;
    externalReviewCompleted: false;
    safeModuleEnabled: true;
    escrowAllowlisted: true;
    fundingAuthorized: false;
    safeTxHash: string;
    transactionHash: string;
    blockNumber: number;
    safeNonce: 1;
    escrowRuntimeCodeHash: string;
    allContractNativeBalancesWei: "0";
    allContractUsdcBalancesAtomic: "0";
  };
};

export function loadASCPMainnetDeployment(): ASCPMainnetDeployment {
  return deploymentRecord as ASCPMainnetDeployment;
}
