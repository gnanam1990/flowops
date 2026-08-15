import type { ChatGPTUser } from "./chatgpt-auth";
import { exchangeSiteSession, loadAdapterConfig, type SessionResponse } from "./flowops-adapter";

const MAX_RESPONSE_BYTES = 256 * 1024;
const REQUEST_TIMEOUT_MS = 6_000;
const IDENTIFIER = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/;

export type DashboardCommandInput =
  | { type: "approval"; requestId: string; action: "APPROVE" | "REJECT"; note: string; operationId: string; stepUpToken: string }
  | { type: "organization-pause"; reason: string; operationId: string; stepUpToken: string };

type PrincipalClaims = {
  principalId: string;
  organizationId: string;
  kind: string;
  role: string;
  readOnly: boolean;
  stepUpUntil: string;
};

type StoredCommand = {
  id: string;
  organizationId: string;
  actorId: string;
  kind: string;
  state: "PENDING" | "SUCCEEDED" | "FAILED";
  errorCode?: string;
};

export class DashboardCommandError extends Error {
  constructor(readonly status: number, readonly code: string, message: string, readonly commandId = "") {
    super(message);
  }
}

export async function submitDashboardCommand(
  user: ChatGPTUser,
  input: DashboardCommandInput,
  request: typeof fetch = fetch,
): Promise<Record<string, unknown>> {
  validateCommandInput(input);
  const config = loadAdapterConfig();
  if (!config) throw new DashboardCommandError(503, "CONTROL_PLANE_NOT_CONFIGURED", "Live control is not configured.");
  const session = await exchangeSiteSession(user, config, request);
  validateSiteSession(session);
  const claims = await upstreamJSON<PrincipalClaims>(request, `${config.controlApiUrl}/v1/session`, {
    headers: { authorization: `Bearer ${input.stepUpToken}` },
  });
  validateStepUpBinding(session, claims);

  const idempotencyKey = await operationKey(input.operationId);
  let path: string;
  let body: Record<string, unknown>;
  if (input.type === "approval") {
    const snapshot = await upstreamJSON<{ organizationId: string; pendingApprovals: Array<{ requestId: string; requestDigest: string }> }>(
      request,
      `${config.controlApiUrl}/v1/dashboard/snapshot`,
      { headers: { authorization: `Bearer ${session.accessToken}` } },
    );
    if (snapshot.organizationId !== session.organizationId || !Array.isArray(snapshot.pendingApprovals)) {
      throw new DashboardCommandError(502, "INVALID_CONTROL_RESPONSE", "The control-plane snapshot could not be verified.");
    }
    const approval = snapshot.pendingApprovals.find((candidate) => candidate.requestId === input.requestId);
    if (!approval || !/^0x[0-9a-f]{64}$/.test(approval.requestDigest)) {
      throw new DashboardCommandError(409, "APPROVAL_NOT_PENDING", "This exact approval is no longer pending. Refresh before deciding.");
    }
    path = `/v1/approvals/${encodeURIComponent(input.requestId)}/decision`;
    body = { requestDigest: approval.requestDigest, action: input.action, note: input.note };
  } else {
    path = "/v1/organization/pause";
    body = { reason: input.reason };
  }
  const response = await upstreamResponse(request, `${config.controlApiUrl}${path}`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${input.stepUpToken}`,
      "content-type": "application/json",
      "idempotency-key": idempotencyKey,
    },
    body: JSON.stringify(body),
  });
  return normalizeCommandResponse(response.status, response.body, session);
}

export async function recoverDashboardCommand(
  user: ChatGPTUser,
  commandId: string,
  request: typeof fetch = fetch,
): Promise<Record<string, unknown>> {
  if (!IDENTIFIER.test(commandId)) throw new DashboardCommandError(400, "INVALID_COMMAND_ID", "The command reference is invalid.");
  const config = loadAdapterConfig();
  if (!config) throw new DashboardCommandError(503, "CONTROL_PLANE_NOT_CONFIGURED", "Live control is not configured.");
  const session = await exchangeSiteSession(user, config, request);
  validateSiteSession(session);
  const response = await upstreamResponse(request, `${config.controlApiUrl}/v1/commands/${encodeURIComponent(commandId)}`, {
    headers: { authorization: `Bearer ${session.accessToken}` },
  });
  return normalizeCommandResponse(response.status, response.body, session);
}

function validateCommandInput(input: DashboardCommandInput): void {
  if (!input || !IDENTIFIER.test(input.operationId) || typeof input.stepUpToken !== "string" || input.stepUpToken.length < 24 || input.stepUpToken.length > 512) {
    throw new DashboardCommandError(400, "INVALID_COMMAND", "The command request is invalid.");
  }
  if (input.type === "approval") {
    if (!exactKeys(input, ["action", "note", "operationId", "requestId", "stepUpToken", "type"])) throw new DashboardCommandError(400, "INVALID_COMMAND", "The approval request contains unsupported fields.");
    if (!IDENTIFIER.test(input.requestId) || (input.action !== "APPROVE" && input.action !== "REJECT") || typeof input.note !== "string" || input.note.length > 2_048) {
      throw new DashboardCommandError(400, "INVALID_COMMAND", "The approval decision is invalid.");
    }
    return;
  }
  if (!exactKeys(input, ["operationId", "reason", "stepUpToken", "type"])) throw new DashboardCommandError(400, "INVALID_COMMAND", "The pause request contains unsupported fields.");
  if (input.type !== "organization-pause" || typeof input.reason !== "string" || !input.reason.trim() || input.reason.length > 1_024) {
    throw new DashboardCommandError(400, "INVALID_COMMAND", "The pause reason is invalid.");
  }
}

function exactKeys(value: object, expected: string[]): boolean {
  return Object.keys(value).sort().join("\u0000") === [...expected].sort().join("\u0000");
}

function validateSiteSession(session: SessionResponse): void {
  if (!IDENTIFIER.test(session.organizationId) || !IDENTIFIER.test(session.principalId) || !IDENTIFIER.test(session.role) ||
      typeof session.accessToken !== "string" || !session.accessToken.startsWith("fos_v1.") || session.accessToken.length > 2_048 ||
      !Number.isFinite(Date.parse(session.expiresAt)) || Date.parse(session.expiresAt) <= Date.now()) {
    throw new DashboardCommandError(401, "MEMBERSHIP_UNAVAILABLE", "The dashboard membership could not be verified.");
  }
}

function validateStepUpBinding(session: SessionResponse, claims: PrincipalClaims): void {
  const expires = Date.parse(claims?.stepUpUntil);
  if (claims?.principalId !== session.principalId || claims?.organizationId !== session.organizationId || claims?.role !== session.role ||
      claims?.kind !== "HUMAN" || claims?.readOnly !== false || !Number.isFinite(expires) || expires <= Date.now()) {
    throw new DashboardCommandError(403, "STEP_UP_BINDING_FAILED", "Use a fresh step-up token for this same dashboard member and organization.");
  }
}

async function operationKey(operationId: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(`flowops-dashboard\u0000${operationId}`));
  return `ui_${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("").slice(0, 64)}`;
}

async function upstreamJSON<T>(request: typeof fetch, url: string, init: RequestInit): Promise<T> {
  const response = await upstreamResponse(request, url, init);
  if (response.status < 200 || response.status >= 300) {
    const error = asRecord(response.body.error);
    throw new DashboardCommandError(response.status, stringField(error, "code") || "UPSTREAM_REJECTED", "The control plane rejected the credential or request.", stringField(response.body, "commandId"));
  }
  return response.body as T;
}

async function upstreamResponse(request: typeof fetch, url: string, init: RequestInit): Promise<{ status: number; body: Record<string, unknown> }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const response = await request(url, { ...init, cache: "no-store", redirect: "manual", signal: controller.signal });
    if (response.status >= 300 && response.status < 400) throw new DashboardCommandError(502, "UPSTREAM_REDIRECT_REJECTED", "The control plane returned an unsafe redirect.");
    const declared = Number(response.headers.get("content-length") ?? "0");
    if (declared > MAX_RESPONSE_BYTES) throw new DashboardCommandError(502, "UPSTREAM_RESPONSE_TOO_LARGE", "The control-plane response was too large.");
    const raw = await response.text();
    if (raw.length > MAX_RESPONSE_BYTES) throw new DashboardCommandError(502, "UPSTREAM_RESPONSE_TOO_LARGE", "The control-plane response was too large.");
    const body = JSON.parse(raw) as unknown;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("invalid JSON object");
    return { status: response.status, body: body as Record<string, unknown> };
  } catch (error) {
    if (error instanceof DashboardCommandError) throw error;
    throw new DashboardCommandError(503, "CONTROL_PLANE_UNRESOLVED", "The command outcome is unresolved. Recover it using the command reference before retrying.");
  } finally {
    clearTimeout(timer);
  }
}

function normalizeCommandResponse(status: number, body: Record<string, unknown>, session: SessionResponse): Record<string, unknown> {
	const nested = asRecord(body.command);
	const command = Object.keys(nested).length > 0 ? nested : (typeof body.id === "string" && typeof body.state === "string" ? body : {});
  const commandId = stringField(command, "id") || stringField(body, "commandId");
  if (commandId && !IDENTIFIER.test(commandId)) throw new DashboardCommandError(502, "INVALID_CONTROL_RESPONSE", "The command reference could not be verified.");
	if (Object.keys(command).length > 0) {
    if (stringField(command, "organizationId") !== session.organizationId || stringField(command, "actorId") !== session.principalId ||
        !["PENDING", "SUCCEEDED", "FAILED"].includes(stringField(command, "state"))) {
      throw new DashboardCommandError(404, "COMMAND_NOT_FOUND", "The command is not available to this dashboard member.");
    }
		const stored = command as StoredCommand;
		const result = asRecord(Object.keys(asRecord(body.result)).length > 0 ? body.result : command.result);
		return { commandId: stored.id, state: stored.state, kind: stored.kind, errorCode: stored.errorCode || "", auditId: stringField(result, "auditId") };
  }
  const error = asRecord(body.error);
  const code = stringField(error, "code") || (status >= 500 ? "CONTROL_PLANE_UNRESOLVED" : "COMMAND_REJECTED");
  throw new DashboardCommandError(status, code, status >= 500 ? "The command outcome is unresolved." : "The command was rejected.", commandId);
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function stringField(value: Record<string, unknown>, key: string): string {
  return typeof value[key] === "string" ? value[key] : "";
}
