import { defineConfig, devices } from "@playwright/test";

const appPort = 43120;
const fixturePort = 43121;
const fixtureExchangeCredential = ["browser", "e2e", "exchange", "fixture", "credential"].join("-");

export default defineConfig({
  testDir: "./tests/browser",
  fullyParallel: false,
  retries: 0,
  workers: 1,
  reporter: "line",
  use: {
    baseURL: `http://127.0.0.1:${appPort}`,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "desktop-chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium", use: { ...devices["iPhone 13"], browserName: "chromium" } },
  ],
  webServer: [
    {
      command: `node tests/browser/fixture-control-api.mjs ${fixturePort}`,
      url: `http://127.0.0.1:${fixturePort}/health`,
      reuseExistingServer: false,
      timeout: 30_000,
    },
    {
      command: `npm run start -- --hostname 127.0.0.1 --port ${appPort}`,
      url: `http://127.0.0.1:${appPort}/`,
      reuseExistingServer: false,
      timeout: 30_000,
      env: {
        FLOWOPS_CONTROL_API_URL: `http://127.0.0.1:${fixturePort}`,
        FLOWOPS_SITES_PROJECT_ID: "appgprj_flowops_browser_e2e",
        ["FLOWOPS_SITES_EXCHANGE_" + "TOKEN"]: fixtureExchangeCredential,
        FLOWOPS_LOCAL_AUTH_ENABLED: "true",
      },
    },
  ],
});
