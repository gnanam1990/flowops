export function isLoopbackHostname(hostname: string): boolean {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1" || hostname === "[::1]";
}

// Local authentication is supported only by the default loopback-bound
// development server. Cloudflare and proxy client addresses must also be
// loopback, so a forged Host value cannot enable this path remotely.
export function isTrustedLocalRequest(request: Request): boolean {
  const url = new URL(request.url);
  if (!isLoopbackHostname(url.hostname) || request.headers.get("forwarded") !== null) return false;

  const cloudflareClient = request.headers.get("cf-connecting-ip");
  if (cloudflareClient !== null && !isLoopbackClientAddress(cloudflareClient)) return false;

  const forwardedFor = request.headers.get("x-forwarded-for");
  return forwardedFor === null || forwardedFor.split(",").every(isLoopbackClientAddress);
}

function isLoopbackClientAddress(value: string): boolean {
  const address = value.trim().toLowerCase();
  return address === "127.0.0.1" || address === "::1" || address === "[::1]" || address === "::ffff:127.0.0.1";
}
