export type DashboardSnapshot = {
  mode: "preview" | "live";
  connection: {
    label: string;
    detail: string;
  };
  generatedAt: string;
  organization: {
    name: string;
    plan: string;
  };
  chain: {
    network: string;
    state: "HEALTHY" | "SUSPECTED_STALL" | "HALTED" | "RECOVERING";
    observers: string;
    lastTrustedBlock: string;
    lastTrustedAt: string;
  };
  money: {
    total: string;
    available: string;
    reserved: string;
    pending: string;
    unresolved: string;
    spentToday: string;
    monthlySpent: string;
    monthlyBudget: string;
    monthlySpentPercent: number | null;
  };
  approvals: Approval[];
  agents: Agent[];
  activity: Activity[];
  risks: Risk[];
};

export type Approval = {
  id: string;
  agent: string;
  agentMark: string;
  title: string;
  vendor: string;
  amount: string;
  requested: string;
  expires: string;
  reason: string;
  risk: "low" | "medium" | "high";
  rail: "Direct x402" | "Direct payment" | "Escrowed call";
  asset?: string;
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
  state: "settled" | "released" | "refunded" | "approval" | "security";
};

export type Risk = {
  id: string;
  severity: "critical" | "warning" | "info";
  title: string;
  detail: string;
  time: string;
};

export const dashboardSnapshot: DashboardSnapshot = {
  mode: "preview",
  connection: {
    label: "Preview data",
    detail: "The authenticated FlowOps control plane is not connected.",
  },
  generatedAt: "18 seconds ago",
  organization: {
    name: "Northstar Labs",
    plan: "Capped pilot",
  },
  chain: {
    network: "Base Sepolia",
    state: "HEALTHY",
    observers: "3 / 3 agree",
    lastTrustedBlock: "45,347,753",
    lastTrustedAt: "18 seconds ago",
  },
  money: {
    total: "$15,140.00",
    available: "$12,840.00",
    reserved: "$1,260.00",
    pending: "$840.00",
    unresolved: "$200.00",
    spentToday: "$428.40",
    monthlySpent: "$7,428",
    monthlyBudget: "$20,000",
    monthlySpentPercent: 37,
  },
  approvals: [
    {
      id: "APR-2048",
      agent: "Research Scout",
      agentMark: "RS",
      title: "Acquire market intelligence dataset",
      vendor: "Signal Harbor",
      amount: "$186.00",
      requested: "4 min ago",
      expires: "26 min",
      reason: "New vendor and amount exceeds the agent’s $100 auto-limit.",
      risk: "medium",
      rail: "Escrowed call",
      asset: "USDC",
      policyVersion: "pol_v14.2",
      requestDigest: "0x83b1…0ca9",
      evidenceRefs: "EV-APR-2048-02 · BASE-OBS-03",
    },
    {
      id: "APR-2047",
      agent: "Growth Operator",
      agentMark: "GO",
      title: "Run multi-channel creative analysis",
      vendor: "Frame Labs",
      amount: "$72.40",
      requested: "11 min ago",
      expires: "49 min",
      reason: "First purchase from this endpoint version.",
      risk: "low",
      rail: "Direct x402",
      asset: "USDC",
      policyVersion: "pol_v14.2",
      requestDigest: "0x4e72…91fd",
      evidenceRefs: "EV-APR-2047-02 · BASE-OBS-03",
    },
    {
      id: "APR-2044",
      agent: "Finance Copilot",
      agentMark: "FC",
      title: "Validate 420 supplier records",
      vendor: "Clearline Data",
      amount: "$340.00",
      requested: "28 min ago",
      expires: "1h 02m",
      reason: "Daily vendor cap requires a Finance approver.",
      risk: "medium",
      rail: "Escrowed call",
      asset: "USDC",
      policyVersion: "pol_v14.2",
      requestDigest: "0xb650…e712",
      evidenceRefs: "EV-APR-2044-02 · BASE-OBS-03",
    },
  ],
  agents: [
    {
      id: "AGT-01",
      name: "Research Scout",
      mark: "RS",
      purpose: "Market and protocol research",
      status: "ACTIVE",
      available: "$2,418",
      spent: "$582",
      limit: "$3,000",
      percent: 19,
      task: "Base ecosystem landscape",
    },
    {
      id: "AGT-02",
      name: "Growth Operator",
      mark: "GO",
      purpose: "Campaign analysis and testing",
      status: "ACTIVE",
      available: "$1,760",
      spent: "$740",
      limit: "$2,500",
      percent: 30,
      task: "Q3 creative refresh",
    },
    {
      id: "AGT-03",
      name: "Finance Copilot",
      mark: "FC",
      purpose: "Supplier and spend operations",
      status: "ACTIVE",
      available: "$4,120",
      spent: "$880",
      limit: "$5,000",
      percent: 18,
      task: "Supplier renewal review",
    },
    {
      id: "AGT-04",
      name: "Support Analyst",
      mark: "SA",
      purpose: "Customer evidence collection",
      status: "PAUSED",
      available: "$980",
      spent: "$20",
      limit: "$1,000",
      percent: 2,
      task: "Paused by owner",
    },
  ],
  activity: [
    {
      id: "EVT-8821",
      time: "2 min ago",
      title: "Evidence Fetch released",
      detail: "Research Scout · docs.base.org · Escrowed call",
      amount: "−$0.08",
      state: "released",
    },
    {
      id: "EVT-8819",
      time: "9 min ago",
      title: "x402 purchase settled",
      detail: "Growth Operator · Frame Labs · 3 confirmations",
      amount: "−$12.40",
      state: "settled",
    },
    {
      id: "EVT-8816",
      time: "18 min ago",
      title: "Approval requested",
      detail: "Finance Copilot · Clearline Data",
      amount: "$340.00",
      state: "approval",
    },
    {
      id: "EVT-8811",
      time: "41 min ago",
      title: "Expired call refunded",
      detail: "Research Scout · unreachable provider · confirmed onchain",
      amount: "+$4.20",
      state: "refunded",
    },
    {
      id: "EVT-8804",
      time: "1h ago",
      title: "Agent paused by owner",
      detail: "Support Analyst · new authorizations blocked",
      state: "security",
    },
  ],
  risks: [
    {
      id: "RSK-301",
      severity: "warning",
      title: "Unresolved execution needs review",
      detail: "A $200 payment broadcast before an RPC disagreement is quarantined.",
      time: "23 min ago",
    },
    {
      id: "RSK-297",
      severity: "info",
      title: "Support Analyst remains paused",
      detail: "No new authorization or signer broadcast can proceed.",
      time: "1h ago",
    },
  ],
};
