import { act } from "react";
import type { useController } from "../lib/useController";
import { runtimeStateStore, type RuntimeState } from "../lib/runtimeStateStore";
import type { Meta } from "../lib/types";
import type { DesktopHostStub } from "../__tests__/desktopHostStub";

type Controller = ReturnType<typeof useController>;

export async function runTodoSessionSwitchScenario(options: {
  controller: () => Controller | undefined;
  desktopStub: DesktopHostStub;
  currentSessionPath: () => string;
  meta: (overrides: Partial<Meta>) => Meta;
  equal: (actual: unknown, expected: unknown, label: string) => void;
}): Promise<void> {
  const { controller, desktopStub, currentSessionPath, meta, equal } = options;
  const pausedTodos = [{ content: "Finish session A", status: "in_progress" }];
  const sessionA = "/sessions/todo-a.jsonl", sessionB = "/sessions/todo-b.jsonl";
  desktopStub.commands.MetaForTab = async () => meta({
    sessionPath: currentSessionPath(), sessionGeneration: 1,
    canonicalTodos: currentSessionPath() === sessionA ? pausedTodos : [],
  });
  let revision = 0;
  const publish = (sessionPath: string, todos: typeof pausedTodos) => {
    const state: RuntimeState = {
      schemaVersion: 1, runtimeEpoch: "todo-runtime", activityRevision: 1, revision: ++revision,
      phase: "idle", running: false, turnId: "paused-turn", turnStatus: "interrupted", turnEventSeq: 1,
      pendingPrompt: false, cancelRequested: false, cancellable: false, backgroundJobs: 0, activity: "", todos,
    };
    runtimeStateStore.commit({ epoch: "todo-app", revision, topics: [], sessions: [{
      tabId: "tab-switch", scope: "project", workspaceRoot: "/repo", topicId: "topic-a",
      sessionPath, sessionGeneration: 1, open: true, remote: false, freshness: "synced", state,
    }] });
  };
  await act(async () => { const navigation = controller()?.resumeSession(sessionA, "tab-switch"); await navigation?.surfaceReady; publish(sessionA, pausedTodos); });
  equal(controller()?.state.meta?.canonicalTodos?.[0]?.content, "Finish session A", "paused A owns its unfinished todo");
  await act(async () => { const navigation = controller()?.resumeSession(sessionB, "tab-switch"); await navigation?.surfaceReady; });
  equal(controller()?.state.meta?.canonicalTodos?.length, 0, "B stays empty while A's runtime snapshot is still current in the feed");
  await act(async () => { publish(sessionB, []); const navigation = controller()?.resumeSession(sessionA, "tab-switch"); await navigation?.surfaceReady; });
  equal(controller()?.state.meta?.canonicalTodos?.[0]?.content, "Finish session A", "returning to A cannot be cleared by B's delayed empty snapshot");
  await act(async () => { publish(sessionB, []); });
  equal(controller()?.state.meta?.canonicalTodos?.[0]?.content, "Finish session A", "late B publication after the return to A cannot erase A's todo");
  await act(async () => { publish(sessionA, [{ content: "Finish session A", status: "completed" }]); });
  equal(controller()?.state.meta?.canonicalTodos?.[0]?.status, "completed", "a matching runtime snapshot still publishes live todo progress");
  await act(async () => { publish(sessionA, []); });
  equal(controller()?.state.meta?.canonicalTodos?.length, 0, "an authoritative empty list for the same session still clears todos");
}
