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
	chainId: number;
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
	chainId: number;
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
	ascp: ControlASCP;
};

type ControlASCP = {
	available: boolean;
	pendingApprovals: Array<{
		approvalId: string;
		reviewDigest: string;
		operationId: string;
		agentId: string;
		taskId: string;
		category: string;
		reason: string;
		policyVersion: string;
		recipient: string;
		asset: string;
		chainId: number;
		assetSymbol?: string;
		assetDecimals?: number;
		amountAtomic: string;
		requestedAt: string;
		expiresAt: string;
	}>;
	assets: Array<{
		asset: string;
		walletDeltaAtomic: string;
		escrowRestrictedAtomic: string;
		recognizedExpenseAtomic: string;
		spentTodayAtomic: string;
		reservedAtomic: string;
		pendingChainAtomic: string;
		unresolvedAtomic: string;
	}>;
	agentBudgets: Array<{
		agentId: string;
		asset?: string;
		dailyLimitAtomic: string;
		spentTodayAtomic: string;
		reservedAtomic: string;
		availableAtomic: string;
		currentTaskId?: string;
		activePolicy: boolean;
		policyVersion?: string;
		policyConfigurationValid: boolean;
	}>;
	activity: Array<{
		id: string;
		kind: string;
		state: string;
		agentId?: string;
		taskId?: string;
		asset?: string;
		amountAtomic?: string;
		detail?: string;
		occurredAt: string;
	}>;
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
	chainId: number;
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
	!isSupportedBaseChain(raw.chain.chainId) ||
    !Number.isSafeInteger(raw.chain.requiredObserverQuorum) ||
    raw.chain.requiredObserverQuorum < 2 ||
    raw.chain.requiredObserverQuorum > 5 ||
	!Number.isSafeInteger(raw.chain.respondingObservers) ||
    raw.chain.respondingObservers < 0 ||
	raw.chain.respondingObservers > 5 ||
	!validReconciliation(raw.reconciliation) ||
	!validASCP(raw.ascp)
  ) {
    throw new Error("invalid dashboard snapshot");
  }
  const generatedAt = parseDate(raw.generatedAt);
  const budgetByAgent = new Map(raw.ascp.agentBudgets.map((budget) => [budget.agentId, budget]));
  const agents = raw.agents.map((agent) => mapAgent(agent, budgetByAgent.get(agent.id), raw.chain.chainId));
  const names = new Map(agents.map((agent) => [agent.id, agent.name]));
	const approvals = [
		...raw.pendingApprovals.map((approval) => mapApproval(approval, names, generatedAt)),
		...raw.ascp.pendingApprovals.map((approval) => mapASCPApproval(approval, names, generatedAt)),
	];
	const activity = [
		...raw.ascp.activity.map((item) => ({ occurredAt: parseDate(item.occurredAt), item: mapASCPActivity(item, names, generatedAt, raw.chain.chainId) })),
		...raw.pendingApprovals.map((item) => ({ occurredAt: new Date(item.submittedAt * 1_000), item: mapLegacyApprovalActivity(item, names, generatedAt) })),
	].sort((left, right) => right.occurredAt.getTime() - left.occurredAt.getTime()).map((entry) => entry.item);
	const risks = liveRisks(raw, agents, generatedAt);
  const checkpoint = raw.chain.lastTrusted;
  if (checkpoint && (!Number.isSafeInteger(checkpoint.blockNumber) || checkpoint.blockNumber < 0)) {
    throw new Error("invalid checkpoint");
  }
  const checkpointTime = checkpoint ? parseDate(checkpoint.observedAt) : null;
  const observerProgress = `${raw.chain.respondingObservers} / ${raw.chain.requiredObserverQuorum}`;
	const unavailable = "Not available";
	const singleAsset = raw.ascp.available && raw.ascp.assets.length === 1 ? raw.ascp.assets[0] : null;
	const singleAssetMetadata = singleAsset ? validatedAssetMetadata(raw.chain.chainId, singleAsset.asset) : null;
	const assetLabel = singleAsset ? (singleAssetMetadata?.symbol ?? shortAddress(singleAsset.asset)) : raw.ascp.assets.length > 1 ? "Multiple assets" : "No ASCP activity";
	const singleBudget = raw.ascp.agentBudgets.length === 1 && raw.ascp.agentBudgets[0].policyConfigurationValid ? raw.ascp.agentBudgets[0] : null;
	const dailySpentPercent = singleBudget ? atomicPercent(singleBudget.spentTodayAtomic, singleBudget.dailyLimitAtomic) : null;
  return {
    mode: "live",
    connection: {
      label: "Live control plane",
      detail: "Organization-scoped read session. Writes require separate step-up authentication.",
    },
    generatedAt: age(generatedAt, new Date()),
    organization: { name: raw.organization.name, plan: "Authorized membership", authorizationsPaused: raw.organization.authorizationsPaused },
    chain: {
	  chainId: raw.chain.chainId,
	  network: networkLabel(raw.chain.chainId),
      state: raw.chain.state,
      observers: raw.chain.state === "HEALTHY" ? `${observerProgress} agree` : `${observerProgress} reporting · authorizations restricted`,
      lastTrustedBlock: checkpoint ? checkpoint.blockNumber.toLocaleString("en-US") : "Unavailable",
      lastTrustedAt: checkpointTime ? age(checkpointTime, new Date()) : "Unavailable",
    },
    money: {
	  asset: assetLabel,
	  total: singleAsset ? tokenAmount(singleAsset.recognizedExpenseAtomic, singleAssetMetadata) : unavailable,
	  walletDelta: singleAsset ? tokenAmount(singleAsset.walletDeltaAtomic, singleAssetMetadata) : unavailable,
	  reserved: singleAsset ? tokenAmount(singleAsset.reservedAtomic, singleAssetMetadata) : unavailable,
	  pending: singleAsset ? tokenAmount(singleAsset.pendingChainAtomic, singleAssetMetadata) : unavailable,
	  unresolved: singleAsset ? tokenAmount(singleAsset.unresolvedAtomic, singleAssetMetadata) : unavailable,
	  spentToday: singleAsset ? tokenAmount(singleAsset.spentTodayAtomic, singleAssetMetadata) : unavailable,
	  dailyLimit: singleBudget ? tokenAmount(singleBudget.dailyLimitAtomic, singleBudget.asset ? validatedAssetMetadata(raw.chain.chainId, singleBudget.asset) : null) : unavailable,
	  dailySpentPercent,
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

function mapAgent(raw: ControlAgent, budget: ControlASCP["agentBudgets"][number] | undefined, chainId: 8453 | 84532): Agent {
  if (
    !isIdentifier(raw?.id) ||
    typeof raw.name !== "string" ||
    !raw.name.trim() ||
    raw.name.length > 200 ||
    typeof raw.purpose !== "string" ||
    raw.purpose.length > 1_024 ||
    !isAgentStatus(raw.status)
  ) throw new Error("invalid agent");
	const metadata = budget?.asset ? validatedAssetMetadata(chainId, budget.asset) : null;
  return {
    id: raw.id,
    name: raw.name,
    mark: initials(raw.name),
    purpose: raw.purpose || "Purpose not supplied",
    status: raw.status,
	available: budget?.policyConfigurationValid ? tokenAmount(budget.availableAtomic, metadata) : "No active policy",
	spent: budget?.policyConfigurationValid ? tokenAmount(budget.spentTodayAtomic, metadata) : "Unavailable",
	limit: budget?.policyConfigurationValid ? tokenAmount(budget.dailyLimitAtomic, metadata) : "Unavailable",
	percent: budget?.policyConfigurationValid ? atomicPercent(budget.spentTodayAtomic, budget.dailyLimitAtomic) : 0,
	task: budget?.currentTaskId || "No recorded ASCP task",
  };
}

function mapASCPApproval(raw: ControlASCP["pendingApprovals"][number], names: Map<string, string>, observedAt: Date): Approval {
	if (!isIdentifier(raw.approvalId) || !/^0x[0-9a-f]{64}$/.test(raw.reviewDigest) || !/^0x[0-9a-f]{64}$/.test(raw.operationId) ||
		!isIdentifier(raw.agentId) || (raw.taskId !== "" && !isBoundedText(raw.taskId, 1024)) ||
		(raw.category !== "" && !isBoundedText(raw.category, 1024)) ||
		!/^0x[0-9a-f]{40}$/.test(raw.recipient) || !/^0x[0-9a-f]{40}$/.test(raw.asset) || !validUnsignedAtomic(raw.amountAtomic) ||
		!isSupportedBaseChain(raw.chainId)) {
		throw new Error("invalid ASCP approval");
	}
	const metadata = validatedAssetMetadata(raw.chainId, raw.asset, raw.assetSymbol, raw.assetDecimals);
	const requestedAt = parseDate(raw.requestedAt);
	const expiresAt = parseDate(raw.expiresAt);
	if (expiresAt <= requestedAt) throw new Error("invalid ASCP approval lifetime");
	const agent = names.get(raw.agentId) ?? raw.agentId;
	return {
		source: "ascp",
		id: raw.approvalId,
		agent,
		agentMark: initials(agent),
		title: raw.category || (raw.taskId ? `Task ${raw.taskId}` : `Approval ${shortDigest(raw.approvalId)}`),
		vendor: shortAddress(raw.recipient),
		recipientAddress: raw.recipient,
		amount: tokenAmount(raw.amountAtomic, metadata),
		amountAtomic: raw.amountAtomic,
		requested: age(requestedAt, observedAt),
		expires: durationUntil(Math.floor(expiresAt.getTime() / 1000), observedAt),
		reason: humanize(raw.reason),
		risk: riskForReason(raw.reason),
		rail: "Escrowed call",
		asset: metadata ? metadata.symbol : shortAddress(raw.asset),
		assetAddress: raw.asset,
		assetSymbol: metadata?.symbol,
		assetDecimals: metadata?.decimals,
		chainId: raw.chainId,
		network: networkLabel(raw.chainId),
		policyVersion: raw.policyVersion,
		requestDigest: raw.reviewDigest,
		evidenceRefs: `Operation ${raw.operationId}`,
	};
}

function mapASCPActivity(raw: ControlASCP["activity"][number], names: Map<string, string>, observedAt: Date, chainId: 8453 | 84532): Activity {
	if (!isIdentifier(raw.id) || typeof raw.kind !== "string" || !raw.kind || typeof raw.state !== "string" || !raw.state ||
		(raw.agentId && !isIdentifier(raw.agentId)) || (raw.taskId && !isBoundedText(raw.taskId, 1024)) ||
		(raw.asset && !/^0x[0-9a-f]{40}$/.test(raw.asset)) || (raw.amountAtomic && !validUnsignedAtomic(raw.amountAtomic))) {
		throw new Error("invalid ASCP activity");
	}
	const occurredAt = parseDate(raw.occurredAt);
	const agent = raw.agentId ? (names.get(raw.agentId) ?? raw.agentId) : "Control plane";
	const state = activityState(raw.kind, raw.state);
	const metadata = raw.asset ? validatedAssetMetadata(chainId, raw.asset) : null;
	return {
		id: `${raw.kind}-${raw.id}-${raw.state}`,
		time: age(occurredAt, observedAt),
		title: activityTitle(raw.kind, raw.state),
		detail: [agent, raw.taskId ? `Task ${raw.taskId}` : "", raw.detail ? humanize(raw.detail) : ""].filter(Boolean).join(" · "),
		amount: raw.amountAtomic ? `${tokenAmount(raw.amountAtomic, metadata)}${raw.asset && !metadata ? ` · ${shortAddress(raw.asset)}` : ""}` : undefined,
		state,
	};
}

function activityState(kind: string, state: string): Activity["state"] {
	if (kind === "CONTROL_AUDIT") return "security";
	if (kind === "ASCP_APPROVAL") return "approval";
	if (kind === "POLICY_DECISION") return "decision";
	if (state === "RELEASED_FINALIZED") return "released";
	if (state === "REFUNDED_FINALIZED") return "refunded";
	if (state.endsWith("FINALIZED")) return "settled";
	return "pending";
}

function activityTitle(kind: string, state: string): string {
	if (kind === "CONTROL_AUDIT") return humanize(state);
	if (kind === "ASCP_APPROVAL") return `Approval ${humanize(state).toLowerCase()}`;
	if (kind === "POLICY_DECISION") return `Policy ${humanize(state).toLowerCase()}`;
	return `Payment ${humanize(state).toLowerCase()}`;
}

function mapLegacyApprovalActivity(raw: ControlApproval, names: Map<string, string>, observedAt: Date): Activity {
	const approval = mapApproval(raw, names, observedAt);
	return {
		id: `LEGACY_APPROVAL-${approval.id}`,
		time: approval.requested,
		title: "Approval requested",
		detail: `${approval.agent} · ${approval.vendor}`,
		amount: approval.amount,
		state: "approval",
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
	!isSupportedBaseChain(raw.intent?.chainId) ||
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
	const metadata = validatedAssetMetadata(raw.intent.chainId, raw.intent.asset);
  return {
	source: "legacy",
    id: raw.requestId,
    agent,
    agentMark: initials(agent),
    title: raw.intent.purpose || `Task ${raw.intent.taskId}`,
    vendor: recipient,
	recipientAddress: raw.intent.recipient,
	amount: tokenAmount(raw.intent.amountAtomic, metadata),
	amountAtomic: raw.intent.amountAtomic,
    requested: age(new Date(raw.submittedAt * 1_000), observedAt),
    expires: durationUntil(raw.approvalExpiresAt, observedAt),
    reason: humanize(raw.decision?.reason || "POLICY_REQUIRES_APPROVAL"),
    risk: riskForReason(raw.decision?.reason),
    rail: mapRail(raw.intent.rail),
	asset: metadata ? metadata.symbol : shortAddress(raw.intent.asset),
	assetAddress: raw.intent.asset,
	assetSymbol: metadata?.symbol,
	assetDecimals: metadata?.decimals,
	chainId: raw.intent.chainId,
	network: networkLabel(raw.intent.chainId),
    policyVersion: raw.decision?.policyVersion || "Not exposed",
	requestDigest: raw.requestDigest,
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

function validASCP(value: ControlASCP | undefined): value is ControlASCP {
	if (!value || typeof value.available !== "boolean" || !Array.isArray(value.pendingApprovals) || !Array.isArray(value.assets) || !Array.isArray(value.agentBudgets) || !Array.isArray(value.activity)) return false;
	if (!value.assets.every((asset) => /^0x[0-9a-f]{40}$/.test(asset.asset) && validSignedAtomic(asset.walletDeltaAtomic) &&
		[asset.escrowRestrictedAtomic, asset.recognizedExpenseAtomic, asset.spentTodayAtomic].every(validSignedAtomic) &&
		[asset.reservedAtomic, asset.pendingChainAtomic, asset.unresolvedAtomic].every(validUnsignedAtomic))) return false;
	if (!value.agentBudgets.every((budget) => isIdentifier(budget.agentId) && (!budget.asset || /^0x[0-9a-f]{40}$/.test(budget.asset)) && typeof budget.activePolicy === "boolean" && typeof budget.policyConfigurationValid === "boolean" &&
		[budget.dailyLimitAtomic, budget.spentTodayAtomic, budget.reservedAtomic, budget.availableAtomic].every((amount) => amount === "" || validUnsignedAtomic(amount)))) return false;
	return true;
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
	  !isSupportedBaseChain(health.chainId) ||
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
		chainId: health.chainId,
		network: networkLabel(health.chainId),
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
	  chainId: 0,
	  network: "Base network unavailable",
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
	walletDelta: "Private",
    reserved: "Private",
    pending: "Private",
    unresolved: "Private",
    spentToday: "Private",
	dailyLimit: "Private",
	dailySpentPercent: null,
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

function isBoundedText(value: unknown, maximum: number): value is string {
	return typeof value === "string" && value.length > 0 && value.length <= maximum && value.trim() === value && !value.includes("\u0000");
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

type AssetMetadata = { symbol: "USDC"; decimals: 6 };

function validatedAssetMetadata(chainId: number, asset: string, reportedSymbol?: string, reportedDecimals?: number): AssetMetadata | null {
	let expected: AssetMetadata | null = null;
	if (chainId === 8453 && asset === "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913") expected = { symbol: "USDC", decimals: 6 };
	if (chainId === 84532 && asset === "0x036cbd53842c5426634e7929541ec2318f3dcf7e") expected = { symbol: "USDC", decimals: 6 };
	if (reportedSymbol !== undefined || reportedDecimals !== undefined) {
		if (!expected || reportedSymbol !== expected.symbol || reportedDecimals !== expected.decimals) throw new Error("asset metadata does not match the canonical chain asset");
	}
	return expected;
}

function tokenAmount(atomic: string, metadata: AssetMetadata | null): string {
	if (!metadata) return `${formatAtomic(atomic)} atomic`;
	const negative = atomic.startsWith("-");
	const magnitude = negative ? atomic.slice(1) : atomic;
	const padded = magnitude.padStart(metadata.decimals + 1, "0");
	const split = padded.length - metadata.decimals;
	return `${negative ? "-" : ""}${BigInt(padded.slice(0, split)).toLocaleString("en-US")}.${padded.slice(split)} ${metadata.symbol}`;
}

function isSupportedBaseChain(value: unknown): value is 8453 | 84532 {
	return value === 8453 || value === 84532;
}

function networkLabel(chainId: 8453 | 84532): string {
	return chainId === 8453 ? "Base Mainnet (8453)" : "Base Sepolia (84532)";
}

function atomicPercent(spent: string, limit: string): number {
	const spentValue = BigInt(spent);
	const limitValue = BigInt(limit);
	if (limitValue <= 0n) return 0;
	const basisPoints = spentValue * 10_000n / limitValue;
	return Math.min(100, Number(basisPoints) / 100);
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
