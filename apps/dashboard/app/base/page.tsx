import type { Metadata } from "next";
import Link from "next/link";
import "./reviewer.css";

export const metadata: Metadata = {
  title: "FlowOps for Base — Reviewer Brief",
  description: "A concise public review package for FlowOps, a Base-native control and evidence plane for agent payments.",
  openGraph: { title: "FlowOps for Base — Reviewer Brief", description: "Public product, architecture, and evidence links for a controlled FlowOps pilot on Base.", images: [] },
  twitter: { title: "FlowOps for Base — Reviewer Brief", description: "Public product, architecture, and evidence links for a controlled FlowOps pilot on Base.", images: [] },
};

const github = "https://github.com/gnanam1990/flowops";
const blockscout = "https://base.blockscout.com";
const proofLinks = [
  { label: "Interactive product walkthrough", detail: "A 60-second, no-wallet product narrative: policy, approval, delivery evidence, refund, and Base posture.", href: "/demo", local: true },
  { label: "Public control room", detail: "Public, non-sensitive control-plane status view. Organization records and payment controls stay private.", href: "/", local: true },
  { label: "Base mainnet proposal anchor", detail: "Verified, evidence-only contract. Permanently experimental and unable to accept funds or create payment vaults.", href: blockscout + "/address/0x149d03ec527ad8667d47e7b6a2d316dd54033250?tab=contract" },
  { label: "Base Sepolia escrow proof", detail: "Reference signer, capped test-USDC escrow funding, onchain acknowledgement, and terminal refund evidence.", href: github + "/blob/main/docs/evidence/REFERENCE_SIGNER_FUNDED_ESCROW_2026-08-15.md" },
];
const docs = [
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
        <p>BASE REVIEWER BRIEF · AUGUST 2026</p>
        <a href={github} rel="noreferrer" target="_blank">Open source ↗</a>
      </nav>
      <section className="review-hero">
        <p className="review-eyebrow">AGENTIC PAYMENTS / CONTROL + EVIDENCE</p>
        <h1>Give every agent payment a reason, a rule, and a receipt.</h1>
        <p className="review-summary">FlowOps is a Base-only control and evidence plane for autonomous agent payments. It evaluates policy, requests human approval when needed, hands a bounded authorization to a customer-managed signer, and reconciles the result against canonical Base evidence.</p>
        <div className="review-actions"><Link href="/demo">Watch the interactive walkthrough <span>↗</span></Link><a href={github} rel="noreferrer" target="_blank">Read the public repository <span>↗</span></a></div>
      </section>
      <section className="review-facts" aria-label="Current status">
        <article><span>PRODUCT</span><strong>Pre-alpha · controlled-pilot design</strong><p>No production payment launch or usage claims.</p></article>
        <article><span>MAINNET</span><strong>Evidence-only proposal anchor</strong><p>Verified on Base; permanently no-funds.</p></article>
        <article><span>TESTNET</span><strong>Escrow lifecycle demonstrated</strong><p>Base Sepolia, capped test USDC, terminal refund proof.</p></article>
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
        <p>The next milestone is an allowlisted pilot with customer-managed signing, caps, service allowlists, manual operational review, and observable Base reconciliation. An audited payment deployment, independent security review, legal/custody review, and a separate approval are required before any unrestricted mainnet funds.</p>
      </section>
      <footer className="review-footer"><span>FLOWOPS / BASE REVIEWER PACKAGE</span><Link href="/demo">Interactive walkthrough</Link><a href={github} rel="noreferrer" target="_blank">Repository</a><a href={blockscout + "/tx/0x7fe3986c45a1c4de2c9ca421222569ba8e41cc6b7fe9173340a3954c9306a76b"} rel="noreferrer" target="_blank">Anchor transaction</a></footer>
    </main>
  );
}
