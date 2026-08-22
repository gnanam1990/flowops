import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";
import { canonicalJSON, digest, domainSeparator, encodedData, keccak256, MANIFEST_SHA256, structHash, TYPES } from "../src/typedData.ts";

const root = resolve(import.meta.dirname, "../../..");
const fixtures = [
  ["ExecutionCommitment", "execution-commitment-v1.json", "commitment"],
  ["SellerQuote", "seller-quote-v1.json", "quote"],
  ["LockAuthorization", "lock-authorization-v1.json", "authorization"],
  ["AllowanceAuthorization", "allowance-authorization-v1.json", "authorization"],
  ["AdminActionAuthorization", "admin-action-authorization-v1.json", "authorization"],
  ["VerdictAttestation", "verdict-attestation-v1.json", "attestation"],
] as const;

test("Keccak-256 is Ethereum Keccak rather than NIST SHA3", () => {
  assert.equal(keccak256(new Uint8Array()), "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470");
});

test("the machine-readable registry pins every TypeScript type and artifact", () => {
  const registry = JSON.parse(readFileSync(resolve(root, "schemas", "ascp-typed-data-v4.registry.json"), "utf8"));
  assert.deepEqual(registry.domain, {
    name: "ASCP",
    version: "4",
    typeString: "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)",
  });
  assert.deepEqual(Object.keys(registry.types).sort(), Object.keys(TYPES).sort());
  for (const [name, type] of Object.entries(TYPES)) {
    assert.equal(registry.types[name].typeString, type.typeString);
  }
});

test("the TypeScript build pins the reviewed artifact manifest", () => {
  const manifest = readFileSync(resolve(root, "artifacts", "ascp-typed-data-v4.manifest.sha256"));
  assert.equal(createHash("sha256").update(manifest).digest("hex"), MANIFEST_SHA256);
});

for (const [typeName, fixtureName, messageProperty] of fixtures) {
  test(`${typeName} matches the shared v4 vector`, () => {
    const fixture = JSON.parse(readFileSync(resolve(root, "vectors", fixtureName), "utf8"));
    const type = TYPES[typeName];
    const message = fixture[messageProperty];
    assert.equal(type.typeString, fixture.typeString);
    assert.equal(domainSeparator(fixture.domain), fixture.domain.separator);
    assert.equal(encodedData(type, message), fixture.encodedData);
    assert.equal(structHash(type, message), fixture.structHash);
    assert.equal(digest(fixture.domain, type, message), fixture.digest);
    assert.equal(canonicalJSON(message), fixture.canonicalJSON);
    assert.match(fixture.signature, /^0x[0-9a-f]{130}$/);
    assert.match(fixture.recoveredSigner, /^0x[0-9a-f]{40}$/);

    const schema = JSON.parse(readFileSync(resolve(root, "schemas", fixture.schema), "utf8"));
    assert.equal(schema.additionalProperties, false);
    assert.deepEqual([...schema.required].sort(), type.fields.map((item) => item.name).sort());
  });
}

test("field omission, extra fields, overflow, and domain substitution fail closed", () => {
  const fixture = JSON.parse(readFileSync(resolve(root, "vectors", "lock-authorization-v1.json"), "utf8"));
  const message = { ...fixture.authorization };
  delete message.module;
  assert.throws(() => structHash(TYPES.LockAuthorization, message), /fields/);
  assert.throws(() => structHash(TYPES.LockAuthorization, { ...fixture.authorization, surprise: 1 }), /fields/);
  assert.throws(() => structHash(TYPES.LockAuthorization, { ...fixture.authorization, leadershipEpoch: "18446744073709551616" }), /overflow/);
  assert.throws(() => structHash(TYPES.LockAuthorization, { ...fixture.authorization, nonce: Number.MAX_SAFE_INTEGER + 1 }), /safe integer/);
  assert.throws(() => domainSeparator({ ...fixture.domain, version: "3" }), /domain/);
  assert.throws(() => domainSeparator({ ...fixture.domain, chainId: "0" }), /chain ID/);
});
