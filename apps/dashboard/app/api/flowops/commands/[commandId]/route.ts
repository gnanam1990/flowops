import { getChatGPTUser } from "../../../../chatgpt-auth";
import { DashboardCommandError, recoverDashboardCommand } from "../../../../flowops-commands";

export const dynamic = "force-dynamic";

export async function GET(_request: Request, context: { params: Promise<{ commandId: string }> }) {
  const user = await getChatGPTUser();
  if (!user) return errorResponse(401, "AUTHENTICATION_REQUIRED", "Sign in before recovering a command.");
  try {
    const { commandId } = await context.params;
    return Response.json(await recoverDashboardCommand(user, commandId), { headers: { "cache-control": "no-store" } });
  } catch (error) {
    if (error instanceof DashboardCommandError) return errorResponse(error.status, error.code, error.message, error.commandId);
    return errorResponse(503, "CONTROL_PLANE_UNRESOLVED", "The command could not be recovered.");
  }
}

function errorResponse(status: number, code: string, message: string, commandId = "") {
  return Response.json({ error: { code, message }, commandId }, { status, headers: { "cache-control": "no-store" } });
}
