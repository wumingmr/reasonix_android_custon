#!/usr/bin/env node

import { readFileSync, appendFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

function milliseconds(start, end) {
  if (!start || !end) return null;
  return Math.max(0, Date.parse(end) - Date.parse(start));
}

function format(ms) {
  if (ms === null || !Number.isFinite(ms)) return "running";
  const seconds = Math.round(ms / 1000);
  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, "0")}s`;
}

function stageKind(job, step) {
  if (step === "Resolve reviewed source") return "release control preflight";
  if (step === "Build each CLI binary once and package both surfaces") return "shared CLI/npm build";
  if (step === "Build and package") return "Desktop build";
  if (/^Finalize (amd64|arm64) in the shared Certum session$/.test(step)) return "Windows signing";
  if (/Install, launch, migrate, restart, and preserve legacy session/.test(step)) return "Windows acceptance";
  if (step === "Run universal DMG smoke on Intel hardware") return "macOS acceptance";
  if (step === "Seal candidate record") return "candidate sealing";
  if (step === "Verify payload provenance, bytes, source, and operation") return "candidate verification";
  if (step === "Create or verify all implementation tags") return "release activation";
  if (step === "Verify public artifacts") return "public verification";
  if (step === "Dispatch and wait for the owned Pages deployment") return "site deployment";
  if (/Install (frontend|memory) dependencies/.test(step)) return "dependency install";
  if (step === "Install browser runtimes") return "browser setup";
  if (/Build (stable|canary|memory) frontend/.test(step)) return "frontend build";
  if (job.startsWith("desktop-browser-group") && step === "Test desktop browser group") return "browser group";
  if (job === "desktop-windows-go" && step === "test (Windows desktop and update helper)") return "Go test";
  if (job.startsWith("shard (") && step === "Run complete independent memory process") return "memory shard";
  return null;
}

export function timingReport(run, jobsPayload, { now = new Date() } = {}) {
  const jobs = (jobsPayload.jobs ?? []).filter(job => job.started_at && job.conclusion !== "skipped");
  const endTimes = jobs.map(job => job.completed_at).filter(Boolean).map(Date.parse);
  const workflowEnd = endTimes.length === jobs.length && jobs.length > 0 ? Math.max(...endTimes) : now.getTime();
  const workflowStart = Date.parse(run.created_at ?? run.run_started_at);
  const rows = [];
  const stages = [];
  let runnerSum = 0;
  let queueSum = 0;
  for (const job of jobs) {
    const execution = milliseconds(job.started_at, job.completed_at);
    const queue = milliseconds(job.created_at, job.started_at);
    if (execution !== null) runnerSum += execution;
    if (queue !== null) queueSum += queue;
    rows.push({ name: job.name, queue, execution, conclusion: job.conclusion ?? "running" });
    for (const step of job.steps ?? []) {
      const kind = stageKind(job.name, step.name);
      if (kind) stages.push({ kind, name: `${job.name} / ${step.name}`, duration: milliseconds(step.started_at, step.completed_at), conclusion: step.conclusion ?? "running" });
    }
  }
  return {
    workflowElapsed: Math.max(0, workflowEnd - workflowStart),
    runnerSum,
    queueSum,
    rows: rows.sort((a, b) => a.name.localeCompare(b.name)),
    stages: stages.sort((a, b) => a.name.localeCompare(b.name)),
  };
}

export function timingMarkdown(report, title = "CI timing") {
  const lines = [
    `## ${title}`,
    "",
    `- Total workflow wait: **${format(report.workflowElapsed)}**`,
    `- Sum of runner execution: **${format(report.runnerSum)}**`,
    `- Sum of recorded job queue time: **${format(report.queueSum)}**`,
    "",
    "Step durations exclude job queue time. Total workflow wait is wall time from workflow creation through the latest completed job.",
  ];
  if (report.stages.length) {
    lines.push("", "| Stage | Step | Execution | Result |", "| --- | --- | ---: | --- |");
    for (const stage of report.stages) lines.push(`| ${stage.kind} | ${stage.name} | ${format(stage.duration)} | ${stage.conclusion} |`);
  }
  lines.push("", "<details><summary>Job timing</summary>", "", "| Job | Queue | Execution | Result |", "| --- | ---: | ---: | --- |");
  for (const row of report.rows) lines.push(`| ${row.name} | ${format(row.queue)} | ${format(row.execution)} | ${row.conclusion} |`);
  lines.push("", "</details>", "");
  return lines.join("\n");
}

function args(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 2) {
    if (!argv[i]?.startsWith("--") || argv[i + 1] === undefined) throw new Error(`invalid argument ${argv[i] ?? ""}`);
    out[argv[i].slice(2)] = argv[i + 1];
  }
  return out;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  try {
    const options = args(process.argv.slice(2));
    const run = JSON.parse(readFileSync(options.run, "utf8"));
    const jobs = JSON.parse(readFileSync(options.jobs, "utf8"));
    const report = timingReport(run, jobs);
    const markdown = timingMarkdown(report, options.title);
    if (options.output) writeFileSync(options.output, `${JSON.stringify(report, null, 2)}\n`);
    if (options.summary) appendFileSync(options.summary, markdown);
    else process.stdout.write(markdown);
  } catch (error) {
    console.error(`ci-timings: ${error.message}`);
    process.exitCode = 1;
  }
}
