"use client";

import Image from "next/image";
import Link from "next/link";
import { useEffect, useState } from "react";
import "./demo.css";

type Chapter = {
  kicker: string;
  title: string;
  copy: string;
  caption: string;
  proof: string;
  signal: string;
  stage: "problem" | "intent" | "approval" | "escrow" | "refund" | "evidence" | "base";
};

const chapters: Chapter[] = [
  { kicker: "THE PROBLEM", title: "Agents can buy. Who keeps them accountable?", copy: "An AI agent can act in seconds. A team still needs to know whose money it used, what it was allowed to do, and what actually came back.", caption: "Every autonomous payment needs a human-readable chain of authority.", proof: "CONTROL BEFORE VALUE", signal: "REQUEST RECEIVED", stage: "problem" },
  { kicker: "THE INTENT", title: "Turn a payment into a bounded job.", copy: "FlowOps packages the acting agent, recipient, exact amount, task, and policy into one intent. It is not a blank cheque.", caption: "Agent identity · recipient pinned · amount exact", proof: "ONE INTENT, ONE PURPOSE", signal: "INTENT PINNED", stage: "intent" },
  { kicker: "THE DECISION", title: "Policy decides before money moves.", copy: "Spend caps, approval rules, and the policy version are evaluated before an action is authorized. The approver sees the decision in context.", caption: "Policy v7 · task cap · approval required", proof: "EXPLAINABLE AUTHORITY", signal: "APPROVAL REQUIRED", stage: "approval" },
  { kicker: "THE DELIVERY", title: "Pay for an outcome, not a promise.", copy: "For eligible services, a provider can acknowledge the task and submit delivery evidence. Release follows the delivery path—not a database assumption.", caption: "Acknowledgement · response metadata · content hash", proof: "EVIDENCE BEFORE RELEASE", signal: "DELIVERY SUBMITTED", stage: "escrow" },
  { kicker: "THE FALLBACK", title: "Missed delivery has a visible refund path.", copy: "If the delivery window expires, the task follows its onchain refund transition. FlowOps never labels a refund complete until the chain confirms it.", caption: "Deadline reached · onchain transition · payer refunded", proof: "NO SILENT RETRIES", signal: "REFUND CONFIRMED", stage: "refund" },
  { kicker: "THE RECORD", title: "One payload. A complete answer.", copy: "FlowOps connects who acted, what policy allowed it, who approved, who received value, and what evidence came back into one reviewable record.", caption: "Authority · decision · settlement · evidence", proof: "AUDITABLE BY DESIGN", signal: "EVIDENCE LINKED", stage: "evidence" },
  { kicker: "BUILT FOR BASE", title: "Start with proof. Keep custody with the customer.", copy: "The public proposal anchor is permanently no-funds and experimental. The escrow lifecycle is demonstrated on Base Sepolia while the pilot stays deliberately bounded.", caption: "Base Sepolia proof · Base mainnet proposal anchor · no public mainnet funds", proof: "EXPERIMENTAL · NO FUNDS", signal: "BASE EVIDENCE", stage: "base" },
];

const chapterDuration = 8500;

export default function FlowOpsDemoPage() {
  const [chapterIndex, setChapterIndex] = useState(0);
  const [isPlaying, setIsPlaying] = useState(true);
  const chapter = chapters[chapterIndex];

  useEffect(() => {
    if (!isPlaying) return;
    const timer = window.setInterval(() => setChapterIndex((current) => (current + 1) % chapters.length), chapterDuration);
    return () => window.clearInterval(timer);
  }, [isPlaying]);

  const chooseChapter = (index: number) => {
    setChapterIndex(index);
    setIsPlaying(false);
  };

  return (
    <main className="film-page">
      <nav className="film-nav" aria-label="Demo navigation">
        <Link className="film-brand" href="/" aria-label="Back to FlowOps control room">
          <span className="film-mark" aria-hidden="true"><i /><i /><i /></span><span>FlowOps</span>
        </Link>
        <span className="film-nav-center">BASE · INTERACTIVE 60 SECONDS</span>
        <Link className="film-back" href="/">Back to control room <span aria-hidden="true">↗</span></Link>
      </nav>

      <section className="film-hero" aria-labelledby="film-title">
        <div className="film-copy" key={`copy-${chapterIndex}`}>
          <p className="film-kicker">{String(chapterIndex + 1).padStart(2, "0")}<span>{chapter.kicker}</span></p>
          <h1 id="film-title">{chapter.title}</h1>
          <p className="film-description">{chapter.copy}</p>
          <p className="film-proof"><i aria-hidden="true" />{chapter.proof}</p>
        </div>

        <div className={`film-console stage-${chapter.stage}`} key={`console-${chapterIndex}`} aria-label={chapter.signal}>
          <div className="film-console-top"><span>FLOWOPS / ECONOMIC CONTROL</span><strong>{chapter.signal}</strong></div>
          <div className="film-dashboard-art" aria-hidden="true"><Image alt="" fill priority sizes="(max-width: 900px) 100vw, 52vw" src="/og.png" /></div>
          <div className="film-console-wash" aria-hidden="true" />
          <div className="film-route" aria-hidden="true">
            <span className="film-origin">AGENT</span><i className="film-line film-line-one" /><b className="film-check film-check-one">✓</b>
            <span className="film-policy">POLICY</span><i className="film-line film-line-two" /><b className="film-check film-check-two">✓</b>
            <span className="film-output">{chapter.stage === "refund" ? "REFUND" : "EVIDENCE"}</span>
          </div>
          <div className="film-console-bottom"><span>{chapter.caption}</span><i>● LIVE WALKTHROUGH</i></div>
        </div>
      </section>

      <section className="film-player" aria-label="Interactive demo player">
        <div className="film-player-top">
          <button className="film-play" onClick={() => setIsPlaying((value) => !value)} type="button">{isPlaying ? "Pause" : "Play"}</button>
          <div className="film-progress" aria-hidden="true"><span key={`progress-${chapterIndex}`} style={{ animationPlayState: isPlaying ? "running" : "paused" }} /></div>
          <span className="film-time">{`0:${String(Math.round(((chapterIndex + 1) / chapters.length) * 60)).padStart(2, "0")}`} <i>/ 1:00</i></span>
        </div>
        <div className="film-player-bottom"><span>TEXT-LED · NO WALLET REQUIRED</span><button className="film-restart" onClick={() => { setChapterIndex(0); setIsPlaying(true); }} type="button">Restart <span aria-hidden="true">↻</span></button></div>
      </section>

      <section className="film-chapters" aria-label="Demo chapters">
        {chapters.map((item, index) => <button className={index === chapterIndex ? "active" : ""} key={item.kicker} onClick={() => chooseChapter(index)} type="button"><span>{String(index + 1).padStart(2, "0")}</span>{item.kicker.replace("THE ", "")}</button>)}
      </section>

      <footer className="film-footer">
        <div><span>NETWORK</span><strong>Base Sepolia proof · Base mainnet proposal anchor</strong></div>
        <div><span>CONTROL POSTURE</span><strong>Customer-controlled signing · no public mainnet funds</strong></div>
        <div><span>IMPORTANT</span><strong>Illustrative walkthrough. It does not access a wallet or send a transaction.</strong></div>
      </footer>
    </main>
  );
}
