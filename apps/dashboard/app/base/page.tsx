import type { Metadata } from "next";
import Link from "next/link";
import "./reviewer.css";

export const metadata: Metadata = {
  title: "FlowOps — Base Batches 004 Reviewer Brief",
  description: "The application and evidence brief for FlowOps, a Base-native control plane for autonomous-agent payments.",
  openGraph: {
    title: "FlowOps — Base Batches 004",
    description: "A working, Base-first control and evidence plane for autonomous-agent payments.",
    images: [{ url: "/og.png", width: 1200, height: 630, alt: "FlowOps economic control for autonomous-agent payments on Base" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "FlowOps — Base Batches 004",
    description: "A working, Base-first control and evidence plane for autonomous-agent payments.",
    images: ["/og.png"],
  },
};

const github = "https://github.com/gnanam1990/flowops";
const blockscout = "https://base.blockscout.com";
const proofLinks = [
  { label: "Working control room", detail: "A fail-closed product surface backed by live control-plane reads when an authorized organization is configured—never by demo payment records.", href: "/", local: true },
  { label: "Base mainnet proposal anchor", detail: "Exact-match source verified on Blockscout. Permanently evidence-only and unable to accept funds or create payment vaults.", href: blockscout + "/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract" },
  { label: "Base Sepolia escrow proof", detail: "Reference signer, capped test-USDC escrow funding, onchain acknowledgement, and terminal refund evidence.", href: github + "/blob/main/docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.md" },
  { label: "Executable acceptance inventory", detail: "Sixty-seven criteria carry executable local evidence across policy, authorization, signing, accounting, reconciliation, Base, and operator surfaces.", href: github + "/blob/main/docs/acceptance/ascp-v3.4.json" },
];
const docs = [
  ["Base Batches 004 application draft", "docs/proposals/FLOWOPS_BASE_BATCHES_004_APPLICATION_DRAFT.md"],
  ["Proposal submission package", "docs/proposals/FLOWOPS_BASE_PROPOSAL_SUBMISSION_PACKAGE_V1.md"],
  ["Product requirements", "docs/product/FLOWOPS_PRD_v1.3.md"],
  ["Customer-managed signer decision", "docs/adr/ADR-0001-customer-managed-signer.md"],
  ["Escrow rail decision", "docs/adr/ADR-0003-escrow-rail.md"],
  ["Chain-halt safety decision", "docs/adr/ADR-0005-chain-halt.md"],
  ["Proposal-anchor evidence", "docs/evidence/BASE_MAINNET_PROPOSAL_ANCHOR_2026-08-15.md"],
] as const;

export default function BaseReviewerBrief() {
  return (
    <main className="review-page">
      <nav className="review-nav">
        <Link className="review-brand" href="/"><span aria-hidden="true"><i /><i /><i /></span>FlowOps</Link>
        <p>BASE BATCHES 004 · REVIEWER BRIEF · AUGUST 2026</p>
        <a href={github} rel="noreferrer" target="_blank">Open source ↗</a>
      </nav>
      <section className="review-hero">
        <p className="review-eyebrow">AGENTIC PAYMENTS / CONTROL + EVIDENCE</p>
        <h1>Give every agent payment a reason, a rule, and a receipt.</h1>
        <p className="review-summary">FlowOps is a Base-first control and evidence plane for autonomous agent payments. It evaluates policy, requests human approval when needed, hands a bounded authorization to a customer-managed signer, and reconciles the result against canonical Base evidence.</p>
        <div className="review-actions"><Link href="/">Review the working product <span>↗</span></Link><a href={github} rel="noreferrer" target="_blank">Inspect the public repository <span>↗</span></a></div>
      </section>
      <section className="review-facts" aria-label="Current status">
        <article><span>PRODUCT</span><strong>Working MVP · pilot-gated</strong><p>Real control-plane paths, with no fabricated payment or traction data.</p></article>
        <article><span>BASE FIRST</span><strong>Payments + AI agents</strong><p>Base is the default settlement network and product focus.</p></article>
        <article><span>ONCHAIN PROOF</span><strong>Mainnet anchor + Sepolia lifecycle</strong><p>Verified provenance plus a capped test-USDC terminal refund.</p></article>
      </section>
      <section className="review-section review-fit">
        <header><p>BASE BATCHES FIT</p><h2>Built for the gap between agent intent and money movement.</h2></header>
        <div className="review-columns"><p>Agents can discover and call paid services faster than teams can supervise them. FlowOps turns each proposed payment into a deterministic policy decision, an exact authorization boundary, and a reconciled evidence trail—while keeping the customer in control of signing.</p><ul><li><strong>Category</strong><span>AI / Agents, with payments as the core product surface.</span></li><li><strong>Stage</strong><span>Working MVP preparing for an allowlisted design-partner pilot.</span></li><li><strong>Program use</strong><span>Refine product-market fit, recruit pilot teams, and productionize the Base settlement path.</span></li></ul></div>
      </section>
      <section className="review-section">
        <header><p>THE PRODUCT</p><h2>Not another wallet. The missing operating layer.</h2></header>
        <div className="review-columns">
          <p>Moving USDC is only one part of an agent payment. Teams also need to answer: which agent acted, whose money was involved, which task was being served, which policy version permitted it, who approved it, which recipient received value, and whether the stated result was delivered.</p>
          <ul><li><strong>Policy</strong><span>Deterministic allow, deny, or approval decisions.</span></li><li><strong>Authorization</strong><span>Exact task, recipient, amount, nonce, and expiry bounds.</span></li><li><strong>Evidence</strong><span>Settlement, delivery, and refund only after canonical chain evidence.</span></li></ul>
        </div>
      </section>
      <section className="review-section review-proof">
        <header><p>PUBLIC PROOF</p><h2>Review the product, then inspect the evidence.</h2></header>
        <div className="proof-grid">{proofLinks.map((item) => item.local ? <Link className="proof-card" href={item.href} key={item.label}><span>OPEN ↗</span><h3>{item.label}</h3><p>{item.detail}</p></Link> : <a className="proof-card" href={item.href} key={item.label} rel="noreferrer" target="_blank"><span>OPEN ↗</span><h3>{item.label}</h3><p>{item.detail}</p></a>)}</div>
      </section>
      <section className="review-section">
        <header><p>WHY BASE</p><h2>Fast settlement needs durable accountability.</h2></header>
        <div className="review-columns"><p>Base is the settlement environment for FlowOps. The product is designed around low-cost, observable onchain activity while keeping signing authority with the customer. FlowOps does not hold customer private keys or treat a database row as payment proof.</p><p className="review-emphasis">The customer signer has its own local trust root, caps, nonce-once behavior, freeze control, and chain-liveness checks. A FlowOps authorization is necessary, never sufficient, for a payment.</p></div>
      </section>
      <section className="review-section review-docs">
        <header><p>TECHNICAL DOCUMENTATION</p><h2>Design decisions, proofs, and known limits.</h2></header>
        <div>{docs.map(([label, path]) => <a href={github + "/blob/main/" + path} key={path} rel="noreferrer" target="_blank"><span>{label}</span><code>{path}</code><b>↗</b></a>)}</div>
      </section>
      <section className="review-boundary">
        <p>HONEST AVAILABILITY</p><h2>FlowOps is ready for reviewer evaluation and a bounded pilot—not unrestricted mainnet payments.</h2>
        <p>This application is for a working MVP and an allowlisted pilot with customer-managed signing, caps, service allowlists, manual operational review, and observable Base reconciliation. Independent audit, legal diligence, production signer admission, and a separate deployment approval belong to a later unrestricted real-money launch—not to the application build presented here.</p>
      </section>
      <footer className="review-footer"><span>FLOWOPS / BASE BATCHES 004</span><Link href="/">Working control room</Link><a href={github} rel="noreferrer" target="_blank">Repository</a><a href={blockscout + "/tx/0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b"} rel="noreferrer" target="_blank">Anchor transaction</a></footer>
    </main>
  );
}
