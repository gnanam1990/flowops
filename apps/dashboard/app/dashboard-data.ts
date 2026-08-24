export type DashboardSnapshot = {
  mode: "public" | "live";
  connection: {
    label: string;
    detail: string;
  };
  generatedAt: string;
  organization: {
    name: string;
    plan: string;
    authorizationsPaused: boolean;
  };
  chain: {
	chainId: number;
    network: string;
    state: "HEALTHY" | "SUSPECTED_STALL" | "HALTED" | "RECOVERING";
    observers: string;
    lastTrustedBlock: string;
    lastTrustedAt: string;
  };
  money: {
	asset: string;
    total: string;
	walletDelta: string;
    reserved: string;
    pending: string;
    unresolved: string;
    spentToday: string;
	dailyLimit: string;
	dailySpentPercent: number | null;
  };
  approvals: Approval[];
  agents: Agent[];
  activity: Activity[];
  risks: Risk[];
	reconciliation: {
		available: boolean;
		checkpointBlock: string;
		observedThroughBlock: string;
		resolvedCandidates: number;
		totalCandidates: number;
		unresolvedOutcomes: number;
		quarantinedOutcomes: number;
		pendingFinality: number;
		readyForManualResume: boolean;
		complete: boolean;
		unclassifiedLedgerTransactions: number;
	};
};

export type Approval = {
	source: "legacy" | "ascp";
  id: string;
  agent: string;
  agentMark: string;
  title: string;
  vendor: string;
	recipientAddress: string;
  amount: string;
	amountAtomic: string;
  requested: string;
  expires: string;
  reason: string;
  risk: "low" | "medium" | "high";
  rail: "Direct x402" | "Direct payment" | "Escrowed call";
  asset?: string;
	assetAddress: string;
	assetSymbol?: string;
	assetDecimals?: number;
	chainId: number;
	network: string;
  policyVersion?: string;
  requestDigest?: string;
  evidenceRefs?: string;
};

export type Agent = {
  id: string;
  name: string;
  mark: string;
  purpose: string;
  status: "DRAFT" | "ACTIVE" | "PAUSED" | "QUARANTINED" | "REVOKED" | "ARCHIVED";
  available: string;
  spent: string;
  limit: string;
  percent: number;
  task: string;
};

export type Activity = {
  id: string;
  time: string;
  title: string;
  detail: string;
  amount?: string;
  state: "settled" | "released" | "refunded" | "approval" | "decision" | "security" | "pending";
};

export type Risk = {
  id: string;
  severity: "critical" | "warning" | "info";
  title: string;
  detail: string;
  time: string;
};
