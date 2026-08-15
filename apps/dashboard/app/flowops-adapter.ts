import type { ChatGPTUser } from "./chatgpt-auth";
import {
  type Activity,
  type Agent,
  type Approval,
  type DashboardSnapshot,
  type Risk,
} from "./dashboard-data";

const MAX_RESPONSE_BYTES = 256 * 1024;
const REQUEST_TIMEOUT_MS = 4_000;

export type AdapterConfig = {
  controlApiUrl: string;
  siteProjectId: string;
  exchangeToken: string;
};

export type SessionResponse = {
  accessToken: string;
  expiresAt: string;
  organizationId: string;
  principalId: string;
  role: string;
};

type PublicHealth = {
  controlPlane: string;
  chainState: DashboardSnapshot["chain"]["state"];
  authorizationsPaused: boolean;
  requiredObservers: number;
  respondingObservers: number;
  lastObservationAt: string;
  readyForManualResume: boolean;
  lastTrusted?: {
    blockNumber: number;
    observedAt: string;
  };
};

type ControlSnapshot = {
  live: boolean;
  generatedAt: string;
  organizationId: string;
  organization: { id: string; name: string; authorizationsPaused: boolean };
  chain: {
    state: DashboardSnapshot["chain"]["state"];
    reason: string;
    requiredObserverQuorum: number;
    respondingObservers: number;
    lastTrusted?: {
      blockNumber: number;
      observedAt: string;
    };
    authorizationsPaused: boolean;
  };
  pendingApprovals: ControlApproval[];
  agents: ControlAgent[];
	reconciliation: ControlReconciliation;
};

type ControlReconciliation = {
	available: boolean;
	recovery: {
		checkpointBlock: number;
		observedThroughBlock: number;
		totalCandidates: number;
		resolvedCandidates: number;
		unresolvedOutcomes: number;
		quarantinedOutcomes: number;
		pendingFinality: number;
		readyForManualResume: boolean;
		complete: boolean;
	};
	assets: Array<{
		asset: string;
		escrowLockedAtomic: string;
		recognizedExpenseAtomic: string;
		spentTodayAtomic: string;
		spentMonthAtomic: string;
		unresolvedAtomic: string;
	}>;
	exceptions: Array<{
		id: string;
		kind: string;
		state: string;
		asset: string;
		amountAtomic: string;
		firstObservedAt: string;
		reason: string;
		operatorActionNeeded: boolean;
	}>;
	unclassifiedLedgerTransactions: number;
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
  const controlApiUrl = config?.controlApiUrl ?? loadControlApiUrl();
  if (!user) return publicDashboard(controlApiUrl, request);
  if (!config) {
    console.warn("[flowops-adapter] runtime configuration is unavailable");
    return publicDashboard(
      controlApiUrl,
      request,
      "Member data is not configured for this deployment. Public operational status remains available.",
    );
  }

  try {
    const session = await exchangeSiteSession(user, config, request);
    if (
      !isIdentifier(session.organizationId) ||
      !isIdentifier(session.principalId) ||
      !isHumanRole(session.role) ||
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
    return publicDashboard(
      controlApiUrl,
      request,
      "Membership could not be authorized. No organization data is shown.",
    );
  }
}

export async function exchangeSiteSession(
  user: ChatGPTUser,
  config: AdapterConfig,
  request: typeof fetch = fetch,
): Promise<SessionResponse> {
  const siteUserKey = await deriveSiteUserKey(config.siteProjectId, user.userId);
  return requestJSON<SessionResponse>(
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
}

function safeErrorMessage(error: unknown): string {
  if (!(error instanceof Error)) return "unknown error";
  return error.message.slice(0, 200);
}

export function loadAdapterConfig(): AdapterConfig | null {
  const controlApiUrl = loadControlApiUrl();
  const siteProjectId = process.env.FLOWOPS_SITES_PROJECT_ID?.trim();
  const exchangeToken = process.env.FLOWOPS_SITES_EXCHANGE_TOKEN?.trim();
  if (!controlApiUrl || !siteProjectId || !exchangeToken) return null;
  if (!isIdentifier(siteProjectId) || exchangeToken.length < 32 || exchangeToken.length > 512) return null;
  return { controlApiUrl, siteProjectId, exchangeToken };
}

export function loadControlApiUrl(): string | null {
  const controlApiUrl = process.env.FLOWOPS_CONTROL_API_URL?.trim();
  if (!controlApiUrl) return null;
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
  return url.href.replace(/\/$/, "");
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
    typeof raw.organization.authorizationsPaused !== "boolean" ||
    !Array.isArray(raw.pendingApprovals) ||
    !Array.isArray(raw.agents) ||
    !isChainState(raw.chain?.state) ||
    !Number.isSafeInteger(raw.chain.requiredObserverQuorum) ||
    raw.chain.requiredObserverQuorum < 2 ||
    raw.chain.requiredObserverQuorum > 5 ||
	!Number.isSafeInteger(raw.chain.respondingObservers) ||
    raw.chain.respondingObservers < 0 ||
	raw.chain.respondingObservers > 5 ||
	!validReconciliation(raw.reconciliation)
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
  const observerProgress = `${raw.chain.respondingObservers} / ${raw.chain.requiredObserverQuorum}`;
	const unavailable = "Not available";
	const singleAsset = raw.reconciliation.available && raw.reconciliation.assets.length === 1 ? raw.reconciliation.assets[0] : null;
	const assetLabel = singleAsset ? shortAddress(singleAsset.asset) : raw.reconciliation.assets.length > 1 ? "Multiple assets" : "Asset unavailable";
  return {
    mode: "live",
    connection: {
      label: "Live control plane",
      detail: "Organization-scoped read session. Writes require separate step-up authentication.",
    },
    generatedAt: age(generatedAt, new Date()),
    organization: { name: raw.organization.name, plan: "Authorized membership", authorizationsPaused: raw.organization.authorizationsPaused },
    chain: {
      network: "Base control plane",
      state: raw.chain.state,
      observers: raw.chain.state === "HEALTHY" ? `${observerProgress} agree` : `${observerProgress} reporting · authorizations restricted`,
      lastTrustedBlock: checkpoint ? checkpoint.blockNumber.toLocaleString("en-US") : "Unavailable",
      lastTrustedAt: checkpointTime ? age(checkpointTime, new Date()) : "Unavailable",
    },
    money: {
	  asset: assetLabel,
	  total: singleAsset ? `${formatAtomic(singleAsset.recognizedExpenseAtomic)} atomic` : unavailable,
      available: unavailable,
	  reserved: singleAsset ? `${formatAtomic(singleAsset.escrowLockedAtomic)} atomic` : unavailable,
	  pending: `${raw.reconciliation.recovery.pendingFinality} item${raw.reconciliation.recovery.pendingFinality === 1 ? "" : "s"}`,
	  unresolved: singleAsset ? `${formatAtomic(singleAsset.unresolvedAtomic)} atomic` : `${raw.reconciliation.recovery.unresolvedOutcomes} item${raw.reconciliation.recovery.unresolvedOutcomes === 1 ? "" : "s"}`,
	  spentToday: singleAsset ? `${formatAtomic(singleAsset.spentTodayAtomic)} atomic` : unavailable,
	  monthlySpent: singleAsset ? `${formatAtomic(singleAsset.spentMonthAtomic)} atomic` : unavailable,
      monthlyBudget: unavailable,
      monthlySpentPercent: null,
    },
    approvals,
    agents,
    activity,
    risks,
	reconciliation: {
		available: raw.reconciliation.available,
		checkpointBlock: raw.reconciliation.recovery.checkpointBlock.toLocaleString("en-US"),
		observedThroughBlock: raw.reconciliation.recovery.observedThroughBlock.toLocaleString("en-US"),
		resolvedCandidates: raw.reconciliation.recovery.resolvedCandidates,
		totalCandidates: raw.reconciliation.recovery.totalCandidates,
		unresolvedOutcomes: raw.reconciliation.recovery.unresolvedOutcomes,
		quarantinedOutcomes: raw.reconciliation.recovery.quarantinedOutcomes,
		pendingFinality: raw.reconciliation.recovery.pendingFinality,
		readyForManualResume: raw.reconciliation.recovery.readyForManualResume,
		complete: raw.reconciliation.recovery.complete,
		unclassifiedLedgerTransactions: raw.reconciliation.unclassifiedLedgerTransactions,
	},
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
	for (const exception of raw.reconciliation.exceptions) {
		risks.push({
			id: `reconciliation-${exception.kind}-${exception.id}`,
			severity: exception.state === "QUARANTINED" ? "critical" : "warning",
			title: `${humanize(exception.kind)} is ${humanize(exception.state).toLowerCase()}`,
			detail: `${formatAtomic(exception.amountAtomic)} atomic on ${shortAddress(exception.asset)}. ${exception.reason || "Canonical outcome remains unresolved."}`,
			time: age(parseDate(exception.firstObservedAt), now),
		});
	}
	if (raw.reconciliation.unclassifiedLedgerTransactions > 0) {
		risks.push({
			id: "unclassified-ledger-transactions", severity: "warning",
			title: "Ledger entries excluded from asset totals",
			detail: `${raw.reconciliation.unclassifiedLedgerTransactions} operational ledger entr${raw.reconciliation.unclassifiedLedgerTransactions === 1 ? "y is" : "ies are"} not bound to a proved asset and were not aggregated.`,
			time: "Current snapshot",
		});
	}
  if (raw.organization.authorizationsPaused) {
    risks.push({
      id: "organization-authorization-pause",
      severity: "critical",
      title: "Organization authorizations are paused",
      detail: "The persistent organization gate blocks new authorization issuance until a separately reviewed recovery action.",
      time: age(parseDate(raw.generatedAt), now),
    });
  }
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

function validReconciliation(value: ControlReconciliation | undefined): value is ControlReconciliation {
	if (!value || typeof value.available !== "boolean" || !Array.isArray(value.assets) || !Array.isArray(value.exceptions)) return false;
	const recovery = value.recovery;
	if (!recovery || ![recovery.checkpointBlock, recovery.observedThroughBlock, recovery.totalCandidates, recovery.resolvedCandidates, recovery.unresolvedOutcomes, recovery.quarantinedOutcomes, recovery.pendingFinality].every((item) => Number.isSafeInteger(item) && item >= 0)) return false;
	if (typeof recovery.readyForManualResume !== "boolean" || typeof recovery.complete !== "boolean" || !Number.isSafeInteger(value.unclassifiedLedgerTransactions) || value.unclassifiedLedgerTransactions < 0) return false;
	if (!value.assets.every((asset) => /^0x[0-9a-f]{40}$/.test(asset.asset) && [asset.escrowLockedAtomic, asset.recognizedExpenseAtomic, asset.spentTodayAtomic, asset.spentMonthAtomic, asset.unresolvedAtomic].every(validSignedAtomic))) return false;
	return value.exceptions.every((exception) => isIdentifier(exception.id) && typeof exception.kind === "string" && typeof exception.state === "string" && /^0x[0-9a-f]{40}$/.test(exception.asset) && validUnsignedAtomic(exception.amountAtomic) && typeof exception.firstObservedAt === "string" && typeof exception.reason === "string" && typeof exception.operatorActionNeeded === "boolean");
}

function validSignedAtomic(value: unknown): value is string {
	return typeof value === "string" && /^-?(?:0|[1-9][0-9]{0,77})$/.test(value);
}

function validUnsignedAtomic(value: unknown): value is string {
	return typeof value === "string" && /^(?:0|[1-9][0-9]{0,77})$/.test(value);
}

async function publicDashboard(
  controlApiUrl: string | null,
  request: typeof fetch,
  detail = "Live, non-sensitive control-plane health. Sign in to view an authorized organization.",
): Promise<DashboardSnapshot> {
  if (!controlApiUrl) return publicUnavailable("Public operational status is not configured.");
  try {
    const health = await requestJSON<PublicHealth>(request, `${controlApiUrl}/health`, {});
    if (
      health?.controlPlane !== "AVAILABLE" ||
      !isChainState(health.chainState) ||
      typeof health.authorizationsPaused !== "boolean" ||
      !Number.isSafeInteger(health.requiredObservers) ||
      health.requiredObservers < 2 ||
      health.requiredObservers > 5 ||
      !Number.isSafeInteger(health.respondingObservers) ||
      health.respondingObservers < 0 ||
      health.respondingObservers > 5 ||
      health.respondingObservers > health.requiredObservers ||
      typeof health.readyForManualResume !== "boolean"
    ) throw new Error("invalid public health response");
    const observedAt = parseDate(health.lastObservationAt);
    const trusted = health.lastTrusted;
    if (trusted && (!Number.isSafeInteger(trusted.blockNumber) || trusted.blockNumber < 0)) {
      throw new Error("invalid public checkpoint");
    }
    const trustedAt = trusted ? parseDate(trusted.observedAt) : null;
    const observerProgress = `${health.respondingObservers} / ${health.requiredObservers}`;
    const risks: Risk[] = [];
    if (health.authorizationsPaused) {
      risks.push({
        id: "public-authorization-pause",
        severity: "critical",
        title: "New authorizations are paused",
        detail: "The public control-plane status reports that the authorization boundary is fail-closed.",
        time: age(observedAt, new Date()),
      });
    }
    if (health.chainState !== "HEALTHY") {
      risks.push({
        id: "public-chain-state",
        severity: health.chainState === "HALTED" ? "critical" : "warning",
        title: `Base state is ${health.chainState.toLowerCase().replaceAll("_", " ")}`,
        detail: "FlowOps does not represent new autonomous authorization capacity while canonical chain evidence is restricted.",
        time: age(observedAt, new Date()),
      });
    }
    return {
      mode: "public",
      connection: { label: "Live public status", detail },
      generatedAt: age(observedAt, new Date()),
      organization: {
        name: "FlowOps",
        plan: "Public operational status",
        authorizationsPaused: health.authorizationsPaused,
      },
      chain: {
        network: "Base observer",
        state: health.chainState,
        observers: health.chainState === "HEALTHY" && health.respondingObservers === health.requiredObservers
          ? `${observerProgress} agree`
          : `${observerProgress} reporting`,
        lastTrustedBlock: trusted ? trusted.blockNumber.toLocaleString("en-US") : "Unavailable",
        lastTrustedAt: trustedAt ? age(trustedAt, new Date()) : "Unavailable",
      },
      money: privateMoney(),
      approvals: [],
      agents: [],
      activity: [],
      risks,
      reconciliation: unavailableReconciliation(),
    };
  } catch (error) {
    console.warn("[flowops-adapter] public health unavailable", safeErrorMessage(error));
    return publicUnavailable("The live control-plane status endpoint is temporarily unavailable. No organization data is shown.");
  }
}

function publicUnavailable(detail: string): DashboardSnapshot {
  return {
    mode: "public",
    connection: { label: "Status unavailable", detail },
    generatedAt: "Unavailable",
    organization: { name: "FlowOps", plan: "Public operational status", authorizationsPaused: true },
    chain: {
      network: "Base observer",
      state: "RECOVERING",
      observers: "Unavailable",
      lastTrustedBlock: "Unavailable",
      lastTrustedAt: "Unavailable",
    },
    money: privateMoney(),
    approvals: [],
    agents: [],
    activity: [],
    risks: [{
      id: "public-status-unavailable",
      severity: "critical",
      title: "Public status is unavailable",
      detail: "FlowOps refuses to infer chain health or authorization availability from missing evidence.",
      time: "Current request",
    }],
    reconciliation: unavailableReconciliation(),
  };
}

function privateMoney(): DashboardSnapshot["money"] {
  return {
    asset: "Member-only",
    total: "Private",
    available: "Private",
    reserved: "Private",
    pending: "Private",
    unresolved: "Private",
    spentToday: "Private",
    monthlySpent: "Private",
    monthlyBudget: "Private",
    monthlySpentPercent: null,
  };
}

function unavailableReconciliation(): DashboardSnapshot["reconciliation"] {
  return {
    available: false,
    checkpointBlock: "Unavailable",
    observedThroughBlock: "Unavailable",
    resolvedCandidates: 0,
    totalCandidates: 0,
    unresolvedOutcomes: 0,
    quarantinedOutcomes: 0,
    pendingFinality: 0,
    readyForManualResume: false,
    complete: false,
    unclassifiedLedgerTransactions: 0,
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

function isHumanRole(value: unknown): boolean {
  return ["OWNER", "ADMIN", "DEVELOPER", "FINANCE", "APPROVER", "AUDITOR", "VIEWER"].includes(String(value));
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
