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
import type { ProposalAnchorDeployment } from "./proposal-anchor";

type Section =
  | "overview"
  | "approvals"
  | "agents"
  | "activity"
  | "security"
  | "developers";

type ControlRoomProps = {
  snapshot: DashboardSnapshot;
  proposalAnchor: ProposalAnchorDeployment;
  viewer: { name: string; email: string; authenticated: boolean };
  accountHref: string | null;
};

const navItems: { id: Section; label: string; mark: string }[] = [
  { id: "overview", label: "Overview", mark: "01" },
  { id: "approvals", label: "Approvals", mark: "03" },
  { id: "agents", label: "Agents", mark: "04" },
  { id: "activity", label: "Activity", mark: "24" },
  { id: "security", label: "Security", mark: "02" },
  { id: "developers", label: "Developers", mark: "↗" },
];

export function ControlRoom({ snapshot, proposalAnchor, viewer, accountHref }: ControlRoomProps) {
  const [section, setSection] = useState<Section>("overview");
  const [approval, setApproval] = useState<Approval | null>(null);
  const [pauseOpen, setPauseOpen] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    const commandId = window.localStorage.getItem("flowops.pendingCommand");
    if (!commandId) {
      if (window.localStorage.getItem("flowops.pendingOperation")) queueMicrotask(() => setNotice("A prior command has no confirmed reference yet. Retry only the exact same action; FlowOps will reuse its idempotency identity."));
      return;
    }
    void fetch(`/api/flowops/commands/${encodeURIComponent(commandId)}`, { cache: "no-store" })
      .then(async (response) => ({ response, body: await response.json() as { state?: string; error?: { message?: string } } }))
      .then(({ response, body }) => {
        if (!response.ok) {
          setNotice(body.error?.message ?? `Command ${commandId} is still unresolved.`);
          return;
        }
        if (body.state === "SUCCEEDED" || body.state === "FAILED") window.localStorage.removeItem("flowops.pendingCommand");
        setNotice(`Recovered command ${commandId}: ${body.state ?? "UNRESOLVED"}.`);
      })
      .catch(() => setNotice(`Command ${commandId} could not be recovered yet. Do not submit a replacement.`));
  }, []);
  const title = navItems.find((item) => item.id === section)?.label ?? "Overview";

  const openSection = (next: Section) => {
    setSection(next);
    setNotice(null);
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  const explainLockedAction = (action: string) => {
    setNotice(
      snapshot.mode === "live"
        ? `${action} requires fresh step-up authentication. This read-only dashboard session submitted no command and performed no write.`
        : `${action} is locked in the public status view. Nothing was submitted and no write occurred. Sign in with an authorized membership to access organization controls.`,
    );
  };

  const submitCommand = async (input: Record<string, string>) => {
    const safeInput = Object.fromEntries(Object.entries(input).filter(([key]) => key !== "stepUpToken" && key !== "operationId"));
    const inputDigest = await browserDigest(JSON.stringify(safeInput));
    const pendingRaw = window.localStorage.getItem("flowops.pendingOperation");
    let operationId = input.operationId;
    if (pendingRaw) {
      try {
        const pending = JSON.parse(pendingRaw) as { operationId?: string; inputDigest?: string };
        if (pending.inputDigest !== inputDigest || !pending.operationId) throw new Error("different command");
        operationId = pending.operationId;
      } catch {
        throw new Error("A different command is unresolved. Recover or retry that exact action before starting another.");
      }
    }
    window.localStorage.setItem("flowops.pendingOperation", JSON.stringify({ operationId, inputDigest }));
    const response = await fetch("/api/flowops/commands", {
      method: "POST",
      cache: "no-store",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ...input, operationId }),
    });
    const body = await response.json() as { commandId?: string; state?: string; auditId?: string; error?: { code?: string; message?: string } };
    if (body.commandId) {
      window.localStorage.setItem("flowops.pendingCommand", body.commandId);
      window.localStorage.removeItem("flowops.pendingOperation");
    } else if (response.status < 500) {
      window.localStorage.removeItem("flowops.pendingOperation");
    }
    if (!response.ok) throw new Error(body.commandId ? `${body.error?.message ?? "Command unresolved"} Reference: ${body.commandId}.` : body.error?.message ?? "Command rejected.");
    if (body.state === "SUCCEEDED" || body.state === "FAILED") {
      window.localStorage.removeItem("flowops.pendingCommand");
      window.localStorage.removeItem("flowops.pendingOperation");
    }
    setNotice(`Authoritative command state: ${body.state ?? "UNRESOLVED"}${body.commandId ? ` · ${body.commandId}` : ""}${body.auditId ? ` · audit ${body.auditId}` : ""}.`);
    if (body.state === "SUCCEEDED") window.setTimeout(() => window.location.reload(), 700);
  };

  const navigationMark = (item: (typeof navItems)[number]) => {
    if (item.id === "approvals") return String(snapshot.approvals.length).padStart(2, "0");
    if (item.id === "agents") return String(snapshot.agents.length).padStart(2, "0");
    if (item.id === "activity") return String(snapshot.activity.length).padStart(2, "0");
    if (item.id === "security") return String(snapshot.risks.length).padStart(2, "0");
    return item.mark;
  };
  const connectionLive = snapshot.mode === "live" || snapshot.connection.label === "Live public status";

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
            <small className="brand-subtitle">Agent spend OS</small>
          </span>
        </div>

        <div className="org-card">
		  <span className="org-monogram">{initials(snapshot.organization.name)}</span>
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
              <em>{navigationMark(item)}</em>
            </button>
          ))}
        </nav>

        <div className="sidebar-foot">
          <span className={connectionLive ? "preview-rail live" : "preview-rail"}><i /> {snapshot.connection.label}</span>
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
            <span className="breadcrumb">
              Control room / {snapshot.mode === "live" ? `${snapshot.organization.name} / ` : ""}{title}
            </span>
          </div>
          <dl className="context-strip">
			<div><dt>{snapshot.mode === "public" ? "Organization data" : "Recognized expense"}</dt><dd>{snapshot.money.total} <span>{snapshot.money.asset}</span></dd></div>
            <div><dt>Base trusted block</dt><dd>#{snapshot.chain.lastTrustedBlock}</dd></div>
            <div><dt>Observer quorum</dt><dd className="quorum"><i />{snapshot.chain.observers}</dd></div>
          </dl>
          <div className="topbar-actions">
            <span className="fresh-label">Fresh {snapshot.chain.lastTrustedAt}</span>
            <span className={connectionLive ? "preview-pill live" : "preview-pill"}>{snapshot.connection.label}</span>
            <button className="icon-button" aria-label="Notifications" type="button">
              <span aria-hidden="true">●</span>
              <i>{snapshot.risks.length}</i>
            </button>
            {accountHref ? (
              <a className="viewer account-link" href={accountHref} aria-label={viewer.authenticated ? `Sign out ${viewer.name}` : "Sign in to FlowOps"}>
                <span>{initials(viewer.name)}</span>
                <strong>{viewer.authenticated ? shortName(viewer.name) : "Sign in"}</strong>
              </a>
            ) : (
              <span className="viewer account-link account-link-disabled" aria-label="Local sign-in is disabled">
                <span>PV</span>
                <strong>Local sign-in disabled</strong>
              </span>
            )}
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

          {section === "overview" ? <ProposalAnchorNotice deployment={proposalAnchor} /> : null}

          {section === "overview" ? (
            <Overview
              snapshot={snapshot}
              onApprovals={() => openSection("approvals")}
              onPause={() => setPauseOpen(true)}
              onApproval={setApproval}
              onAgents={() => openSection("agents")}
              onActivity={() => openSection("activity")}
              accountHref={accountHref}
              authenticated={viewer.authenticated}
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
			  reconciliation={snapshot.reconciliation}
              authorizationsPaused={snapshot.organization.authorizationsPaused}
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
              <span>{navigationMark(item)}</span>
              {item.label}
            </button>
          ))}
        </nav>
      </div>

      {approval ? (
        <ApprovalDrawer
          approval={approval}
          mode={snapshot.mode}
          onClose={() => setApproval(null)}
          onAction={explainLockedAction}
          onCommand={submitCommand}
        />
      ) : null}
      {pauseOpen ? (
        <PauseDialog
          organization={snapshot.organization.name}
          mode={snapshot.mode}
          onClose={() => setPauseOpen(false)}
          onConfirm={() => {
            setPauseOpen(false);
            explainLockedAction("Emergency pause");
          }}
          onCommand={submitCommand}
        />
      ) : null}
    </div>
  );
}

function ProposalAnchorNotice({ deployment }: { deployment: ProposalAnchorDeployment }) {
  const deployed = deployment.status === "experimental-unaudited";
  return (
    <section className="proposal-anchor-notice" aria-label="Base mainnet proposal deployment status">
      <div className="proposal-anchor-copy">
        <span>BASE MAINNET · SAFETY BOUNDARY</span>
        <h2>{deployed ? "Experimental evidence anchor only" : "Mainnet payment deployment is not active"}</h2>
        <p>
          {deployed
            ? "Evidence-only deployment. It is not a factory, vault, escrow, audited release, or production payment contract."
            : "Production contracts remain structurally blocked. No factory, vault, escrow, or payment contract is being represented as live."}
        </p>
        {deployment.address && deployment.explorerHref ? (
          <a href={deployment.explorerHref} target="_blank" rel="noreferrer">
            <code>{deployment.address}</code>
            <span>View address on Base Blockscout ↗</span>
          </a>
        ) : null}
      </div>
      <dl className="proposal-anchor-controls">
        <div><dt>Production ready</dt><dd>No</dd></div>
		<div><dt>Source verified</dt><dd>Unavailable</dd></div>
        <div><dt>Vault creation</dt><dd>Disabled</dd></div>
        <div><dt>USDC deposits</dt><dd>Disabled</dd></div>
        <div><dt>Asset warning</dt><dd>Do not send ETH or tokens</dd></div>
      </dl>
    </section>
  );
}

async function browserDigest(value: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function Overview({
  snapshot,
  onApprovals,
  onPause,
  onApproval,
  onAgents,
  onActivity,
  accountHref,
  authenticated,
}: {
  snapshot: DashboardSnapshot;
  onApprovals: () => void;
  onPause: () => void;
  onApproval: (approval: Approval) => void;
  onAgents: () => void;
  onActivity: () => void;
  accountHref: string | null;
  authenticated: boolean;
}) {
  return (
    <>
      <section className="command-header">
        <div className="command-index" aria-hidden="true">CONTROL / 01</div>
        <div className="command-copy">
          <div className="eyebrow"><span className={snapshot.organization.authorizationsPaused ? "halted-dot" : "healthy-dot"} /> {snapshot.organization.authorizationsPaused ? "Organization authorizations paused" : "Authorization boundary online"}</div>
          <h1>{snapshot.mode === "public" ? "Agent spend, under control." : "Treasury control, without custody."}</h1>
          <p>{snapshot.mode === "public" ? "Policy, human approval, bounded signing, and Base evidence—one operating layer for autonomous payments." : "Govern every agent payment from frozen intent to canonical Base evidence while customer keys stay outside FlowOps."}</p>
          <div className="observation-meta">
            <span>Observed {snapshot.generatedAt}</span>
            <span>{snapshot.mode === "live" ? "Organization-scoped read" : "Public operational read"}</span>
            <span>{snapshot.connection.detail}</span>
          </div>
        </div>
        <div className="command-actions">
          {snapshot.mode === "public" ? (
            <>
              {accountHref && !authenticated ? (
                <a className="primary-button account-cta" href={accountHref}>Enter control room <span>→</span></a>
              ) : (
                <button className="primary-button account-cta" type="button" disabled>
                  {authenticated ? "Identity active · authorized membership required" : "Enable local sign-in to continue"}
                </button>
              )}
              <button className="danger-button" type="button" disabled>Organization controls locked</button>
            </>
          ) : (
            <>
              <button className="primary-button" type="button" onClick={onApprovals}>
                Review {snapshot.approvals.length} approvals <span>→</span>
              </button>
              <button className="danger-button" type="button" onClick={onPause} disabled={snapshot.organization.authorizationsPaused}>
                {snapshot.organization.authorizationsPaused ? "Authorizations paused" : "Emergency pause"}
              </button>
            </>
          )}
        </div>
      </section>

      <section className="money-grid balance-ledger" aria-label="Organization balance">
        <div className="balance-card primary-balance">
		  <span>{snapshot.mode === "live" ? "Recognized economic expense" : snapshot.mode === "public" ? "Organization economic data" : "Observed treasury / USDC"}</span>
          <strong>{snapshot.money.total}</strong>
		  <small><i /> {snapshot.mode === "live" ? `ASCP PostgreSQL subledger · ${snapshot.money.asset} · not a wallet balance` : "Private by default · sign in with an authorized membership"}</small>
          <div className="balance-signal" aria-hidden="true"><i /><i /><i /><i /><i /><i /><i /><i /></div>
        </div>
		<MoneyCard label="Wallet ledger delta" value={snapshot.money.walletDelta} tone="neutral" />
        <MoneyCard label="Reserved" value={snapshot.money.reserved} tone="reserved" />
        <MoneyCard label="Pending chain evidence" value={snapshot.money.pending} tone="pending" />
        <MoneyCard label="Unresolved" value={snapshot.money.unresolved} tone="risk" />
      </section>

      <div className="overview-grid">
        <section className="panel approval-panel">
          <PanelHeader
            kicker="Decision queue"
            title="Needs your attention"
            meta={`${snapshot.approvals.length} pending`}
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
            {snapshot.approvals.length === 0 ? <p className="empty-state">Approval data is organization-private. Sign in to view an authorized queue.</p> : null}
          </div>
        </section>

        <section className="panel budget-panel">
		  <PanelHeader kicker="Budget" title="Today’s policy usage" meta={snapshot.money.dailySpentPercent === null ? "Per-agent" : "Current"} />
          <div className="budget-total">
            <strong>{snapshot.money.spentToday}</strong>
            <span>spent today</span>
          </div>
          <div className="progress-label">
			<span>Daily usage</span>
			<strong>{snapshot.money.dailySpentPercent === null ? "See agents" : `${snapshot.money.dailySpentPercent}%`}</strong>
          </div>
		  <div className="progress-track" aria-label={snapshot.money.dailySpentPercent === null ? "Daily budget usage is available per agent" : `${snapshot.money.dailySpentPercent}% of daily policy limit used`}>
			<i style={{ width: `${snapshot.money.dailySpentPercent ?? 0}%` }} />
          </div>
          <div className="budget-foot">
			<span>{snapshot.money.spentToday} spent</span>
			<span>{snapshot.money.dailyLimit} daily limit</span>
          </div>
          <div className="budget-note">
            <span>i</span>
			<p>{snapshot.mode === "live" ? <><strong>Operational subledger only</strong><br />Wallet balance and spendable funds are not inferred</> : <><strong>Private by design</strong><br />Public status never exposes organization spend or wallet balances</>}</p>
          </div>
        </section>

        <section className="panel agents-panel">
          <PanelHeader kicker="Governed agents" title="Active fleet" meta={`${snapshot.agents.filter((agent) => agent.status === "ACTIVE").length} active`} onView={onAgents} />
          <div className="agent-rows">
            {snapshot.agents.slice(0, 3).map((agent) => (
              <AgentRow key={agent.id} agent={agent} />
            ))}
            {snapshot.agents.length === 0 ? <p className="empty-state">Governed agents are visible only to authorized organization members.</p> : null}
          </div>
        </section>

        <section className="panel chain-panel">
          <PanelHeader kicker="Base truth" title="Chain health" meta={snapshot.chain.state} />
          <dl className="chain-facts">
			<div><dt>Network</dt><dd>{snapshot.chain.network}</dd></div>
            <div><dt>Observer quorum</dt><dd>{snapshot.chain.observers}</dd></div>
            <div><dt>Last trusted block</dt><dd>#{snapshot.chain.lastTrustedBlock}</dd></div>
            <div><dt>Freshness</dt><dd>{snapshot.chain.lastTrustedAt}</dd></div>
          </dl>
        </section>

        <section className="panel activity-panel">
          <PanelHeader kicker="Evidence graph" title="Economic activity" meta={snapshot.mode === "live" ? "Observed" : "Private"} onView={onActivity} />
          <ActivityRows activity={snapshot.activity.slice(0, 4)} />
          {snapshot.activity.length === 0 ? <p className="empty-state">Economic activity and evidence records require an authorized membership.</p> : null}
        </section>
      </div>
	  <p className="freshness"><span>END OF OBSERVED WINDOW</span> Snapshot refreshed {snapshot.generatedAt}. {snapshot.mode === "live" ? "Values marked unavailable are not exposed by the control plane; writes require a separate step-up session." : snapshot.connection.label === "Status unavailable" ? "Public health evidence is unavailable; FlowOps is not representing chain health or authorization capacity." : "This is live public health data; organization records remain private and no control is available without sign-in."}</p>
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
          {(["all", "DRAFT", "ACTIVE", "PAUSED", "QUARANTINED", "REVOKED", "ARCHIVED"] as const).map((item) => <button className={status === item ? "active" : ""} key={item} onClick={() => setStatus(item)} type="button">{item === "all" ? "All" : capitalize(item.toLowerCase())}</button>)}
        </div>
      </div>
      <div className="agent-directory">
        {visible.map((agent) => (
          <article className="agent-card" key={agent.id}>
            <header><AgentMark mark={agent.mark} /><span><strong>{agent.name}</strong><small>{agent.purpose}</small></span><StatusBadge status={agent.status} /></header>
            <div className="agent-balance"><span>Available budget</span><strong>{agent.available}</strong><small>of {agent.limit}</small></div>
            <div className="progress-track small"><i style={{ width: `${agent.percent}%` }} /></div>
			<footer><span>Latest recorded task</span><strong>{agent.task}</strong><small>Signer boundary: customer-controlled</small></footer>
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
		{(["all", "pending", "decision", "settled", "released", "refunded", "approval", "security"] as const).map((item) => <button className={filter === item ? "active" : ""} key={item} onClick={() => setFilter(item)} type="button">{capitalize(item)}</button>)}
      </div>
      <div className="activity-view panel"><ActivityRows activity={visible} /></div>
    </section>
  );
}

function Security({ risks, chain, reconciliation, authorizationsPaused, onPause }: { risks: Risk[]; chain: DashboardSnapshot["chain"]; reconciliation: DashboardSnapshot["reconciliation"]; authorizationsPaused: boolean; onPause: () => void }) {
  return (
    <section className="section-stack">
      <SectionHeading eyebrow="Fail closed" title="Security & recovery" description="Critical controls stay visible until the underlying risk is acknowledged or canonically resolved." action={<button className="danger-button" onClick={onPause} disabled={authorizationsPaused} type="button">{authorizationsPaused ? "Authorizations paused" : "Emergency pause"}</button>} />
	  <div className="security-grid">
        <div className="panel security-state"><span className={authorizationsPaused ? "halted-dot" : "healthy-dot"} /><div><small>Authorization state</small><strong>{authorizationsPaused ? "PAUSED" : "Protected"}</strong><p>{authorizationsPaused ? "The persistent organization gate rejects new authorization issuance." : "Organization and signer boundaries are accepting policy-valid requests."}</p></div></div>
		<div className="panel security-state"><span className={chain.state === "HEALTHY" ? "healthy-dot" : "halted-dot"} /><div><small>Base canonical state</small><strong>{chain.state}</strong><p>{chain.observers} at block #{chain.lastTrustedBlock}.</p></div></div>
	  </div>
	  <div className="panel recovery-progress">
		<PanelHeader kicker="Canonical recovery" title="Reconciliation progress" meta={reconciliation.complete ? "Complete" : `${reconciliation.unresolvedOutcomes} unresolved`} />
		<dl className="chain-facts">
		  <div><dt>Candidate outcomes</dt><dd>{reconciliation.resolvedCandidates} / {reconciliation.totalCandidates} resolved</dd></div>
		  <div><dt>Observed through</dt><dd>#{reconciliation.observedThroughBlock}</dd></div>
		  <div><dt>Pending finality</dt><dd>{reconciliation.pendingFinality}</dd></div>
		  <div><dt>Quarantined</dt><dd>{reconciliation.quarantinedOutcomes}</dd></div>
		  <div><dt>Manual resume gate</dt><dd>{reconciliation.readyForManualResume ? "Ready" : "Blocked"}</dd></div>
		  <div><dt>Excluded ledger entries</dt><dd>{reconciliation.unclassifiedLedgerTransactions}</dd></div>
		</dl>
	  </div>
      <div className="risk-list">
        {risks.map((risk) => <RiskRow key={risk.id} risk={risk} />)}
      </div>
		<div className="panel recovery-card"><div><span>Recovery rule</span><h2>No silent retries.</h2><p>Unknown broadcasts stay unresolved or explicitly quarantined. Settlement, release, and refund appear only after independent Base evidence agrees.</p></div><dl><div><dt>Resolved candidates</dt><dd>{chain.state === "HEALTHY" ? "Canonical" : "Review state"}</dd></div><div><dt>Observer quorum</dt><dd>2 minimum</dd></div><div><dt>Manual resume</dt><dd>Required</dd></div></dl></div>
    </section>
  );
}

function Developers({ snapshot }: { snapshot: DashboardSnapshot }) {
  return (
    <section className="section-stack">
      <SectionHeading eyebrow="Build with boundaries" title="Developer center" description="Connect agents through MCP or the SDK without giving them an unrestricted wallet or FlowOps a customer key." />
      <div className="developer-grid">
		<article className="panel code-card"><span className="code-kicker">MCP connection</span><h2>Use the deployed control-plane endpoint</h2><p>The runtime endpoint and credential are deployment secrets and are never replaced with a sample URL in this dashboard. Follow the repository deployment runbook to configure the real MCP transport.</p></article>
        <article className="panel api-health"><PanelHeader kicker="Integration health" title="All boundaries" meta={snapshot.mode === "live" ? "Read only" : "Public status"} /><dl><div><dt>Control plane</dt><dd>{snapshot.mode === "live" ? "Membership authorized" : snapshot.connection.label}</dd></div><div><dt>Customer signer</dt><dd>Not exposed</dd></div><div><dt>x402 facilitator</dt><dd>Not exposed</dd></div><div><dt>Base observers</dt><dd>{snapshot.chain.observers}</dd></div></dl><p>{snapshot.connection.detail}</p></article>
      </div>
      <div className="panel logs-card"><PanelHeader kicker="Recent requests" title="Developer logs" meta="Unavailable" /><div className="log-row head"><span>Time</span><span>Request</span><span>Agent</span><span>Outcome</span><span>Latency</span></div><p className="empty-state">Request logs are not exposed by the current control-plane snapshot.</p></div>
    </section>
  );
}

function ApprovalDrawer({ approval, mode, onClose, onAction, onCommand }: { approval: Approval; mode: DashboardSnapshot["mode"]; onClose: () => void; onAction: (action: string) => void; onCommand: (input: Record<string, string>) => Promise<void> }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  const [stepUpToken, setStepUpToken] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    closeRef.current?.focus();
    const close = (event: KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);
  const decide = async (action: "APPROVE" | "REJECT") => {
    if (mode !== "live") {
      onClose();
      onAction(action === "APPROVE" ? "Approval" : "Denial");
      return;
    }
    setBusy(true);
    setError("");
    try {
	  await onCommand(approval.source === "ascp"
		? { type: "ascp-approval", approvalId: approval.id, reviewDigest: approval.requestDigest, action, operationId: crypto.randomUUID(), stepUpToken }
		: { type: "approval", requestId: approval.id, requestDigest: approval.requestDigest, action, note, operationId: crypto.randomUUID(), stepUpToken });
      setStepUpToken("");
      onClose();
    } catch (cause) {
      setStepUpToken("");
      setError(cause instanceof Error ? cause.message : "Command outcome is unresolved.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="overlay" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside className="drawer" role="dialog" aria-modal="true" aria-labelledby="approval-title">
        <header><span>Approval {approval.id}</span><button ref={closeRef} onClick={onClose} aria-label="Close approval details" type="button">×</button></header>
        <div className="drawer-title"><AgentMark mark={approval.agentMark} /><div><small>{approval.agent}</small><h2 id="approval-title">{approval.title}</h2></div></div>
		<div className="drawer-amount"><span>Exact requested amount</span><strong>{approval.amount}</strong><small>{formatAtomicForConfirmation(approval.amountAtomic)} atomic · {approval.rail} · {approval.network}</small></div>
		<dl className="detail-list"><div><dt>Recipient / vendor</dt><dd className="mono">{approval.recipientAddress}</dd></div><div><dt>Asset</dt><dd className="mono">{approval.assetSymbol ? `${approval.assetSymbol} · ` : ""}{approval.assetAddress}</dd></div><div><dt>Chain</dt><dd>{approval.network}</dd></div><div><dt>Agent</dt><dd>{approval.agent}</dd></div><div><dt>Task</dt><dd>{approval.title}</dd></div><div><dt>Rail</dt><dd>{approval.rail}</dd></div><div><dt>Risk</dt><dd>{capitalize(approval.risk)}</dd></div><div><dt>Policy snapshot</dt><dd className="mono">{approval.policyVersion ?? "Not exposed"}</dd></div><div><dt>Evidence refs</dt><dd className="mono">{approval.evidenceRefs ?? "Not exposed"}</dd></div><div><dt>Created</dt><dd>{approval.requested}</dd></div><div><dt>Expires</dt><dd>{approval.expires}</dd></div><div><dt>Request digest</dt><dd className="mono">{approval.requestDigest ?? "Not exposed"}</dd></div></dl>
        <div className="reason-box"><span>Why approval is required</span><p>{approval.reason}</p></div>
        <div className="truth-box"><strong>What this decision means</strong><p>Approval authorizes only this frozen intent. Any change to amount, recipient, task, rail, or request digest requires a new decision.</p></div>
		{mode === "live" ? <div className="step-up-box"><label htmlFor="approval-step-up">Fresh step-up token</label><input id="approval-step-up" type="password" autoComplete="off" value={stepUpToken} onChange={(event) => setStepUpToken(event.target.value)} disabled={busy} />{approval.source === "legacy" ? <><label htmlFor="approval-note">Decision note</label><textarea id="approval-note" value={note} maxLength={2048} onChange={(event) => setNote(event.target.value)} disabled={busy} /></> : null}<small>Held in memory for this request only. Never stored by the dashboard.</small>{error ? <p role="alert">{error}</p> : null}</div> : null}
        <footer><button className="secondary-button" onClick={() => void decide("REJECT")} disabled={busy || (mode === "live" && !stepUpToken)} type="button">Deny</button><button className="primary-button" onClick={() => void decide("APPROVE")} disabled={busy || (mode === "live" && !stepUpToken)} aria-busy={busy} type="button">{busy ? "Verifying…" : "Approve exact intent"}</button></footer>
      </aside>
    </div>
  );
}

function PauseDialog({ organization, mode, onClose, onConfirm, onCommand }: { organization: string; mode: DashboardSnapshot["mode"]; onClose: () => void; onConfirm: () => void; onCommand: (input: Record<string, string>) => Promise<void> }) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const [stepUpToken, setStepUpToken] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    cancelRef.current?.focus();
    const close = (event: KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);
  const pause = async () => {
    if (mode !== "live") return onConfirm();
    setBusy(true);
    setError("");
    try {
      await onCommand({ type: "organization-pause", reason, operationId: crypto.randomUUID(), stepUpToken });
      setStepUpToken("");
      onClose();
    } catch (cause) {
      setStepUpToken("");
      setError(cause instanceof Error ? cause.message : "Command outcome is unresolved.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="overlay centered" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="pause-dialog" role="alertdialog" aria-modal="true" aria-labelledby="pause-title">
        <span className="pause-symbol">!</span><small>Emergency control</small><h2 id="pause-title">Pause every agent?</h2><p>This requests an organization-wide authorization stop for <strong>{organization}</strong>. Pending evidence remains readable and no transaction is silently retried.</p><div className="pause-warning">{mode === "live" ? "Requires the same member's fresh step-up token. The result is shown only after the durable command is read back." : "The public status view cannot send commands. Sign in with an authorized membership and complete step-up authentication."}</div>{mode === "live" ? <div className="step-up-box"><label htmlFor="pause-reason">Containment reason</label><textarea id="pause-reason" value={reason} maxLength={1024} onChange={(event) => setReason(event.target.value)} disabled={busy} /><label htmlFor="pause-step-up">Fresh step-up token</label><input id="pause-step-up" type="password" autoComplete="off" value={stepUpToken} onChange={(event) => setStepUpToken(event.target.value)} disabled={busy} /><small>Held in memory for this request only. Never stored by the dashboard.</small>{error ? <p role="alert">{error}</p> : null}</div> : null}<footer><button ref={cancelRef} className="secondary-button" onClick={onClose} disabled={busy} type="button">Keep running</button><button className="danger-solid" onClick={() => void pause()} disabled={busy || (mode === "live" && (!stepUpToken || !reason.trim()))} aria-busy={busy} type="button">{busy ? "Verifying…" : "Request emergency pause"}</button></footer>
      </section>
    </div>
  );
}

function PanelHeader({ kicker, title, meta, onView }: { kicker: string; title: string; meta: string; onView?: () => void }) {
  return <header className="panel-header"><div><span>{kicker}</span><h2>{title}</h2></div><div className="panel-header-actions"><em>{meta}</em>{onView ? <button onClick={onView} type="button">View all <span>→</span></button> : null}</div></header>;
}

function SectionHeading({ eyebrow, title, description, action }: { eyebrow: string; title: string; description: string; action?: ReactNode }) {
  return <header className="section-heading"><div><span>{eyebrow}</span><h1>{title}</h1><p>{description}</p></div>{action}</header>;
}

function MoneyCard({ label, value, tone }: { label: string; value: string; tone: string }) {
	const detail = value === "Private" ? "Authorized members only" : value === "Not available" ? "No authoritative record" : tone === "neutral" ? "Signed subledger effect" : tone === "risk" ? "Needs review" : "Tracked separately";
  return <div className={`balance-card compact ${tone}`}><span>{label}</span><strong>{value}</strong><small><i /> {detail}</small></div>;
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

function activityMark(state: Activity["state"]) { return { settled: "✓", released: "↗", refunded: "↙", approval: "?", decision: "§", security: "!", pending: "·" }[state]; }
function initials(name: string) { return name.split(/\s|@/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join("") || "OP"; }
function shortName(name: string) { return name.includes("@") ? name.split("@")[0] : name.split(" ")[0]; }
function capitalize(value: string) { return value.charAt(0).toUpperCase() + value.slice(1); }
function formatAtomicForConfirmation(value: string) { return BigInt(value).toLocaleString("en-US"); }
