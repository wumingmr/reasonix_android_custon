import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { verifyArchive } from "./verify-release-artifact-archive.mjs";

test("verifies the exact downloaded archive against GitHub artifact metadata", async t => {
  const dir = mkdtempSync(path.join(tmpdir(), "reasonix-artifact-digest-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const archive = path.join(dir, "payload.zip");
  writeFileSync(archive, "archive bytes");
  const artifact = { id: 42, expired: false, digest: `sha256:${createHash("sha256").update("archive bytes").digest("hex")}` };
  assert.equal(await verifyArchive(artifact, archive), artifact.digest);
  writeFileSync(archive, "substituted");
  await assert.rejects(verifyArchive(artifact, archive), /digest mismatch/);
  await assert.rejects(verifyArchive({ ...artifact, digest: undefined }, archive), /trustworthy/);
  await assert.rejects(verifyArchive({ ...artifact, expired: true }, archive), /trustworthy/);
});
