import { accountPathForUser, getChatGPTUser } from "./chatgpt-auth";
import { ControlRoom } from "./control-room";
import { dashboardForUser } from "./flowops-adapter";
import { loadASCPMainnetDeployment } from "./mainnet/ascp-mainnet-deployment";
import { loadMainnetIntentAnchorDeployment } from "./mainnet/mainnet-intent-anchor.server";

export const dynamic = "force-dynamic";

export default async function Home() {
  const user = await getChatGPTUser();
  const snapshot = await dashboardForUser(user);
  const ascpMainnetDeployment = loadASCPMainnetDeployment();
  const mainnetIntentAnchor = loadMainnetIntentAnchorDeployment();
  const accountHref = await accountPathForUser(user);

  return (
    <ControlRoom
      snapshot={snapshot}
      ascpMainnetDeployment={ascpMainnetDeployment}
      mainnetIntentAnchor={mainnetIntentAnchor}
      viewer={{
        name: user?.displayName ?? "Public visitor",
        email: user?.email ?? "",
        authenticated: user !== null,
      }}
      accountHref={accountHref}
    />
  );
}
