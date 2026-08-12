import { getChatGPTUser } from "./chatgpt-auth";
import { ControlRoom } from "./control-room";
import { dashboardForUser } from "./flowops-adapter";

export const dynamic = "force-dynamic";

export default async function Home() {
  const user = await getChatGPTUser();
  const snapshot = await dashboardForUser(user);

  return (
    <ControlRoom
      snapshot={snapshot}
      viewer={{
        name: user?.displayName ?? "Local operator",
        email: user?.email ?? "preview@flowops.local",
      }}
    />
  );
}
