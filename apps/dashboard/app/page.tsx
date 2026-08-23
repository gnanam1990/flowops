import { accountPathForUser, getChatGPTUser } from "./chatgpt-auth";
import { ControlRoom } from "./control-room";
import { dashboardForUser } from "./flowops-adapter";
import { loadProposalAnchorDeployment } from "./proposal-anchor";

export const dynamic = "force-dynamic";

export default async function Home() {
  const user = await getChatGPTUser();
  const snapshot = await dashboardForUser(user);
  const proposalAnchor = loadProposalAnchorDeployment();
  const accountHref = await accountPathForUser(user);

  return (
    <ControlRoom
      snapshot={snapshot}
      proposalAnchor={proposalAnchor}
      viewer={{
        name: user?.displayName ?? "Public visitor",
        email: user?.email ?? "",
        authenticated: user !== null,
      }}
      accountHref={accountHref}
    />
  );
}
