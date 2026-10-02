import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  artifactNamespace,
  candidateId,
  desktopPlatforms,
  npmPackages,
  sealCandidate,
  sealedNotesPath,
  verifyCandidate,
} from "./release-candidate.mjs";

function fixture(t) {
  const root = mkdtempSync(path.join(tmpdir(), "reasonix-candidate-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const platform of desktopPlatforms) {
    const dir = path.join(root, "desktop", platform, "files");
    mkdirSync(dir, { recursive: true });
    const artifact = `${platform}.zip`;
    writeFileSync(path.join(dir, artifact), platform);
    const sha256 = createHash("sha256").update(platform).digest("hex");
    writeFileSync(path.join(root, "desktop", platform, "identity.json"), JSON.stringify({
      schema: 1,
      platform,
      sourceSHA: "a".repeat(40),
      controlSHA: "b".repeat(40),
      tag: "desktop-v1.2.3",
      version: "v1.2.3",
      channel: "stable",
      signingFingerprint: "v1:fingerprint",
      prefix: "desktop-123-1-preflight",
      files: [{ name: artifact, size: Buffer.byteLength(platform), sha256 }],
    }));
  }
  const cli = path.join(root, "cli");
  mkdirSync(cli);
  for (const name of ["reasonix-darwin-amd64.tar.gz", "reasonix-darwin-arm64.tar.gz", "reasonix-linux-amd64.tar.gz", "reasonix-linux-arm64.tar.gz", "reasonix-windows-amd64.zip", "reasonix-windows-arm64.zip", "SHA256SUMS", "reasonix.rb"]) {
    writeFileSync(path.join(cli, name), name);
  }
  const npm = path.join(root, "npm");
  mkdirSync(npm);
  for (const name of npmPackages) writeFileSync(path.join(npm, `${name}-1.2.3.tgz`), name);
  const evidence = path.join(root, "evidence");
  mkdirSync(evidence);
  for (const kind of ["windows-amd64", "windows-arm64", "macos-universal-intel"]) {
    const platform = kind === "macos-universal-intel" ? "darwin-universal" : kind;
    writeFileSync(path.join(evidence, `${kind}.json`), JSON.stringify({
      schema: 1,
      kind,
      status: "passed",
      version: "v1.2.3",
      sha256: createHash("sha256").update(platform).digest("hex"),
    }));
  }
  writeFileSync(path.join(evidence, "release-notes.md"), sealedNotes);
  return root;
}

const sealedNotes = "notes by @alice\n";

const metadata = {
  version: "1.2.3", sourceSHA: "a".repeat(40), buildControlSHA: "b".repeat(40),
  acceptanceControlSHA: "c".repeat(40), notesSourceSHA: "a".repeat(40),
  catalogSha256: "d".repeat(64), renderedNotesSha256: createHash("sha256").update("notes by @alice\n").digest("hex"),
  repository: "esengine/DeepSeek-Reasonix", workflow: ".github/workflows/release-candidate.yml",
  runId: "123", runAttempt: "1", desktopPrefix: "desktop-123-1-preflight",
  payloadArtifactId: "456", payloadArtifactName: `release-candidate-payload-${candidateId("1.2.3", "a".repeat(40), "d".repeat(64))}`,
  evidenceArtifactId: "457", evidenceArtifactName: `release-candidate-evidence-${candidateId("1.2.3", "a".repeat(40), "d".repeat(64))}`,
  desktopFingerprint: "v1:fingerprint", createdAt: "2026-01-01T00:00:00.000Z",
  expiresAt: "2026-02-01T00:00:00.000Z",
  acceptance: ["windows-amd64", "windows-arm64", "macos-universal-intel"].map(kind => ({ kind, status: "passed", evidencePath: `evidence/${kind}.json` })),
};

test("seals and verifies a complete immutable candidate", t => {
  const root = fixture(t);
  const record = sealCandidate(root, metadata);
  assert.equal(record.candidateId, candidateId("1.2.3", "a".repeat(40), "d".repeat(64)));
  assert.doesNotThrow(() => verifyCandidate(root, record, new Date("2026-01-02T00:00:00Z")));
});

test("the rendered notes are sealed into the payload and bound to their hash", t => {
  const root = fixture(t);
  const record = sealCandidate(root, metadata);
  assert.equal(record.notes.renderedPath, sealedNotesPath);
  assert.equal(readFileSync(path.join(root, record.notes.renderedPath), "utf8"), sealedNotes);
  assert.throws(() => sealCandidate(root, { ...metadata, renderedNotesSha256: "e".repeat(64) }), /do not match renderedSha256/);
  writeFileSync(path.join(root, sealedNotesPath), "notes by @mallory\n");
  assert.throws(() => verifyCandidate(root, record, new Date("2026-01-02T00:00:00Z")), /digest mismatch/);
  rmSync(path.join(root, sealedNotesPath));
  assert.throws(() => sealCandidate(root, metadata), /missing sealed release notes/);
});

test("a candidate sealed before notes were sealed still verifies", t => {
  const root = fixture(t);
  const record = sealCandidate(root, metadata);
  rmSync(path.join(root, sealedNotesPath));
  const legacy = structuredClone(record);
  delete legacy.notes.renderedPath;
  legacy.files = legacy.files.filter(file => file.path !== sealedNotesPath);
  assert.doesNotThrow(() => verifyCandidate(root, legacy, new Date("2026-01-02T00:00:00Z")));
  const forged = { ...legacy, notes: { ...legacy.notes, renderedPath: "evidence/other.md" } };
  assert.throws(() => verifyCandidate(root, forged, new Date("2026-01-02T00:00:00Z")), /sealed release notes path/);
});

test("rehearsal bytes remain isolated from the publication identity", t => {
  const root = fixture(t);
  const id = candidateId(metadata.version, metadata.sourceSHA, metadata.catalogSha256);
  const record = sealCandidate(root, {
    ...metadata, purpose: "rehearsal",
    payloadArtifactName: `${artifactNamespace("rehearsal")}-payload-${id}`,
    evidenceArtifactName: `${artifactNamespace("rehearsal")}-evidence-${id}`,
  });
  assert.equal(record.purpose, "rehearsal");
  assert.doesNotThrow(() => verifyCandidate(root, record, new Date("2026-01-02T00:00:00Z"), "rehearsal"));
  assert.throws(() => verifyCandidate(root, record, new Date("2026-01-02T00:00:00Z")), /purpose mismatch/);
  assert.throws(() => sealCandidate(root, { ...metadata, purpose: "rehearsal" }), /payloadArtifactName/);
  assert.throws(() => sealCandidate(root, { ...metadata, purpose: "unknown" }), /purpose/);
});

for (const mutation of ["payload", "expired", "revoked", "wrong-source", "missing-acceptance"]) {
  test(`rejects ${mutation} candidate reuse`, t => {
    const root = fixture(t);
    const record = sealCandidate(root, metadata);
    if (mutation === "payload") writeFileSync(path.join(root, "cli", "SHA256SUMS"), "changed");
    if (mutation === "expired") record.validity.expiresAt = "2025-01-01T00:00:00.000Z";
    if (mutation === "revoked") record.validity.revoked = true;
    if (mutation === "wrong-source") record.source.repository = "fork/Reasonix";
    if (mutation === "missing-acceptance") record.acceptance.pop();
    assert.throws(() => verifyCandidate(root, record, new Date("2026-01-02T00:00:00Z")));
  });
}

test("rejects a candidate missing a platform before sealing", t => {
  const root = fixture(t);
  rmSync(path.join(root, "desktop", desktopPlatforms[0]), { recursive: true });
  assert.throws(() => sealCandidate(root, metadata), /missing Desktop bundle/);
});

test("rejects acceptance evidence that names bytes outside the sealed platform bundle", t => {
  const root = fixture(t);
  const receipt = path.join(root, "evidence", "windows-amd64.json");
  const value = JSON.parse(readFileSync(receipt, "utf8"));
  value.sha256 = "f".repeat(64);
  writeFileSync(receipt, JSON.stringify(value));
  assert.throws(() => sealCandidate(root, metadata), /does not name a sealed file/);
});

test("candidate id command accepts the catalog digest as its third argument", () => {
  const output = execFileSync(process.execPath, [
    fileURLToPath(new URL("./release-candidate.mjs", import.meta.url)),
    "id", "1.2.3", "a".repeat(40), "d".repeat(64),
  ], { encoding: "utf8" }).trim();
  assert.equal(output, candidateId("1.2.3", "a".repeat(40), "d".repeat(64)));
});
