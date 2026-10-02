import assert from "node:assert/strict";
import type { IpcMain } from "electron";
import { test } from "node:test";
import { IPC, type IpcResult } from "../shared/ipc.js";
import { parseContract } from "./contract.js";
import { isOpenableExternalURL, parseRendererDiagnostic, registerRendererIpc } from "./ipc.js";
import { RpcError } from "./rpc.js";

function fakeIpc() {
  const handlers = new Map<string, (event: unknown, ...args: unknown[]) => Promise<IpcResult>>();
  const listeners = new Map<string, (event: { sender: unknown; senderFrame: unknown; returnValue?: unknown }) => void>();
  const ipcMain = {
    handle: (channel: string, fn: (event: unknown, ...args: unknown[]) => Promise<IpcResult>) => handlers.set(channel, fn),
    on: (channel: string, fn: (event: { sender: unknown; senderFrame: unknown; returnValue?: unknown }) => void) => listeners.set(channel, fn),
  } as unknown as IpcMain;
  return { ipcMain, handlers, listeners };
}

test("external URLs are limited to http, https and mailto", () => {
  assert.equal(isOpenableExternalURL("https://example.com/x"), true);
  assert.equal(isOpenableExternalURL("http://example.com"), true);
  assert.equal(isOpenableExternalURL("mailto:a@b.c"), true);
  assert.equal(isOpenableExternalURL("file:///etc/passwd"), false);
  assert.equal(isOpenableExternalURL("javascript:alert(1)"), false);
  assert.equal(isOpenableExternalURL("reasonix://app/"), false);
  assert.equal(isOpenableExternalURL("not a url"), false);
  assert.equal(isOpenableExternalURL(42), false);
});

test("renderer transcript diagnostics accept only the bounded stable schema", () => {
  const valid = {
    kind: "transcript", event: "failure", stage: "delta_validate", reason: "revision_gap", transport: "local", errorType: "classified",
    revision: 12, commit: 8, attempts: 1, failures: 2, durationMs: 1500,
  };
  assert.deepEqual(parseRendererDiagnostic(valid), valid);
  assert.throws(() => parseRendererDiagnostic({ ...valid, reason: "raw server response" }), /invalid renderer diagnostic value/);
  assert.throws(() => parseRendererDiagnostic({ ...valid, prompt: "secret" }), /invalid renderer diagnostic field/);
  assert.throws(() => parseRendererDiagnostic({ ...valid, failures: -1 }), /invalid renderer diagnostic failures/);
  assert.throws(() => parseRendererDiagnostic({ ...valid, prompt: "x".repeat(2100) }), /payload too large/);
});

test("renderer invokes are gated by sender identity and the contract allowlist", async (t) => {
  let now = 1_000;
  t.mock.method(Date, "now", () => now);
  const { ipcMain, handlers, listeners } = fakeIpc();
  const trustedSender = { id: 1 };
  const trustedFrame = {};
  const invoked: Array<{ method: string; args: unknown[] }> = [];
  const nativeCalls: string[] = [];
  const logMessages: string[] = [];
  const warningMessages: string[] = [];
  let diagnosticReads = 0;
  const performanceCalls: string[] = [];
  registerRendererIpc({
    ipcMain,
    contract: parseContract({ digest: "sha256:a", commands: ["OpenProjectTab", "MinimiseMainWindow", "ToggleMaximiseMainWindow", "IsMainWindowMaximised", "CloseMainWindow"] }),
    window: {
      isTrustedSender: (sender, frame) => sender === trustedSender && frame === trustedFrame,
      minimise() { nativeCalls.push("minimise"); },
      toggleMaximise() { nativeCalls.push("toggle"); },
      isMaximised: () => true,
      close() { nativeCalls.push("close"); },
      bounds: () => ({ x: 1, y: 2, width: 3, height: 4, maximised: false }),
      setTheme() {},
      setBackgroundColour() {},
      getAppZoom: async () => 1,
      setAppZoom: async () => 1,
      resetAppZoom: async () => 1,
    },
    serviceState: () => ({ phase: "ready" as const, generation: "g-test" }),
    processDiagnostics: () => { diagnosticReads++; return { scope: "electron", samples: [] }; },
    performance: {
      captureRendererProfile: async () => { performanceCalls.push("capture"); return { status: "captured" }; },
      cancelRendererProfile: () => { performanceCalls.push("cancel"); },
      exportHeapSnapshot: async () => { performanceCalls.push("heap"); return { status: "cancelled" }; },
      dispose() {},
    },
    invoke: async (method, args) => {
      invoked.push({ method, args });
      if (method === "OpenProjectTab" && args[0] === "/missing") throw new Error("workspace not found");
      if (args[0] === "/pending-settings") throw new RpcError(-32000,"settings pending",{submissionOutcome:"not_accepted",modelApplication:{code:"model_settings_pending"}});
      return { opened: args[0] };
    },
    clipboard: { writeText: async () => undefined, readText: async () => "clip" },
    openExternal: async () => undefined,
    log: { info: (message) => logMessages.push(message), warn: (message) => warningMessages.push(message), error() {} },
  });
  const invoke = handlers.get(IPC.invoke);
  assert.ok(invoke);
  const trusted = { sender: trustedSender, senderFrame: trustedFrame };
  const diagnostics = handlers.get(IPC.processDiagnostics)!;
  assert.deepEqual(await diagnostics({ sender: trustedSender, senderFrame: {} }), { ok: false, message: "untrusted sender" });
  assert.equal(diagnosticReads, 0);
  assert.deepEqual(await diagnostics(trusted), { ok: true, value: { scope: "electron", samples: [] } });
  assert.equal(diagnosticReads, 1);
  for (const channel of [IPC.captureRendererProfile, IPC.cancelRendererProfile, IPC.exportHeapSnapshot]) {
    assert.deepEqual(await handlers.get(channel)!({ sender: trustedSender, senderFrame: {} }), { ok: false, message: "untrusted sender" });
  }
  assert.deepEqual(performanceCalls, []);
  await handlers.get(IPC.cancelRendererProfile)!(trusted);
  assert.deepEqual(performanceCalls, [], "unscoped IPC cancellation is ignored");
  for (const channel of [IPC.captureRendererProfile, IPC.cancelRendererProfile, IPC.exportHeapSnapshot]) await handlers.get(channel)!(trusted, "test-request");
  assert.deepEqual(performanceCalls, ["capture", "cancel", "heap"]);
  assert.deepEqual(await invoke({ sender: { id: 9 }, senderFrame: trustedFrame }, "OpenProjectTab", ["/p"]), { ok: false, message: "untrusted sender" });
  assert.deepEqual(await invoke({ sender: trustedSender, senderFrame: {} }, "OpenProjectTab", ["/p"]), { ok: false, message: "untrusted sender" });
  assert.deepEqual(await invoke(trusted, "OpenProjectTab", ["/p"]), { ok: true, value: { opened: "/p" } });
  assert.deepEqual(await invoke(trusted, "OpenProjectTab", ["/missing"]), { ok: false, message: "workspace not found" });
  assert.deepEqual(await invoke(trusted,"OpenProjectTab",["/pending-settings"]),{ok:false,message:"settings pending",code:-32000,data:{submissionOutcome:"not_accepted",modelApplication:{code:"model_settings_pending"}}});
  assert.deepEqual(await invoke(trusted, "MinimiseMainWindow", []), { ok: true, value: undefined });
  assert.deepEqual(await invoke(trusted, "ToggleMaximiseMainWindow", []), { ok: true, value: undefined });
  assert.deepEqual(await invoke(trusted, "IsMainWindowMaximised", []), { ok: true, value: true });
  assert.deepEqual(await invoke(trusted, "CloseMainWindow", []), { ok: true, value: undefined });
  assert.deepEqual(nativeCalls, ["minimise", "toggle", "close"]);
  assert.deepEqual(await invoke(trusted, "DeleteEverything", []), { ok: false, message: "-32601 method not found: DeleteEverything",code:-32601,data:undefined });
  assert.deepEqual(await invoke(trusted, "__proto__", []), { ok: false, message: "-32601 method not found: __proto__",code:-32601,data:undefined });
  assert.deepEqual(invoked.map((call) => call.method), ["OpenProjectTab", "OpenProjectTab", "OpenProjectTab"]);

  const contract = listeners.get(IPC.contract);
  assert.ok(contract);
  const event: { sender: unknown; senderFrame: unknown; returnValue?: unknown } = { ...trusted };
  contract(event);
  assert.deepEqual(event.returnValue, {
    protocolVersion: 1,
    digest: "sha256:a",
    commands: ["OpenProjectTab", "MinimiseMainWindow", "ToggleMaximiseMainWindow", "IsMainWindowMaximised", "CloseMainWindow"],
  });
  const untrusted: { sender: unknown; senderFrame: unknown; returnValue?: unknown } = { sender: { id: 9 }, senderFrame: trustedFrame };
  contract(untrusted);
  assert.equal(untrusted.returnValue, null);

  const open = handlers.get(IPC.openExternal);
  assert.ok(open);
  assert.deepEqual(await open(trusted, "file:///etc/passwd"), { ok: false, message: "refusing to open file:///etc/passwd" });
  assert.deepEqual(await open(trusted, "https://example.com"), { ok: true, value: undefined });
  const bounds = handlers.get(IPC.windowGetBounds);
  assert.ok(bounds);
  assert.deepEqual(await bounds(trusted), { ok: true, value: { x: 1, y: 2, width: 3, height: 4, maximised: false } });
  const read = handlers.get(IPC.clipboardRead);
  assert.ok(read);
  assert.deepEqual(await read(trusted), { ok: true, value: "clip" });

  const rendererDiagnostic = handlers.get(IPC.rendererDiagnostic)!;
  const diagnostic = {
    kind: "transcript", event: "failure", stage: "delta_validate", reason: "revision_gap", transport: "local", errorType: "classified",
    revision: 12, commit: 8, attempts: 1, failures: 2, durationMs: 1500,
  };
  assert.deepEqual(await rendererDiagnostic({ sender: { id: 9 }, senderFrame: trustedFrame }, diagnostic), { ok: false, message: "untrusted sender" });
  assert.deepEqual(await rendererDiagnostic(trusted, diagnostic), { ok: true, value: undefined });
  assert.match(logMessages.at(-1) ?? "", /stage=delta_validate reason=revision_gap type=classified.*service=ready generation=g-test/);
  assert.deepEqual(await rendererDiagnostic(trusted, { ...diagnostic, response: "raw" }), { ok: false, message: "invalid renderer diagnostic field" });
  for (let index = 0; index < 10; index++) await rendererDiagnostic(trusted, diagnostic);
  assert.equal(logMessages.length, 10, "only ten renderer diagnostics are accepted in one second");
  now = 2_000;
  await rendererDiagnostic(trusted, diagnostic);
  assert.ok(warningMessages.some(message => message === "renderer diagnostics rate limited dropped=1"));
  assert.equal(logMessages.length, 11, "the next rate window accepts diagnostics again");
});
