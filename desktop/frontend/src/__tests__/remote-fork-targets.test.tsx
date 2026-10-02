// Run: tsx src/__tests__/remote-fork-targets.test.tsx
// A remote fork reads the serve's persisted turn records and creates the child
// through the create-only command. A serve that cannot create one is refused on
// its advertised capability: the switching /fork route must never run, because
// it rebinds the parent session the user is still reading.
import { register } from "node:module";
// The surface module graph imports an SVG asset; register the stub loader
// before any dynamic import of the remote surface runs.
register(new URL("../../scripts/svg-loader.mjs", import.meta.url));
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { app } from "../lib/bridge";
import { useRemoteSession, type RemoteSessionApi } from "../lib/useRemoteSession";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0;
let failed = 0;
function ok(value: unknown, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nremote fork targets");

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });

// The capability is the tab's own advertised field, and the surface must read it
// rather than inferring support from how many targets the read returned.
const surfaceSource = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../components/RemoteSessionSurface.tsx"), "utf8");
const hookSource = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../lib/useRemoteSession.ts"), "utf8");
ok(/forkBlocked=\{tab\.forkTargetsSupported \? null : "unsupported"\}/.test(surfaceSource),
  "the serve's advertised capability decides the unsupported reason");
ok(/onFork=\{tab\.forkTargetsSupported \?/.test(surfaceSource),
  "an unsupported serve offers no fork entry at all");

const TAB = "remote-fork-1";
const calls: string[] = [];
let targetView: unknown = { targets: [], verifiable: false };
let createView: unknown = { opened: true, sessionId: "child-1", operationId: "operation-1" };
const forkTarget = (turnId: string) => ({ sourceSessionId: "parent-1", sessionGeneration: 1, turnId,
  boundarySequence: 9, turnNumber: 1, status: "committed", available: true });

const commands = {
  RemoteTabSnapshot: async (tabId: string) => { calls.push(`snapshot:${tabId}`); return { history: [] }; },
  RemoteTabMetadata: async () => ({ history: [] }),
  RemoteTabStatus: async (tabId: string) => { calls.push(`status:${tabId}`); return { plan: false, toolApprovalMode: "ask", goal: "", label: "m", running: false }; },
  ForkTargetsRemoteTab: async (tabId: string) => { calls.push(`targets:${tabId}`); return targetView; },
  CreateForkRemoteTab: async (tabId: string, target: ReturnType<typeof forkTarget>) => {
    calls.push(`create:${tabId}:${target.turnId}:${target.sourceSessionId}:${target.boundarySequence}`);
    return createView;
  },
  AcknowledgeForkOperation: async (tabId: string, operationId: string) => { calls.push(`ack:${tabId}:${operationId}`); },
  ForkRemoteTab: async (tabId: string, turn: number) => { calls.push(`switching:${tabId}:${turn}`); },
};

const desktopStub = installDesktopHostStub(commands as unknown as typeof app);
const PARENT_SESSION = "/sessions/parent.jsonl";
let sessionPath: string | undefined = PARENT_SESSION;
let probe: RemoteSessionApi | undefined;
function Harness() { probe = useRemoteSession(TAB, undefined, sessionPath); return null; }
const root = createRoot(document.getElementById("root")!);
const settle = async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 40)); }); };
const fork = async (target: ReturnType<typeof forkTarget>) => {
  let result: Awaited<ReturnType<RemoteSessionApi["forkTurn"]>> = undefined;
  await act(async () => { result = await probe!.forkTurn(target); });
  return result;
};

try {
  await act(async () => { root.render(<Harness />); });
  // The serve publishes readiness; hydration starts from that transition.
  await act(async () => { desktopStub.emit(`remote-tab:${TAB}:state`, { state: "ready" }); });
  await settle();
  ok(probe?.hydrated === true, "a ready serve hydrates the transcript");
  ok(calls.includes(`targets:${TAB}`), "hydration reads the serve's fork targets through the same command the local path uses");
  targetView = { sourceSessionId: "parent-1", sessionGeneration: 1,
    targets: [{ ...forkTarget("turn-1"), messageId: "msg-1" }], verifiable: true };
  await act(async () => { await probe!.retryHydration(); });
  await settle();
  ok(probe?.transcript.forkTargets?.targets[0]?.messageId === "msg-1", "the serve's target identity reaches the transcript");
  // The identical command the local path uses, so both surfaces carry the
  // serve's own target set into the shared reducer.
  ok(!/targets.length > 0 \|\| .*verifiable/.test(hookSource), "support is never inferred from the target list");
  ok((hookSource.match(/forkTargetsRefreshRef\.current\?\.\(\)/g) ?? []).length >= 2,
    "the read refreshes once hydration lands and once a turn finishes");

  calls.length = 0;
  ok((await fork(forkTarget("turn-1")))?.sessionId === "child-1", "a created child returns the serve's session identity");
  const operations = calls.filter((call) => call.startsWith("create:")).map((call) => call.split(":")[3]);
  const turnIds = calls.filter((call) => call.startsWith("create:")).map((call) => call.split(":")[2]);
  ok(operations.length === 1 && operations[0] === "parent-1", "the create carries the observed source identity");
  ok(turnIds[0] === "turn-1", "the create carries the turn identity, not a display index");
  ok(!calls.some((call) => call.startsWith("switching:")), "the switching route is never reached");

  // A refusal keeps the serve's reason, localized, and creates no second child.
  createView = { opened: false, code: "fork_unavailable", reason: "turn_open", error: "turn is open" };
  calls.length = 0;
  ok((await fork(forkTarget("turn-2"))) === undefined, "a refused turn opens nothing");
  await settle();
  ok(probe!.promptError.includes("not finished yet"), "the refusal reason reaches the user through the surface's own alert");
  ok(!probe!.promptError.includes("turn_open"), "the reason token itself is not shown");
  // The serve keeps "this boundary cannot be proven" and "this boundary is
  // proven but unsafe" apart, so the surface must not report the second as the
  // first: only one of them is an absent boundary.
  createView = { opened: false, code: "fork_unavailable", reason: "active_authority", error: "authority remains active" };
  ok((await fork(forkTarget("turn-2"))) === undefined, "a proven but unsafe boundary starts no child");
  await settle();
  ok(probe!.promptError.includes("question or approval"), "an unsafe boundary keeps its own reason");
  ok(!probe!.promptError.includes("no verifiable branch boundary"), "a proven boundary is not reported as unverifiable");
  createView = { opened: false, code: "fork_unavailable", reason: "stale_source", error: "source changed" };
  ok((await fork(forkTarget("turn-2"))) === undefined, "a stale source starts no child");
  await settle();
  ok(probe!.promptError.includes("session changed"), "stale_source uses its localized explanation");
  // A child the serve published comes back even when its surface did not open,
  // so the caller can open it; the child it could not open is remembered.
  createView = { opened: false, sessionId: "child-9", operationId: "operation-9", error: "conversation fork was created but could not be opened" };
  ok((await fork(forkTarget("turn-3")))?.sessionId === "child-9", "a created child is returned so its surface can be opened");
  await settle();
  ok(probe!.promptError === "", "a published child is not a failure of the create");
  ok(!calls.some((call) => call.startsWith("ack:")), "an unopened child remains unacknowledged for host recovery");

  // A remote fork navigates this same tab to its child. Its next anchored create
  // still goes through Desktop; no renderer child cache can answer it.
  calls.length = 0;
  createView = { opened: true, sessionId: "child-10", operationId: "operation-10" };
  sessionPath = "/sessions/child-9.jsonl";
  await act(async () => { root.render(<Harness />); });
  await settle();
  ok((await fork(forkTarget("turn-3")))?.sessionId === "child-10", "the current source anchor creates its own child");
  ok(calls.some((call) => call.startsWith("create:")), "the fork creates its own child in the session the tab shows");
} finally {
  await act(async () => { root.unmount(); });
  desktopStub.uninstall();
  dom.window.close();
}

process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
