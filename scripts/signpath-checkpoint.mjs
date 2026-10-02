// Same-run recovery receipts contain no credentials or signed payloads. A receipt
// is usable only with exactly the same input bytes and protected release identity.
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { appendFile, lstat, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;

export async function inputDigest(directory) {
  const files = [];
  async function walk(relative = "") {
    const current = path.join(directory, relative);
    const stat = await lstat(current);
    if (stat.isSymbolicLink()) throw new Error("signing input contains a symbolic link");
    if (stat.isDirectory()) {
      for (const name of (await readdir(current)).sort()) await walk(relative ? `${relative}/${name}` : name);
    } else if (stat.isFile()) {
      const hash = createHash("sha256");
      for await (const chunk of createReadStream(current)) hash.update(chunk);
      files.push([relative, stat.size, hash.digest("hex")]);
    } else throw new Error("signing input contains a non-regular file");
  }
  await walk();
  if (!files.length) throw new Error("empty signing input");
  return createHash("sha256").update(JSON.stringify(files)).digest("hex");
}

export function identity(env) {
  const fields = ["GITHUB_REPOSITORY", "GITHUB_RUN_ID", "SIGNPATH_SOURCE_SHA", "SIGNPATH_CONTROL_SHA",
    "SIGNPATH_FINGERPRINT", "SIGNPATH_VERSION", "SIGNPATH_CHANNEL", "SIGNPATH_MODE", "SIGNPATH_PLATFORM",
    "SIGNPATH_ORGANIZATION_ID", "SIGNPATH_PROJECT", "SIGNPATH_POLICY", "SIGNPATH_CONFIGURATION"];
  const result = Object.fromEntries(fields.map(key => {
    if (!env[key] || /[\r\n]/.test(env[key])) throw new Error(`missing or invalid ${key}`);
    return [key, env[key]];
  }));
  if (!/^[1-9][0-9]*$/.test(result.GITHUB_RUN_ID)
    || !/^[\w.-]+\/[\w.-]+$/.test(result.GITHUB_REPOSITORY)
    || !uuid.test(result.SIGNPATH_ORGANIZATION_ID)
    || ![result.SIGNPATH_SOURCE_SHA, result.SIGNPATH_CONTROL_SHA].every(value => /^[a-f0-9]{40}$/.test(value))) {
    throw new Error("invalid signing checkpoint identity");
  }
  return result;
}

export function receiptName(expected) {
  // Deliberately exclude run_attempt and input digest. Changed bytes on a retry
  // must fail closed instead of silently making another quota-consuming request.
  return `signpath-request-${expected.GITHUB_RUN_ID}-${identityDigest(expected).slice(0, 24)}`;
}

export const identityDigest = expected => createHash("sha256").update(JSON.stringify(expected)).digest("hex");

export function validateReceipt(receipt, expected, digest, attempt) {
  if (receipt.schema !== 1 || !uuid.test(receipt.requestId ?? "")
    || !/^[1-9][0-9]*$/.test(receipt.attempt ?? "")
    || !/^[1-9][0-9]*$/.test(attempt ?? "") || BigInt(receipt.attempt) > BigInt(attempt)) {
    throw new Error("invalid signing recovery receipt");
  }
  if (receipt.identityDigest !== identityDigest(expected) || receipt.digest !== digest) {
    throw new Error("signing recovery identity or input bytes changed; refusing to reuse or resubmit");
  }
  return receipt.requestId;
}

async function githubGet(expected, route, token, fetcher) {
  if (!token) throw new Error("GH_TOKEN is required for signing recovery");
  const url = `https://api.github.com/repos/${expected.GITHUB_REPOSITORY}/actions/runs/${expected.GITHUB_RUN_ID}/${route}`;
  const response = await fetcher(url, {
    headers: { Authorization: `Bearer ${token}`, Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28" },
    signal: AbortSignal.timeout(30000),
  });
  if (!response.ok) throw new Error(`cannot read signing recovery evidence (HTTP ${response.status}); refusing to resubmit`);
  return response.json();
}

export async function findReceipt(expected, token, fetcher = fetch) {
  const name = receiptName(expected);
  const data = await githubGet(expected, `artifacts?name=${name}&per_page=100`, token, fetcher);
  if (!Array.isArray(data.artifacts) || !Number.isSafeInteger(data.total_count)
    || data.total_count !== data.artifacts.length) throw new Error("incomplete signing receipt lookup");
  const matches = data.artifacts.filter(artifact => artifact.name === name);
  if (matches.length > 1 || matches.some(artifact => artifact.expired)) throw new Error("ambiguous or expired signing receipt");
  return matches.length === 1;
}

export function requireRecoverable(exists, attempt, neverSubmitted = false) {
  if (!/^[1-9][0-9]*$/.test(attempt ?? "") || Number(attempt) > 100) throw new Error("invalid or excessive workflow attempt");
  if (!exists && attempt !== "1" && !neverSubmitted) {
    throw new Error("no signing receipt on a rerun; check SignPath request history before starting a new run");
  }
}

// A missing receipt alone says nothing about whether submission happened. Read
// every prior attempt, including ones omitted by a failed-jobs-only retry. Only
// a terminal skipped step (or no job in a complete listing) proves non-execution.
export async function wasNeverSubmitted(expected, attempt, token, fetcher = fetch) {
  requireRecoverable(true, attempt);
  const stepName = {
    "windows-payload": "Submit Windows payload for Authenticode signing",
    "windows-installer-v2": "Submit installer for Authenticode signing",
  }[expected.SIGNPATH_CONFIGURATION];
  if (!stepName || !["preflight", "release"].includes(expected.SIGNPATH_MODE)
    || !["windows-amd64", "windows-arm64"].includes(expected.SIGNPATH_PLATFORM)) throw new Error("unsupported signing stage");
  const jobName = `build (${expected.SIGNPATH_PLATFORM}, ${expected.SIGNPATH_MODE})`;
  for (let previous = 1; previous < Number(attempt); previous++) {
    const jobs = [];
    let total;
    for (let page = 1; ; page++) {
      if (page > 100) throw new Error("excessive signing job history");
      const data = await githubGet(expected, `attempts/${previous}/jobs?per_page=100&page=${page}`, token, fetcher);
      if (!Array.isArray(data.jobs) || !Number.isSafeInteger(data.total_count) || data.total_count < 0
        || (total !== undefined && total !== data.total_count)) throw new Error("invalid signing job history");
      total = data.total_count;
      jobs.push(...data.jobs);
      if (jobs.length === total) break;
      if (!data.jobs.length || jobs.length > total) throw new Error("incomplete signing job history");
    }
    if (jobs.some(job => typeof job.name !== "string")) throw new Error("invalid signing job name");
    const matches = jobs.filter(job => job.name === jobName || job.name.endsWith(` / ${jobName}`));
    if (matches.length > 1) throw new Error("ambiguous signing job history");
    for (const job of matches) {
      if (job.status !== "completed") throw new Error("signing job history is not terminal");
      if (!Array.isArray(job.steps)) throw new Error("missing signing step history");
      const submissions = job.steps.filter(step => step.name === stepName);
      if (submissions.length === 0 && job.conclusion === "skipped" && job.steps.length === 0) continue;
      if (submissions.length !== 1) throw new Error("missing or ambiguous signing submission step");
      if (submissions[0].status !== "completed" || submissions[0].conclusion !== "skipped") return false;
    }
  }
  return true;
}

export async function canReuseRequest(expected, attempt, token, fetcher = fetch) {
  requireRecoverable(true, attempt);
  const exists = await findReceipt(expected, token, fetcher);
  const neverSubmitted = !exists && attempt !== "1" && await wasNeverSubmitted(expected, attempt, token, fetcher);
  requireRecoverable(exists, attempt, neverSubmitted);
  return exists;
}

async function main() {
  const [command, input, checkpoint, configuration] = process.argv.slice(2);
  if (!["prepare", "restore", "record"].includes(command) || !input || !checkpoint || !configuration) {
    throw new Error("usage: signpath-checkpoint.mjs prepare|restore|record INPUT CHECKPOINT CONFIGURATION");
  }
  const expected = identity({ ...process.env, SIGNPATH_CONFIGURATION: configuration });
  const output = async (key, value) => appendFile(process.env.GITHUB_OUTPUT, `${key}=${value}\n`);
  if (command === "prepare") {
    const exists = await canReuseRequest(expected, process.env.GITHUB_RUN_ATTEMPT, process.env.GH_TOKEN);
    await output("name", receiptName(expected));
    await output("exists", String(exists));
    return;
  }
  const digest = await inputDigest(input);
  const receiptPath = path.join(checkpoint, "request.json");
  if (command === "restore") {
    const receipt = JSON.parse(await readFile(receiptPath, "utf8"));
    await output("request_id", validateReceipt(receipt, expected, digest, process.env.GITHUB_RUN_ATTEMPT));
  } else {
    // Bind configuration without publishing the organization's private ID.
    const receipt = { schema: 1, identityDigest: identityDigest(expected), digest, attempt: process.env.GITHUB_RUN_ATTEMPT, requestId: process.env.SIGNPATH_REQUEST_ID };
    validateReceipt(receipt, expected, digest, process.env.GITHUB_RUN_ATTEMPT);
    await mkdir(checkpoint, { recursive: true });
    await writeFile(receiptPath, JSON.stringify(receipt, null, 2), { flag: "wx" });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
