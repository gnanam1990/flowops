import type { Metadata } from "next";
import { MainnetIntentWorkspace } from "./workspace";
import { loadMainnetIntentAnchorDeployment } from "./mainnet-intent-anchor.server";
import "./mainnet.css";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "FlowOps — Base mainnet intent workspace",
  description: "Prepare, anchor, and independently verify a bounded FlowOps agent-spend intent on Base mainnet.",
  openGraph: {
    title: "FlowOps — Base mainnet intent workspace",
    description: "A functional, non-custodial Base mainnet integration for immutable agent-spend intent evidence.",
    images: [{ url: "/og.png", width: 1200, height: 630, alt: "FlowOps economic control for autonomous agents on Base" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "FlowOps — Base mainnet intent workspace",
    description: "A functional, non-custodial Base mainnet integration for immutable agent-spend intent evidence.",
    images: ["/og.png"],
  },
};

export default function MainnetIntentPage() {
  return <MainnetIntentWorkspace deployment={loadMainnetIntentAnchorDeployment()} />;
}
