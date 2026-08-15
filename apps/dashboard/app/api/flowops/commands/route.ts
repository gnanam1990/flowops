import { getChatGPTUser } from "../../../chatgpt-auth";
import { DashboardCommandError, submitDashboardCommand, type DashboardCommandInput } from "../../../flowops-commands";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const user = await getChatGPTUser();
  if (!user) return errorResponse(401, "AUTHENTICATION_REQUIRED", "Sign in before submitting a command.");
  let input: DashboardCommandInput;
  try {
    const raw = await request.text();
    if (raw.length > 16 * 1024) return errorResponse(413, "REQUEST_TOO_LARGE", "The command request is too large.");
    input = JSON.parse(raw) as DashboardCommandInput;
    return Response.json(await submitDashboardCommand(user, input), { headers: { "cache-control": "no-store" } });
  } catch (error) {
    if (error instanceof DashboardCommandError) return errorResponse(error.status, error.code, error.message, error.commandId);
    return errorResponse(400, "INVALID_JSON", "The command request is invalid.");
  }
}

function errorResponse(status: number, code: string, message: string, commandId = "") {
  return Response.json({ error: { code, message }, commandId }, { status, headers: { "cache-control": "no-store" } });
}
