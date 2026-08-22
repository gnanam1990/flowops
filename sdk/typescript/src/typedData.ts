export type Field = Readonly<{ name: string; type: string }>;
export type TypedMessage = Readonly<{
  typeString: string;
  fields: readonly Field[];
}>;
export type Domain = Readonly<{
  name: "ASCP";
  version: "4";
  chainId: string;
  verifyingContract: string;
}>;

const field = (name: string, type: string): Field => ({ name, type });

export const DOMAIN_TYPE_STRING =
  "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)";
export const MANIFEST_SHA256 = "87eee19267c1684f91e10454a8f1a26880a2434e65f5609791c54b803154bff5";

export const TYPES = {
  ExecutionCommitment: define("ExecutionCommitment", [
    field("orgDomain", "bytes32"), field("operationId", "bytes32"), field("rail", "uint8"),
    field("schemeVersion", "uint16"), field("protection", "uint8"), field("escrowContract", "address"),
    field("purchaseSpecHash", "bytes32"), field("quoteHash", "bytes32"), field("verificationSpecHash", "bytes32"),
    field("declaredWorkTime", "uint64"), field("verificationBudgetSeconds", "uint64"), field("directoryVersion", "uint64"),
    field("sellerId", "bytes32"), field("resourceId", "bytes32"), field("payTo", "address"),
    field("ackAuthority", "address"), field("amount", "uint256"), field("chainId", "uint256"),
    field("asset", "address"), field("quoteExpiresAt", "uint64"), field("acceptBy", "uint64"),
    field("deliverBy", "uint64"), field("settleBy", "uint64"),
  ]),
  SellerQuote: define("SellerQuote", [
    field("purchaseSpecHash", "bytes32"), field("sellerId", "bytes32"), field("resourceId", "bytes32"),
    field("directoryVersion", "uint64"), field("schemeVersion", "uint16"), field("chainId", "uint256"),
    field("asset", "address"), field("amountBaseUnits", "uint256"), field("payTo", "address"),
    field("ackAuthority", "address"), field("verificationSpecHash", "bytes32"), field("declaredWorkTime", "uint64"),
    field("verificationBudgetSeconds", "uint64"), field("quoteExpiresAt", "uint64"), field("quoteNonce", "bytes32"),
  ]),
  LockAuthorization: define("LockAuthorization", [
    field("orgDomain", "bytes32"), field("safe", "address"), field("module", "address"),
    field("operationId", "bytes32"), field("commitmentHash", "bytes32"), field("calldataHash", "bytes32"),
    field("escrow", "address"), field("amount", "uint256"), field("nonce", "uint256"),
    field("validAfter", "uint64"), field("validBefore", "uint64"), field("leadershipEpoch", "uint64"),
    field("authorizerEpoch", "uint64"),
  ]),
  AllowanceAuthorization: define("AllowanceAuthorization", [
    field("orgDomain", "bytes32"), field("safe", "address"), field("module", "address"),
    field("adminOperationId", "bytes32"), field("token", "address"), field("spender", "address"),
    field("expectedAllowance", "uint256"), field("newAllowance", "uint256"), field("nonce", "uint256"),
    field("validAfter", "uint64"), field("validBefore", "uint64"), field("leadershipEpoch", "uint64"),
    field("authorizerEpoch", "uint64"),
  ]),
  AdminActionAuthorization: define("AdminActionAuthorization", [
    field("orgDomain", "bytes32"), field("contractAddress", "address"), field("chainId", "uint256"),
    field("authorityRole", "bytes32"), field("functionSelector", "bytes4"), field("payloadHash", "bytes32"),
    field("adminOperationId", "bytes32"), field("adminNonce", "uint256"), field("adminEpoch", "uint64"),
    field("validAfter", "uint64"), field("validBefore", "uint64"), field("workflowId", "bytes32"),
  ]),
  VerdictAttestation: define("VerdictAttestation", [
    field("callId", "bytes32"), field("commitmentHash", "bytes32"), field("escrowContract", "address"),
    field("verifierEpoch", "uint64"), field("verificationSpecHash", "bytes32"), field("verifierSoftwareHash", "bytes32"),
    field("deliveryHash", "bytes32"), field("deliveredAt", "uint64"), field("evidenceHash", "bytes32"),
    field("verdict", "uint8"), field("verdictNonce", "uint256"), field("issuedAt", "uint64"),
    field("validUntil", "uint64"),
  ]),
} as const;

function define(name: string, fields: readonly Field[]): TypedMessage {
  return { typeString: `${name}(${fields.map((item) => `${item.type} ${item.name}`).join(",")})`, fields };
}

const MASK = (1n << 64n) - 1n;
const ROTATIONS = [
  0, 1, 62, 28, 27, 36, 44, 6, 55, 20, 3, 10, 43, 25, 39, 41, 45, 15, 21, 8, 18, 2, 61, 56, 14,
] as const;
const ROUND_CONSTANTS = [
  0x0000000000000001n, 0x0000000000008082n, 0x800000000000808an, 0x8000000080008000n,
  0x000000000000808bn, 0x0000000080000001n, 0x8000000080008081n, 0x8000000000008009n,
  0x000000000000008an, 0x0000000000000088n, 0x0000000080008009n, 0x000000008000000an,
  0x000000008000808bn, 0x800000000000008bn, 0x8000000000008089n, 0x8000000000008003n,
  0x8000000000008002n, 0x8000000000000080n, 0x000000000000800an, 0x800000008000000an,
  0x8000000080008081n, 0x8000000000008080n, 0x0000000080000001n, 0x8000000080008008n,
] as const;

export function keccak256(input: Uint8Array | string): string {
  const bytes = typeof input === "string" ? new TextEncoder().encode(input) : input;
  const state = Array<bigint>(25).fill(0n);
  const rate = 136;
  let offset = 0;
  while (offset + rate <= bytes.length) {
    absorb(state, bytes.subarray(offset, offset + rate));
    keccakF(state);
    offset += rate;
  }
  const finalBlock = new Uint8Array(rate);
  finalBlock.set(bytes.subarray(offset));
  finalBlock[bytes.length - offset] = 0x01;
  finalBlock[rate - 1] |= 0x80;
  absorb(state, finalBlock);
  keccakF(state);
  const output = new Uint8Array(32);
  for (let index = 0; index < output.length; index += 1) {
    output[index] = Number((state[Math.floor(index / 8)] >> BigInt((index % 8) * 8)) & 0xffn);
  }
  return bytesToHex(output);
}

function absorb(state: bigint[], block: Uint8Array): void {
  for (let lane = 0; lane < block.length / 8; lane += 1) {
    let value = 0n;
    for (let byte = 0; byte < 8; byte += 1) value |= BigInt(block[lane * 8 + byte]) << BigInt(byte * 8);
    state[lane] ^= value;
  }
}

function rotate(value: bigint, count: number): bigint {
  if (count === 0) return value & MASK;
  const shift = BigInt(count);
  return ((value << shift) | (value >> (64n - shift))) & MASK;
}

function keccakF(state: bigint[]): void {
  for (const roundConstant of ROUND_CONSTANTS) {
    const columns = Array<bigint>(5);
    const delta = Array<bigint>(5);
    for (let x = 0; x < 5; x += 1) {
      columns[x] = state[x] ^ state[x + 5] ^ state[x + 10] ^ state[x + 15] ^ state[x + 20];
    }
    for (let x = 0; x < 5; x += 1) delta[x] = columns[(x + 4) % 5] ^ rotate(columns[(x + 1) % 5], 1);
    for (let x = 0; x < 5; x += 1) for (let y = 0; y < 5; y += 1) state[x + 5 * y] ^= delta[x];
    const mixed = Array<bigint>(25).fill(0n);
    for (let x = 0; x < 5; x += 1) {
      for (let y = 0; y < 5; y += 1) {
        mixed[y + 5 * ((2 * x + 3 * y) % 5)] = rotate(state[x + 5 * y], ROTATIONS[x + 5 * y]);
      }
    }
    for (let x = 0; x < 5; x += 1) {
      for (let y = 0; y < 5; y += 1) {
        state[x + 5 * y] = mixed[x + 5 * y] ^ ((~mixed[(x + 1) % 5 + 5 * y] & MASK) & mixed[(x + 2) % 5 + 5 * y]);
      }
    }
    state[0] ^= roundConstant;
  }
}

export function encodedData(type: TypedMessage, message: Readonly<Record<string, unknown>>): string {
  requireExactFields(type, message);
  return bytesToHex(concat(hexToBytes(keccak256(type.typeString)), ...type.fields.map((item) => encodeWord(item.type, message[item.name]))));
}

export function structHash(type: TypedMessage, message: Readonly<Record<string, unknown>>): string {
  return keccak256(hexToBytes(encodedData(type, message)));
}

export function domainSeparator(domain: Domain): string {
  if (domain.name !== "ASCP" || domain.version !== "4") throw new Error("invalid ASCP EIP-712 domain");
  if (domain.chainId === "0") throw new Error("invalid ASCP chain ID");
  const words = concat(
    hexToBytes(keccak256(DOMAIN_TYPE_STRING)),
    hexToBytes(keccak256(domain.name)),
    hexToBytes(keccak256(domain.version)),
    encodeWord("uint256", domain.chainId),
    encodeWord("address", domain.verifyingContract),
  );
  return keccak256(words);
}

export function digest(domain: Domain, type: TypedMessage, message: Readonly<Record<string, unknown>>): string {
  return keccak256(concat(new Uint8Array([0x19, 0x01]), hexToBytes(domainSeparator(domain)), hexToBytes(structHash(type, message))));
}

export function canonicalJSON(value: unknown): string {
  if (value === null || typeof value === "boolean" || typeof value === "string") return JSON.stringify(value);
  if (typeof value === "number") {
    if (!Number.isSafeInteger(value)) throw new Error("canonical JSON only accepts safe integers");
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(record[key])}`).join(",")}}`;
  }
  throw new Error("unsupported canonical JSON value");
}

function requireExactFields(type: TypedMessage, message: Readonly<Record<string, unknown>>): void {
  const expected = type.fields.map((item) => item.name).sort();
  const actual = Object.keys(message).sort();
  if (expected.length !== actual.length || expected.some((name, index) => name !== actual[index])) {
    throw new Error("typed message fields do not match the registry");
  }
}

function encodeWord(type: string, value: unknown): Uint8Array {
  if (type === "address") {
    if (typeof value !== "string" || !/^0x[0-9a-f]{40}$/.test(value) || /^0x0{40}$/.test(value)) throw new Error("invalid address");
    return leftPad(hexToBytes(value), 32);
  }
  if (type === "bytes32" || type === "bytes4") {
    const size = Number(type.slice(5));
    if (typeof value !== "string" || !new RegExp(`^0x[0-9a-f]{${size * 2}}$`).test(value)) throw new Error(`invalid ${type}`);
    return rightPad(hexToBytes(value), 32);
  }
  const match = /^uint(8|16|64|256)$/.exec(type);
  if (match === null || (typeof value !== "string" && typeof value !== "number")) throw new Error(`unsupported ${type}`);
  if (typeof value === "number" && !Number.isSafeInteger(value)) throw new Error(`${type} requires a safe integer or decimal string`);
  const raw = typeof value === "number" ? String(value) : value;
  if (!/^(0|[1-9][0-9]*)$/.test(raw)) throw new Error(`invalid ${type}`);
  const integer = BigInt(raw);
  if (integer >= (1n << BigInt(Number(match[1])))) throw new Error(`${type} overflow`);
  const result = new Uint8Array(32);
  let remaining = integer;
  for (let index = 31; index >= 0; index -= 1) {
    result[index] = Number(remaining & 0xffn);
    remaining >>= 8n;
  }
  return result;
}

function concat(...parts: Uint8Array[]): Uint8Array {
  const result = new Uint8Array(parts.reduce((total, part) => total + part.length, 0));
  let offset = 0;
  for (const part of parts) { result.set(part, offset); offset += part.length; }
  return result;
}

function leftPad(value: Uint8Array, length: number): Uint8Array {
  const result = new Uint8Array(length); result.set(value, length - value.length); return result;
}
function rightPad(value: Uint8Array, length: number): Uint8Array {
  const result = new Uint8Array(length); result.set(value); return result;
}
function hexToBytes(value: string): Uint8Array {
  if (!/^0x(?:[0-9a-f]{2})*$/.test(value)) throw new Error("invalid lowercase hex");
  const result = new Uint8Array((value.length - 2) / 2);
  for (let index = 0; index < result.length; index += 1) result[index] = Number.parseInt(value.slice(2 + index * 2, 4 + index * 2), 16);
  return result;
}
function bytesToHex(value: Uint8Array): string {
  return `0x${Array.from(value, (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}
