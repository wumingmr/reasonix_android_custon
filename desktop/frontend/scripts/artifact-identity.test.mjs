import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { buildInputIdentity, createFrontendArtifact, frontendProducerAttempt, verifyFrontendArtifact } from "./artifact-identity.mjs";

function fixture(t) {
  const root = mkdtempSync(path.join(os.tmpdir(), "reasonix-frontend-artifact-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const dir of ["desktop/frontend/src", "desktop/frontend/dist"]) mkdirSync(path.join(root, dir), { recursive: true });
  for (const [name, value] of [["desktop/package.json", "{}"], ["desktop/pnpm-lock.yaml", "lock"],
    ["desktop/frontend/package.json", "{}"], ["desktop/frontend/src/App.tsx", "export {}"], ["desktop/frontend/dist/index.html", "ok"]])
    writeFileSync(path.join(root, name), value);
  execFileSync("git", ["init"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture"], { cwd: root });
  const sourceSHA = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  const options = { root, dist: path.join(root, "desktop/frontend/dist"), manifest: path.join(root, "manifest.json"),
    shell: "electron", channel: "stable", sourceSHA, runId: "12", attempt: "3", pnpmVersion: "10.0.0" };
  createFrontendArtifact(options);
  return options;
}

test("matching artifact verifies across producer platforms", t => {
  const options = fixture(t);
  const body = verifyFrontendArtifact(options);
  body.toolchain.platform = body.toolchain.platform === "linux" ? "darwin" : "linux";
  body.toolchain.arch = "other";
  writeFileSync(options.manifest, JSON.stringify(body));
  assert.equal(verifyFrontendArtifact(options).sourceSHA, options.sourceSHA);
  writeFileSync(path.join(options.root, "desktop/frontend/src/App.tsx"), "export {}\r\n");
  assert.equal(verifyFrontendArtifact(options).sourceSHA, options.sourceSHA);
});

test("batched blob reads preserve the version-one input digest", t => {
  const options = fixture(t);
  const identity = buildInputIdentity(options.root);
  const hash = createHash("sha256");
  for (const name of identity.files) {
    hash.update(name);
    hash.update("\0");
    hash.update(execFileSync("git", ["-C", options.root, "show", `HEAD:${name}`]));
    hash.update("\0");
  }
  assert.equal(identity.sha256, hash.digest("hex"));
});

test("variant, workflow and toolchain identity mismatches fail", t => {
  const options = fixture(t);
  for (const changed of [{ shell: "browser" }, { channel: "canary" }, { runId: "13" }, { attempt: "4" }, { pnpmVersion: "10.1.0" }])
    assert.throws(() => verifyFrontendArtifact({ ...options, ...changed }), /mismatch/);
  const body = verifyFrontendArtifact(options);
  body.toolchain.node = process.version + "-different";
  writeFileSync(options.manifest, JSON.stringify(body));
  assert.throws(() => verifyFrontendArtifact(options), /node mismatch/);
});

test("consumer retries verify the producer attempt while rebuilt producers advance it", t => {
  const options = fixture(t);
  const firstProducer = { ...options, attempt: "1" };
  createFrontendArtifact(firstProducer);
  assert.equal(verifyFrontendArtifact(firstProducer).workflow.attempt, "1");
  // A consumer may be on run attempt 2 while its successful producer remains
  // on attempt 1. Verification must use the producer output, not consumer state.
  const consumerAttempt = "2";
  const env = { GITHUB_RUN_ATTEMPT: consumerAttempt, REASONIX_FRONTEND_PRODUCER_ATTEMPT: "1" };
  assert.equal(verifyFrontendArtifact({ ...options, attempt: frontendProducerAttempt(env) }).workflow.attempt, "1");
  assert.throws(() => verifyFrontendArtifact({ ...options, attempt: frontendProducerAttempt({ GITHUB_RUN_ATTEMPT: consumerAttempt }) }), /attempt mismatch/);
  assert.equal(frontendProducerAttempt({}), undefined);
  assert.notEqual(consumerAttempt, firstProducer.attempt);
  assert.equal(verifyFrontendArtifact(firstProducer).workflow.attempt, "1");
  const rebuiltProducer = { ...options, attempt: "2" };
  createFrontendArtifact(rebuiltProducer);
  assert.equal(verifyFrontendArtifact(rebuiltProducer).workflow.attempt, "2");
  assert.throws(() => verifyFrontendArtifact(firstProducer), /attempt mismatch/);
});

test("changed build input, dist, missing and old manifests fail", t => {
  const options = fixture(t);
  const body = verifyFrontendArtifact(options);
  body.inputs.sha256 = "0".repeat(64);
  writeFileSync(options.manifest, JSON.stringify(body));
  assert.throws(() => verifyFrontendArtifact(options), /build inputs/);
  createFrontendArtifact(options);
  writeFileSync(path.join(options.dist, "index.html"), "changed");
  assert.throws(() => verifyFrontendArtifact(options), /contents/);
  writeFileSync(options.manifest, JSON.stringify({ schemaVersion: 0 }));
  assert.throws(() => verifyFrontendArtifact(options), /schemaVersion mismatch/);
  assert.throws(() => verifyFrontendArtifact({ ...options, manifest: path.join(options.root, "missing.json") }), /unavailable/);
});
