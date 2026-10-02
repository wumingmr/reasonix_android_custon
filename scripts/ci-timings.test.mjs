import assert from "node:assert/strict";
import test from "node:test";
import { timingMarkdown, timingReport } from "./ci-timings.mjs";

const stamp = seconds => new Date(Date.UTC(2026, 0, 1, 0, 0, seconds)).toISOString();

test("timing report separates queue, execution, wall time and selected stages", () => {
  const run = { created_at: stamp(0) };
  const jobs = { jobs: [
    { name: "desktop-prepare", created_at: stamp(1), started_at: stamp(6), completed_at: stamp(26), conclusion: "success", steps: [
      { name: "Install frontend dependencies", started_at: stamp(7), completed_at: stamp(12), conclusion: "success" },
      { name: "Build stable frontend", started_at: stamp(12), completed_at: stamp(20), conclusion: "success" },
    ] },
    { name: "desktop-browser-group (motion)", created_at: stamp(2), started_at: stamp(12), completed_at: stamp(32), conclusion: "success", steps: [
      { name: "Test desktop browser group", started_at: stamp(20), completed_at: stamp(30), conclusion: "success" },
    ] },
    { name: "desktop-windows-go", created_at: stamp(3), started_at: stamp(4), completed_at: stamp(24), conclusion: "success", steps: [
      { name: "test (Windows desktop and update helper)", started_at: stamp(8), completed_at: stamp(23), conclusion: "success" },
    ] },
    { name: "sign Windows candidate", created_at: stamp(3), started_at: stamp(4), completed_at: stamp(24), conclusion: "success", steps: [
      { name: "Finalize amd64 in the shared Certum session", started_at: stamp(8), completed_at: stamp(23), conclusion: "success" },
    ] },
    { name: "skipped", created_at: stamp(1), conclusion: "skipped", steps: [] },
  ] };
  const report = timingReport(run, jobs);
  assert.equal(report.workflowElapsed, 32_000);
  assert.equal(report.runnerSum, 80_000);
  assert.equal(report.queueSum, 17_000);
  assert.deepEqual(report.stages.map(stage => stage.kind), ["browser group", "frontend build", "dependency install", "Go test", "Windows signing"]);
  const markdown = timingMarkdown(report, "Measured CI timing");
  assert.match(markdown, /Total workflow wait: \*\*0m 32s\*\*/);
  assert.match(markdown, /Sum of runner execution: \*\*1m 20s\*\*/);
  assert.match(markdown, /Step durations exclude job queue time/);
});

test("running jobs use the observation time for wall time", () => {
  const report = timingReport({ created_at: stamp(0) }, { jobs: [
    { name: "app-memory", created_at: stamp(1), started_at: stamp(2), completed_at: null, conclusion: null, steps: [] },
  ] }, { now: new Date(stamp(45)) });
  assert.equal(report.workflowElapsed, 45_000);
  assert.equal(report.runnerSum, 0);
});
