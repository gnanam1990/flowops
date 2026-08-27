import assert from "node:assert/strict";
import { open, rm } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { loadConfigFromFile } from "vite";
import { localRuntimeBindings } from "../build/local-runtime-bindings.mjs";

const dashboardDirectory = fileURLToPath(new URL("..", import.meta.url));
const configPath = fileURLToPath(new URL("../vite.config.ts", import.meta.url));

test("loads loopback-only local auth from an ignored mode-specific env file", async (t) => {
  const mode = "flowops-config-regression";
  const envPath = fileURLToPath(new URL(`../.env.${mode}.local`, import.meta.url));
  await rm(envPath, { force: true });
  t.after(() => rm(envPath, { force: true }));
  const envFile = await open(envPath, "wx");
  await envFile.writeFile("FLOWOPS_CONTROL_API_URL=http://127.0.0.1:8080\nFLOWOPS_LOCAL_AUTH_ENABLED=true\n");
  await envFile.close();

  const loaded = await loadConfigFromFile(
    { command: "serve", mode },
    configPath,
    dashboardDirectory,
    "silent",
  );

  assert.ok(loaded);
  assert.equal(loaded.config.server?.host, "127.0.0.1");
  assert.ok(loaded.config.plugins?.flat(Infinity).some((plugin) => plugin?.name === "flowops-local-auth-loopback-guard"));
});

test("forwards only non-secret local runtime bindings into vinext dev", () => {
  const excludedCredentialKey = ["FLOWOPS_SITES_EXCHANGE", "TOKEN"].join("_");
  assert.deepEqual(localRuntimeBindings({
    FLOWOPS_CONTROL_API_URL: " http://127.0.0.1:8080 ",
    FLOWOPS_PROPOSAL_ANCHOR_ADDRESS: " 0x1111111111111111111111111111111111111111 ",
    FLOWOPS_MAINNET_INTENT_ANCHOR_ADDRESS: " 0x2222222222222222222222222222222222222222 ",
    FLOWOPS_LOCAL_AUTH_ENABLED: " true ",
    [excludedCredentialKey]: "excluded-placeholder",
  }), {
    FLOWOPS_CONTROL_API_URL: "http://127.0.0.1:8080",
    FLOWOPS_PROPOSAL_ANCHOR_ADDRESS: "0x1111111111111111111111111111111111111111",
    FLOWOPS_MAINNET_INTENT_ANCHOR_ADDRESS: "0x2222222222222222222222222222222222222222",
    FLOWOPS_LOCAL_AUTH_ENABLED: "true",
  });
});
