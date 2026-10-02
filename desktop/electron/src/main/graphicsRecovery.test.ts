import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { GraphicsFaultRecord, GraphicsRecovery } from "./graphicsRecovery.js";

const gpu = { role: "GPU" as const, reason: "crashed", exitCode: 1 };
const renderer = { ...gpu, role: "renderer" as const };
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
  let now = 0, stopping = false, acceleration = true;
  const prompts: boolean[] = [], recorded: unknown[] = [];
  let acknowledged = 0;
  const recovery = new GraphicsRecovery({
    now: () => now, stopping: () => stopping, accelerationEnabled: () => acceleration,
    record: f => { recorded.push(f); }, acknowledge: () => { acknowledged++; },
    offer: async gpu => { prompts.push(gpu); }, error: error => { throw error; },
  });
  return { recovery, prompts, recorded, advance: (ms: number) => { now += ms; }, stop: () => { stopping = true; }, disable: () => { acceleration = false; }, acknowledged: () => acknowledged };
}

test("GPU crash bursts prompt once, without treating ordinary exits as GPU faults", async () => {
  const f = fixture();
  for (const reason of ["clean-exit", "killed", "unknown"]) f.recovery.fault({ ...gpu, reason });
  assert.equal(f.recorded.length, 0);
  f.recovery.fault(gpu);
  await tick();
  assert.deepEqual(f.prompts, []);
  for (let i = 0; i < 20; i++) f.recovery.fault(gpu);
  await tick();
  assert.deepEqual(f.prompts, [true]);
  f.recovery.fault(gpu);
  await tick();
  assert.deepEqual(f.prompts, [true]);
});

test("renderer reload budget distinguishes OOM and ordinary stalls from GPU failures", async () => {
  const f = fixture();
  assert.equal(f.recovery.fault(renderer), true);
  assert.equal(f.recovery.fault(renderer), false);
  await tick();
  assert.deepEqual(f.prompts, [false]);
  const oom = fixture();
  assert.equal(oom.recovery.fault({ ...renderer, reason: "oom" }), false);
  await tick();
  assert.deepEqual(oom.prompts, [false]);
  const related = fixture();
  related.recovery.fault(gpu);
  related.recovery.fault(renderer);
  related.recovery.fault(renderer);
  await tick();
  assert.deepEqual(related.prompts, [true]);
  const killed = fixture();
  assert.equal(killed.recovery.fault({ ...renderer, reason: "killed" }), true);
  assert.equal(killed.recovery.fault({ ...renderer, reason: "killed" }), false);
  await tick(); assert.deepEqual(killed.prompts, [false]);
  const startup = fixture();
  assert.equal(startup.recovery.fault(renderer, false), false);
  await tick(); assert.deepEqual(startup.prompts, [false]);
});

test("recovered transients and shutdown never trigger compatibility restart", async () => {
  const f = fixture();
  f.recovery.unresponsive(); f.advance(14_000); f.recovery.tick();
  f.recovery.responsive(); f.advance(2000); f.recovery.tick();
  await tick(); assert.deepEqual(f.prompts, []);
  f.recovery.unresponsive(); f.advance(15_000); f.recovery.tick();
  await tick(); assert.deepEqual(f.prompts, [false]);
  const stopped = fixture(); stopped.stop();
  stopped.recovery.fault(gpu); stopped.recovery.fault(gpu);
  await tick(); assert.deepEqual(stopped.recorded, []);
  const software = fixture(); software.disable();
  software.recovery.fault(gpu); software.recovery.fault(gpu);
  await tick(); assert.deepEqual(software.prompts, [false]);
});

test("time window expires and healthy recovery acknowledges prior fault", async () => {
  const f = fixture(); f.recovery.fault(gpu); f.advance(60_001);
  f.recovery.healthy(); assert.equal(f.acknowledged(), 1);
  f.recovery.fault(gpu); await tick(); assert.deepEqual(f.prompts, []);
  assert.equal(f.recovery.fault(renderer), true);
  f.advance(60_001); assert.equal(f.recovery.fault(renderer), true);
});

test("a healthy interval rearms recovery for a later independent failure", async () => {
  for (const kind of ["renderer", "GPU", "unresponsive"] as const) {
    const f = fixture();
    const fail = () => {
      if (kind === "unresponsive") {
        f.recovery.unresponsive(); f.advance(15_000); f.recovery.tick();
      } else {
        f.recovery.fault(kind === "GPU" ? gpu : renderer);
        f.recovery.fault(kind === "GPU" ? gpu : renderer);
      }
    };
    fail(); await tick();
    assert.equal(f.prompts.length, 1);
    f.recovery.responsive(); f.advance(60_001); f.recovery.healthy();
    fail(); await tick();
    assert.equal(f.prompts.length, 2, `${kind}: a dismissed prompt must not suppress all future recovery`);
  }
  const startup = fixture();
  startup.recovery.offer(true); await tick();
  startup.advance(60_001); startup.recovery.healthy();
  startup.recovery.fault(gpu); startup.recovery.fault(gpu); await tick();
  assert.deepEqual(startup.prompts, [true, true], "a previous-launch warning must also rearm after healthy use");
});

test("acknowledging an old dialog preserves a GPU fault recorded while it was open", t => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-gpu-ack-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const path = join(root, "fault.json");
  const record = new GraphicsFaultRecord(path, "v1", () => 100);
  record.record(gpu);
  const shown = record.checkpoint();
  record.record(gpu); // Same timestamp still identifies a different failure.
  record.acknowledge(shown);
  assert.equal(new GraphicsFaultRecord(path, "v1", () => 100).pending(), true);
  record.acknowledge(record.checkpoint());
  assert.equal(new GraphicsFaultRecord(path, "v1", () => 100).pending(), false);
});

test("GPU failure waits behind a native notice without being lost or opening duplicate dialogs", async () => {
  const f = fixture(); let release!: () => void;
  assert.equal(f.recovery.notice(() => new Promise<void>(resolve => { release = resolve; })), true);
  await tick();
  f.recovery.fault(gpu); f.recovery.fault(gpu); f.recovery.fault(gpu);
  assert.deepEqual(f.prompts, []);
  release(); await tick(); await tick();
  assert.deepEqual(f.prompts, [true]);
});

test("a stall that recovers while another notice is open does not leave a stale recovery prompt", async () => {
  const f = fixture(); let release!: () => void;
  f.recovery.notice(() => new Promise<void>(resolve => { release = resolve; }));
  await tick();
  f.recovery.unresponsive(); f.advance(15_000); f.recovery.tick();
  f.recovery.responsive();
  release(); await tick(); await tick();
  assert.deepEqual(f.prompts, []);
  f.recovery.unresponsive(); f.advance(15_000); f.recovery.tick();
  await tick(); assert.deepEqual(f.prompts, [false]);
});

test("pending record survives crash, expires, preserves unknown fields and rejects future schemas", t => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-gpu-record-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const path = join(root, "fault.json"); let now = 100;
  const open = (build = "v1") => new GraphicsFaultRecord(path, build, () => now);
  assert.equal(open().pending(), false);
  writeFileSync(path, JSON.stringify({ version: 1, future: { retained: true } }));
  open().record(renderer); assert.equal(open().pending(), false);
  open().record(gpu); assert.equal(open().pending(), true);
  assert.equal(open("v2").pending(), false);
  assert.deepEqual(JSON.parse(readFileSync(path, "utf8")).future, { retained: true });
  now += 86_400_001; assert.equal(open().pending(), false);
  open().record(gpu); open().acknowledge(); assert.equal(open().pending(), false);
  for (const contents of ['{"version":2,"pending":true}', 'broken']) {
    writeFileSync(path, contents); open().record(gpu); open().acknowledge();
    assert.equal(open().pending(), false); assert.equal(readFileSync(path, "utf8"), contents);
  }
});
