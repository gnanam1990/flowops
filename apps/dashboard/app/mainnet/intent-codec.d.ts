export type IntentFormInput = {
  taskId: string;
  agentId: string;
  recipient: string;
  amountAtomic: string;
  purpose: string;
  policyVersion: string;
  lifetimeDays: string;
};

export const CANONICAL_BASE_USDC: string;
export const EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256: string;
export const MAX_ANCHOR_GAS: bigint;
export function validateIntentForm(input: IntentFormInput): string;
export function buildCanonicalIntent(input: IntentFormInput, controller: string, expiresAt: number): {
  canonicalPayload: string;
  canonicalPolicy: string;
};
export function encodeAnchorIntent(intentDigest: string, policyDigest: string, expiresAt: number): string;
export function encodeGetIntent(controller: string, intentDigest: string): string;
export function decodeIntentRecord(raw: string): {
  policyDigest: string;
  anchoredAt: number;
  expiresAt: number;
  active: boolean;
  exists: boolean;
};
export function parseBoundedGasEstimate(value: unknown): string;
export function isRecoverablePreparedIntent(value: unknown): value is {
  controller: string;
  canonicalPayload: string;
  intentDigest: string;
  policyDigest: string;
  expiresAt: number;
};
