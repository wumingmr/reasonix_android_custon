#!/usr/bin/env node

import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const FRONTEND_ARTIFACT_SCHEMA = 1;

export function frontendProducerAttempt(env = process.env) {
  return env.REASONIX_FRONTEND_PRODUCER_ATTEMPT || env.GITHUB_RUN_ATTEMPT || undefined;
}

function sha256(parts) {
  const hash = createHash("sha256");
  for (const part of parts) hash.update(part);
  return hash.digest("hex");
}

function git(root, args, options = {}) {
  return execFileSync("git", ["-C", root, ...args], { encoding: "utf8", ...options }).trim();
}

function gitBlobContents(root, names) {
  const result = spawnSync("git", ["-C", root, "cat-file", "--batch"], {
    input: names.map(name => `HEAD:${name}\n`).join(""),
    maxBuffer: 256 * 1024 * 1024,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(result.stderr.toString("utf8").trim() || "git cat-file --batch failed");
  const contents = [];
  let offset = 0;
  for (const name of names) {
    const headerEnd = result.stdout.indexOf(10, offset);
    if (headerEnd < 0) throw new Error(`missing git blob header for ${name}`);
    const header = result.stdout.subarray(offset, headerEnd).toString("utf8");
    const match = header.match(/^[0-9a-f]+ blob (\d+)$/);
    if (!match) throw new Error(`invalid git blob header for ${name}: ${header}`);
    const bodyStart = headerEnd + 1;
    const bodyEnd = bodyStart + Number(match[1]);
    if (bodyEnd >= result.stdout.length || result.stdout[bodyEnd] !== 10)
      throw new Error(`truncated git blob for ${name}`);
    contents.push(result.stdout.subarray(bodyStart, bodyEnd));
    offset = bodyEnd + 1;
  }
  if (offset !== result.stdout.length) throw new Error("unexpected trailing git cat-file output");
  return contents;
}

function filesBelow(directory, relative = "") {
  const out = [];
  for (const entry of readdirSync(path.join(directory, relative), { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    const name = path.posix.join(relative.replaceAll("\\", "/"), entry.name);
    if (entry.isDirectory()) out.push(...filesBelow(directory, name));
    else out.push(name);
  }
  return out;
}

export function distIdentity(dist) {
  const files = filesBelow(dist).map(name => {
    const content = readFileSync(path.join(dist, name));
    return { name, size: content.length, sha256: sha256([content]) };
  });
  return { sha256: sha256(files.flatMap(file => [file.name, "\0", file.sha256, "\0"])), files };
}

export function buildInputIdentity(root) {
  const names = git(root, ["ls-files", "-z", "--", "desktop/package.json", "desktop/pnpm-lock.yaml", "desktop/pnpm-workspace.yaml", "desktop/frontend"])
    .split("\0").filter(name => name && !name.startsWith("desktop/frontend/dist/")).sort();
  const contents = gitBlobContents(root, names);
  return {
    // Git blobs are the portable source identity. Reading checkout bytes here
    // would make a Windows CRLF checkout disagree with the Linux producer.
    sha256: sha256(names.flatMap((name, index) => [name, "\0", contents[index], "\0"])),
    files: names,
  };
}

function required(value, name) {
  if (!String(value ?? "").trim()) throw new Error(`${name} is required`);
  return String(value).trim();
}

export function createFrontendArtifact({ root, dist, manifest, shell, channel, sourceSHA, runId, attempt, pnpmVersion }) {
  const actualSHA = git(root, ["rev-parse", "HEAD"]);
  const expectedSHA = required(sourceSHA, "source SHA");
  if (expectedSHA !== actualSHA) throw new Error(`source SHA mismatch: expected ${expectedSHA}, checkout is ${actualSHA}`);
  if (!statSync(dist).isDirectory()) throw new Error(`frontend dist is not a directory: ${dist}`);
  const body = {
    schemaVersion: FRONTEND_ARTIFACT_SCHEMA,
    sourceSHA: actualSHA,
    workflow: { runId: required(runId, "run ID"), attempt: required(attempt, "run attempt") },
    variant: { shell: required(shell, "shell"), channel: required(channel, "channel") },
    toolchain: { node: process.version, pnpm: required(pnpmVersion, "pnpm version"), platform: process.platform, arch: process.arch },
    inputs: buildInputIdentity(root),
    dist: distIdentity(dist),
  };
  writeFileSync(manifest, JSON.stringify(body, null, 2) + "\n");
  return body;
}

export function verifyFrontendArtifact({ root, dist, manifest, shell, channel, sourceSHA, runId, attempt, pnpmVersion }) {
  let body;
  try {
    body = JSON.parse(readFileSync(manifest, "utf8"));
  } catch (error) {
    throw new Error(`frontend artifact manifest is unavailable or invalid: ${error.message}`);
  }
  const expected = {
    schemaVersion: FRONTEND_ARTIFACT_SCHEMA,
    sourceSHA: sourceSHA || git(root, ["rev-parse", "HEAD"]),
    shell,
    channel,
    runId,
    attempt,
    node: process.version,
    pnpm: pnpmVersion,
  };
  const actual = {
    schemaVersion: body.schemaVersion,
    sourceSHA: body.sourceSHA,
    shell: body.variant?.shell,
    channel: body.variant?.channel,
    runId: body.workflow?.runId,
    attempt: body.workflow?.attempt,
    node: body.toolchain?.node,
    pnpm: body.toolchain?.pnpm,
  };
  for (const [name, value] of Object.entries(expected)) {
    if (value !== undefined && String(actual[name]) !== String(value))
      throw new Error(`frontend artifact ${name} mismatch: expected ${value}, got ${actual[name]}`);
  }
  const inputs = buildInputIdentity(root);
  if (body.inputs?.sha256 !== inputs.sha256) throw new Error("frontend artifact build inputs do not match this checkout");
  const built = distIdentity(dist);
  if (body.dist?.sha256 !== built.sha256 || JSON.stringify(body.dist.files) !== JSON.stringify(built.files))
    throw new Error("frontend artifact contents do not match its manifest");
  return body;
}

function parseArgs(argv) {
  const args = { command: argv[0] };
  for (let i = 1; i < argv.length; i += 2) {
    if (!argv[i]?.startsWith("--") || argv[i + 1] === undefined) throw new Error(`invalid argument ${argv[i] ?? ""}`);
    args[argv[i].slice(2).replaceAll("-", "_")] = argv[i + 1];
  }
  return args;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const root = path.resolve(args.root ?? path.join(path.dirname(fileURLToPath(import.meta.url)), "../../.."));
    const options = {
      root,
      dist: path.resolve(args.dist ?? path.join(root, "desktop/frontend/dist")),
      manifest: path.resolve(args.manifest ?? path.join(root, "desktop/frontend/.reasonix-frontend-artifact.json")),
      shell: args.shell,
      channel: args.channel,
      sourceSHA: args.source_sha,
      runId: args.run_id,
      attempt: args.attempt,
      pnpmVersion: args.pnpm_version ?? execFileSync("pnpm", ["--version"], { encoding: "utf8" }).trim(),
    };
    const result = args.command === "create" ? createFrontendArtifact(options)
      : args.command === "verify" ? verifyFrontendArtifact(options)
        : (() => { throw new Error("command must be create or verify"); })();
    console.log(`frontend artifact ${args.command}: ${result.sourceSHA} ${result.variant.shell}/${result.variant.channel} ${result.dist.sha256}`);
  } catch (error) {
    console.error(`artifact-identity: ${error.message}`);
    process.exitCode = 1;
  }
}
