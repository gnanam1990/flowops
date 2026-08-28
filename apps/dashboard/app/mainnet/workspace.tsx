"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import type { MainnetIntentAnchorDeployment } from "./mainnet-types";
import {
  buildCanonicalIntent,
  decodeIntentRecord,
  encodeAnchorIntent,
  encodeGetIntent,
  EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256,
  isRecoverablePreparedIntent,
  parseBoundedGasEstimate,
  validateIntentForm,
} from "./intent-codec.js";

const BASE_CHAIN_HEX = "0x2105";
const BASE_EXPLORER = "https://base.blockscout.com";
const ADDRESS_PATTERN = /^0x[0-9a-fA-F]{40}$/;
const PENDING_STORAGE_SLOT = "flowops.mainnetIntent.pendingV1";

type EthereumProvider = {
  request(input: { method: string; params?: unknown[] }): Promise<unknown>;
};

type PreparedIntent = {
  controller: string;
  canonicalPayload: string;
  intentDigest: string;
  policyDigest: string;
  expiresAt: number;
};

type Receipt = { status?: string; blockNumber?: string; transactionHash?: string };

declare global {
  interface Window {
    ethereum?: EthereumProvider;
  }
}

export function MainnetIntentWorkspace({ deployment }: { deployment: MainnetIntentAnchorDeployment }) {
  const [account, setAccount] = useState("");
  const [taskId, setTaskId] = useState("vendor-research-001");
  const [agentId, setAgentId] = useState("research-agent");
  const [recipient, setRecipient] = useState("");
  const [amountAtomic, setAmountAtomic] = useState("1000000");
  const [purpose, setPurpose] = useState("Purchase one verified research result");
  const [policyVersion, setPolicyVersion] = useState("pilot-v1");
  const [lifetimeDays, setLifetimeDays] = useState("1");
  const [prepared, setPrepared] = useState<PreparedIntent | null>(null);
  const [transactionHash, setTransactionHash] = useState("");
  const [confirmedBlock, setConfirmedBlock] = useState("");
  const [recordState, setRecordState] = useState<"unknown" | "active" | "expired" | "missing">("unknown");
  const [pendingUnresolved, setPendingUnresolved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("Connect a wallet to prepare an exact, non-payment intent record.");

  const deploymentLive = deployment.status === "limited-mainnet-live" && deployment.address !== null;
  const shortAccount = useMemo(() => account ? `${account.slice(0, 6)}…${account.slice(-4)}` : "Not connected", [account]);

  useEffect(() => {
    const provider = window.ethereum;
    if (!provider) return;
    void provider.request({ method: "eth_accounts" }).then((accounts) => {
      const first = Array.isArray(accounts) && typeof accounts[0] === "string" ? accounts[0] : "";
      if (ADDRESS_PATTERN.test(first)) setAccount(first);
    }).catch(() => undefined);

    const pending = window.localStorage.getItem(PENDING_STORAGE_SLOT);
    if (!pending) return;
    try {
      const parsed = JSON.parse(pending) as { transactionHash?: string; prepared?: PreparedIntent };
      if (/^0x[0-9a-fA-F]{64}$/.test(parsed.transactionHash ?? "") && isRecoverablePreparedIntent(parsed.prepared)) {
        const hash = parsed.transactionHash ?? "";
        queueMicrotask(() => {
          setTransactionHash(hash);
          setPrepared(parsed.prepared ?? null);
          setPendingUnresolved(true);
          setMessage("Recovered a submitted mainnet transaction. Checking its canonical receipt before any retry.");
          void recoverPendingReceipt(provider, hash).then((receipt) => {
            if (!receipt) {
              setMessage("The prior transaction is still unresolved. Inspect it on Base before sending any replacement.");
              return;
            }
            if (receipt.status !== "0x1") {
              window.localStorage.removeItem(PENDING_STORAGE_SLOT);
              setPendingUnresolved(false);
              setMessage("The prior transaction reverted. Its intent was not anchored.");
              return;
            }
            window.localStorage.removeItem(PENDING_STORAGE_SLOT);
            setPendingUnresolved(false);
            setConfirmedBlock(hexToDecimalString(receipt.blockNumber ?? "0x0"));
            setMessage("Canonical receipt found. Verify the stored record against the exact prepared digest.");
          }).catch(() => {
            setMessage("Receipt recovery is unavailable. Do not replace the transaction until the explorer resolves it.");
          });
        });
      }
    } catch {
      window.localStorage.removeItem(PENDING_STORAGE_SLOT);
    }
  }, []);

  const connectWallet = async () => {
    if (!window.ethereum) {
      setMessage("No browser wallet was detected. Open this page in a wallet-enabled browser.");
      return;
    }
    setBusy(true);
    try {
      const accounts = await window.ethereum.request({ method: "eth_requestAccounts" });
      const selected = Array.isArray(accounts) && typeof accounts[0] === "string" ? accounts[0] : "";
      if (!ADDRESS_PATTERN.test(selected)) throw new Error("Wallet returned no valid account.");
      await ensureBaseMainnet(window.ethereum);
      setAccount(selected);
      if (pendingUnresolved) {
        setMessage(selected.toLowerCase() === prepared?.controller.toLowerCase()
          ? "Wallet reconnected. The prior transaction remains unresolved; inspect or recover it before another write."
          : "A different wallet is active while a prior transaction remains unresolved. Switch back before verification.");
      } else {
        setPrepared(null);
        setTransactionHash("");
        setConfirmedBlock("");
        setRecordState("unknown");
        setMessage("Wallet connected to Base mainnet. Review every field before preparing the digest.");
      }
    } catch (error) {
      setMessage(errorMessage(error, "Wallet connection was rejected."));
    } finally {
      setBusy(false);
    }
  };

  const prepareIntent = async (event: FormEvent) => {
    event.preventDefault();
    if (pendingUnresolved || window.localStorage.getItem(PENDING_STORAGE_SLOT)) {
      setMessage("A prior mainnet transaction is unresolved. Recover it before preparing another intent.");
      return;
    }
    if (!account) {
      setMessage("Connect the controlling wallet before preparing an intent.");
      return;
    }
    const input = { taskId, agentId, recipient, amountAtomic, purpose, policyVersion, lifetimeDays };
    const error = validateIntentForm(input);
    if (error) {
      setMessage(error);
      return;
    }

    const expiresAt = Math.floor(Date.now() / 1000) + Number(lifetimeDays) * 86_400;
    const { canonicalPayload, canonicalPolicy } = buildCanonicalIntent(input, account, expiresAt);
    const nextPrepared = {
      controller: account,
      canonicalPayload,
      intentDigest: await sha256Hex(canonicalPayload),
      policyDigest: await sha256Hex(canonicalPolicy),
      expiresAt,
    };
    setPrepared(nextPrepared);
    setTransactionHash("");
    setConfirmedBlock("");
    setRecordState("unknown");
    setMessage("Exact intent prepared. Any field change requires preparing a new digest; no transaction has been sent.");
  };

  const anchorIntent = async () => {
    if (!deploymentLive || !deployment.address || !prepared || !account || !window.ethereum || pendingUnresolved) return;
    setBusy(true);
    try {
      await ensureBaseMainnet(window.ethereum);
      await assertExactIntentAnchorRuntime(window.ethereum, deployment.address);
      const currentAccounts = await window.ethereum.request({ method: "eth_accounts" });
      const current = Array.isArray(currentAccounts) && typeof currentAccounts[0] === "string" ? currentAccounts[0] : "";
      if (current.toLowerCase() !== prepared.controller.toLowerCase()) {
        throw new Error("The active wallet changed after preparation. Prepare the intent again for this wallet.");
      }
      const data = encodeAnchorIntent(prepared.intentDigest, prepared.policyDigest, prepared.expiresAt);
      const estimatedGas = await window.ethereum.request({
        method: "eth_estimateGas",
        params: [{ chainId: BASE_CHAIN_HEX, from: account, to: deployment.address, value: "0x0", data }],
      });
      const gas = parseBoundedGasEstimate(estimatedGas);
      const currentChain = await window.ethereum.request({ method: "eth_chainId" });
      if (currentChain !== BASE_CHAIN_HEX) throw new Error("Wallet left Base mainnet before submission.");
      const hash = await window.ethereum.request({
        method: "eth_sendTransaction",
        params: [{ chainId: BASE_CHAIN_HEX, from: account, to: deployment.address, value: "0x0", gas, data }],
      });
      if (typeof hash !== "string" || !/^0x[0-9a-fA-F]{64}$/.test(hash)) {
        throw new Error("Wallet returned no valid transaction hash.");
      }
      setTransactionHash(hash);
      setPendingUnresolved(true);
      window.localStorage.setItem(PENDING_STORAGE_SLOT, JSON.stringify({ transactionHash: hash, prepared }));
      setMessage("Transaction submitted. Waiting for a canonical Base receipt; do not send a replacement.");

      const receipt = await waitForReceipt(window.ethereum, hash);
      if (!receipt) {
        setMessage("Confirmation is still unresolved. Inspect the transaction on Base before retrying.");
        return;
      }
      if (receipt.status !== "0x1") {
        window.localStorage.removeItem(PENDING_STORAGE_SLOT);
        setPendingUnresolved(false);
        throw new Error("The mainnet transaction reverted. No intent record was created.");
      }
      window.localStorage.removeItem(PENDING_STORAGE_SLOT);
      setPendingUnresolved(false);
      setConfirmedBlock(hexToDecimalString(receipt.blockNumber ?? "0x0"));
      setMessage("Intent anchored on Base mainnet. Verify the stored record against the exact digest.");
    } catch (error) {
      setMessage(errorMessage(error, "The intent could not be anchored."));
    } finally {
      setBusy(false);
    }
  };

  const verifyIntent = async () => {
    if (!deploymentLive || !deployment.address || !prepared || !window.ethereum) return;
    setBusy(true);
    try {
      await ensureBaseMainnet(window.ethereum);
      await assertExactIntentAnchorRuntime(window.ethereum, deployment.address);
      const data = encodeGetIntent(prepared.controller, prepared.intentDigest);
      const raw = await window.ethereum.request({
        method: "eth_call",
        params: [{ to: deployment.address, data }, "latest"],
      });
      if (typeof raw !== "string") throw new Error("Base returned an invalid record response.");
      const record = decodeIntentRecord(raw);
      if (!record.exists) {
        setRecordState("missing");
        setMessage("No record exists for this controller and exact intent digest.");
        return;
      }
      if (record.policyDigest.toLowerCase() !== prepared.policyDigest.toLowerCase() || record.expiresAt !== prepared.expiresAt) {
        throw new Error("The stored record does not match the prepared policy digest and expiry.");
      }
      setRecordState(record.active ? "active" : "expired");
      setMessage(record.active ? "Verified: the exact controller-scoped intent is active on Base mainnet." : "Verified: the immutable record exists but its authorization window has expired.");
    } catch (error) {
      setMessage(errorMessage(error, "The record could not be verified."));
    } finally {
      setBusy(false);
    }
  };

  const recoverPending = async () => {
    if (!transactionHash || !window.ethereum) return;
    setBusy(true);
    try {
      await ensureBaseMainnet(window.ethereum);
      const receipt = await recoverPendingReceipt(window.ethereum, transactionHash);
      if (!receipt) {
        setMessage("The prior transaction is still unresolved. Inspect it on Base before sending any replacement.");
        return;
      }
      window.localStorage.removeItem(PENDING_STORAGE_SLOT);
      setPendingUnresolved(false);
      if (receipt.status !== "0x1") {
        setMessage("The prior transaction reverted. Its intent was not anchored.");
        return;
      }
      setConfirmedBlock(hexToDecimalString(receipt.blockNumber ?? "0x0"));
      setMessage("Canonical Base receipt recovered. Verify the stored record against the exact prepared digest.");
    } catch (error) {
      setMessage(errorMessage(error, "Receipt recovery is unavailable. Do not replace the transaction yet."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="mainnet-page">
      <nav className="mainnet-nav">
        <Link className="mainnet-brand" href="/"><span aria-hidden="true" />FlowOps</Link>
        <div><i /> BASE MAINNET · CHAIN ID 8453</div>
        <Link href="/">Control overview ↗</Link>
      </nav>

      <section className="mainnet-hero">
        <div>
          <p>FUNCTIONAL MAINNET INTEGRATION</p>
          <h1>Make an agent’s spending intent independently verifiable.</h1>
          <span>FlowOps anchors an exact intent and policy digest. It never moves USDC, receives funds, or gains permission to spend.</span>
        </div>
        <dl>
          <div><dt>Network</dt><dd>Base mainnet</dd></div>
          <div><dt>Wallet</dt><dd>{shortAccount}</dd></div>
          <div><dt>Contract</dt><dd>{deploymentLive && deployment.explorerHref ? <a href={deployment.explorerHref} target="_blank" rel="noreferrer">{deployment.address} ↗</a> : "Deployment pending"}</dd></div>
          <div><dt>Money movement</dt><dd>None</dd></div>
        </dl>
      </section>

      {!deploymentLive ? (
        <section className="deployment-gate" role="status">
          <div><strong>Mainnet contract deployment pending</strong><p>The product flow is implemented, but transaction controls remain disabled until the reviewed contract address is configured.</p></div>
          <span>NO BROADCAST · NO FUNDS</span>
        </section>
      ) : null}

      <section className="mainnet-grid">
        <form className="intent-form" onSubmit={prepareIntent} onChange={() => prepared && setPrepared(null)}>
          <header><span>01 / DEFINE</span><h2>Bound the exact intent</h2><p>These fields become a canonical SHA-256 digest. They are never sent to FlowOps as wallet credentials.</p></header>
          <fieldset disabled={pendingUnresolved || busy}>
            <div className="field-pair">
              <label>Agent ID<input value={agentId} onChange={(event) => setAgentId(event.target.value)} maxLength={64} /></label>
              <label>Task ID<input value={taskId} onChange={(event) => setTaskId(event.target.value)} maxLength={64} /></label>
            </div>
            <label>Recipient on Base<input value={recipient} onChange={(event) => setRecipient(event.target.value)} placeholder="0x…" spellCheck={false} /></label>
            <div className="field-pair">
              <label>Maximum USDC atomic units<input inputMode="numeric" value={amountAtomic} onChange={(event) => setAmountAtomic(event.target.value)} /></label>
              <label>Authorization lifetime<select value={lifetimeDays} onChange={(event) => setLifetimeDays(event.target.value)}><option value="1">1 day</option><option value="7">7 days</option><option value="30">30 days</option></select></label>
            </div>
            <label>Purpose<textarea value={purpose} onChange={(event) => setPurpose(event.target.value)} maxLength={280} /></label>
            <label>Policy version<input value={policyVersion} onChange={(event) => setPolicyVersion(event.target.value)} maxLength={64} /></label>
          </fieldset>
          <div className="form-actions">
            <button className="secondary-action" type="button" onClick={() => void connectWallet()} disabled={busy}>{account ? "Reconnect wallet" : "Connect wallet"}</button>
            <button className="primary-action" type="submit" disabled={busy || !account || pendingUnresolved}>Prepare exact intent</button>
          </div>
        </form>

        <aside className="intent-proof">
          <header><span>02 / REVIEW</span><h2>Proof boundary</h2><p>Nothing is broadcast until the wallet shows the exact Base mainnet transaction.</p></header>
          <dl>
            <div><dt>Controller</dt><dd>{prepared?.controller ?? "Prepare an intent"}</dd></div>
            <div><dt>Intent digest</dt><dd>{prepared?.intentDigest ?? "—"}</dd></div>
            <div><dt>Policy digest</dt><dd>{prepared?.policyDigest ?? "—"}</dd></div>
            <div><dt>Expires</dt><dd>{prepared ? new Date(prepared.expiresAt * 1000).toISOString() : "—"}</dd></div>
          </dl>
          <details className="canonical-payload">
            <summary>Canonical verifier payload</summary>
            <code>{prepared?.canonicalPayload ?? "Prepare an intent to reveal the exact hash preimage."}</code>
          </details>
          <div className="proof-warning"><strong>Zero-value call</strong><span>This contract cannot transfer tokens or execute payments. Wallet value must remain exactly zero.</span></div>
          <button className="primary-action anchor-action" type="button" onClick={() => void anchorIntent()} disabled={busy || pendingUnresolved || !deploymentLive || !prepared}>{busy ? "Checking Base…" : pendingUnresolved ? "Prior transaction unresolved" : "Anchor on Base mainnet"}</button>
        </aside>
      </section>

      <section className="receipt-panel" aria-live="polite">
        <div><span>03 / VERIFY</span><h2>Receipt and independent read</h2><p>{message}</p></div>
        <dl>
          <div><dt>Transaction</dt><dd>{transactionHash ? <a href={`${BASE_EXPLORER}/tx/${transactionHash}`} target="_blank" rel="noreferrer">{transactionHash} ↗</a> : "Not submitted"}</dd></div>
          <div><dt>Confirmed block</dt><dd>{confirmedBlock || "Pending"}</dd></div>
          <div><dt>Record state</dt><dd className={`record-${recordState}`}>{recordState}</dd></div>
        </dl>
        {pendingUnresolved ? (
          <button className="secondary-action" type="button" onClick={() => void recoverPending()} disabled={busy || !transactionHash}>Recover Base receipt</button>
        ) : (
          <button className="secondary-action" type="button" onClick={() => void verifyIntent()} disabled={busy || !deploymentLive || !prepared}>Verify exact record</button>
        )}
      </section>

      <section className="mainnet-boundary">
        <article><span>POLICY</span><strong>Exact inputs</strong><p>Controller, task, agent, recipient, Base USDC, cap, purpose, policy version, and expiry are digest-bound.</p></article>
        <article><span>AUTHORITY</span><strong>Wallet scoped</strong><p>Another wallet cannot reserve or overwrite your digest. Replays from the same controller fail.</p></article>
        <article><span>MONEY</span><strong>Never touched</strong><p>No approvals, deposits, token calls, arbitrary execution, upgrade role, or admin withdrawal exists.</p></article>
      </section>
    </main>
  );
}

async function ensureBaseMainnet(provider: EthereumProvider) {
  const currentChain = await provider.request({ method: "eth_chainId" });
  if (currentChain === BASE_CHAIN_HEX) return;
  await provider.request({ method: "wallet_switchEthereumChain", params: [{ chainId: BASE_CHAIN_HEX }] });
  const switchedChain = await provider.request({ method: "eth_chainId" });
  if (switchedChain !== BASE_CHAIN_HEX) throw new Error("Wallet did not switch to Base mainnet.");
}

async function assertExactIntentAnchorRuntime(provider: EthereumProvider, address: string) {
  const rawCode = await provider.request({ method: "eth_getCode", params: [address, "latest"] });
  if (typeof rawCode !== "string" || !/^0x(?:[0-9a-fA-F]{2})+$/.test(rawCode)) {
    throw new Error("The configured mainnet address has no valid contract runtime.");
  }
  const codeHex = rawCode.slice(2);
  const code = Uint8Array.from(codeHex.match(/.{2}/g) ?? [], (byte) => Number.parseInt(byte, 16));
  const digest = await crypto.subtle.digest("SHA-256", code);
  const actual = `0x${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
  if (actual !== EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256) {
    throw new Error("The configured mainnet contract does not match the reviewed FlowOpsIntentAnchor runtime.");
  }
}

async function sha256Hex(value: string) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return `0x${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}

async function waitForReceipt(provider: EthereumProvider, hash: string) {
  for (let attempt = 0; attempt < 45; attempt += 1) {
    const receipt = await provider.request({ method: "eth_getTransactionReceipt", params: [hash] }) as Receipt | null;
    if (receipt) return receipt;
    await new Promise((resolve) => window.setTimeout(resolve, 2_000));
  }
  return null;
}

async function recoverPendingReceipt(provider: EthereumProvider, hash: string) {
  const chainId = await provider.request({ method: "eth_chainId" });
  if (chainId !== BASE_CHAIN_HEX) throw new Error("Switch to Base mainnet before recovering the prior receipt.");
  return await provider.request({ method: "eth_getTransactionReceipt", params: [hash] }) as Receipt | null;
}

function hexToDecimalString(value: string) {
  try {
    return BigInt(value).toString(10);
  } catch {
    return "Unknown";
  }
}

function errorMessage(error: unknown, fallback: string) {
  if (error && typeof error === "object" && "message" in error && typeof error.message === "string") return error.message;
  return fallback;
}
