import assert from "node:assert/strict";
import test from "node:test";
import {
  buildCanonicalIntent,
  decodeIntentRecord,
  encodeAnchorIntent,
  encodeGetIntent,
  EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256,
  isRecoverablePreparedIntent,
  parseBoundedGasEstimate,
  validateIntentForm,
} from "../app/mainnet/intent-codec.js";

const controller = "0x1111111111111111111111111111111111111111";
const recipient = "0x2222222222222222222222222222222222222222";
const digest = `0x${"a".repeat(64)}`;
const policy = `0x${"b".repeat(64)}`;
const valid = {
  taskId: "task-001",
  agentId: "research-agent",
  recipient,
  amountAtomic: "1000000",
  purpose: "Purchase one verified result",
  policyVersion: "pilot-v1",
  lifetimeDays: "1",
};

test("builds deterministic controller-bound canonical intent and policy payloads", () => {
  assert.equal(validateIntentForm(valid), "");
  const first = buildCanonicalIntent(valid, controller.toUpperCase().replace("0X", "0x"), 2_000_086_400);
  const second = buildCanonicalIntent({ ...valid }, controller, 2_000_086_400);
  assert.deepEqual(first, second);
  assert.match(first.canonicalPayload, /"schema":"flowops-mainnet-intent-v1"/);
  assert.match(first.canonicalPayload, /"chainId":8453/);
  assert.match(first.canonicalPayload, new RegExp(`"controller":"${controller}"`));
  assert.match(first.canonicalPayload, /"maxAmountAtomic":"1000000"/);
  assert.match(first.canonicalPolicy, /"schema":"flowops-mainnet-policy-v1"/);
  assert.doesNotMatch(first.canonicalPolicy, /"purpose"|"taskId"|"expiresAt"/);
});

test("rejects malformed, zero, oversized, and unsupported intent inputs", () => {
  assert.match(validateIntentForm({ ...valid, agentId: "spaces rejected" }), /Agent ID/);
  assert.match(validateIntentForm({ ...valid, recipient: `0x${"0".repeat(40)}` }), /non-zero/);
  assert.match(validateIntentForm({ ...valid, amountAtomic: "0" }), /positive/);
  assert.match(validateIntentForm({ ...valid, amountAtomic: "1".repeat(19) }), /positive/);
  assert.match(validateIntentForm({ ...valid, purpose: "" }), /Purpose/);
  assert.match(validateIntentForm({ ...valid, lifetimeDays: "31" }), /supported/);
});

test("encodes exact Solidity selectors and fixed-width arguments", () => {
  assert.equal(EXPECTED_INTENT_ANCHOR_RUNTIME_SHA256, "0x0f3cfa13e6e029ab7ce2c4d3afa478ab1a4034e8e4fc91e456df1127ca91bdc8");
  const encoded = encodeAnchorIntent(digest, policy, 2_000_086_400);
  assert.match(encoded, /^0xa12af16b[0-9a-f]{192}$/);
  assert.equal(encoded.slice(10, 74), "a".repeat(64));
  assert.equal(encoded.slice(74, 138), "b".repeat(64));
  assert.equal(BigInt(`0x${encoded.slice(138)}`), 2_000_086_400n);

  const read = encodeGetIntent(controller, digest);
  assert.match(read, /^0x563e691b[0-9a-f]{128}$/);
  assert.equal(read.slice(10, 74), controller.slice(2).padStart(64, "0"));
  assert.equal(read.slice(74), "a".repeat(64));
});

test("decodes only canonical four-word intent records", () => {
  const encoded = `0x${"b".repeat(64)}${2_000_000_000n.toString(16).padStart(64, "0")}${2_000_086_400n.toString(16).padStart(64, "0")}${"1".padStart(64, "0")}`;
  assert.deepEqual(decodeIntentRecord(encoded), {
    policyDigest: policy,
    anchoredAt: 2_000_000_000,
    expiresAt: 2_000_086_400,
    active: true,
    exists: true,
  });
  assert.throws(() => decodeIntentRecord("0x01"), /encoding/);
  assert.throws(() => decodeIntentRecord(`${encoded.slice(0, -64)}${"2".padStart(64, "0")}`), /active flag/);
});

test("bounds wallet gas estimates and rejects malformed estimates", () => {
  assert.equal(parseBoundedGasEstimate("0x01d4c0"), "0x1d4c0");
  assert.throws(() => parseBoundedGasEstimate("0x0"), /gas ceiling/);
  assert.throws(() => parseBoundedGasEstimate("0x01d4c1"), /gas ceiling/);
  assert.throws(() => parseBoundedGasEstimate("120000"), /invalid gas estimate/);
});

test("recovers only structurally complete persisted intent state", () => {
  const prepared = {
    controller,
    canonicalPayload: "{\"schema\":\"flowops-mainnet-intent-v1\"}",
    intentDigest: digest,
    policyDigest: policy,
    expiresAt: 2_000_086_400,
  };
  assert.equal(isRecoverablePreparedIntent(prepared), true);
  assert.equal(isRecoverablePreparedIntent({ ...prepared, controller: "0xbroken" }), false);
  assert.equal(isRecoverablePreparedIntent({ ...prepared, intentDigest: "0x01" }), false);
  assert.equal(isRecoverablePreparedIntent({ ...prepared, canonicalPayload: "" }), false);
  assert.equal(isRecoverablePreparedIntent({ ...prepared, expiresAt: Number.NaN }), false);
});
