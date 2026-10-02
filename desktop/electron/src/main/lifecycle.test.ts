import assert from "node:assert/strict";
import { test } from "node:test";
import { QuitSequencer } from "./lifecycle.js";

const silent = { info() {}, warn() {}, error() {} };
const tick = () => new Promise((resolve) => setImmediate(resolve));

test("recovery bounds a wedged draft save, confirms loss, and shuts down before relaunch", async () => {
  const events: string[] = [];
  let q!: QuitSequencer;
  const { app, calls } = fakeApp(() => q);
  q = new QuitSequencer({
    app, log: silent, recoveryDraftTimeoutMs: 5,
    service: { beforeClose: async () => false, shutdown: async () => { events.push("shutdown"); } },
    flushRenderer: () => new Promise(() => {}),
    confirmRecoveryDraftLoss: async () => { events.push("confirm"); return true; },
    onCloseAllowed: () => { events.push("closed"); },
  });
  q.recoverRenderer(["--reasonix-graphics-recovery"]);
  q.recoverRenderer(["duplicate"]);
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.equal(q.currentPhase, "completed");
  assert.deepEqual(events, ["confirm", "shutdown", "closed"]);
  assert.equal(calls.filter(c => c.startsWith("relaunch:")).length, 1);
  assert.ok(calls.includes("relaunch:--reasonix-graphics-recovery"));
});

test("cancelled recovery releases editing without leaving a stale relaunch armed", async () => {
  let q!: QuitSequencer, fail = true, shutdowns = 0, resumed = 0;
  const { app, calls } = fakeApp(() => q);
  q = new QuitSequencer({
    app, log: silent,
    service: { beforeClose: async () => false, shutdown: async () => { shutdowns++; } },
    flushRenderer: async () => { if (fail) throw new Error("renderer gone"); },
    confirmRecoveryDraftLoss: async () => false,
    resumeRenderer: async () => { resumed++; },
    onPrepareFailed: () => { assert.fail("recovery cancellation must not open another failure prompt"); },
    onCloseAllowed() {},
  });
  q.recoverRenderer(["--reasonix-graphics-recovery"]);
  await tick(); await tick();
  assert.equal(q.currentPhase, "idle"); assert.equal(shutdowns, 0);
  assert.equal(resumed, 1);
  fail = false; app.quit(); await tick(); await tick();
  assert.equal(shutdowns, 1);
  assert.equal(calls.some(c => c.startsWith("relaunch:")), false);
});

for (const lateFailure of [false, true]) {
  test(`a cancelled recovery's late draft ${lateFailure ? "failure" : "success"} cannot complete a newer quit`, async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const oldFlush = deferred(), nextFlush = deferred();
    let q!: QuitSequencer, saves = 0, shutdowns = 0;
    const { app, calls } = fakeApp(() => q);
    q = new QuitSequencer({
      app, log: silent, recoveryDraftTimeoutMs: 5,
      service: { beforeClose: async () => false, shutdown: async () => { shutdowns++; } },
      flushRenderer: () => ++saves === 1 ? oldFlush.promise : nextFlush.promise,
      confirmRecoveryDraftLoss: async () => false,
      resumeRenderer: async () => {}, onCloseAllowed() {},
    });
    q.recoverRenderer(["--reasonix-graphics-recovery"]);
    t.mock.timers.tick(5); await tick();
    assert.equal(q.currentPhase, "idle");
    app.quit();
    if (lateFailure) oldFlush.reject(new Error("late draft failure")); else oldFlush.resolve();
    await tick();
    assert.equal(shutdowns, 0, "the new quit must wait for its own draft flush");
    nextFlush.resolve(); await tick(); await tick();
    assert.equal(shutdowns, 1);
    assert.equal(calls.some(c => c.startsWith("relaunch:")), false);
  });
}

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function fakeApp(sequencer: () => QuitSequencer) {
  const calls: string[] = [];
  // Mirrors Electron: app.quit() re-enters before-quit until the sequencer lets it pass.
  const app = {
    quit: () => {
      calls.push("quit");
      if (sequencer().onBeforeQuit()) calls.push("exit");
    },
    relaunch: (args: string[], execPath?: string) => calls.push(`relaunch:${args.join(",")}${execPath ? `@${execPath}` : ""}`),
  };
  return { app, calls };
}

function build(options: { prevent?: boolean; beforeCloseError?: Error; flushError?: Error; } = {},) {
  const log: string[] = [];
  let sequencer!: QuitSequencer;
  const { app, calls } = fakeApp(() => sequencer);
  const service = {
    beforeClose: async (reason: string) => {
      log.push(`beforeClose:${reason}`);
      if (options.beforeCloseError) throw options.beforeCloseError;
      return options.prevent ?? false;
    },
    shutdown: async () => {
      log.push("shutdown");
    },
  };
  sequencer = new QuitSequencer({
    service,
    app,
    flushRenderer: async () => {
      log.push("flush");
      if (options.flushError) throw options.flushError;
    },
    resumeRenderer: async () => { log.push("resume"); },
    onCloseAllowed: () => log.push("closeAllowed"),
    log: silent,
  });
  return { sequencer, app, calls, log };
}

test("a plain quit asks Go, shuts the service down once, then exits", async () => {
  const { sequencer, app, calls, log } = build();
  app.quit();
  assert.equal(sequencer.currentPhase, "preparing");
  await tick();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "quit", "quit", "exit"]);
  assert.equal(sequencer.currentPhase, "completed");
});

test("Go can veto the quit; the shell stays running", async () => {
  const { sequencer, app, calls, log } = build({ prevent: true });
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "resume"]);
  assert.deepEqual(calls, ["quit"]);
  assert.equal(sequencer.currentPhase, "idle");
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "resume", "flush", "beforeClose:quit", "resume"], "a later quit asks again");
});

test("re-entrant quits while asking do not ask twice", async () => {
  const { app, calls, log } = build({ prevent: true });
  app.quit();
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "resume"]);
  assert.deepEqual(calls, ["quit", "quit"]);
});

test("repeated native window closes share one draft save and one shutdown", async () => {
  const saved = deferred();
  const events: string[] = [];
  let q!: QuitSequencer;
  const { app, calls } = fakeApp(() => q);
  q = new QuitSequencer({
    service: {
      beforeClose: async () => { events.push("beforeClose"); return false; },
      shutdown: async () => { events.push("shutdown"); },
    },
    app,
    flushRenderer: async () => { events.push("flush"); await saved.promise; },
    onCloseAllowed: () => events.push("closeAllowed"),
    log: silent,
  });

  const first = q.requestWindowClose();
  const second = q.requestWindowClose();
  assert.equal(first, second);
  assert.deepEqual(events, ["flush"]);
  saved.resolve();
  await Promise.all([first, second]);
  await tick();
  await tick();

  assert.deepEqual(events, ["flush", "beforeClose", "shutdown", "closeAllowed"]);
  assert.equal(events.filter(event => event === "shutdown").length, 1);
  assert.equal(calls.at(-1), "exit");
});

test("an explicit quit upgrades an in-flight background window close", async () => {
  const policy = deferred<boolean>();
  const events: string[] = [];
  const reasons: string[] = [];
  let q!: QuitSequencer;
  const { app } = fakeApp(() => q);
  q = new QuitSequencer({
    service: {
      beforeClose: async () => { events.push("beforeClose"); return policy.promise; },
      shutdown: async (reason) => { reasons.push(reason ?? ""); events.push("shutdown"); },
    },
    app,
    flushRenderer: async () => { events.push("flush"); },
    resumeRenderer: async () => { events.push("resume"); },
    onWindowClosePrevented: () => events.push("hidden"),
    onCloseAllowed: () => events.push("closeAllowed"),
    log: silent,
  });

  const close = q.requestWindowClose();
  await tick();
  q.requestQuit("system_signal");
  policy.resolve(true);
  await close;
  await tick();
  await tick();

  assert.deepEqual(reasons, ["system_signal"]);
  assert.equal(events.filter(event => event === "flush").length, 1);
  assert.equal(events.filter(event => event === "shutdown").length, 1);
  assert.ok(!events.includes("hidden"));
  assert.ok(!events.includes("resume"));
});

test("a background close restores draft editing, hides, and remains reusable", async () => {
  const events: string[] = [];
  let policyChecks = 0;
  let q!: QuitSequencer;
  const { app } = fakeApp(() => q);
  q = new QuitSequencer({
    service: {
      beforeClose: async () => { policyChecks++; return true; },
      shutdown: async () => { events.push("shutdown"); },
    },
    app,
    flushRenderer: async () => { events.push("flush"); },
    resumeRenderer: async () => { events.push("resume"); },
    onWindowClosePrevented: () => events.push("hidden"),
    onCloseAllowed: () => events.push("closeAllowed"),
    log: silent,
  });

  await q.requestWindowClose();
  assert.deepEqual(events, ["flush", "resume", "hidden"]);
  assert.equal(q.currentPhase, "idle");
  assert.equal(q.isQuitting, false);
  await q.requestWindowClose();
  assert.equal(policyChecks, 2, "a reopened window can close to background again");
  assert.deepEqual(events, ["flush", "resume", "hidden", "flush", "resume", "hidden"]);
  assert.ok(!events.includes("shutdown"));
});

for (const trigger of ["quit", "relaunch"] as const) {
  test(`${trigger} during background resume upgrades only after the preparation releases ownership`, async () => {
    const resumed = deferred();
    const events: string[] = [];
    let q!: QuitSequencer;
    const { app, calls } = fakeApp(() => q);
    q = new QuitSequencer({
      service: {
        beforeClose: async () => true,
        shutdown: async reason => { events.push(`shutdown:${reason}`); },
      }, app,
      flushRenderer: async () => { events.push("flush"); },
      resumeRenderer: async () => { events.push("resume"); await resumed.promise; },
      onWindowClosePrevented: () => events.push("hidden"), onCloseAllowed() {}, log: silent,
    });
    const closing = q.requestWindowClose();
    await tick();
    assert.deepEqual(events, ["flush", "resume"]);
    assert.equal(q.currentPhase, "preparing");
    if (trigger === "quit") q.requestQuit("system_signal");
    else q.relaunch(["--updated"]);
    assert.equal(q.requestWindowClose(), closing);
    resumed.resolve();
    await closing;
    await tick();
    assert.deepEqual(events, ["flush", "resume", "flush", `shutdown:${trigger === "quit" ? "system_signal" : "update_restart"}`]);
    assert.equal(q.currentPhase, "completed");
    assert.equal(calls.at(-1), "exit");
  });
}

for (const approved of [false, true]) {
  test(`quit during draft failure prompt can retry (${approved ? "approved" : "window"} close)`, async () => {
    const prompt = deferred();
    let saves = 0, prompts = 0, shutdowns = 0;
    let q!: QuitSequencer;
    const { app } = fakeApp(() => q);
    q = new QuitSequencer({
      service: { beforeClose: async () => false, shutdown: async () => { shutdowns++; } }, app,
      flushRenderer: async () => { if (++saves === 1) throw new Error("draft conflict"); },
      onPrepareFailed: async () => { prompts++; await prompt.promise; },
      onCloseAllowed() {}, log: silent,
    });
    if (approved) q.approve(); else void q.requestWindowClose();
    await tick();
    assert.equal(prompts, 1);
    assert.equal(q.currentPhase, "preparing");
    q.requestQuit(); q.requestQuit(); void q.requestWindowClose();
    assert.equal(saves, 1);
    prompt.resolve();
    await tick();
    assert.equal(saves, 2);
    assert.equal(shutdowns, 1);
    assert.equal(q.currentPhase, "completed");
  });
}

test("a quit received while a veto resumes editing starts a new policy check", async () => {
  const resumed = deferred();
  let checks = 0, saves = 0, shutdowns = 0;
  let q!: QuitSequencer;
  const { app } = fakeApp(() => q);
  q = new QuitSequencer({
    service: { beforeClose: async () => ++checks === 1, shutdown: async () => { shutdowns++; } }, app,
    flushRenderer: async () => { saves++; }, resumeRenderer: () => resumed.promise,
    onCloseAllowed() {}, log: silent,
  });
  q.requestQuit(); await tick();
  q.requestQuit(); resumed.resolve(); await tick();
  assert.equal(checks, 2);
  assert.equal(saves, 2);
  assert.equal(shutdowns, 1);
  assert.equal(q.currentPhase, "completed");
});

test("host/app.quit approval skips beforeClose and goes straight to shutdown", async () => {
  const { sequencer, calls, log } = build({ prevent: true });
  sequencer.approve();
  await tick();
  assert.deepEqual(log, ["flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "quit", "exit"]);
});

test("a failed beforeClose does not trap the user in a shell that cannot quit", async () => {
  const { app, calls, log } = build({ beforeCloseError: new Error("service dead"), });
  app.quit();
  await tick();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "shutdown", "closeAllowed"]);
  assert.equal(calls[calls.length - 1], "exit");
});

test("a failed renderer flush cancels quit before Go is asked", async () => {
  const { sequencer, app, calls, log } = build({ flushError: new Error("draft conflict"), });
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush"]);
  assert.deepEqual(calls, ["quit"]);
  assert.equal(sequencer.currentPhase, "idle");
  assert.equal(sequencer.isQuitting, false);
});

test("direct approval also withholds shutdown when renderer flush fails", async () => {
  const { sequencer, calls, log } = build({ flushError: new Error("draft conflict"), });
  sequencer.approve();
  await tick();
  assert.deepEqual(log, ["flush"]);
  assert.deepEqual(calls, ["quit"]);
  assert.equal(sequencer.currentPhase, "idle");
  assert.equal(sequencer.isQuitting, false);
});

test("relaunch runs the shutdown and re-spawns with the requested args", async () => {
  const { sequencer, calls, log } = build();
  sequencer.relaunch(["--after-update"]);
  await tick();
  assert.deepEqual(log, ["flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "relaunch:--after-update", "quit", "exit"]);
});

test("relaunch waits for shutdown then starts the committed stable launcher", async () => {
  const { sequencer, calls, log } = build();
  sequencer.relaunch(["--after-update"], "/opt/reasonix/reasonix-launcher");
  assert.deepEqual(calls, ["quit"]);
  await tick();
  assert.deepEqual(log, ["flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "relaunch:--after-update@/opt/reasonix/reasonix-launcher", "quit", "exit"]);
});

test("the first exit trigger keeps ownership of the shutdown reason", async () => {
  const reasons: string[] = [];
  let q!: QuitSequencer;
  const { app } = fakeApp(() => q);
  q = new QuitSequencer({
    service: {
      beforeClose: async () => false,
      shutdown: async (reason) => { reasons.push(reason ?? ""); },
    },
    app,
    flushRenderer: async () => {},
    onCloseAllowed() {},
    log: silent,
  });

  q.relaunch(["--after-update"]);
  q.requestQuit("system_signal");
  await tick();
  await tick();

  assert.deepEqual(reasons, ["update_restart"]);
});

test("cleanup failure cannot skip later cleanup or the final quit deadline", async () => {
  const events: string[] = [];
  let deadline: (() => void) | undefined;
  let q!: QuitSequencer;
  q = new QuitSequencer({
    service: { beforeClose: async () => false, shutdown: async () => { events.push("stopped"); }, },
    app: { quit: () => { if (q.onBeforeQuit()) events.push("quit"); }, exit: () => events.push("forced"), relaunch() {}, },
    onCloseAllowed: () => { throw new Error("window cleanup"); },
    cleanup: [{ name: "tray", run: () => { events.push("tray"); }, },],
    schedule: (run, ms) => { assert.equal(ms, 5000); deadline = run; }, log: silent,
  });
  q.approve(); await tick();
  assert.deepEqual(events, ["stopped", "tray", "quit"]);
  deadline?.(); assert.equal(events.at(-1), "forced");
});

test("service termination failure withholds shell exit and permits an exit retry", async () => {
  let attempts = 0;
  let exits = 0;
  let resumes = 0;
  let q!: QuitSequencer;
  q = new QuitSequencer({
    service: { beforeClose: async () => false, shutdown: async () => { if (++attempts === 1) throw new Error("child alive"); }, },
    app: { quit: () => { if (q.onBeforeQuit()) exits++; }, relaunch() {}, },
    resumeRenderer: async () => { resumes++; },
    onCloseAllowed() {}, log: silent,
  });
  q.approve(); await tick();
  assert.equal(exits, 0); assert.equal(q.isQuitting, true); assert.equal(q.currentPhase, "failed");
  assert.equal(resumes, 0);
  q.approve();
  await tick();
  assert.equal(exits, 1);
});

test("shutdown failure prompt can retry without restoring renderer editing", async () => {
  let attempts = 0;
  let prompts = 0;
  let exits = 0;
  let
  q!: QuitSequencer;
  q = new QuitSequencer({
    service: {
      beforeClose: async () => false,
      shutdown: async () => {
        if (++attempts === 1) throw new Error("disk full");
      },
    },
    app: {
      quit: () => {
        if (q.onBeforeQuit()) exits++;
      },
      relaunch() {},
    },
    onShutdownFailed: async (message) => {
      prompts++;
      assert.match(message, /disk full/);
      return true;
    },
    onCloseAllowed() {},
    log: silent,
  });
  q.approve(); await tick();
  await tick();
  await tick(); assert.equal(attempts, 2);
  assert.equal(prompts, 1);
  assert.equal(exits, 1);
  assert.equal(q.currentPhase, "completed");
});

test("repeated close and quit requests share one pending shutdown failure prompt", async () => {
  const prompt = deferred<boolean>();
  let attempts = 0;
  let prompts = 0;
  let q!: QuitSequencer;
  q = new QuitSequencer({
    service: {
      beforeClose: async () => false,
      shutdown: async () => { attempts++; throw new Error("save failed"); },
    },
    app: { quit: () => { q.onBeforeQuit(); }, relaunch() {}, },
    onShutdownFailed: async () => { prompts++; return prompt.promise; },
    onCloseAllowed() {},
    log: silent,
  });

  q.approve();
  await tick();
  assert.equal(prompts, 1);
  q.requestWindowClose();
  q.requestQuit();
  assert.equal(attempts, 1);
  assert.equal(prompts, 1);
  prompt.resolve(false);
  await tick();
  assert.equal(q.currentPhase, "failed");
});
