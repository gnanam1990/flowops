import assert from "node:assert/strict";
import { createServer } from "node:http";
import test from "node:test";

async function render({ headers = {}, env = {} } = {}) {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request("http://localhost/", { headers: { accept: "text/html", ...headers } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) }, ...env },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("renders the FlowOps economic control room", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.match(html, /<title>FlowOps — Economic control for autonomous agents<\/title>/i);
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
  const exchangeToken = "sites-exchange-test-credential-000000000001";
  const sessionToken = "fos_v1.test-payload.test-signature";
  const now = new Date();
  let snapshotOrganizationId = "org_live";
  const upstream = createServer(async (request, response) => {
    if (request.url === "/v1/sites/session") {
      assert.equal(request.method, "POST");
      assert.equal(request.headers.authorization, `Bearer ${exchangeToken}`);
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
        organization: { id: snapshotOrganizationId, name: "Acme Operators" },
        chain: {
          state: "HEALTHY",
          reason: "independent observers agree",
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
            recipient: `0x${"1".repeat(40)}`, amountAtomic: "1250000", purpose: "Buy verified dataset",
          },
        }],
        agents: [{ id: "agent_live", name: "Research Agent", purpose: "Evidence acquisition", status: "ACTIVE" }],
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
    env: {
      FLOWOPS_CONTROL_API_URL: `http://127.0.0.1:${address.port}`,
      FLOWOPS_SITES_PROJECT_ID: "appgprj_flowops_test",
      FLOWOPS_SITES_EXCHANGE_TOKEN: exchangeToken,
    },
  });
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /Live control plane/);
  assert.match(html, /Acme Operators/);
  assert.match(html, /Buy verified dataset/);
  assert.match(html, /1\.25 USDC/);
  assert.match(html, /Ledger aggregates not exposed/);
  assert.match(html, /Monthly usage<\/span><strong>Unavailable/);
  assert.doesNotMatch(html, /On track|Spendable now/);
  assert.doesNotMatch(html, /Northstar Labs|\$15,140\.00|Signal Harbor/);
  assert.doesNotMatch(html, new RegExp(exchangeToken));
  assert.doesNotMatch(html, new RegExp(sessionToken.replaceAll(".", "\\.")));

  snapshotOrganizationId = "org_substituted";
  const substituted = await render({
    headers: {
      "oai-authenticated-user-id": "sites-user-opaque",
      "oai-authenticated-user-email": "owner@example.com",
    },
    env: {
      FLOWOPS_CONTROL_API_URL: `http://127.0.0.1:${address.port}`,
      FLOWOPS_SITES_PROJECT_ID: "appgprj_flowops_test",
      FLOWOPS_SITES_EXCHANGE_TOKEN: exchangeToken,
    },
  });
  const substitutedHtml = await substituted.text();
  assert.match(substitutedHtml, /Preview data/);
  assert.doesNotMatch(substitutedHtml, /Acme Operators|Live control plane/);
});

test("removes starter content and never claims preview controls executed", async () => {
  const response = await render();
  const html = await response.text();
  assert.doesNotMatch(html, /Your site is taking shape|Building your site|codex-preview/);
  assert.doesNotMatch(html, /react-loading-skeleton/);
  assert.doesNotMatch(html, /pause successful|payment successful|approval successful/i);
  assert.doesNotMatch(html, /submitted successfully|write succeeded/i);
});
