import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";

export type ChatGPTUser = {
  userId: string;
  displayName: string;
  email: string;
  fullName: string | null;
};

const USER_ID_HEADER = "oai-authenticated-user-id";
const USER_EMAIL_HEADER = "oai-authenticated-user-email";
const USER_FULL_NAME_HEADER = "oai-authenticated-user-full-name";
const USER_FULL_NAME_ENCODING_HEADER =
  "oai-authenticated-user-full-name-encoding";
const PERCENT_ENCODED_UTF8 = "percent-encoded-utf-8";
const SIGN_IN_PATH = "/signin-with-chatgpt";
const SIGN_OUT_PATH = "/signout-with-chatgpt";
const CALLBACK_PATH = "/callback";
const LOCAL_SIGN_IN_PATH = "/api/local-auth/signin";
const LOCAL_SIGN_OUT_PATH = "/api/local-auth/signout";
const LOCAL_SESSION_COOKIE = "flowops-local-session";
const LOCAL_SESSION_VALUE = "active";
const LOCAL_REQUEST_HEADER = "x-flowops-loopback-request";

const LOCAL_USER: ChatGPTUser = {
  userId: "flowops-local-developer",
  displayName: "Local Developer",
  email: "local@flowops.invalid",
  fullName: "Local Developer",
};

export async function getChatGPTUser(): Promise<ChatGPTUser | null> {
  const requestHeaders = await headers();
  const userId = requestHeaders.get(USER_ID_HEADER);
  const email = requestHeaders.get(USER_EMAIL_HEADER);
  if (!userId || !email) {
    if (!localAuthEnabled(requestHeaders.get(LOCAL_REQUEST_HEADER) === "1")) return null;
    const cookieStore = await cookies();
    return cookieStore.get(LOCAL_SESSION_COOKIE)?.value === LOCAL_SESSION_VALUE ? LOCAL_USER : null;
  }

  const encodedFullName = requestHeaders.get(USER_FULL_NAME_HEADER);
  const fullName =
    encodedFullName &&
    requestHeaders.get(USER_FULL_NAME_ENCODING_HEADER) === PERCENT_ENCODED_UTF8
      ? safeDecodeURIComponent(encodedFullName)
      : null;

  return {
    userId,
    displayName: fullName ?? email,
    email,
    fullName,
  };
}

export async function requireChatGPTUser(
  returnTo: string,
): Promise<ChatGPTUser> {
  const user = await getChatGPTUser();
  if (user) return user;

  redirect(chatGPTSignInPath(returnTo));
}

export function chatGPTSignInPath(returnTo: string): string {
  const safeReturnTo = safeRelativeReturnPath(returnTo);
  return `${SIGN_IN_PATH}?return_to=${encodeURIComponent(safeReturnTo)}`;
}

export function chatGPTSignOutPath(returnTo = "/"): string {
  const safeReturnTo = safeRelativeReturnPath(returnTo);
  return `${SIGN_OUT_PATH}?return_to=${encodeURIComponent(safeReturnTo)}`;
}

export async function accountPathForUser(user: ChatGPTUser | null, returnTo = "/"): Promise<string | null> {
  const requestHeaders = await headers();
  if (requestHeaders.get(LOCAL_REQUEST_HEADER) === "1") {
    if (!localAuthEnabled(true)) return null;
    return localAuthPath(user ? LOCAL_SIGN_OUT_PATH : LOCAL_SIGN_IN_PATH, returnTo);
  }
  return user ? chatGPTSignOutPath(returnTo) : chatGPTSignInPath(returnTo);
}

export function localAuthRedirect(request: Request, signedIn: boolean): Response {
  const url = new URL(request.url);
  if (!localAuthEnabled(localHostname(url.hostname))) {
    return Response.json({ error: "LOCAL_AUTH_UNAVAILABLE" }, {
      status: 404,
      headers: { "cache-control": "no-store" },
    });
  }
  const returnTo = safeRelativeReturnPath(url.searchParams.get("return_to") ?? "/");
  const target = new URL(returnTo, url.origin);
  const maxAge = signedIn ? 8 * 60 * 60 : 0;
  return new Response(null, {
    status: 303,
    headers: {
      "cache-control": "no-store",
      location: target.href,
      "set-cookie": `${LOCAL_SESSION_COOKIE}=${signedIn ? LOCAL_SESSION_VALUE : ""}; Path=/; HttpOnly; SameSite=Strict; Max-Age=${maxAge}`,
    },
  });
}

function localAuthPath(path: string, returnTo: string): string {
  return `${path}?return_to=${encodeURIComponent(safeRelativeReturnPath(returnTo))}`;
}

function localAuthEnabled(localRequest: boolean): boolean {
  return process.env.FLOWOPS_LOCAL_AUTH_ENABLED === "true" && localRequest;
}

function localHostname(hostname: string): boolean {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1" || hostname === "[::1]";
}

function safeRelativeReturnPath(value: string): string {
  if (!value.startsWith("/") || value.startsWith("//")) return "/";

  let url: URL;
  try {
    url = new URL(value, "https://app.local");
  } catch {
    return "/";
  }
  if (url.origin !== "https://app.local") return "/";
  if (isReservedAuthPath(url.pathname)) return "/";

  return `${url.pathname}${url.search}${url.hash}`;
}

function isReservedAuthPath(pathname: string): boolean {
  return (
    pathname === SIGN_IN_PATH ||
    pathname === SIGN_OUT_PATH ||
    pathname === CALLBACK_PATH ||
    pathname === LOCAL_SIGN_IN_PATH ||
    pathname === LOCAL_SIGN_OUT_PATH
  );
}

function safeDecodeURIComponent(value: string): string | null {
  try {
    return decodeURIComponent(value);
  } catch {
    return null;
  }
}
