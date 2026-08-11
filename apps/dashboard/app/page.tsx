import { getChatGPTUser } from "./chatgpt-auth";
import { ControlRoom } from "./control-room";
import { dashboardSnapshot } from "./dashboard-data";

export const dynamic = "force-dynamic";

export default async function Home() {
  const user = await getChatGPTUser();

  return (
    <ControlRoom
      snapshot={dashboardSnapshot}
      viewer={{
        name: user?.displayName ?? "Local operator",
        email: user?.email ?? "preview@flowops.local",
      }}
    />
  );
}
