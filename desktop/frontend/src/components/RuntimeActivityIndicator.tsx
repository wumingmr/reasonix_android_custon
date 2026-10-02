import { useSyncExternalStore } from "react";
import { runtimeStateStore, selectRuntime } from "../lib/runtimeStateStore";
import { useT } from "../lib/i18n";
import { asArray } from "../lib/array";
import type { ProjectNode } from "../lib/types";

/**
 * Workspace identity the runtime store is matched against. Sidebars differ in
 * how they list sessions, so the activity rule lives here instead of being
 * reimplemented per surface; `topics` only supplies human labels and
 * `fallbackActive` only covers surfaces with no runtime snapshot yet.
 */
export type RuntimeActivityTarget = {
  scope: "global" | "project";
  root: string;
  remote?: { hostId: string; workspace: string };
  topics?: ProjectNode[];
  fallbackActive?: boolean;
};

export default function RuntimeActivityIndicator({ target }: { target: RuntimeActivityTarget }) {
  const snapshot = useSyncExternalStore(runtimeStateStore.subscribe, runtimeStateStore.getSnapshot);
  const failed = useSyncExternalStore(runtimeStateStore.subscribe, runtimeStateStore.getFailed);
  const t = useT();
  const matched = snapshot?.sessions.filter(session => target.remote
    ? session.remote && session.hostId === target.remote.hostId && session.workspaceRoot === target.remote.workspace
    : session.scope === target.scope && (target.scope === "global" || session.workspaceRoot === target.root));
  const sessions = matched && [...new Map(matched.map(session => [`${session.hostId ?? "local"}\0${session.workspaceRoot}\0${session.sessionPath}`, session])).values()];
  if (sessions) {
    const active = sessions.filter(session => {
      const view = selectRuntime(session, failed);
      return view.unknown || (view.known && (view.kind !== "idle" || session.state.backgroundJobs > 0));
    });
    if (!active.length) return null;
    const details = active.map(session => {
      const state = session.state;
      const topic = asArray(target.topics).find(node => node.topicId === session.topicId || (session.sessionPath && node.sessionPath === session.sessionPath));
      const labels: string[] = [];
      if (failed || session.freshness !== "synced") labels.push(t("runtime.unknown"));
      if (state.phase === "finishing") labels.push(t("runtime.finishing"));
      if (state.cancelRequested) labels.push(t("status.jobStopping"));
      if (state.pendingPrompt) labels.push(t("projectTree.status.waitingConfirmation"));
      if (state.phase === "executing") labels.push(t(state.activity === "streaming" ? "projectTree.status.streaming" : "projectTree.status.thinking"));
      if (state.backgroundJobs) labels.push(t("runtime.background", { count: state.backgroundJobs }));
      return `${topic?.label || session.tabId}: ${labels.join(" · ")}`;
    }).join("; ");
    const spinning = active.some(session => selectRuntime(session, failed).spinning);
    return <span className={`runtime-activity-indicator${spinning ? "" : " runtime-activity-indicator--static"}`} style={spinning ? undefined : { animation: "none" }} role="status" aria-label={details} title={details} />;
  }
  if (!target.fallbackActive) return null;
  return <span className="runtime-activity-indicator" role="status" aria-label={t("projectTree.status.thinking")} />;
}
