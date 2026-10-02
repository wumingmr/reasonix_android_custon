import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rename, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { spawnSync } from "node:child_process";
import { canReuseRequest, findReceipt, identity, identityDigest, inputDigest, receiptName, requireRecoverable, validateReceipt } from "./signpath-checkpoint.mjs";

const env = {
  GITHUB_REPOSITORY: "example/project", GITHUB_RUN_ID: "123", GITHUB_RUN_ATTEMPT: "1",
  SIGNPATH_SOURCE_SHA: "a".repeat(40), SIGNPATH_CONTROL_SHA: "b".repeat(40),
  SIGNPATH_FINGERPRINT: `v1:${"c".repeat(64)}`, SIGNPATH_VERSION: "1.2.3", SIGNPATH_CHANNEL: "stable",
  SIGNPATH_MODE: "preflight", SIGNPATH_PLATFORM: "windows-amd64",
  SIGNPATH_ORGANIZATION_ID: "11111111-1111-1111-1111-111111111111",
  SIGNPATH_PROJECT: "project", SIGNPATH_POLICY: "release-signing", SIGNPATH_CONFIGURATION: "windows-payload",
};
const expected = identity(env);
const requestId = "22222222-2222-2222-2222-222222222222";
const receipt = { schema: 1, identityDigest: identityDigest(expected), digest: "digest", attempt: "1", requestId };

test("an interrupted submission without a saved request cannot be blindly repeated", () => {
  assert.doesNotThrow(() => requireRecoverable(false, "1"));
  assert.doesNotThrow(() => requireRecoverable(true, "2"));
  assert.throws(() => requireRecoverable(false, "2"), /check SignPath request history/);
  assert.throws(() => requireRecoverable(true, ""), /invalid or excessive workflow attempt/);
});

const payloadStep = "Submit Windows payload for Authenticode signing";
const installerStep = "Submit installer for Authenticode signing";
const completedStep = (name, conclusion) => ({ name, status: "completed", conclusion });
const previousJob = steps => ({ name: "verify stable SignPath control plane / build (windows-amd64, preflight)",
  status: "completed", conclusion: "failure", steps });
function historyAPI(receipts, attempts, calls = []) {
  return async url => {
    calls.push(url);
    const parsed = new URL(url);
    if (parsed.pathname.endsWith("/artifacts")) {
      const artifacts = receipts.filter(item => item.name === parsed.searchParams.get("name"));
      return { ok: true, json: async () => ({ artifacts, total_count: artifacts.length }) };
    }
    const attempt = /\/attempts\/(\d+)\/jobs$/.exec(parsed.pathname)?.[1];
    assert.ok(attempt, url);
    const jobs = attempts[attempt];
    if (!jobs) return { ok: false, status: 404 };
    const page = Number(parsed.searchParams.get("page"));
    return { ok: true, json: async () => ({ jobs: jobs.slice((page - 1) * 100, page * 100), total_count: jobs.length }) };
  };
}

test("retry restores the signed payload and first-submits the installer skipped after a download failure", async () => {
  const installer = identity({ ...env, SIGNPATH_CONFIGURATION: "windows-installer-v2" });
  const calls = [];
  const api = historyAPI([{ name: receiptName(expected), expired: false }], {
    1: [previousJob([completedStep(payloadStep, "success"), completedStep(installerStep, "skipped")])],
  }, calls);
  assert.equal(await canReuseRequest(expected, "2", "test-only", api), true);
  assert.equal(calls.length, 1, "known receipt does not need step history");
  assert.equal(await canReuseRequest(installer, "2", "test-only", api), false, "allow the first installer submission");
  assert.ok(calls.some(url => url.includes("/attempts/1/jobs")));
});

test("a pre-signing build or input-upload failure permits the previously skipped submission", async () => {
  for (const steps of [
    [completedStep("Build desktop", "failure"), completedStep(payloadStep, "skipped")],
    [completedStep("Upload unsigned Windows payload for SignPath", "success"), completedStep(payloadStep, "skipped")],
  ]) assert.equal(await canReuseRequest(expected, "2", "test-only", historyAPI([], { 1: [previousJob(steps)] })), false);
});

test("a missing receipt after any attempted submission remains blocked, even if later attempts skipped it", async () => {
  for (const conclusion of ["success", "failure", "cancelled", null]) {
    const api = historyAPI([], {
      1: [previousJob([completedStep(payloadStep, conclusion)])],
      2: [previousJob([completedStep(payloadStep, "skipped")])],
    });
    await assert.rejects(canReuseRequest(expected, "3", "test-only", api), /check SignPath request history/);
  }
});

test("all attempt pages are read and unrelated platform/mode jobs cannot authorize resubmission", async () => {
  const unrelated = Array.from({ length: 100 }, (_, index) => ({ name: `unrelated-${index}` }));
  const target = previousJob([completedStep(payloadStep, "success")]);
  const api = historyAPI([], { 1: [...unrelated, target] });
  await assert.rejects(canReuseRequest(expected, "2", "test-only", api), /check SignPath request history/);
  const skipped = previousJob([completedStep(payloadStep, "skipped")]);
  const jobs = [skipped, { ...target, name: "build (windows-arm64, preflight)" }, { ...target, name: "build (windows-amd64, release)" }];
  assert.equal(await canReuseRequest(expected, "3", "test-only", historyAPI([], { 1: jobs, 2: [] })), false);
});

test("unavailable, nonterminal or ambiguous step evidence fails closed", async () => {
  const skipped = previousJob([completedStep(payloadStep, "skipped")]);
  for (const jobs of [
    [{ ...skipped, status: "in_progress" }], [skipped, skipped],
    [{ ...skipped, steps: [] }], [{ ...skipped, steps: undefined }],
    [{ ...skipped, steps: [completedStep(payloadStep, "skipped"), completedStep(payloadStep, "success")] }],
  ]) await assert.rejects(canReuseRequest(expected, "2", "test-only", historyAPI([], { 1: jobs })));
  await assert.rejects(canReuseRequest(expected, "2", "test-only", historyAPI([], {})), /HTTP 404/);
});

test("receipt resumes one request across attempts but rejects changed release identity or bytes", () => {
  assert.equal(validateReceipt(receipt, expected, "digest", "2"), requestId);
  assert.equal(receiptName(expected), receiptName(identity({ ...env, GITHUB_RUN_ATTEMPT: "2" })));
  for (const key of Object.keys(expected)) {
    assert.throws(() => validateReceipt(receipt, { ...expected, [key]: `${expected[key]}changed` }, "digest", "2"), /identity/);
  }
  assert.throws(() => validateReceipt(receipt, expected, "changed", "2"), /bytes changed/);
  for (const change of [{ schema: 2 }, { attempt: "3" }, { attempt: "" }, { requestId: "bad\nrequest_id=unsafe" }]) {
    assert.throws(() => validateReceipt({ ...receipt, ...change }, expected, "digest", "2"), /invalid/);
  }
});

test("input digest covers nested filenames and bytes, ignores mtime, rejects symlinks", async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), "signpath-input-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await assert.rejects(inputDigest(root), /empty/);
  await mkdir(path.join(root, "app"));
  const file = path.join(root, "app", "app.exe");
  await writeFile(file, "original");
  const digest = await inputDigest(root);
  await writeFile(file, "original");
  assert.equal(await inputDigest(root), digest);
  await writeFile(file, "modified");
  assert.notEqual(await inputDigest(root), digest);
  await writeFile(file, "original");
  await rename(file, path.join(root, "app", "renamed.exe"));
  assert.notEqual(await inputDigest(root), digest);
  await symlink("renamed.exe", file);
  await assert.rejects(inputDigest(root), /symbolic link/);
});

test("receipt lookup is same-run and fails closed on API failures, expiry and ambiguity", async () => {
  const item = { name: receiptName(expected), expired: false };
  const fetcher = artifacts => async (url, options) => {
    assert.match(url, /repos\/example\/project\/actions\/runs\/123\/artifacts\?name=signpath-request-123-/);
    assert.equal(options.headers.Authorization, "Bearer test-only");
    return { ok: true, json: async () => ({ artifacts, total_count: artifacts.length }) };
  };
  assert.equal(await findReceipt(expected, "test-only", fetcher([])), false);
  assert.equal(await findReceipt(expected, "test-only", fetcher([item])), true);
  for (const artifacts of [[item, item], [{ ...item, expired: true }]]) {
    await assert.rejects(findReceipt(expected, "test-only", fetcher(artifacts)), /ambiguous or expired/);
  }
  await assert.rejects(findReceipt(expected, "test-only", async () => ({ ok: false, status: 403 })), /refusing to resubmit/);
  await assert.rejects(findReceipt(expected, "test-only", async () => ({ ok: true, json: async () => ({ total_count: 101, artifacts: [] }) })), /incomplete/);
  await assert.rejects(findReceipt(expected, ""), /GH_TOKEN/);
});

test("CLI records and restores the original request without a signing API call", async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), "signpath-receipt-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const input = path.join(root, "input");
  const checkpoint = path.join(root, "receipt");
  const output = path.join(root, "output");
  await mkdir(input);
  await writeFile(path.join(input, "app.exe"), "test artifact");
  const run = (command, extra = {}) => spawnSync(process.execPath,
    [new URL("./signpath-checkpoint.mjs", import.meta.url).pathname, command, input, checkpoint, env.SIGNPATH_CONFIGURATION],
    { env: { ...process.env, ...env, GITHUB_OUTPUT: output, SIGNPATH_REQUEST_ID: requestId, ...extra }, encoding: "utf8" });
  assert.equal(run("record").status, 0);
  assert.ok(!(await readFile(path.join(checkpoint, "request.json"), "utf8")).includes(env.SIGNPATH_ORGANIZATION_ID));
  assert.equal(run("restore", { GITHUB_RUN_ATTEMPT: "2" }).status, 0);
  assert.equal(await readFile(output, "utf8"), `request_id=${requestId}\n`);
  await writeFile(path.join(input, "app.exe"), "different artifact");
  const failed = run("restore", { GITHUB_RUN_ATTEMPT: "2" });
  assert.notEqual(failed.status, 0);
  assert.match(failed.stderr, /input bytes changed/);
  assert.equal(await readFile(output, "utf8"), `request_id=${requestId}\n`);
  assert.notEqual(run("record").status, 0, "cannot overwrite an existing receipt");
});

test("release no longer consumes SignPath quota while legacy receipts remain readable", async () => {
  const workflow = await readFile(new URL("../.github/workflows/release-desktop.yml", import.meta.url), "utf8");
  assert.ok(!workflow.includes("signpath/github-action-submit-signing-request"));
  assert.ok(!workflow.includes("secrets.SIGNPATH_API_TOKEN"));
  assert.ok(!workflow.includes("node release-control/scripts/signpath-checkpoint.mjs"));
  assert.match(workflow, /uses: \.\/release-control\/\.github\/actions\/setup-certum/);
});
