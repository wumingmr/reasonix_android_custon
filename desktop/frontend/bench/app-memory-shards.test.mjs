import test from "node:test";
import assert from "node:assert/strict";
import { aggregateShards, completeShard, memoryProtocol, MEMORY_FIXTURES } from "./app-memory-shards.mjs";

const identity = { sourceSHA: "a".repeat(40), trackedDiffSHA256: "clean-diff", untrackedSourceSHA256: "clean-untracked", buildSHA256: "shared-build", node: "v24", platform: "linux", arch: "x64", sourceStatus: "" };

function fixture(profile = "full", executionId = "123:1") {
  const protocol = memoryProtocol(profile);
  const manifest = { identity, protocol, executionId };
  const report = id => {
    const sample = (phase, roundTrips) => ({ phase, roundTrips,
      lifecycle: { liveRenderTokenIds: [1], liveRenderTokens: 1, activeOperations: 0, activeSubscriptions: 2, overflow: false, invariantViolations: 0 },
      dom: { nodes: 10, jsEventListeners: 2 }, heap: { usedSize: 100 } });
    const samples = [sample("baseline", 0)];
    for (const phase of ["full", "windowed", "safety", "mixed"]) {
      const count = phase === "mixed" ? protocol.mixedCycles : protocol.cycles;
      for (let round = 32; round <= count; round += 32) samples.push(sample(phase, round));
    }
    samples.push(sample("settled", protocol.mixedCycles));
    return { identity: structuredClone(identity), fixtures: structuredClone(MEMORY_FIXTURES), protocol: structuredClone(protocol),
      shard: { id, total: protocol.shards, executionId }, cycles: protocol.cycles, mixedCycles: protocol.mixedCycles,
      shardComplete: true, protocolComplete: false, verdict: "SHARD_PASS", processes: [{ process: id, browser: "chromium-fixed", samples,
        snapshots: ["baseline", "full", "windowed", "safety", "mixed"].map(phase => ({ file: `${id}-${phase}.heapsnapshot`, summary: {} })),
        checks: { evidenceIntegrity: true, instrumentedOperationsReleased: true, noPageErrors: true }, metrics: { pageErrors: [] } }] };
  };
  return { manifest, protocol, report, aggregate: reports => aggregateShards(reports, manifest, identity.sourceSHA) };
}

test("full screening preserves three independent complete shards", () => {
  const { aggregate, report } = fixture("full");
  const result = aggregate([report(3), report(1), report(2)]);
  assert.equal(result.verdict, "PASS");
  assert.equal(result.protocolComplete, true);
  assert.equal(result.screeningLevel, "full");
  assert.deepEqual(result.processes.map(run => run.process), [1, 2, 3]);
  assert.ok(result.processes.every(run => run.attribution.reasons.includes("heap-retainer-and-control-evidence-required")));
});

test("short screening is one complete 32/128 shard and remains explicitly labeled", () => {
  const { aggregate, report } = fixture("short");
  assert.equal(completeShard(report(1)), true);
  const result = aggregate([report(1)]);
  assert.equal(result.verdict, "PASS");
  assert.equal(result.screeningLevel, "short");
  assert.equal(result.protocol.shards, 1);
  assert.equal(result.processes.length, 1);
});

test("each profile rejects another profile's shard count and protocol", () => {
  const full = fixture("full");
  const short = fixture("short");
  assert.throws(() => full.aggregate([full.report(1)]), /3 complete/);
  assert.throws(() => short.aggregate([short.report(1), short.report(1)]), /1 complete/);
  assert.throws(() => aggregateShards([short.report(1)], full.manifest, identity.sourceSHA));
  assert.throws(() => memoryProtocol("unknown"), /unknown App memory profile/);
});

for (const [name, mutate] of [
  ["duplicate shard", reports => { reports[2] = fixture("full").report(1); }],
  ["different commit", reports => { reports[1].identity.sourceSHA = "b".repeat(40); }],
  ["different build", reports => { reports[1].identity.buildSHA256 = "other-build"; }],
  ["dirty source", reports => { reports[1].identity.sourceStatus = " M source.ts"; }],
  ["another workflow attempt", reports => { reports[1].shard.executionId = "123:2"; }],
  ["different fixture", reports => { reports[1].fixtures.windowed.label = "short-fixture"; }],
  ["old protocol", reports => { reports[1].protocol.version = 3; }],
  ["short cycles", reports => { reports[1].cycles = 127; }],
  ["missing checkpoint", reports => { reports[1].processes[0].samples.splice(5, 1); }],
  ["missing heap snapshot", reports => { reports[1].processes[0].snapshots.pop(); }],
  ["different browser", reports => { reports[1].processes[0].browser = "another-browser"; }],
  ["missing checks", reports => { reports[1].processes[0].checks = {}; }],
  ["page error", reports => { reports[1].processes[0].metrics.pageErrors.push("boom"); }],
  ["persistent DOM drift despite claimed pass", reports => { reports[1].processes[0].samples.at(-1).dom.nodes++; }],
  ["unfinished operations despite claimed pass", reports => { reports[1].processes[0].samples.at(-1).lifecycle.activeOperations++; }],
  ["missing token identity", reports => { delete reports[1].processes[0].samples[0].lifecycle.liveRenderTokenIds; }],
]) test(`aggregate rejects ${name}`, () => {
  const { aggregate, report } = fixture("full");
  const reports = [report(1), report(2), report(3)];
  mutate(reports);
  assert.throws(() => aggregate(reports));
});

test("requested head and manifest protocol must match", () => {
  const { manifest, report } = fixture("full");
  assert.throws(() => aggregateShards([report(1), report(2), report(3)], manifest, "wrong-head"));
  assert.throws(() => aggregateShards([report(1), report(2), report(3)], { ...manifest, protocol: { ...manifest.protocol, viewport: { width: 1, height: 1 } } }, identity.sourceSHA));
});
