import { attributeRetention, evidenceIntegrity, retainedCohorts, screeningBlockers } from "./app-memory-evidence.mjs";

const COMMON_PROTOCOL = Object.freeze({ version: 4, hydration: "async-task", pointerRest: Object.freeze([0, 0]), viewport: Object.freeze({ width: 1440, height: 1000 }) });
export const MEMORY_PROTOCOLS = Object.freeze({
  full: Object.freeze({ ...COMMON_PROTOCOL, profile: "full", shards: 3, cycles: 128, mixedCycles: 512 }),
  short: Object.freeze({ ...COMMON_PROTOCOL, profile: "short", shards: 1, cycles: 32, mixedCycles: 128 }),
});
export const MEMORY_PROTOCOL = MEMORY_PROTOCOLS.full;
export const MEMORY_FIXTURES = Object.freeze({
  full: { label: "bench:small-6t", marker: "ASYNC LAYOUT EXPANSION COMPLETE" },
  geometry: { label: "bench:geometry", marker: "Geometry contract fixture complete." },
  windowed: { label: "bench:windowed-1000t", marker: "Windowed turn 1000" },
});
const identityFields = ["sourceSHA", "trackedDiffSHA256", "untrackedSourceSHA256", "buildSHA256", "node", "platform", "arch"];

export function memoryProtocol(profile = "full") {
  const protocol = MEMORY_PROTOCOLS[profile];
  if (!protocol) throw new Error(`unknown App memory profile: ${profile}`);
  return protocol;
}

function registeredProtocol(value) {
  return Object.values(MEMORY_PROTOCOLS).find(protocol => JSON.stringify(value) === JSON.stringify(protocol));
}

export function verifyIdentity(actual, expected) {
  if (!actual || !expected || actual.sourceStatus !== "" || expected.sourceStatus !== "") throw new Error("memory evidence requires a clean source checkout");
  for (const field of identityFields) {
    if (typeof actual[field] !== "string" || !actual[field] || actual[field] !== expected[field]) throw new Error(`memory identity mismatch: ${field}`);
  }
}

export function protocolSamples(samples, protocol = MEMORY_PROTOCOL) {
  const expected = [["baseline", 0]];
  for (const phase of ["full", "windowed", "safety", "mixed"]) {
    const count = phase === "mixed" ? protocol.mixedCycles : protocol.cycles;
    for (let round = 32; round <= count; round += 32) expected.push([phase, round]);
  }
  expected.push(["settled", protocol.mixedCycles]);
  return Array.isArray(samples) && samples.length === expected.length
    && samples.every((sample, index) => sample.phase === expected[index][0] && sample.roundTrips === expected[index][1]);
}

export function completeShard(report, expectedProtocol = report.protocol) {
  const protocol = registeredProtocol(expectedProtocol);
  const run = report.processes?.[0];
  return Boolean(protocol) && JSON.stringify(report.protocol) === JSON.stringify(protocol)
    && report.cycles === protocol.cycles && report.mixedCycles === protocol.mixedCycles
    && report.processes?.length === 1 && run.process === report.shard?.id
    && protocolSamples(run.samples, protocol)
    && Array.isArray(run.snapshots) && run.snapshots.length === 5
    && ["baseline", "full", "windowed", "safety", "mixed"].every((phase, index) =>
      run.snapshots[index].file === `${run.process}-${phase}.heapsnapshot` && run.snapshots[index].summary);
}

export function aggregateShards(reports, manifest, sourceSHA) {
  const protocol = registeredProtocol(manifest.protocol);
  if (!protocol || manifest.identity?.sourceSHA !== sourceSHA || !manifest.executionId) throw new Error("invalid memory build manifest");
  if (!Array.isArray(reports) || reports.length !== protocol.shards) throw new Error(`${protocol.shards} complete independent memory shard(s) required for ${protocol.profile} screening`);
  const seen = new Set();
  const processes = [];
  let browser;
  for (const report of reports) {
    const id = report.shard?.id;
    if (!Number.isInteger(id) || id < 1 || id > protocol.shards || seen.has(id)) throw new Error("duplicate or invalid memory shard");
    seen.add(id);
    if (report.shard.executionId !== manifest.executionId || report.shard.total !== protocol.shards) throw new Error("memory shard belongs to another workflow attempt");
    verifyIdentity(report.identity, manifest.identity);
    if (JSON.stringify(report.fixtures) !== JSON.stringify(MEMORY_FIXTURES)) throw new Error("memory fixture configuration differs");
    if (report.failure || report.verdict !== "SHARD_PASS" || !report.shardComplete || !completeShard(report, protocol)) throw new Error(`incomplete memory shard ${id}`);
    const run = report.processes[0];
    if (!run.browser || (browser && browser !== run.browser)) throw new Error("memory browser versions differ");
    browser = run.browser;
    const cohorts = retainedCohorts(run.samples);
    const attribution = attributeRetention(run.samples, cohorts);
    if (!evidenceIntegrity(run.samples) || !run.samples.every(sample => sample.lifecycle.activeOperations === 0)
      || !["evidenceIntegrity", "instrumentedOperationsReleased", "noPageErrors"].every(key => run.checks?.[key] === true)
      || !Array.isArray(run.metrics?.pageErrors) || run.metrics.pageErrors.length !== 0
      || screeningBlockers(attribution.reasons).length !== 0) throw new Error(`memory screening failed in shard ${id}`);
    processes.push({ ...run, cohorts, attribution });
  }
  return { identity: manifest.identity, executionId: manifest.executionId, protocol,
    screeningLevel: protocol.profile, protocolComplete: true, verdict: "PASS", attribution: "pending",
    processes: processes.sort((a, b) => a.process - b.process) };
}
