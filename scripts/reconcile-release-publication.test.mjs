import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, mkdirSync, copyFileSync, writeFileSync, readFileSync, rmSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { createCoreLedger, createPointerLedger, mergeLedgers, npmPackageNames } from "./release-publication-ledger.mjs";

const sha = "a".repeat(40);
function fixture(t) {
  const root = realpathSync(mkdtempSync(path.join(tmpdir(), "release-reconcile-")));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const scripts = path.join(root, "scripts"), bin = path.join(root, "bin");
  mkdirSync(scripts); mkdirSync(bin);
  for (const file of ["reconcile-release-publication.sh", "fetch-stable-release-manifest.sh", "check-release-public-access.sh", "release-publication-ledger.mjs"]) copyFileSync(`scripts/${file}`, path.join(scripts, file));
  const release = { isDraft: false, isPrerelease: false, assets: [] };
  const core = createCoreLedger({ version: "1.2.3", sourceSHA: sha, operation: "recover", cliRelease: release, desktopRelease: release, npmPackages: npmPackageNames.map(name => ({ name, version: "1.2.3", latest: "1.2.3", gitHead: sha, integrity: "sha512-example" })) });
  writeFileSync(path.join(root, "core.json"), JSON.stringify(core));
  writeFileSync(path.join(root, "full.json"), JSON.stringify(mergeLedgers(core, createPointerLedger({ version: "1.2.3", sourceSHA: sha, operation: "recover", manifest: { version: "v1.2.3" }, homebrewVersion: "1.2.3" }))));
  writeFileSync(path.join(scripts, "verify-stable-release-artifacts.sh"), `#!/usr/bin/env bash
set -euo pipefail
test "$VERIFY_PUBLIC_POINTERS" = true
test "\${VERIFY_FAIL:-}" != true
if [ "\${NEWER_POINTER:-}" = true ]; then cp "$FIXTURE/core.json" "$RELEASE_LEDGER_OUTPUT"; else cp "$FIXTURE/full.json" "$RELEASE_LEDGER_OUTPUT"; fi
`);
  writeFileSync(path.join(scripts, "release-event.mjs"), `import {writeFileSync} from 'node:fs'; writeFileSync(process.argv[process.argv.indexOf('--output')+1], '{}');`);
  writeFileSync(path.join(bin, "go"), `#!/usr/bin/env node
const args=process.argv.slice(2);
if (args[0]!=='run' || !args[1].endsWith('/release-manifest-fetch/main.go') || args[2]!=='1.2.3') process.exit(99);
if(process.env.HTTP_FAIL==='true') process.exit(22);
const body=process.env.MANIFEST_BODY || JSON.stringify({version:process.env.POINTER || 'v1.2.3'});
try { if(!/^v[0-9]+\\.[0-9]+\\.[0-9]+$/.test(JSON.parse(body).version)) process.exit(1); }
catch { process.exit(1); }
require('node:fs').writeFileSync(args[3],body);
`, { mode: 0o755 });
  writeFileSync(path.join(bin, "gh"), `#!/usr/bin/env node
const fs=require('node:fs'), path=require('node:path');
const args=process.argv.slice(2), root=process.env.FIXTURE;
fs.appendFileSync(path.join(root,'calls'),JSON.stringify(args)+'\\n');
if(args[0]==='api') console.log('2026-01-01T00:00:00Z');
else if(args[0]==='release' && args[1]==='view') console.log(process.env.MISSING_EVENT==='true'?'0':'1');
else if(args[0]==='release' && args[1]==='download') {
  if(process.env.EVENT_DOWNLOAD_FAIL==='true') process.exit(1);
  fs.writeFileSync(args[args.indexOf('--output')+1],process.env.EVENT_CONFLICT==='true'?'conflict':'{}');
} else if(args[0]==='release' && args[1]==='upload') {
  if(path.basename(args.at(-1))!=='release-event.json') process.exit(3);
} else process.exit(4);
`, { mode: 0o755 });
  const env = { ...process.env, FIXTURE: root, PATH: `${bin}:${process.env.PATH}`, GITHUB_ACTIONS: "true", GITHUB_REPOSITORY: "esengine/DeepSeek-Reasonix", GITHUB_REF: "refs/heads/main-v2", GITHUB_REF_PROTECTED: "true", GITHUB_RUN_ID: "123", GITHUB_RUN_ATTEMPT: "2", RELEASE_REPOSITORY: "esengine/DeepSeek-Reasonix", RELEASE_VERSION: "1.2.3", RELEASE_OPERATION: "recover", RELEASE_EXPECTED_SHA: sha, RELEASE_LEDGER_OUTPUT: path.join(root, "ledger.json") };
  return { root, scripts, env,
    run: extra => spawnSync("bash", [path.join(scripts, "reconcile-release-publication.sh")], { env: { ...env, ...extra }, encoding: "utf8" }),
    ledger: () => JSON.parse(readFileSync(env.RELEASE_LEDGER_OUTPUT, "utf8")),
    calls: () => { try { return readFileSync(path.join(root, "calls"), "utf8"); } catch { return ""; } },
  };
}

test("publication completes once every product surface is verified", t => {
  const f = fixture(t), result = f.run({});
  assert.equal(result.status, 0, result.stderr);
  const ledger = f.ledger();
  assert.equal(ledger.completionState, "complete");
  assert.deepEqual(Object.keys(ledger.surfaces).sort(), ["cli", "desktop", "homebrew", "npm", "stableManifest", "tags"]);
  assert.equal(ledger.verificationContext.stage, "complete");
  assert.doesNotMatch(f.calls(), /"(?:upload|publish|push)"/);
});

test("newer pointer is preserved and still counts as recovered", t => {
  const f = fixture(t), result = f.run({ NEWER_POINTER: "true" });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(f.ledger().completionState, "immutable-complete-newer-pointer-preserved");
});

for (const [extra, stage] of [
  [{ VERIFY_FAIL: "true" }, "immutable"],
  [{ EVENT_DOWNLOAD_FAIL: "true" }, "release-event"],
  [{ EVENT_CONFLICT: "true" }, "release-event"],
]) test(`failure evidence survives ${JSON.stringify(extra)}`, t => {
  const f = fixture(t), result = f.run(extra);
  assert.notEqual(result.status, 0);
  const ledger = f.ledger();
  assert.equal(ledger.verificationContext.stage, stage);
  assert.equal(ledger.completionState, stage === "immutable" ? "verification-failed" : "release-event-pending");
  assert.doesNotMatch(f.calls(), /upload/);
});

test("a missing event is created with the immutable asset filename", t => {
  const f = fixture(t), result = f.run({ MISSING_EVENT: "true" });
  assert.equal(result.status, 0, result.stderr);
  assert.match(f.calls(), /release-event\.json/);
  assert.match(f.calls(), /upload/);
});

test("public access preflight accepts the previous stable version but rejects challenges and invalid JSON", t => {
  const f = fixture(t);
  for (const [extra, success] of [[{ POINTER: "v1.0.0" }, true], [{ HTTP_FAIL: "true" }, false], [{ MANIFEST_BODY: "{}" }, false], [{ MANIFEST_BODY: "html" }, false]]) {
    const result = spawnSync("bash", [path.join(f.scripts, "check-release-public-access.sh"), "1.2.3"], { env: { ...f.env, ...extra }, encoding: "utf8" });
    assert.equal(result.status === 0, success, result.stderr);
  }
});

test("real public verifier rejects tag drift before inspecting any packages", t => {
  const f = fixture(t);
  writeFileSync(path.join(f.root, "bin/git"), `#!/usr/bin/env node\nconsole.log('${"b".repeat(40)}\\trefs/tags/v1.2.3');`, { mode: 0o755 });
  const result = spawnSync("bash", ["scripts/verify-stable-release-artifacts.sh"], { env: { ...f.env, CLI_TAG: "v1.2.3", DESKTOP_TAG: "desktop-v1.2.3" }, encoding: "utf8" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /differs from the verified source SHA/);
  assert.equal(f.calls(), "");
});

test("the 1.x release control plane neither deploys nor verifies the website", () => {
  const promote = readFileSync(".github/workflows/release-promote.yml", "utf8");
  assert.match(readFileSync(".github/workflows/release-stable.yml", "utf8"), /group: stable-release-publication/);
  assert.match(promote, /group: stable-release-publication/);
  assert.match(promote, /bash scripts\/reconcile-release-publication.sh/);
  assert.match(promote, /retention-days: 90/);
  for (const file of [
    ".github/workflows/release-promote.yml", ".github/workflows/release-stable.yml",
    ".github/workflows/release-desktop.yml", ".github/workflows/release-verify.yml",
    "scripts/reconcile-release-publication.sh", "scripts/verify-stable-release-artifacts.sh",
  ]) {
    const text = readFileSync(file, "utf8");
    assert.doesNotMatch(text, /pages\.yml|--ref website|reasonix\.io\/changelog\/v\$|--dump-dom/, file);
  }
});
