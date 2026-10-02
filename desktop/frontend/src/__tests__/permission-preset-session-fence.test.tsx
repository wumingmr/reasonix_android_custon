// Run: tsx src/__tests__/permission-preset-session-fence.test.tsx
//
// A preset choice is read from one session's permission snapshot. When the tab
// navigates to another session before the choice is applied, the choice must
// neither apply to nor be recorded for the session the tab now shows. The
// permission revision cannot tell the two apart: every freshly opened session
// starts from the same number.

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { useController } from "../lib/useController";
import type { AppBindings } from "../lib/bridge";
import type { ContextInfo, EffortInfo, Meta, TabMeta } from "../lib/types";
import { installDesktopHostStub } from "./desktopHostStub";

let failed = 0;
function eq(actual: unknown, expected: unknown, label: string) {
  const pass = JSON.stringify(actual) === JSON.stringify(expected);
  process.stdout.write(`  ${pass ? "PASS" : "FAIL"}  ${label}${pass ? "" : `: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`}\n`);
  if (!pass) failed += 1;
}

console.log("\npermission preset session fence");

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.CustomEvent = dom.window.CustomEvent;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);

const flushPromises = () => new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 0));

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

// The host side of one tab: the session it shows, each session's live preset
// and revision, and the per-session records a restart would restore.
// session-b sits at the revision session-a reaches after its first choice.
const sessions: Record<string, { preset: string; revision: number }> = {
  "session-a": { preset: "workspace-write", revision: 2 },
  "session-b": { preset: "workspace-write", revision: 3 },
};
const records: Record<string, string> = {};
let current = "session-a";
let navigateAfterSnapshot = false;
let setCalls = 0;
let metaReads = 0;

function applyChoice(preset: string, expectedRevision: number) {
  const live = sessions[current];
  if (live.revision !== expectedRevision) throw new Error("permission revision changed");
  live.preset = preset;
  live.revision += 1;
  records[current] = preset;
}

function tabMeta(): TabMeta {
  return {
    id: "tab-a", scope: "project", workspaceRoot: "/repo", workspaceName: "repo", workspacePath: "/repo",
    topicId: "topic-a", topicTitle: "General", sessionId: current, label: "model", ready: true,
    running: false, cancellable: false, mode: "normal", toolApprovalMode: sessions[current].preset,
    tokenMode: "full", active: true, cwd: "/repo",
  } as TabMeta;
}

function metaForTab(): Meta {
  return {
    label: "model", ready: true, eventChannel: "agent:event", cwd: "/repo", workspaceRoot: "/repo",
    workspaceName: "repo", workspacePath: "/repo", autoApproveTools: false, bypass: false,
    collaborationMode: "normal", toolApprovalMode: sessions[current].preset, tokenMode: "full",
    goal: "", goalStatus: "stopped", session: { hostId: "local", sessionId: current },
  } as Meta;
}

const context: ContextInfo = { used: 0, window: 100, sessionTokens: 0 };
const effortInfo: EffortInfo = { supported: true, current: "auto", default: "auto", levels: ["auto"] };

installDesktopHostStub(({
  main: {
    App: {
      RegisterNavigationIntent: async () => {},
      ListTabs: async () => [tabMeta()],
      MetaForTab: async () => {
        metaReads += 1;
        return metaForTab();
      },
      ContextUsageForTab: async () => context,
      EffortForTab: async () => effortInfo,
      BalanceForTab: async () => ({ available: false, display: "" }),
      JobsForTab: async () => [],
      CheckpointsForTab: async () => [],
      ForkTargetsForTab: async () => ({ targets: [], verifiable: false }),
      HistoryForTab: async () => [],
      HistoryPageForTab: async () => ({ messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false }),
      HistoryCheckpointTurnsForTab: async () => [],
      ReplayPendingPrompts: async () => {},
      SetActiveTab: async () => {},
      PermissionSnapshotForTab: async () => {
        const snapshot = {
          sessionId: current, generation: 1, revision: sessions[current].revision, preset: sessions[current].preset,
          workspaceRoot: "/repo", grants: [],
          capabilities: { backend: "seatbelt", enforcement: "full", supportedPresets: ["read-only", "workspace-write", "danger-full-access"] },
        };
        if (navigateAfterSnapshot) current = "session-b";
        return snapshot;
      },
      SetPermissionPresetForTab: async (...args: unknown[]) => {
        setCalls += 1;
        if (args.length < 4) {
          // A caller that names no session is fenced by the revision alone.
          const [, preset, expectedRevision] = args as [string, string, number];
          applyChoice(preset, expectedRevision);
        } else {
          const [, expectedSessionID, preset, expectedRevision] = args as [string, string, string, number];
          if (expectedSessionID !== current) throw new Error("reasonix_error:permission_session_changed");
          applyChoice(preset, expectedRevision);
        }
        return { sessionId: current, generation: 1, revision: sessions[current].revision, preset: sessions[current].preset, workspaceRoot: "/repo", grants: [] };
      },
      SetComposerProfileForTab: async () => [],
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

let controller: ReturnType<typeof useController> | undefined;
function Probe() {
  controller = useController();
  return null;
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);
await act(async () => {
  root.render(<Probe />);
  await flushPromises();
});
await waitFor("active tab", () => controller?.activeTabId === "tab-a");

await act(async () => {
  await controller?.setToolApprovalModeForTab("tab-a", "read-only");
  await flushPromises();
});
eq(sessions["session-a"].preset, "read-only", "a choice read and applied in the same session lands on it");
eq(records["session-a"], "read-only", "that choice is recorded for the session it was made in");

navigateAfterSnapshot = true;
const setCallsBefore = setCalls;
const metaReadsBefore = metaReads;
let thrown: unknown;
await act(async () => {
  try {
    await controller?.setToolApprovalModeForTab("tab-a", "danger-full-access");
  } catch (error) {
    thrown = error;
  }
  await flushPromises();
});
eq(current, "session-b", "the tab navigated between the snapshot and the set");
eq(sessions["session-b"].preset, "workspace-write", "the stale choice is not applied to the session the tab now shows");
eq(records["session-b"], undefined, "the stale choice is not recorded for the session the tab now shows");
eq(sessions["session-a"].preset, "read-only", "the session the choice was read from is untouched");
eq(setCalls - setCallsBefore, 1, "the refused choice is not retried on the new session");
eq(thrown === undefined, true, "a session change is a settled refusal, not a failure the caller sees");
eq(metaReads > metaReadsBefore, true, "the refusal re-reads the tab's state");

await act(async () => {
  root.unmount();
});
if (failed > 0) {
  console.log(`\n${failed} failed`);
  process.exit(1);
}
console.log("\nall passed");
process.exit(0);
