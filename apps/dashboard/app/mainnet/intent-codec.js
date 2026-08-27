export const CANONICAL_BASE_USDC = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913";
export const EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256 = "0x0f3cfa13e6e029ab7ce2c4d3afa478ab1a4034e8e4fc91e456df1127ca91bdc8";
export const MAX_ANCHOR_GAS = 120_000n;

const ANCHOR_INTENT_SELECTOR = "a12af16b";
const GET_INTENT_SELECTOR = "563e691b";
const ADDRESS_PATTERN = /^0x[0-9a-fA-F]{40}$/;
const ATOMIC_PATTERN = /^\d{1,18}$/;

export function validateIntentForm(input) {
  if (!/^[a-zA-Z0-9._:-]{1,64}$/.test(input.agentId)) return "Agent ID must use 1–64 letters, numbers, dot, underscore, colon, or hyphen characters.";
  if (!/^[a-zA-Z0-9._:-]{1,64}$/.test(input.taskId)) return "Task ID must use 1–64 letters, numbers, dot, underscore, colon, or hyphen characters.";
  if (!ADDRESS_PATTERN.test(input.recipient) || /^0x0{40}$/i.test(input.recipient)) return "Enter a non-zero Base recipient address.";
  if (!ATOMIC_PATTERN.test(input.amountAtomic) || BigInt(input.amountAtomic) === 0n) return "Enter a positive whole-number USDC atomic-unit cap.";
  if (!input.purpose.trim() || input.purpose.trim().length > 280) return "Purpose must contain 1–280 characters.";
  if (!/^[a-zA-Z0-9._:-]{1,64}$/.test(input.policyVersion)) return "Policy version must use 1–64 safe identifier characters.";
  if (!["1", "7", "30"].includes(input.lifetimeDays)) return "Choose a supported intent lifetime.";
  return "";
}

export function buildCanonicalIntent(input, controller, expiresAt) {
  if (!ADDRESS_PATTERN.test(controller) || !Number.isSafeInteger(expiresAt) || expiresAt <= 0) {
    throw new Error("Invalid canonical intent binding.");
  }
  const normalizedRecipient = input.recipient.toLowerCase();
  const canonicalPayload = JSON.stringify({
    schema: "flowops-mainnet-intent-v1",
    chainId: 8453,
    controller: controller.toLowerCase(),
    taskId: input.taskId.trim(),
    agentId: input.agentId.trim(),
    recipient: normalizedRecipient,
    asset: CANONICAL_BASE_USDC.toLowerCase(),
    maxAmountAtomic: input.amountAtomic,
    purpose: input.purpose.trim(),
    policyVersion: input.policyVersion.trim(),
    expiresAt,
  });
  const canonicalPolicy = JSON.stringify({
    schema: "flowops-mainnet-policy-v1",
    chainId: 8453,
    controller: controller.toLowerCase(),
    agentId: input.agentId.trim(),
    recipient: normalizedRecipient,
    asset: CANONICAL_BASE_USDC.toLowerCase(),
    maxAmountAtomic: input.amountAtomic,
    policyVersion: input.policyVersion.trim(),
  });
  return { canonicalPayload, canonicalPolicy };
}

export function encodeAnchorIntent(intentDigest, policyDigest, expiresAt) {
  if (!Number.isSafeInteger(expiresAt) || expiresAt <= 0) throw new Error("Invalid expiry.");
  return `0x${ANCHOR_INTENT_SELECTOR}${word(intentDigest)}${word(policyDigest)}${word(`0x${BigInt(expiresAt).toString(16)}`)}`;
}

export function encodeGetIntent(controller, intentDigest) {
  return `0x${GET_INTENT_SELECTOR}${word(controller)}${word(intentDigest)}`;
}

export function decodeIntentRecord(raw) {
  const body = raw.startsWith("0x") ? raw.slice(2) : raw;
  if (!/^[0-9a-fA-F]{256}$/.test(body)) throw new Error("Invalid intent record encoding.");
  const policyDigest = `0x${body.slice(0, 64)}`;
  const anchoredAt = Number(BigInt(`0x${body.slice(64, 128)}`));
  const expiresAt = Number(BigInt(`0x${body.slice(128, 192)}`));
  const activeWord = BigInt(`0x${body.slice(192, 256)}`);
  if (activeWord !== 0n && activeWord !== 1n) throw new Error("Invalid intent active flag.");
  return { policyDigest, anchoredAt, expiresAt, active: activeWord === 1n, exists: anchoredAt !== 0 };
}

export function parseBoundedGasEstimate(value) {
  if (typeof value !== "string" || !/^0x[0-9a-fA-F]+$/.test(value)) {
    throw new Error("Base returned an invalid gas estimate.");
  }
  const estimate = BigInt(value);
  if (estimate <= 0n || estimate > MAX_ANCHOR_GAS) {
    throw new Error("The intent call exceeds the reviewed 120,000 gas ceiling.");
  }
  return `0x${estimate.toString(16)}`;
}

export function isRecoverablePreparedIntent(value) {
  if (!value || typeof value !== "object") return false;
  return ADDRESS_PATTERN.test(value.controller ?? "")
    && typeof value.canonicalPayload === "string"
    && value.canonicalPayload.length > 0
    && value.canonicalPayload.length <= 2_048
    && /^0x[0-9a-fA-F]{64}$/.test(value.intentDigest ?? "")
    && /^0x[0-9a-fA-F]{64}$/.test(value.policyDigest ?? "")
    && Number.isSafeInteger(value.expiresAt)
    && value.expiresAt > 0;
}

function word(value) {
  const raw = value.startsWith("0x") ? value.slice(2) : value;
  if (!/^[0-9a-fA-F]+$/.test(raw) || raw.length > 64) throw new Error("Invalid ABI value.");
  return raw.toLowerCase().padStart(64, "0");
}
