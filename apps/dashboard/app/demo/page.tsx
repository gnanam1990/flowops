"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import Image from "next/image";
import "./demo.css";

type Scene = {
  eyebrow: string;
  title: string;
  copy: string;
  action: string;
  decision: string;
  detail: string;
  status: string;
};

const scenes: Scene[] = [
  {
    eyebrow: "01 · REQUEST",
    title: "An agent asks to pay.",
    copy: "Every purchase begins as a scoped request: who is acting, what it needs, who receives value, and the exact task it serves.",
    action: "Research agent requests $18.00 USDC",
    decision: "Intent created",
    detail: "Task-bound · recipient pinned · amount exact",
    status: "AWAITING POLICY",
  },
  {
    eyebrow: "02 · GOVERN",
    title: "Policy makes the decision.",
    copy: "FlowOps evaluates the request against current authority, spend rules, approval requirements, and the evidence required for that job.",
    action: "Policy checks an exact payment envelope",
    decision: "Approval required",
    detail: "Policy v7 · $25 task cap · human confirmation",
    status: "GOVERNED",
  },
  {
    eyebrow: "03 · PROVE",
    title: "Delivery earns release.",
    copy: "When an eligible provider delivers, FlowOps records evidence before any release. A missed deadline follows the contract’s refund path.",
    action: "Provider submits delivery proof",
    decision: "Evidence recorded",
    detail: "Content hash · response metadata · Base settlement",
    status: "VERIFIABLE",
  },
];

export default function FlowOpsDemoPage() {
  const [sceneIndex, setSceneIndex] = useState(0);
  const scene = scenes[sceneIndex];

  useEffect(() => {
    const timer = window.setInterval(() => {
      setSceneIndex((current) => (current + 1) % scenes.length);
    }, 7800);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <main className="demo-page">
      <nav className="demo-nav" aria-label="Demo navigation">
        <Link className="demo-brand" href="/" aria-label="Back to FlowOps control room">
          <span aria-hidden="true" className="demo-brand-mark"><i /><i /><i /></span>
          <span>FlowOps</span>
        </Link>
        <span className="demo-nav-label">ANIMATED PRODUCT WALKTHROUGH</span>
        <Link className="demo-return" href="/">Open control room <span aria-hidden="true">↗</span></Link>
      </nav>

      <section className="demo-stage" aria-labelledby="demo-title">
        <div className="demo-grid" aria-hidden="true" />
        <div className="demo-orbit demo-orbit-one" aria-hidden="true" />
        <div className="demo-orbit demo-orbit-two" aria-hidden="true" />

        <div className="demo-copy" key={`copy-${sceneIndex}`}>
          <p className="demo-eyebrow"><span />{scene.eyebrow}</p>
          <h1 id="demo-title">{scene.title}</h1>
          <p className="demo-description">{scene.copy}</p>
          <div className="demo-scene-tabs" role="tablist" aria-label="Demo chapters">
            {scenes.map((item, index) => (
              <button
                aria-selected={index === sceneIndex}
                className={index === sceneIndex ? "active" : ""}
                key={item.eyebrow}
                onClick={() => setSceneIndex(index)}
                role="tab"
                type="button"
              >
                0{index + 1}
              </button>
            ))}
          </div>
        </div>

        <div className={`demo-machine scene-${sceneIndex}`} key={`machine-${sceneIndex}`} aria-label={scene.action}>
          <div className="demo-machine-topline"><span>FLOWOPS / LIVE INTENT</span><i>{scene.status}</i></div>
          <div className="demo-screen" aria-hidden="true">
            <Image alt="" fill priority sizes="(max-width: 880px) 100vw, 52vw" src="/og.png" />
            <span />
          </div>
          <div className="demo-machine-body">
            <div className="demo-node demo-node-agent">
              <span className="demo-node-index">AGT</span>
              <strong>Agent</strong>
              <small>Task identity</small>
            </div>
            <div className="demo-pulse demo-pulse-one" aria-hidden="true" />
            <div className="demo-rail demo-rail-one" aria-hidden="true" />
            <div className="demo-node demo-node-policy">
              <span className="demo-node-index">POL</span>
              <strong>Policy</strong>
              <small>Authority check</small>
            </div>
            <div className="demo-pulse demo-pulse-two" aria-hidden="true" />
            <div className="demo-rail demo-rail-two" aria-hidden="true" />
            <div className="demo-node demo-node-evidence">
              <span className="demo-node-index">EV</span>
              <strong>Evidence</strong>
              <small>Proof recorded</small>
            </div>
          </div>
          <div className="demo-machine-footer">
            <span>{scene.action}</span>
            <strong>{scene.decision}</strong>
          </div>
          <div className="demo-machine-detail"><i />{scene.detail}</div>
        </div>
      </section>

      <section className="demo-proof" aria-label="What FlowOps records">
        <p>ONE PAYLOAD. A COMPLETE ANSWER.</p>
        <div>
          <article><span>01</span><h2>Who acted?</h2><p>Agent identity, task, and authorization.</p></article>
          <article><span>02</span><h2>What was allowed?</h2><p>Policy version, cap, and approval decision.</p></article>
          <article><span>03</span><h2>What happened?</h2><p>Recipient, settlement, and delivery evidence.</p></article>
        </div>
      </section>

      <footer className="demo-footer">
        <p><i /> Illustrative product walkthrough. It does not send a transaction or access a wallet.</p>
        <button onClick={() => setSceneIndex(0)} type="button">Replay from start <span aria-hidden="true">↻</span></button>
      </footer>
    </main>
  );
}
