import type { ChatGPTUser } from "./chatgpt-auth";
import { deriveSiteUserKey } from "./flowops-adapter";

export type SiteEnrollment = {
  siteProjectId: string;
  siteUserKey: string;
  email: string;
};

export async function siteEnrollmentForUser(
  user: ChatGPTUser | null,
  siteProjectId = process.env.FLOWOPS_SITES_PROJECT_ID?.trim(),
): Promise<SiteEnrollment | null> {
  if (!user || !siteProjectId) return null;
  try {
    return {
      siteProjectId,
      siteUserKey: await deriveSiteUserKey(siteProjectId, user.userId),
      email: user.email,
    };
  } catch {
    return null;
  }
}
