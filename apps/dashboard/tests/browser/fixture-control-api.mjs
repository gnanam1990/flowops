import { createServer } from "node:http";

const port = Number(process.argv[2] ?? "43121");
const now = new Date();
const usdc = "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913";
const recipient = "0x1111111111111111111111111111111111111111";
const requestDigest = `0x${"a".repeat(64)}`;
const exchangeCredential = ["browser", "e2e", "exchange", "fixture", "credential"].join("-");
const sessionToken = ["fos_v1", "browser-payload", "browser-signature"].join(".");

function send(response, status, body) {
  response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" });
  response.end(JSON.stringify(body));
}

const server = createServer(async (request, response) => {
  if (request.url === "/health") {
    return send(response, 200, {
      controlPlane: "AVAILABLE", chainId: 8453, chainState: "HEALTHY", authorizationsPaused: false,
      requiredObservers: 2, respondingObservers: 2, lastObservationAt: now.toISOString(), readyForManualResume: false,
      lastTrusted: { blockNumber: 12345678, observedAt: now.toISOString() },
    });
  }
  if (request.url === "/v1/sites/session" && request.method === "POST") {
    if (request.headers.authorization !== `Bearer ${exchangeCredential}`) {
      return send(response, 401, { error: { code: "UNAUTHENTICATED" } });
    }
    return send(response, 201, {
      accessToken: sessionToken,
      expiresAt: new Date(Date.now() + 120_000).toISOString(),
      organizationId: "org_browser", principalId: "owner_browser", role: "OWNER",
    });
  }
  if (request.url === "/v1/dashboard/snapshot" && request.method === "GET") {
    if (request.headers.authorization !== `Bearer ${sessionToken}`) {
      return send(response, 401, { error: { code: "UNAUTHENTICATED" } });
    }
    return send(response, 200, {
      live: true, generatedAt: now.toISOString(), organizationId: "org_browser",
      organization: { id: "org_browser", name: "Browser Operators", authorizationsPaused: false },
      chain: {
        chainId: 8453, state: "HEALTHY", reason: "independent observers agree",
        requiredObserverQuorum: 2, respondingObservers: 2,
        lastTrusted: { blockNumber: 12345678, observedAt: now.toISOString() }, authorizationsPaused: false,
      },
      pendingApprovals: [{
        requestId: "req_browser_1", requestDigest,
        submittedAt: Math.floor(now.getTime() / 1000) - 30,
        approvalExpiresAt: Math.floor(now.getTime() / 1000) + 300,
        decision: { reason: "HUMAN_APPROVAL_THRESHOLD", policyVersion: "policy_browser_1" },
        intent: {
          chainId: 8453, agentId: "agent_browser", taskId: "task_browser", rail: "ESCROW",
          recipient, asset: usdc, amountAtomic: "1250000", purpose: "Buy verified browser dataset",
        },
      }],
      agents: [{ id: "agent_browser", name: "Browser Agent", purpose: "Browser acceptance", status: "ACTIVE" }],
      reconciliation: {
        available: true,
        recovery: { checkpointBlock: 12345670, observedThroughBlock: 12345678, totalCandidates: 0, resolvedCandidates: 0,
          unresolvedOutcomes: 0, quarantinedOutcomes: 0, pendingFinality: 0, readyForManualResume: false, complete: true },
        assets: [], exceptions: [], unclassifiedLedgerTransactions: 0,
      },
      ascp: {
        available: true, pendingApprovals: [],
        assets: [{ asset: usdc, walletDeltaAtomic: "-1250000", escrowRestrictedAtomic: "-1250000",
          recognizedExpenseAtomic: "0", spentTodayAtomic: "0", reservedAtomic: "1250000",
          pendingChainAtomic: "0", unresolvedAtomic: "0" }],
        agentBudgets: [{ agentId: "agent_browser", asset: usdc, dailyLimitAtomic: "10000000",
          spentTodayAtomic: "0", reservedAtomic: "1250000", availableAtomic: "8750000", currentTaskId: "task_browser",
          activePolicy: true, policyVersion: "policy_browser_1", policyConfigurationValid: true }],
        activity: [],
      },
    });
  }
  return send(response, 404, { error: { code: "NOT_FOUND" } });
});

server.listen(port, "127.0.0.1");

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
