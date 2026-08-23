import assert from "node:assert/strict";
import { open, rm } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { loadConfigFromFile } from "vite";

const dashboardDirectory = fileURLToPath(new URL("..", import.meta.url));
const configPath = fileURLToPath(new URL("../vite.config.ts", import.meta.url));

test("loads loopback-only local auth from an ignored mode-specific env file", async (t) => {
  const mode = "flowops-config-regression";
  const envPath = fileURLToPath(new URL(`../.env.${mode}.local`, import.meta.url));
  const envFile = await open(envPath, "wx");
  await envFile.writeFile("FLOWOPS_LOCAL_AUTH_ENABLED=true\n");
  await envFile.close();
  t.after(() => rm(envPath, { force: true }));

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
