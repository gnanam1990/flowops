import { getChatGPTUser } from "../../../chatgpt-auth";
import { siteEnrollmentForUser } from "../../../site-enrollment";

export const dynamic = "force-dynamic";

export async function GET() {
  const user = await getChatGPTUser();
  if (!user) {
    return Response.json({ error: "AUTHENTICATION_REQUIRED" }, {
      status: 401,
      headers: { "cache-control": "no-store" },
    });
  }
  const enrollment = await siteEnrollmentForUser(user);
  if (!enrollment) {
    return Response.json({ error: "ENROLLMENT_NOT_CONFIGURED" }, {
      status: 503,
      headers: { "cache-control": "no-store" },
    });
  }
  return Response.json(enrollment, {
    headers: { "cache-control": "no-store" },
  });
}
