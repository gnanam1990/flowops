import assert from "node:assert/strict";
import { createServer } from "node:http";
import test from "node:test";

let workerPromise;

async function render({ path = "/", method = "GET", headers = {}, body, env = {} } = {}) {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerPromise ??= import(workerUrl.href).then((module) => module.default);
  const worker = await workerPromise;
  return worker.fetch(
    new Request(`http://localhost${path}`, { method, headers: { accept: "text/html", ...headers }, body }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) }, ...env },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

function configuredEnvironment(controlApiUrl, exchangeCredential) {
  return Object.fromEntries([
    ["FLOWOPS_CONTROL_API_URL", controlApiUrl],
    ["FLOWOPS_SITES_PROJECT_ID", "appgprj_flowops_test"],
    ["FLOWOPS_SITES_EXCHANGE_TOKEN", exchangeCredential],
  ]);
}

test("returns only a derived owner enrollment code from authenticated Sites identity", async () => {
  const anonymous = await render({
    path: "/api/flowops/enrollment",
    env: { FLOWOPS_SITES_PROJECT_ID: "appgprj_flowops_test" },
  });
  assert.equal(anonymous.status, 401);

  const response = await render({
    path: "/api/flowops/enrollment",
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: { FLOWOPS_SITES_PROJECT_ID: "appgprj_flowops_test" },
  });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  const enrollment = await response.json();
  assert.equal(enrollment.siteProjectId, "appgprj_flowops_test");
  assert.equal(enrollment.email, "owner@example.com");
  assert.match(enrollment.siteUserKey, /^[0-9a-f]{64}$/);
  assert.doesNotMatch(JSON.stringify(enrollment), /sites-user-opaque/);

  const page = await render({
    path: "/enrollment",
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: { FLOWOPS_SITES_PROJECT_ID: "appgprj_flowops_test" },
  });
  assert.equal(page.status, 200);
  const pageHtml = await page.text();
  assert.match(pageHtml, /Sites enrollment identity/);
  assert.match(pageHtml, /appgprj_flowops_test/);
  assert.match(pageHtml, /owner@example\.com/);
  assert.doesNotMatch(pageHtml, /sites-user-opaque/);
});

test("binds dashboard writes to the same stepped-up member and authoritative approval digest", async (t) => {
  const exchangeCredential = "sites-exchange-command-credential-000000000001";
  const siteToken = "fos_v1.command-payload.command-signature";
  const stepUpToken = "fo_step_owner_live_00000000000000000001";
  const exactDigest = `0x${"b".repeat(64)}`;
  let decisionRequests = 0;
  let stepPrincipal = "owner_live";
  const upstream = createServer(async (request, response) => {
    const json = (status, body) => { response.writeHead(status, { "content-type": "application/json" }); response.end(JSON.stringify(body)); };
    if (request.url === "/v1/sites/session") {
      assert.equal(request.headers.authorization, `Bearer ${exchangeCredential}`);
      return json(201, { accessToken: siteToken, expiresAt: new Date(Date.now() + 120_000).toISOString(), organizationId: "org_live", principalId: "owner_live", role: "OWNER" });
    }
    if (request.url === "/v1/session") {
      assert.equal(request.headers.authorization, `Bearer ${stepUpToken}`);
      return json(200, { principalId: stepPrincipal, organizationId: "org_live", kind: "HUMAN", role: "OWNER", readOnly: false, stepUpUntil: new Date(Date.now() + 120_000).toISOString() });
    }
    if (request.url === "/v1/dashboard/snapshot") {
      assert.equal(request.headers.authorization, `Bearer ${siteToken}`);
      return json(200, { organizationId: "org_live", pendingApprovals: [{ requestId: "req_live_1", requestDigest: exactDigest }] });
    }
    if (request.url === "/v1/approvals/req_live_1/decision") {
      decisionRequests += 1;
      assert.equal(request.headers.authorization, `Bearer ${stepUpToken}`);
      assert.match(request.headers["idempotency-key"], /^ui_[0-9a-f]{64}$/);
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      assert.deepEqual(JSON.parse(Buffer.concat(chunks).toString("utf8")), { requestDigest: exactDigest, action: "APPROVE", note: "verified" });
      return json(200, { command: { id: "cmd_live_1", organizationId: "org_live", actorId: "owner_live", kind: "approval.decide", state: "SUCCEEDED" } });
    }
    if (request.url === "/v1/commands/cmd_live_1") {
      assert.equal(request.headers.authorization, `Bearer ${siteToken}`);
      return json(200, { id: "cmd_live_1", organizationId: "org_live", actorId: "owner_live", kind: "approval.decide", state: "SUCCEEDED" });
    }
    json(404, { error: { code: "NOT_FOUND" } });
  });
  await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => upstream.close(resolve)));
  const address = upstream.address();
  assert.ok(address && typeof address !== "string");
  const env = configuredEnvironment(`http://127.0.0.1:${address.port}`, exchangeCredential);
  const identityHeaders = { "oai-authenticated-user-id": "sites-user-opaque", "oai-authenticated-user-email": "owner@example.com" };
  const submitted = await render({
    path: "/api/flowops/commands", method: "POST",
    headers: { ...identityHeaders, "content-type": "application/json" },
    body: JSON.stringify({ type: "approval", requestId: "req_live_1", action: "APPROVE", note: "verified", operationId: "op_live_1", stepUpToken }), env,
  });
  assert.equal(submitted.status, 200);
  assert.deepEqual(await submitted.json(), { commandId: "cmd_live_1", state: "SUCCEEDED", kind: "approval.decide", errorCode: "", auditId: "" });
  assert.equal(decisionRequests, 1);

  stepPrincipal = "owner_substituted";
  const substituted = await render({
    path: "/api/flowops/commands", method: "POST",
    headers: { ...identityHeaders, "content-type": "application/json" },
    body: JSON.stringify({ type: "approval", requestId: "req_live_1", action: "APPROVE", note: "verified", operationId: "op_live_substituted", stepUpToken }), env,
  });
  assert.equal(substituted.status, 403);
  assert.equal((await substituted.json()).error.code, "STEP_UP_BINDING_FAILED");
  assert.equal(decisionRequests, 1);
  stepPrincipal = "owner_live";

  const recovered = await render({ path: "/api/flowops/commands/cmd_live_1", headers: identityHeaders, env });
  assert.equal(recovered.status, 200);
  assert.deepEqual(await recovered.json(), { commandId: "cmd_live_1", state: "SUCCEEDED", kind: "approval.decide", errorCode: "", auditId: "" });
});

test("renders the FlowOps economic control room", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.match(html, /<title>FlowOps — Economic control for autonomous agents<\/title>/i);
  assert.match(
    html,
    /<meta name="base:app_id" content="6a8039cbe4a8a41598e7a325"\s*\/?>/i,
  );
  assert.match(
    html,
    /<meta property="og:image" content="http:\/\/localhost(?::3000)?\/og\.png"\s*\/?>/i,
  );
  assert.match(
    html,
    /<meta name="twitter:image" content="http:\/\/localhost(?::3000)?\/og\.png"\s*\/?>/i,
  );
  assert.match(html, /Treasury command/);
  assert.match(html, /Review (?:<!-- -->)?3(?:<!-- -->)? approvals/);
  assert.match(html, /Base Sepolia/);
  assert.match(html, /Preview data/);
  assert.match(html, /Pending chain evidence/);
  assert.match(html, /Non-custodial/);
  assert.match(html, /Observer quorum/);
  assert.match(html, /Preview records are illustrative and cannot move funds/);
  assert.match(html, /Emergency pause/);
  assert.match(html, /Economic activity/);
});

test("exchanges Sites identity server-side and renders only authorized live fields", async (t) => {
  const exchangeCredential = "sites-exchange-test-credential-000000000001";
  const sessionToken = "fos_v1.test-payload.test-signature";
  const now = new Date();
  let snapshotOrganizationId = "org_live";
  let organizationPaused = false;
  const upstream = createServer(async (request, response) => {
    if (request.url === "/v1/sites/session") {
      assert.equal(request.method, "POST");
      assert.equal(request.headers.authorization, `Bearer ${exchangeCredential}`);
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      assert.equal(body.siteProjectId, "appgprj_flowops_test");
      assert.equal(body.email, "owner@example.com");
      assert.match(body.siteUserKey, /^[0-9a-f]{64}$/);
      response.writeHead(201, { "content-type": "application/json" });
      response.end(JSON.stringify({
        accessToken: sessionToken,
        expiresAt: new Date(Date.now() + 120_000).toISOString(),
        organizationId: "org_live",
        principalId: "owner_live",
        role: "OWNER",
      }));
      return;
    }
    if (request.url === "/v1/dashboard/snapshot") {
      assert.equal(request.headers.authorization, `Bearer ${sessionToken}`);
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({
        live: true,
        generatedAt: now.toISOString(),
        organizationId: snapshotOrganizationId,
        organization: { id: snapshotOrganizationId, name: "Acme Operators", authorizationsPaused: organizationPaused },
        chain: {
          state: "HEALTHY",
          reason: "independent observers agree",
          requiredObserverQuorum: 2,
          respondingObservers: 2,
          lastTrusted: { blockNumber: 12345678, observedAt: now.toISOString() },
          authorizationsPaused: false,
        },
        pendingApprovals: [{
          requestId: "req_live_1",
          requestDigest: `0x${"a".repeat(64)}`,
          submittedAt: Math.floor(now.getTime() / 1000) - 30,
          approvalExpiresAt: Math.floor(now.getTime() / 1000) + 300,
          decision: { reason: "HUMAN_APPROVAL_THRESHOLD", policyVersion: "policy_live_1" },
          intent: {
            agentId: "agent_live", taskId: "task_live", rail: "X402",
            recipient: `0x${"1".repeat(40)}`, asset: `0x${"2".repeat(40)}`,
            amountAtomic: "1250000", purpose: "Buy verified dataset",
          },
        }],
		agents: [{ id: "agent_live", name: "Research Agent", purpose: "Evidence acquisition", status: "ACTIVE" }],
		reconciliation: {
		  available: true,
		  recovery: {
			checkpointBlock: 12345670, observedThroughBlock: 12345678,
			totalCandidates: 4, resolvedCandidates: 3, unresolvedOutcomes: 1,
			quarantinedOutcomes: 0, pendingFinality: 1,
			readyForManualResume: false, complete: false,
		  },
		  assets: [{
			asset: `0x${"2".repeat(40)}`, escrowLockedAtomic: "250000",
			recognizedExpenseAtomic: "1750000", spentTodayAtomic: "500000",
			spentMonthAtomic: "1750000", unresolvedAtomic: "125000",
		  }],
		  exceptions: [{
			id: "exec_unresolved", kind: "DIRECT_EXECUTION", state: "PENDING_CHAIN_RECOVERY",
			asset: `0x${"2".repeat(40)}`, amountAtomic: "125000",
			firstObservedAt: new Date(now.getTime() - 60_000).toISOString(),
			reason: "canonical outcome remains unresolved", operatorActionNeeded: true,
		  }],
		  unclassifiedLedgerTransactions: 0,
		},
      }));
      return;
    }
    response.writeHead(404).end();
  });
  await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => upstream.close(resolve)));
  const address = upstream.address();
  assert.ok(address && typeof address !== "string");

  const response = await render({
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: configuredEnvironment(`http://127.0.0.1:${address.port}`, exchangeCredential),
  });
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /Live control plane/);
  assert.match(html, /Acme Operators/);
  assert.match(html, /2 \/ 2 agree/);
  assert.match(html, /Buy verified dataset/);
  assert.match(html, /1,250,000 atomic/);
  assert.match(html, /0x2222…2222/);
	assert.match(html, /Recognized economic expense/);
	assert.match(html, /1,750,000 atomic/);
	assert.match(html, /Journal-derived/);
	assert.match(html, /Direct execution is pending chain recovery/);
	assert.match(html, /Monthly usage<\/span><strong>Unavailable/);
  assert.doesNotMatch(html, /On track|Spendable now/);
  assert.doesNotMatch(html, /Northstar Labs|\$15,140\.00|Signal Harbor/);
  assert.doesNotMatch(html, new RegExp(exchangeCredential));
  assert.doesNotMatch(html, new RegExp(sessionToken.replaceAll(".", "\\.")));

  organizationPaused = true;
  const paused = await render({
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: configuredEnvironment(`http://127.0.0.1:${address.port}`, exchangeCredential),
  });
  const pausedHtml = await paused.text();
  assert.match(pausedHtml, /Organization authorizations paused/);
  assert.match(pausedHtml, /The persistent organization gate blocks new authorization issuance/);
  organizationPaused = false;

  snapshotOrganizationId = "org_substituted";
  const substituted = await render({
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: configuredEnvironment(`http://127.0.0.1:${address.port}`, exchangeCredential),
  });
  const substitutedHtml = await substituted.text();
  assert.match(substitutedHtml, /Preview data/);
  assert.doesNotMatch(substitutedHtml, /Acme Operators|Live control plane/);

  const missingBindings = await render({
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
  });
  const missingBindingsHtml = await missingBindings.text();
  assert.match(missingBindingsHtml, /Live data is not configured for this deployment/);
  assert.doesNotMatch(missingBindingsHtml, /Acme Operators|Live control plane/);
});

test("does not replay the Sites exchange credential across an upstream redirect", async (t) => {
  let redirectedRequests = 0;
  const redirectTarget = createServer((_request, response) => {
    redirectedRequests += 1;
    response.writeHead(500).end();
  });
  await new Promise((resolve) => redirectTarget.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => redirectTarget.close(resolve)));
  const targetAddress = redirectTarget.address();
  assert.ok(targetAddress && typeof targetAddress !== "string");

  const upstream = createServer((_request, response) => {
    response.writeHead(307, {
      location: `http://127.0.0.1:${targetAddress.port}/credential-capture`,
    }).end();
  });
  await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => upstream.close(resolve)));
  const upstreamAddress = upstream.address();
  assert.ok(upstreamAddress && typeof upstreamAddress !== "string");

  const response = await render({
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: {
	  ...configuredEnvironment(
		`http://127.0.0.1:${upstreamAddress.port}`,
		["sites", "exchange", "test", "credential", "000000000002"].join("-"),
	  ),
	},
  });
  const html = await response.text();
  assert.match(html, /Preview data/);
  assert.equal(redirectedRequests, 0);
});

test("removes starter content and never claims preview controls executed", async () => {
  const response = await render();
  const html = await response.text();
  assert.doesNotMatch(html, /Your site is taking shape|Building your site|codex-preview/);
  assert.doesNotMatch(html, /react-loading-skeleton/);
  assert.doesNotMatch(html, /pause successful|payment successful|approval successful/i);
  assert.doesNotMatch(html, /submitted successfully|write succeeded/i);
});
