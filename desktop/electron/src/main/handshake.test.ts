import assert from "node:assert/strict";
import { test } from "node:test";
import { buildHelloParams, describeHandshakeFailure, HANDSHAKE_CODES, HandshakeError, validateHelloResult } from "./handshake.js";
import { RpcError } from "./rpc.js";

const goodResult = {
  protocolVersion: 11,
  contractDigest: "sha256:abc",
  service: { version: "v1.30.0", channel: "stable", commit: "abc123", pid: 4242 },
  runtimeGeneration: "g-01J",
  runId: "run-1",
  incidentId: "incident-1",
  diagnosticsEnabled: true,
  resources: { origin: "http://127.0.0.1:51234", token: "secret" },
  window: { width: 1280, height: 820, minWidth: 760, minHeight: 480, frameless: false, zoomFactor: 1 },
  instance: { identityVersion: 2, identityDigest: "sha256:def", legacyId: "com.reasonix.desktop.0123456789abcdef" },
};

test("hello params carry the documented shape", () => {
  const params = buildHelloParams({
    protocolVersion: 11,
    contractDigest: "sha256:abc",
    version: "v1.30.0",
    channel: "stable",
    commit: "abc123",
    hostVersion: "44.2.0",
    chromeVersion: "152.0.0",
    platform: "darwin",
    arch: "arm64",
    home: "/Users/x/.reasonix",
    dev: false,
  });
  assert.deepEqual(params, {
    protocolVersion: 11,
    contractDigest: "sha256:abc",
    build: { version: "v1.30.0", channel: "stable", commit: "abc123" },
    host: { name: "electron", version: "44.2.0", chrome: "152.0.0", platform: "darwin", arch: "arm64" },
    instance: { home: "/Users/x/.reasonix", dev: false },
  });
});

test("a valid hello result is accepted and normalised", () => {
  const result = validateHelloResult(goodResult);
  assert.equal(result.runtimeGeneration, "g-01J");
  assert.equal(result.window.zoomFactor, 1);
  assert.equal(result.instance?.identityVersion, 2);
  assert.equal(validateHelloResult({ ...goodResult, instance: undefined }).instance, undefined, "old services remain readable");
  const noZoom = validateHelloResult({ ...goodResult, window: { ...goodResult.window, zoomFactor: 0 } });
  assert.equal(noZoom.window.zoomFactor, 1, "a non-positive zoom factor falls back to 1");
});

test("invalid hello results are rejected with a precise message", () => {
  assert.throws(
    () => validateHelloResult({ ...goodResult, protocolVersion: 2 }),
    (error: unknown) => error instanceof HandshakeError && /protocolVersion 2/.test(error.message),
  );
  assert.equal(validateHelloResult({ ...goodResult, protocolVersion: 4 }, 4).protocolVersion, 4, "the embedded contract selects the expected protocol");
  assert.throws(() => validateHelloResult({ ...goodResult, resources: { origin: "" } }), /resources\.origin/);
  assert.throws(() => validateHelloResult({ ...goodResult, window: undefined }), /result\.window must be an object/);
  assert.throws(() => validateHelloResult({ ...goodResult, window: { ...goodResult.window, width: 0 } }), /positive/);
  assert.throws(() => validateHelloResult({ ...goodResult, runtimeGeneration: "" }), /runtimeGeneration/);
  assert.throws(() => validateHelloResult({ ...goodResult, instance: { ...goodResult.instance, identityVersion: 0 } }), /identityVersion/);
  assert.throws(() => validateHelloResult("nope"), /result must be an object/);
});

test("optional saved position survives handshake including zero and negative origins", () => {
  assert.equal(validateHelloResult(goodResult).window.position, undefined);
  for (const position of [
    { x: 0, y: 0 },
    { x: -1800, y: -900 },
  ]) {
    assert.deepEqual(validateHelloResult({ ...goodResult, window: { ...goodResult.window, position } }).window.position, position);
  }
  for (const position of [{ x: 1 }, { x: NaN, y: 0 }, { x: 0, y: Infinity }]) {
    assert.throws(() => validateHelloResult({ ...goodResult, window: { ...goodResult.window, position } }), /position/);
  }
});

test("handshake failures map every documented code and keep the real error text", () => {
  for (const [name, code] of Object.entries(HANDSHAKE_CODES)) {
    const failure = describeHandshakeFailure(new RpcError(code, `real text for ${name}`));
    assert.equal(failure.code, code);
    assert.equal(failure.name, name);
    assert.equal(failure.detail, `real text for ${name}`);
    assert.notEqual(failure.title, "");
  }
  const unknownCode = describeHandshakeFailure(new RpcError(-32000, "boom"));
  assert.equal(unknownCode.name, "rpc_error");
  const invalid = describeHandshakeFailure(new HandshakeError("bad window"));
  assert.equal(invalid.name, "invalid_result");
  assert.equal(invalid.detail, "bad window");
  const spawn = describeHandshakeFailure(new Error("spawn ENOENT"));
  assert.equal(spawn.name, "service_failure");
  assert.equal(spawn.code, null);
  assert.equal(spawn.detail, "spawn ENOENT");
});

test("a service without lifecycle diagnostics reports empty run and incident ids", () => {
  const result = validateHelloResult({ ...goodResult, runId: "", incidentId: "", diagnosticsEnabled: false });
  assert.equal(result.runId, "");
  assert.equal(result.incidentId, "");
  assert.equal(result.diagnosticsEnabled, false);
  assert.throws(() => validateHelloResult({ ...goodResult, runId: undefined }), /result\.runId/);
});
