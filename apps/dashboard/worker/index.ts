/** Cloudflare Worker entry point for the FlowOps operator dashboard. */
import { handleImageOptimization, DEFAULT_DEVICE_SIZES, DEFAULT_IMAGE_SIZES } from "vinext/server/image-optimization";
import handler from "vinext/server/app-router-entry";

interface Env {
  ASSETS: Fetcher;
  FLOWOPS_CONTROL_API_URL?: string;
  FLOWOPS_SITES_PROJECT_ID?: string;
  FLOWOPS_SITES_EXCHANGE_TOKEN?: string;
  FLOWOPS_PROPOSAL_ANCHOR_ADDRESS?: string;
  IMAGES: {
    input(stream: ReadableStream): {
      transform(options: Record<string, unknown>): {
        output(options: { format: string; quality: number }): Promise<{ response(): Response }>;
      };
    };
  };
}

interface ExecutionContext {
  waitUntil(promise: Promise<unknown>): void;
  passThroughOnException(): void;
}

const SERVER_ENVIRONMENT_KEYS = [
  "FLOWOPS_CONTROL_API_URL",
  "FLOWOPS_SITES_PROJECT_ID",
  "FLOWOPS_SITES_EXCHANGE_TOKEN",
  "FLOWOPS_PROPOSAL_ANCHOR_ADDRESS",
] as const;

const initialServerEnvironment = Object.fromEntries(
  SERVER_ENVIRONMENT_KEYS.map((key) => [key, process.env[key]]),
) as Record<(typeof SERVER_ENVIRONMENT_KEYS)[number], string | undefined>;

// Image security config. SVG sources with .svg extension auto-skip the
// optimization endpoint on the client side (served directly, no proxy).
// To route SVGs through the optimizer (with security headers), set
// dangerouslyAllowSVG: true in next.config.js and uncomment below:
// const imageConfig: ImageConfig = { dangerouslyAllowSVG: true };

const worker = {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    exposeServerEnvironment(env);
    const url = new URL(request.url);

    if (url.pathname === "/_vinext/image") {
      const allowedWidths = [...DEFAULT_DEVICE_SIZES, ...DEFAULT_IMAGE_SIZES];
      return handleImageOptimization(request, {
        fetchAsset: (path) => env.ASSETS.fetch(new Request(new URL(path, request.url))),
        transformImage: async (body, { width, format, quality }) => {
          const result = await env.IMAGES.input(body).transform(width > 0 ? { width } : {}).output({ format, quality });
          return result.response();
        },
      }, allowedWidths);
    }

    return handler.fetch(request, env, ctx);
  },
};

function exposeServerEnvironment(env: Env): void {
  for (const key of SERVER_ENVIRONMENT_KEYS) {
    const value = typeof env[key] === "string" ? env[key] : initialServerEnvironment[key];
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
}

export default worker;
