import vinext from "vinext";
import { defineConfig, loadEnv } from "vite";
import hostingConfig from "./.openai/hosting.json" with { type: "json" };
import { isLoopbackHostname } from "./app/local-auth-boundary.ts";
import { sites } from "./build/sites-vite-plugin.ts";

const SITE_CREATOR_PLACEHOLDER_DATABASE_ID =
  "00000000-0000-4000-8000-000000000000";

const { d1, r2 } = hostingConfig;

// macOS Seatbelt blocks FSEvents, so Codex previews need polling for HMR.
const isCodexSeatbeltSandbox = process.env.CODEX_SANDBOX === "seatbelt";

export default defineConfig(async ({ mode }) => {
  const resolvedEnv = loadEnv(mode, process.cwd(), ["FLOWOPS_LOCAL_AUTH_ENABLED"]);
  const localAuthValue = resolvedEnv.FLOWOPS_LOCAL_AUTH_ENABLED ?? "false";
  const localAuthRequested = localAuthValue === "true";

  const localAuthLoopbackGuard = {
    name: "flowops-local-auth-loopback-guard",
    apply: "serve" as const,
    configResolved(config: { server: { host?: string | boolean } }) {
      const host = config.server.host;
      if (localAuthRequested && (typeof host !== "string" || !isLoopbackHostname(host))) {
        throw new Error("FLOWOPS_LOCAL_AUTH_ENABLED requires a loopback-only development server bind");
      }
    },
  };

  const localBindingConfig = {
    main: "./worker/index.ts",
    compatibility_flags: ["nodejs_compat"],
    // Shell variables and ignored `.env*` values are not automatically Worker
    // bindings in vinext dev. Forward only this non-secret, loopback-gated flag.
    vars: {
      FLOWOPS_LOCAL_AUTH_ENABLED: localAuthValue,
    },
    d1_databases: d1
      ? [
          {
            binding: d1,
            database_name: "flowops-dashboard-d1",
            database_id: SITE_CREATOR_PLACEHOLDER_DATABASE_ID,
          },
        ]
      : [],
    r2_buckets: r2
      ? [
          {
            binding: r2,
            bucket_name: "flowops-dashboard-r2",
          },
        ]
      : [],
  };

  // Keep Wrangler and Miniflare state project-local. These are non-secret tool
  // settings; application environment belongs in ignored `.env*` files.
  process.env.WRANGLER_WRITE_LOGS ??= "false";
  process.env.WRANGLER_LOG_PATH ??= ".wrangler/logs";
  process.env.MINIFLARE_REGISTRY_PATH ??= ".wrangler/registry";

  // Wrangler snapshots its log path while the Cloudflare plugin is imported.
  const { cloudflare } = await import("@cloudflare/vite-plugin");

  return {
    server: {
      ...(localAuthRequested ? { host: "127.0.0.1" } : {}),
      ...(isCodexSeatbeltSandbox
        ? { watch: { useFsEvents: false, usePolling: true } }
        : {}),
    },
    plugins: [
      localAuthLoopbackGuard,
      vinext(),
      sites(),
      cloudflare({
        viteEnvironment: { name: "rsc", childEnvironments: ["ssr"] },
        config: localBindingConfig,
      }),
    ],
  };
});
