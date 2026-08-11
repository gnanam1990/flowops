"use client";

import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type {
  Activity,
  Agent,
  Approval,
  DashboardSnapshot,
  Risk,
} from "./dashboard-data";

type Section =
  | "overview"
  | "approvals"
  | "agents"
  | "activity"
  | "security"
  | "developers";

type ControlRoomProps = {
  snapshot: DashboardSnapshot;
  viewer: { name: string; email: string };
};

const navItems: { id: Section; label: string; mark: string }[] = [
  { id: "overview", label: "Overview", mark: "01" },
  { id: "approvals", label: "Approvals", mark: "03" },
  { id: "agents", label: "Agents", mark: "04" },
  { id: "activity", label: "Activity", mark: "24" },
  { id: "security", label: "Security", mark: "02" },
  { id: "developers", label: "Developers", mark: "↗" },
];

export function ControlRoom({ snapshot, viewer }: ControlRoomProps) {
  const [section, setSection] = useState<Section>("overview");
  const [approval, setApproval] = useState<Approval | null>(null);
  const [pauseOpen, setPauseOpen] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const title = navItems.find((item) => item.id === section)?.label ?? "Overview";

  const openSection = (next: Section) => {
    setSection(next);
    setNotice(null);
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  const explainPreview = (action: string) => {
    setNotice(
      `${action} is locked in preview mode. Nothing was submitted and no write occurred. Connect the authenticated FlowOps control plane to execute this action.`,
    );
  };

  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="Primary navigation">
        <div className="brand-lockup">
          <span className="brand-mark" aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          <span>
            <strong className="brand-name">FlowOps</strong>
            <small className="brand-subtitle">Control plane</small>
          </span>
        </div>

        <div className="org-card">
          <span className="org-monogram">NL</span>
          <span>
            <strong>{snapshot.organization.name}</strong>
            <small>{snapshot.organization.plan}</small>
          </span>
          <span aria-hidden="true" className="chevron">
            ⌄
          </span>
        </div>

        <nav className="nav-list">
          {navItems.map((item) => (
            <button
              className={section === item.id ? "nav-item active" : "nav-item"}
              key={item.id}
              onClick={() => openSection(item.id)}
              aria-current={section === item.id ? "page" : undefined}
              type="button"
            >
              <span>{item.label}</span>
              <em>{item.mark}</em>
            </button>
          ))}
        </nav>

        <div className="sidebar-foot">
          <span className="preview-rail"><i /> Preview data</span>
          <div className="chain-summary">
            <span className="chain-dot" aria-hidden="true" />
            <span>
              <strong>{snapshot.chain.network}</strong>
              <small>{snapshot.chain.observers}</small>
            </span>
          </div>
          <p className="signer-note"><strong>Non-custodial.</strong> Customer signing keys stay outside FlowOps.</p>
        </div>
      </aside>

      <div className="workspace">
        <header className="topbar">
          <div className="topbar-title">
            <span className="mobile-brand">FlowOps</span>
            <span className="breadcrumb">Control room / {title}</span>
          </div>
          <dl className="context-strip">
            <div><dt>Total observed</dt><dd>{snapshot.money.total} <span>USDC</span></dd></div>
            <div><dt>Base trusted block</dt><dd>#{snapshot.chain.lastTrustedBlock}</dd></div>
            <div><dt>Observer quorum</dt><dd className="quorum"><i />{snapshot.chain.observers}</dd></div>
          </dl>
          <div className="topbar-actions">
            <span className="fresh-label">Fresh {snapshot.chain.lastTrustedAt}</span>
            <span className="preview-pill">Preview data</span>
            <button className="icon-button" aria-label="Notifications" type="button">
              <span aria-hidden="true">●</span>
              <i>2</i>
            </button>
            <button className="viewer" type="button" aria-label={`Account for ${viewer.name}`}>
              <span>{initials(viewer.name)}</span>
              <strong>{shortName(viewer.name)}</strong>
            </button>
          </div>
        </header>

        <main className="main-content">
          {notice ? (
            <div className="notice" role="status">
              <span>{notice}</span>
              <button onClick={() => setNotice(null)} type="button">
                Dismiss
              </button>
            </div>
          ) : null}

          {section === "overview" ? (
            <Overview
              snapshot={snapshot}
              onApprovals={() => openSection("approvals")}
              onPause={() => setPauseOpen(true)}
              onApproval={setApproval}
              onAgents={() => openSection("agents")}
              onActivity={() => openSection("activity")}
            />
          ) : null}
          {section === "approvals" ? (
            <Approvals approvals={snapshot.approvals} onSelect={setApproval} />
          ) : null}
          {section === "agents" ? <Agents agents={snapshot.agents} /> : null}
          {section === "activity" ? (
            <ActivityView activity={snapshot.activity} />
          ) : null}
          {section === "security" ? (
            <Security
              risks={snapshot.risks}
              chain={snapshot.chain}
              onPause={() => setPauseOpen(true)}
            />
          ) : null}
          {section === "developers" ? <Developers snapshot={snapshot} /> : null}
        </main>

        <nav className="mobile-nav" aria-label="Mobile navigation">
          {navItems.slice(0, 5).map((item) => (
            <button
              className={section === item.id ? "active" : ""}
              key={item.id}
              onClick={() => openSection(item.id)}
              type="button"
            >
              <span>{item.mark}</span>
              {item.label}
            </button>
          ))}
        </nav>
      </div>

      {approval ? (
        <ApprovalDrawer
          approval={approval}
          onClose={() => setApproval(null)}
          onAction={explainPreview}
        />
      ) : null}
      {pauseOpen ? (
        <PauseDialog
          organization={snapshot.organization.name}
          onClose={() => setPauseOpen(false)}
          onConfirm={() => {
            setPauseOpen(false);
            explainPreview("Emergency pause");
          }}
        />
      ) : null}
    </div>
  );
}

function Overview({
  snapshot,
  onApprovals,
  onPause,
  onApproval,
  onAgents,
  onActivity,
}: {
  snapshot: DashboardSnapshot;
  onApprovals: () => void;
  onPause: () => void;
  onApproval: (approval: Approval) => void;
  onAgents: () => void;
  onActivity: () => void;
}) {
  const [range, setRange] = useState<"24h" | "7d" | "30d">("7d");
  return (
    <>
      <section className="hero">
        <div>
          <div className="eyebrow">
            <span className="healthy-dot" /> Autonomous execution healthy
          </div>
          <h1>Every agent dollar,<br />under control.</h1>
          <p>
            See what your agents can spend, what needs a decision, and what Base
            has actually confirmed.
          </p>
          <div className="observation-meta">
            <span>Observed {snapshot.generatedAt}</span>
            <span>Policy profile v14.2</span>
            <span>Customer-controlled signers</span>
          </div>
        </div>
        <div className="hero-actions">
          <div className="time-range" aria-label="Time range">
            {(["24h", "7d", "30d"] as const).map((item) => (
              <button aria-pressed={range === item} className={range === item ? "active" : ""} key={item} onClick={() => setRange(item)} type="button">{item}</button>
            ))}
          </div>
          <button className="primary-button" type="button" onClick={onApprovals}>
            Review 3 approvals <span>→</span>
          </button>
          <button className="danger-button" type="button" onClick={onPause}>
            Emergency pause
          </button>
        </div>
      </section>

      <section className="money-grid" aria-label="Organization balance">
        <div className="balance-card primary-balance">
          <span>Total observed USDC</span>
          <strong>{snapshot.money.total}</strong>
          <small>Across 4 customer-controlled signers</small>
        </div>
        <MoneyCard label="Available" value={snapshot.money.available} tone="good" />
        <MoneyCard label="Reserved" value={snapshot.money.reserved} tone="reserved" />
        <MoneyCard label="Pending chain evidence" value={snapshot.money.pending} tone="pending" />
        <MoneyCard label="Unresolved" value={snapshot.money.unresolved} tone="risk" />
      </section>

      <div className="overview-grid">
        <section className="panel approval-panel">
          <PanelHeader
            kicker="Decision queue"
            title="Needs your attention"
            meta="3 pending"
            onView={onApprovals}
          />
          <div className="approval-list">
            {snapshot.approvals.map((item) => (
              <button className="approval-row" key={item.id} onClick={() => onApproval(item)} type="button">
                <AgentMark mark={item.agentMark} />
                <span className="approval-copy">
                  <strong>{item.title}</strong>
                  <small>{item.agent} · {item.vendor}</small>
                </span>
                <span className="approval-time">
                  <strong>{item.amount}</strong>
                  <small>{item.requested}</small>
                </span>
                <span className="row-arrow" aria-hidden="true">→</span>
              </button>
            ))}
          </div>
        </section>

        <section className="panel budget-panel">
          <PanelHeader kicker="Budget" title="August spend" meta="On track" />
          <div className="budget-total">
            <strong>{snapshot.money.spentToday}</strong>
            <span>spent today</span>
          </div>
          <div className="progress-label">
            <span>Monthly usage</span>
            <strong>{snapshot.money.monthlySpentPercent}%</strong>
          </div>
          <div className="progress-track" aria-label={`${snapshot.money.monthlySpentPercent}% of monthly budget used`}>
            <i style={{ width: `${snapshot.money.monthlySpentPercent}%` }} />
          </div>
          <div className="budget-foot">
            <span>$7,428 spent</span>
            <span>{snapshot.money.monthlyBudget} limit</span>
          </div>
          <div className="budget-note">
            <span>↗</span>
            <p><strong>12% below forecast</strong><br />Based on the last 14 days</p>
          </div>
        </section>

        <section className="panel agents-panel">
          <PanelHeader kicker="Governed agents" title="Active fleet" meta="3 active" onView={onAgents} />
          <div className="agent-rows">
            {snapshot.agents.slice(0, 3).map((agent) => (
              <AgentRow key={agent.id} agent={agent} />
            ))}
          </div>
        </section>

        <section className="panel chain-panel">
          <PanelHeader kicker="Base truth" title="Chain health" meta={snapshot.chain.state} />
          <div className="chain-visual" aria-hidden="true">
            {[31, 48, 42, 68, 54, 72, 61, 79, 67, 88, 82, 94].map((height, index) => (
              <i key={index} style={{ height: `${height}%` }} />
            ))}
          </div>
          <dl className="chain-facts">
            <div><dt>Observer quorum</dt><dd>{snapshot.chain.observers}</dd></div>
            <div><dt>Last trusted block</dt><dd>#{snapshot.chain.lastTrustedBlock}</dd></div>
            <div><dt>Freshness</dt><dd>{snapshot.chain.lastTrustedAt}</dd></div>
          </dl>
        </section>

        <section className="panel activity-panel">
          <PanelHeader kicker="Evidence graph" title="Economic activity" meta="Live" onView={onActivity} />
          <ActivityRows activity={snapshot.activity.slice(0, 4)} />
        </section>
      </div>
      <p className="freshness">Snapshot refreshed {snapshot.generatedAt}. Preview records are illustrative and cannot move funds.</p>
    </>
  );
}

function Approvals({ approvals, onSelect }: { approvals: Approval[]; onSelect: (approval: Approval) => void }) {
  const [filter, setFilter] = useState<"all" | "high" | "medium" | "low">("all");
  const visible = approvals.filter((item) => filter === "all" || item.risk === filter);
  return (
    <section className="section-stack">
      <SectionHeading
        eyebrow="Human decisions"
        title="Approval inbox"
        description="Every decision is bound to the exact amount, vendor, task, and policy version shown here."
      />
      <div className="filter-bar" aria-label="Approval filters">
        {(["all", "high", "medium", "low"] as const).map((item) => (
          <button className={filter === item ? "active" : ""} onClick={() => setFilter(item)} key={item} type="button">
            {item === "all" ? "All pending" : `${capitalize(item)} risk`}
          </button>
        ))}
      </div>
      <div className="approval-table">
        {visible.map((item) => (
          <button className="approval-card" key={item.id} onClick={() => onSelect(item)} type="button">
            <AgentMark mark={item.agentMark} />
            <span className="approval-card-main">
              <span className="card-badges"><em className={`risk-${item.risk}`}>{item.risk} risk</em><em>{item.rail}</em></span>
              <strong>{item.title}</strong>
              <small>{item.agent} wants to pay {item.vendor}</small>
              <p>{item.reason}</p>
            </span>
            <span className="approval-card-side"><strong>{item.amount}</strong><small>Expires in {item.expires}</small><i>Review →</i></span>
          </button>
        ))}
      </div>
    </section>
  );
}

function Agents({ agents }: { agents: Agent[] }) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<"all" | Agent["status"]>("all");
  const visible = agents.filter((agent) => {
    const matchesQuery = `${agent.name} ${agent.purpose} ${agent.task}`.toLowerCase().includes(query.toLowerCase());
    return matchesQuery && (status === "all" || agent.status === status);
  });
  return (
    <section className="section-stack">
      <SectionHeading eyebrow="Machine principals" title="Governed agents" description="Purpose, owner policy, signer health, and spend remain visible as one operational unit." />
      <div className="directory-controls">
        <label className="search-field"><span>Search agents</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Name, purpose, or task" /></label>
        <div className="filter-bar" aria-label="Agent status filters">
          {(["all", "ACTIVE", "PAUSED", "QUARANTINED"] as const).map((item) => <button className={status === item ? "active" : ""} key={item} onClick={() => setStatus(item)} type="button">{item === "all" ? "All" : capitalize(item.toLowerCase())}</button>)}
        </div>
      </div>
      <div className="agent-directory">
        {visible.map((agent) => (
          <article className="agent-card" key={agent.id}>
            <header><AgentMark mark={agent.mark} /><span><strong>{agent.name}</strong><small>{agent.purpose}</small></span><StatusBadge status={agent.status} /></header>
            <div className="agent-balance"><span>Available budget</span><strong>{agent.available}</strong><small>of {agent.limit}</small></div>
            <div className="progress-track small"><i style={{ width: `${agent.percent}%` }} /></div>
            <footer><span>Current task</span><strong>{agent.task}</strong><small>Signer boundary: customer-controlled</small></footer>
          </article>
        ))}
      </div>
      {visible.length === 0 ? <p className="empty-state">No governed agents match these filters.</p> : null}
    </section>
  );
}

function ActivityView({ activity }: { activity: Activity[] }) {
  const [filter, setFilter] = useState<"all" | Activity["state"]>("all");
  const visible = activity.filter((item) => filter === "all" || item.state === filter);
  return (
    <section className="section-stack">
      <SectionHeading eyebrow="Policy to receipt" title="Economic activity" description="A single timeline for tasks, approvals, payments, delivery evidence, refunds, and security events." />
      <div className="filter-bar" aria-label="Activity filters">
        {(["all", "settled", "released", "refunded", "approval", "security"] as const).map((item) => <button className={filter === item ? "active" : ""} key={item} onClick={() => setFilter(item)} type="button">{capitalize(item)}</button>)}
      </div>
      <div className="activity-view panel"><ActivityRows activity={visible} /></div>
    </section>
  );
}

function Security({ risks, chain, onPause }: { risks: Risk[]; chain: DashboardSnapshot["chain"]; onPause: () => void }) {
  return (
    <section className="section-stack">
      <SectionHeading eyebrow="Fail closed" title="Security & recovery" description="Critical controls stay visible until the underlying risk is acknowledged or canonically resolved." action={<button className="danger-button" onClick={onPause} type="button">Emergency pause</button>} />
      <div className="security-grid">
        <div className="panel security-state"><span className="healthy-dot" /><div><small>Authorization state</small><strong>Protected</strong><p>Organization and signer boundaries are accepting policy-valid requests.</p></div></div>
        <div className="panel security-state"><span className="healthy-dot" /><div><small>Base canonical state</small><strong>{chain.state}</strong><p>{chain.observers} at block #{chain.lastTrustedBlock}.</p></div></div>
      </div>
      <div className="risk-list">
        {risks.map((risk) => <RiskRow key={risk.id} risk={risk} />)}
      </div>
      <div className="panel recovery-card"><div><span>Recovery rule</span><h2>No silent retries.</h2><p>Unknown broadcasts stay quarantined. Settlement, release, and refund appear only after independent Base evidence agrees.</p></div><dl><div><dt>Authorization pause</dt><dd>&lt; 1 sec target</dd></div><div><dt>Observer quorum</dt><dd>2 minimum</dd></div><div><dt>Manual resume</dt><dd>Required</dd></div></dl></div>
    </section>
  );
}

function Developers({ snapshot }: { snapshot: DashboardSnapshot }) {
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">(
    "idle",
  );
  const configuration = `{
  "mcpServers": {
    "flowops": {
      "url": "https://api.flowops.dev/mcp",
      "headers": { "Authorization": "Bearer ••••" }
    }
  }
}`;

  const copyConfiguration = async () => {
    try {
      await navigator.clipboard.writeText(configuration);
      setCopyState("copied");
    } catch {
      setCopyState("failed");
    }
  };

  return (
    <section className="section-stack">
      <SectionHeading eyebrow="Build with boundaries" title="Developer center" description="Connect agents through MCP or the SDK without giving them an unrestricted wallet or FlowOps a customer key." />
      <div className="developer-grid">
        <article className="panel code-card"><span className="code-kicker">MCP connection</span><pre><code>{configuration}</code></pre><button type="button" onClick={copyConfiguration}>{copyState === "copied" ? "Copied" : copyState === "failed" ? "Copy unavailable" : "Copy configuration"}</button></article>
        <article className="panel api-health"><PanelHeader kicker="Integration health" title="All boundaries" meta="Preview" /><dl><div><dt>Control plane</dt><dd>Not connected</dd></div><div><dt>Customer signer</dt><dd>Fixture healthy</dd></div><div><dt>x402 facilitator</dt><dd>Fixture healthy</dd></div><div><dt>Base observers</dt><dd>{snapshot.chain.observers}</dd></div></dl><p>Production controls stay disabled until the authenticated control plane is connected.</p></article>
      </div>
      <div className="panel logs-card"><PanelHeader kicker="Recent requests" title="Developer logs" meta="Redacted" /><div className="log-row head"><span>Time</span><span>Request</span><span>Agent</span><span>Outcome</span><span>Latency</span></div>{[
        ["14:42:18", "req_7fa2", "Research Scout", "ALLOW", "48ms"],
        ["14:39:04", "req_7f91", "Growth Operator", "APPROVAL", "61ms"],
        ["14:32:51", "req_7f78", "Finance Copilot", "DENY", "44ms"],
      ].map((row) => <div className="log-row" key={row[1]}>{row.map((cell, index) => <span key={cell} className={index === 3 ? `outcome ${cell.toLowerCase()}` : ""}>{cell}</span>)}</div>)}</div>
    </section>
  );
}

function ApprovalDrawer({ approval, onClose, onAction }: { approval: Approval; onClose: () => void; onAction: (action: string) => void }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const close = (event: KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);
  return (
    <div className="overlay" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside className="drawer" role="dialog" aria-modal="true" aria-labelledby="approval-title">
        <header><span>Approval {approval.id}</span><button ref={closeRef} onClick={onClose} aria-label="Close approval details" type="button">×</button></header>
        <div className="drawer-title"><AgentMark mark={approval.agentMark} /><div><small>{approval.agent}</small><h2 id="approval-title">{approval.title}</h2></div></div>
        <div className="drawer-amount"><span>Exact authorized amount</span><strong>{approval.amount}</strong><small>{approval.rail} · Base Sepolia USDC</small></div>
        <dl className="detail-list"><div><dt>Recipient / vendor</dt><dd>{approval.vendor}</dd></div><div><dt>Agent</dt><dd>{approval.agent}</dd></div><div><dt>Task</dt><dd>{approval.title}</dd></div><div><dt>Rail</dt><dd>{approval.rail}</dd></div><div><dt>Risk</dt><dd>{capitalize(approval.risk)}</dd></div><div><dt>Policy snapshot</dt><dd className="mono">pol_v14.2</dd></div><div><dt>Evidence refs</dt><dd className="mono">EV-{approval.id}-02 · BASE-OBS-03</dd></div><div><dt>Created</dt><dd>{approval.requested}</dd></div><div><dt>Expires</dt><dd>{approval.expires}</dd></div><div><dt>Request digest</dt><dd className="mono">0x83b1…0ca9</dd></div></dl>
        <div className="reason-box"><span>Why approval is required</span><p>{approval.reason}</p></div>
        <div className="truth-box"><strong>What this decision means</strong><p>Approval authorizes only this frozen intent. Any change to amount, recipient, task, rail, or request digest requires a new decision.</p></div>
        <footer><button className="secondary-button" onClick={() => { onClose(); onAction("Denial"); }} type="button">Deny</button><button className="primary-button" onClick={() => { onClose(); onAction("Approval"); }} type="button">Approve exact intent</button></footer>
      </aside>
    </div>
  );
}

function PauseDialog({ organization, onClose, onConfirm }: { organization: string; onClose: () => void; onConfirm: () => void }) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const close = (event: KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);
  return (
    <div className="overlay centered" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="pause-dialog" role="alertdialog" aria-modal="true" aria-labelledby="pause-title">
        <span className="pause-symbol">!</span><small>Emergency control</small><h2 id="pause-title">Pause every agent?</h2><p>This requests an organization-wide authorization stop for <strong>{organization}</strong>. Pending evidence remains readable and no transaction is silently retried.</p><div className="pause-warning">Preview mode cannot send this command. A live action also requires step-up authentication.</div><footer><button ref={cancelRef} className="secondary-button" onClick={onClose} type="button">Keep running</button><button className="danger-solid" onClick={onConfirm} type="button">Request emergency pause</button></footer>
      </section>
    </div>
  );
}

function PanelHeader({ kicker, title, meta, onView }: { kicker: string; title: string; meta: string; onView?: () => void }) {
  return <header className="panel-header"><div><span>{kicker}</span><h2>{title}</h2></div>{onView ? <button onClick={onView} type="button">View all <span>→</span></button> : <em>{meta}</em>}</header>;
}

function SectionHeading({ eyebrow, title, description, action }: { eyebrow: string; title: string; description: string; action?: ReactNode }) {
  return <header className="section-heading"><div><span>{eyebrow}</span><h1>{title}</h1><p>{description}</p></div>{action}</header>;
}

function MoneyCard({ label, value, tone }: { label: string; value: string; tone: string }) {
  return <div className={`balance-card compact ${tone}`}><span>{label}</span><strong>{value}</strong><small><i /> {tone === "good" ? "Spendable now" : tone === "risk" ? "Needs review" : "Tracked separately"}</small></div>;
}

function AgentMark({ mark }: { mark: string }) { return <span className="agent-mark" aria-hidden="true">{mark}</span>; }

function AgentRow({ agent }: { agent: Agent }) {
  return <div className="agent-row"><AgentMark mark={agent.mark} /><span><strong>{agent.name}</strong><small>{agent.task}</small></span><StatusBadge status={agent.status} /><span className="agent-spend"><strong>{agent.spent}</strong><small>spent</small></span></div>;
}

function StatusBadge({ status }: { status: Agent["status"] }) { return <em className={`status-badge ${status.toLowerCase()}`}>{status}</em>; }

function ActivityRows({ activity }: { activity: Activity[] }) {
  return <div className="activity-rows">{activity.map((item) => <div className="activity-row" key={item.id}><span className={`activity-icon ${item.state}`} aria-hidden="true">{activityMark(item.state)}</span><span><strong>{item.title}</strong><small>{item.detail}</small></span><span className="activity-amount"><strong>{item.amount ?? "—"}</strong><small>{item.time}</small></span></div>)}</div>;
}

function RiskRow({ risk }: { risk: Risk }) { return <article className={`risk-row ${risk.severity}`}><span>{risk.severity === "warning" ? "!" : "i"}</span><div><small>{risk.severity} · {risk.time}</small><strong>{risk.title}</strong><p>{risk.detail}</p></div><em>Open review</em></article>; }

function activityMark(state: Activity["state"]) { return { settled: "✓", released: "↗", refunded: "↙", approval: "?", security: "!" }[state]; }
function initials(name: string) { return name.split(/\s|@/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join("") || "OP"; }
function shortName(name: string) { return name.includes("@") ? name.split("@")[0] : name.split(" ")[0]; }
function capitalize(value: string) { return value.charAt(0).toUpperCase() + value.slice(1); }
