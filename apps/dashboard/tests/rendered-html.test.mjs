import assert from "node:assert/strict";
import test from "node:test";

async function render() {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request("http://localhost/", { headers: { accept: "text/html" } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
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
  assert.match(html, /Every agent dollar/);
  assert.match(html, /Review 3 approvals/);
  assert.match(html, /Base Sepolia/);
  assert.match(html, /Preview data/);
  assert.match(html, /Pending chain evidence/);
  assert.match(html, /Non-custodial/);
  assert.match(html, /Observer quorum/);
  assert.match(html, /Preview records are illustrative and cannot move funds/);
  assert.match(html, /Emergency pause/);
  assert.match(html, /Economic activity/);
});

test("removes starter content and never claims preview controls executed", async () => {
  const response = await render();
  const html = await response.text();
  assert.doesNotMatch(html, /Your site is taking shape|Building your site|codex-preview/);
  assert.doesNotMatch(html, /react-loading-skeleton/);
  assert.doesNotMatch(html, /pause successful|payment successful|approval successful/i);
  assert.doesNotMatch(html, /submitted successfully|write succeeded/i);
});
