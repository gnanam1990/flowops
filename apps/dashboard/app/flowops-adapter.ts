import type { ChatGPTUser } from "./chatgpt-auth";
import {
  dashboardSnapshot,
  type Activity,
  type Agent,
  type Approval,
  type DashboardSnapshot,
  type Risk,
} from "./dashboard-data";

const MAX_RESPONSE_BYTES = 256 * 1024;
const REQUEST_TIMEOUT_MS = 4_000;

type AdapterConfig = {
  controlApiUrl: string;
  siteProjectId: string;
  exchangeToken: string;
};

type SessionResponse = {
  accessToken: string;
  expiresAt: string;
  organizationId: string;
};

type ControlSnapshot = {
  live: boolean;
  generatedAt: string;
  organizationId: string;
  organization: { id: string; name: string };
  chain: {
    state: DashboardSnapshot["chain"]["state"];
    reason: string;
    lastTrusted?: {
      blockNumber: number;
      observedAt: string;
    };
    authorizationsPaused: boolean;
  };
  pendingApprovals: ControlApproval[];
  agents: ControlAgent[];
};

type ControlApproval = {
  requestId: string;
  requestDigest: string;
  submittedAt: number;
  approvalExpiresAt: number;
  decision: { reason: string; policyVersion: string };
  intent: {
    agentId: string;
    taskId: string;
    rail: string;
    recipient: string;
    asset: string;
    amountAtomic: string;
    purpose: string;
  };
};

type ControlAgent = {
  id: string;
  name: string;
  purpose: string;
  status: Agent["status"];
};

export async function dashboardForUser(
  user: ChatGPTUser | null,
  config = loadAdapterConfig(),
  request: typeof fetch = fetch,
): Promise<DashboardSnapshot> {
  if (!user) return preview("Sign in with ChatGPT to check FlowOps membership.");
  if (!config) {
    console.warn("[flowops-adapter] runtime configuration is unavailable");
    return preview("Live data is not configured for this deployment.");
  }

  try {
    const siteUserKey = await deriveSiteUserKey(config.siteProjectId, user.userId);
    const session = await requestJSON<SessionResponse>(
      request,
      `${config.controlApiUrl}/v1/sites/session`,
      {
        method: "POST",
        headers: {
          authorization: `Bearer ${config.exchangeToken}`,
          "content-type": "application/json",
        },
        body: JSON.stringify({
          siteProjectId: config.siteProjectId,
          siteUserKey,
          email: user.email,
        }),
      },
    );
    if (
      !isIdentifier(session.organizationId) ||
      typeof session.accessToken !== "string" ||
      !session.accessToken.startsWith("fos_v1.") ||
      session.accessToken.length > 2_048 ||
      !isFreshExpiry(session.expiresAt)
    ) {
      throw new Error("invalid session response");
    }

    const snapshot = await requestJSON<ControlSnapshot>(
      request,
      `${config.controlApiUrl}/v1/dashboard/snapshot`,
      {
        headers: { authorization: `Bearer ${session.accessToken}` },
      },
    );
    return mapControlSnapshot(snapshot, session.organizationId);
  } catch (error) {
    console.warn("[flowops-adapter] live snapshot unavailable", safeErrorMessage(error));
    return preview("Membership or control-plane data is unavailable. No live state is shown.");
  }
}

function safeErrorMessage(error: unknown): string {
  if (!(error instanceof Error)) return "unknown error";
  return error.message.slice(0, 200);
}

export function loadAdapterConfig(): AdapterConfig | null {
  const controlApiUrl = process.env.FLOWOPS_CONTROL_API_URL?.trim();
  const siteProjectId = process.env.FLOWOPS_SITES_PROJECT_ID?.trim();
  const exchangeToken = process.env.FLOWOPS_SITES_EXCHANGE_TOKEN?.trim();
  if (!controlApiUrl || !siteProjectId || !exchangeToken) return null;
  if (!isIdentifier(siteProjectId) || exchangeToken.length < 32 || exchangeToken.length > 512) return null;
  let url: URL;
  try {
    url = new URL(controlApiUrl);
  } catch {
    return null;
  }
  const local = url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "::1";
  if ((url.protocol !== "https:" && !(local && url.protocol === "http:")) || url.username || url.password || url.search || url.hash) {
    return null;
  }
  return { controlApiUrl: url.href.replace(/\/$/, ""), siteProjectId, exchangeToken };
}

export async function deriveSiteUserKey(siteProjectId: string, siteUserId: string): Promise<string> {
  if (!isIdentifier(siteProjectId) || !siteUserId.trim() || siteUserId.length > 512) throw new Error("invalid site identity");
  const input = new TextEncoder().encode(`${siteProjectId}\u0000${siteUserId}`);
  const digest = await crypto.subtle.digest("SHA-256", input);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function requestJSON<T>(request: typeof fetch, url: string, init: RequestInit): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const response = await request(url, {
      ...init,
      cache: "no-store",
      // Cloudflare Workers reject `redirect: "error"`. Manual mode keeps
      // authorization headers from being replayed to a redirected origin;
      // every 3xx response then fails the non-2xx check below.
      redirect: "manual",
      signal: controller.signal,
    });
    if (!response.ok) throw new Error(`upstream status ${response.status}`);
    const declaredSize = Number(response.headers.get("content-length") ?? "0");
    if (declaredSize > MAX_RESPONSE_BYTES) throw new Error("upstream response is too large");
    const raw = await response.text();
    if (raw.length > MAX_RESPONSE_BYTES) throw new Error("upstream response is too large");
    return JSON.parse(raw) as T;
  } finally {
    clearTimeout(timer);
  }
}

function mapControlSnapshot(raw: ControlSnapshot, sessionOrganizationId: string): DashboardSnapshot {
  if (
    raw?.live !== true ||
    raw.organizationId !== sessionOrganizationId ||
    raw.organization?.id !== sessionOrganizationId ||
    typeof raw.organization?.name !== "string" ||
    !raw.organization.name.trim() ||
    raw.organization.name.length > 200 ||
    !Array.isArray(raw.pendingApprovals) ||
    !Array.isArray(raw.agents) ||
    !isChainState(raw.chain?.state)
  ) {
    throw new Error("invalid dashboard snapshot");
  }
  const generatedAt = parseDate(raw.generatedAt);
  const agents = raw.agents.map(mapAgent);
  const names = new Map(agents.map((agent) => [agent.id, agent.name]));
  const approvals = raw.pendingApprovals.map((approval) => mapApproval(approval, names, generatedAt));
  const activity: Activity[] = approvals.map((approval) => ({
    id: `event-${approval.id}`,
    time: approval.requested,
    title: "Approval requested",
    detail: `${approval.agent} · ${approval.vendor}`,
    amount: approval.amount,
    state: "approval",
  }));
  const risks = liveRisks(raw, agents, generatedAt);
  const checkpoint = raw.chain.lastTrusted;
  if (checkpoint && (!Number.isSafeInteger(checkpoint.blockNumber) || checkpoint.blockNumber < 0)) {
    throw new Error("invalid checkpoint");
  }
  const checkpointTime = checkpoint ? parseDate(checkpoint.observedAt) : null;
  const unavailable = "Not available";
  return {
    mode: "live",
    connection: {
      label: "Live control plane",
      detail: "Organization-scoped read session. Writes require separate step-up authentication.",
    },
    generatedAt: age(generatedAt, new Date()),
    organization: { name: raw.organization.name, plan: "Authorized membership" },
    chain: {
      network: "Base control plane",
      state: raw.chain.state,
      observers: raw.chain.state === "HEALTHY" ? "Canonical checkpoint trusted" : "Authorizations restricted",
      lastTrustedBlock: checkpoint ? checkpoint.blockNumber.toLocaleString("en-US") : "Unavailable",
      lastTrustedAt: checkpointTime ? age(checkpointTime, new Date()) : "Unavailable",
    },
    money: {
      total: unavailable,
      available: unavailable,
      reserved: unavailable,
      pending: unavailable,
      unresolved: unavailable,
      spentToday: unavailable,
      monthlySpent: unavailable,
      monthlyBudget: unavailable,
      monthlySpentPercent: null,
    },
    approvals,
    agents,
    activity,
    risks,
  };
}

function mapAgent(raw: ControlAgent): Agent {
  if (
    !isIdentifier(raw?.id) ||
    typeof raw.name !== "string" ||
    !raw.name.trim() ||
    raw.name.length > 200 ||
    typeof raw.purpose !== "string" ||
    raw.purpose.length > 1_024 ||
    !isAgentStatus(raw.status)
  ) throw new Error("invalid agent");
  return {
    id: raw.id,
    name: raw.name,
    mark: initials(raw.name),
    purpose: raw.purpose || "Purpose not supplied",
    status: raw.status,
    available: "Not available",
    spent: "Not available",
    limit: "Not available",
    percent: 0,
    task: "Task telemetry not exposed",
  };
}

function mapApproval(raw: ControlApproval, names: Map<string, string>, observedAt: Date): Approval {
  if (
    !isIdentifier(raw?.requestId) ||
    !/^0x[0-9a-f]{64}$/.test(raw.requestDigest) ||
    !Number.isSafeInteger(raw.submittedAt) ||
    raw.submittedAt <= 0 ||
    !Number.isSafeInteger(raw.approvalExpiresAt) ||
    raw.approvalExpiresAt <= raw.submittedAt ||
    !isIdentifier(raw.intent?.agentId) ||
    !isIdentifier(raw.intent?.taskId) ||
    typeof raw.intent.purpose !== "string" ||
    raw.intent.purpose.length > 1_024 ||
    !/^0x[0-9a-f]{40}$/.test(raw.intent.recipient) ||
    !/^0x[0-9a-f]{40}$/.test(raw.intent.asset) ||
    !/^\d{1,78}$/.test(raw.intent.amountAtomic ?? "")
  ) {
    throw new Error("invalid approval");
  }
  const agent = names.get(raw.intent.agentId) ?? raw.intent.agentId;
  const recipient = shortAddress(raw.intent.recipient);
  return {
    id: raw.requestId,
    agent,
    agentMark: initials(agent),
    title: raw.intent.purpose || `Task ${raw.intent.taskId}`,
    vendor: recipient,
    amount: `${formatAtomic(raw.intent.amountAtomic)} atomic`,
    requested: age(new Date(raw.submittedAt * 1_000), observedAt),
    expires: durationUntil(raw.approvalExpiresAt, observedAt),
    reason: humanize(raw.decision?.reason || "POLICY_REQUIRES_APPROVAL"),
    risk: riskForReason(raw.decision?.reason),
    rail: mapRail(raw.intent.rail),
    asset: shortAddress(raw.intent.asset),
    policyVersion: raw.decision?.policyVersion || "Not exposed",
    requestDigest: shortDigest(raw.requestDigest),
    evidenceRefs: "Not issued before decision",
  };
}

function liveRisks(raw: ControlSnapshot, agents: Agent[], now: Date): Risk[] {
  const risks: Risk[] = [];
  if (raw.chain.state !== "HEALTHY" || raw.chain.authorizationsPaused) {
    risks.push({
      id: "chain-control-state",
      severity: raw.chain.state === "HALTED" ? "critical" : "warning",
      title: `Base state is ${raw.chain.state.toLowerCase().replaceAll("_", " ")}`,
      detail: raw.chain.reason || "Authorizations are restricted pending canonical evidence.",
      time: age(parseDate(raw.generatedAt), now),
    });
  }
  for (const agent of agents.filter((item) => item.status === "PAUSED" || item.status === "QUARANTINED")) {
    risks.push({
      id: `agent-${agent.id}`,
      severity: agent.status === "QUARANTINED" ? "warning" : "info",
      title: `${agent.name} is ${agent.status.toLowerCase()}`,
      detail: "New authorizations are not represented as available for this agent.",
      time: "Current snapshot",
    });
  }
  return risks;
}

function preview(detail: string): DashboardSnapshot {
  return {
    ...dashboardSnapshot,
    connection: { label: "Preview data", detail },
  };
}

function isFreshExpiry(value: string): boolean {
  const expiry = Date.parse(value);
  const now = Date.now();
  return Number.isFinite(expiry) && expiry > now && expiry <= now + 6 * 60_000;
}

function parseDate(value: string): Date {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) throw new Error("invalid date");
  return date;
}

function isIdentifier(value: unknown): value is string {
  return typeof value === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(value);
}

function isChainState(value: unknown): value is DashboardSnapshot["chain"]["state"] {
  return value === "HEALTHY" || value === "SUSPECTED_STALL" || value === "HALTED" || value === "RECOVERING";
}

function isAgentStatus(value: unknown): value is Agent["status"] {
  return value === "DRAFT" || value === "ACTIVE" || value === "PAUSED" || value === "QUARANTINED" || value === "REVOKED" || value === "ARCHIVED";
}

function age(then: Date, now: Date): string {
  const seconds = Math.max(0, Math.round((now.getTime() - then.getTime()) / 1_000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ago`;
}

function durationUntil(unixSeconds: number, now: Date): string {
  if (!Number.isSafeInteger(unixSeconds) || unixSeconds <= 0) return "Unavailable";
  const seconds = Math.max(0, unixSeconds - Math.floor(now.getTime() / 1_000));
  if (seconds < 60) return `${seconds}s`;
  return `${Math.ceil(seconds / 60)}m`;
}

function formatAtomic(atomic: string): string {
  return BigInt(atomic).toLocaleString("en-US");
}

function initials(value: string): string {
  return value.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join("") || "AG";
}

function shortAddress(value: string): string {
  return /^0x[0-9a-f]{40}$/.test(value) ? `${value.slice(0, 6)}…${value.slice(-4)}` : "Recipient unavailable";
}

function shortDigest(value: string): string {
  return /^0x[0-9a-f]{64}$/.test(value) ? `${value.slice(0, 8)}…${value.slice(-6)}` : "Not exposed";
}

function humanize(value: string): string {
  return value.toLowerCase().replaceAll("_", " ").replace(/^./, (character) => character.toUpperCase());
}

function riskForReason(value: string | undefined): Approval["risk"] {
  if (value === "DAILY_BUDGET_EXCEEDED" || value === "TASK_BUDGET_EXCEEDED") return "high";
  return value === "HUMAN_APPROVAL_THRESHOLD" ? "medium" : "low";
}

function mapRail(value: string): Approval["rail"] {
  if (value === "X402") return "Direct x402";
  if (value === "DIRECT") return "Direct payment";
  if (value === "ESCROW") return "Escrowed call";
  throw new Error("invalid rail");
}
