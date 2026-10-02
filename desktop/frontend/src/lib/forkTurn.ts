// Forking a persisted turn: the child is created from one committed turn record
// and adopted without rewinding or switching the source tab, which is what
// separates this path from the switching fork in forkWorktree.ts. The host
// persists an unacknowledged operation, so an unknown result recovers the same
// child across renderer and Desktop restarts.

import { asArray } from "./array";
import { app } from "./bridge";
import { errorMessage } from "./controllerNotices";
import { forkCreateFailureText, type ForkTargetSetView, type ForkTargetView } from "./forkTargets";
import { t } from "./i18n";
import type { TabMeta } from "./types";
import type { ForkAnchorView, ForkCreationView } from "../generated/desktopContract.generated";

/** The bridge commands a turn fork needs, so a caller can supply its own. */
export interface ForkTurnBindings {
  ListTabs(): Promise<TabMeta[]>;
  CreateForkForTab(tabID: string, anchor: ForkAnchorView): Promise<ForkCreationView>;
  AcknowledgeForkOperation(tabID: string, operationID: string): Promise<void>;
}

/** The fork slice of a tab's controller state. */
export interface ForkTurnState {
  /** Persisted fork boundaries of the shown session; undefined until the first read resolves. */
  forkTargets?: ForkTargetSetView;
  /** A create-fork request for this tab is in flight. */
  forkCreating: boolean;
}

/** A tab that never forked holds no targets and has no request in flight. */
export const initialForkTurnState: ForkTurnState = { forkCreating: false };

/** The fork actions a tab's reducer accepts, alongside every other action it handles. */
export type ForkTurnAction =
  | { type: "fork_targets"; targets: ForkTargetSetView }
  | { type: "fork_creating"; creating: boolean }

/** The fork slice's response to one fork action; an unchanged answer returns the same state. */
export function reduceForkTurn(state: ForkTurnState, action: ForkTurnAction): ForkTurnState {
  switch (action.type) {
    case "fork_targets": return { ...state, forkTargets: action.targets };
    case "fork_creating": return state.forkCreating === action.creating ? state : { ...state, forkCreating: action.creating };
  }
}

/**
 * The per-tab read of fork boundaries. The host derives them from persisted turn
 * records, so the answer stays valid while a turn runs; a read that resolves
 * after a newer one for the same tab is dropped, because the newer read is the
 * one that reflects the current source.
 */
export function createForkTargetsRefresh(dispatch: (tabId: string, action: ForkTurnAction) => void): {
  invalidate(tabId: string): void;
  refresh(tabId: string): Promise<void>;
} {
  const latestSeq = new Map<string, number>();
  const invalidate = (tabId: string): number => {
    const seq = (latestSeq.get(tabId) ?? 0) + 1;
    latestSeq.set(tabId, seq);
    return seq;
  };
  const refresh = async (tabId: string): Promise<void> => {
    // A shell that predates the command reads no targets instead of failing the
    // transcript around it.
    if (typeof app.ForkTargetsForTab !== "function") return;
    const seq = invalidate(tabId);
    const targets = await app.ForkTargetsForTab(tabId).catch(() => undefined);
    if (latestSeq.get(tabId) !== seq || targets === undefined) return;
    dispatch(tabId, { type: "fork_targets", targets: { ...targets, targets: asArray(targets.targets), verifiable: Boolean(targets.verifiable) } });
  };
  return { invalidate, refresh };
}

/** The notice a refused fork shows the user, dispatched like a fork action. */
export type ForkTurnNotice = { type: "local_notice"; level: "info" | "warn"; text: string };

/** What one fork request needs from the tab that owns it. */
export interface ForkTurnStep {
  /** Applies a fork action, or the notice a refusal shows, to the source tab's state. */
  dispatch(action: ForkTurnAction | ForkTurnNotice): void;
  /** Brings the tab the host opened for the child to the foreground. */
  adopt(tab: TabMeta): Promise<unknown>;
  /** Re-reads the active tab from the backend, for a child that opened without being adopted. */
  sync(): Promise<unknown>;
  /** Resolves once the given tab's runtime accepts a fork. */
  waitForTabReady(tabId: string): Promise<unknown>;
}

// The host answers with the opened tab's id rather than its meta, so the tab
// list supplies the identity, with one moment for the registry to publish a tab
// the host reports as already open.
async function listedTab(bindings: ForkTurnBindings, tabId: string): Promise<TabMeta | undefined> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    const tab = asArray(await bindings.ListTabs().catch(() => [] as TabMeta[])).find((candidate) => candidate.id === tabId);
    if (tab) return tab;
    await new Promise((resolve) => window.setTimeout(resolve, 50));
  }
  return undefined;
}

/**
 * Creates an independent child session from one persisted turn of a source tab
 * and adopts the tab the host opened for it. The source keeps its transcript,
 * its running turn, and its controller, so this path never rewinds or switches
 * the parent. Every failure reaches the user through the same notice channel
 * the switching fork used.
 */
export async function settleForkTurnForTab(
  bindings: ForkTurnBindings,
  sourceTabId: string,
  target: ForkTargetView,
  step: ForkTurnStep,
): Promise<boolean> {
  if (!sourceTabId || !target?.turnId) return false;
  step.dispatch({ type: "fork_creating", creating: true });
  try {
    await step.waitForTabReady(sourceTabId);
    const created = await bindings.CreateForkForTab(sourceTabId, target);
    if (created?.opened && created.tabId) {
      const tab = await listedTab(bindings, created.tabId);
      if (tab) {
        await step.adopt(tab);
        if (created.operationId) await bindings.AcknowledgeForkOperation(sourceTabId, created.operationId);
        return true;
      }
    }
    // The child becomes durable before its tab opens, so it is remembered and
    // recovered by name: creating it again would publish a second fork of the
    // same turn.
    if (created?.sessionId) {
      step.dispatch({ type: "local_notice", level: "warn", text: t("chat.branchRecoverChild", { session: created.sessionId }) });
      if (created.opened) await step.sync();
      return false;
    }
    const detail = errorMessage(created?.error ?? "");
    step.dispatch({ type: "local_notice", level: "warn", text: created?.reason
      ? forkCreateFailureText(detail, created.reason)
      : detail ? t("chat.branchFailedDetail", { detail }) : t("chat.branchFailed") });
    return false;
  } catch (error) {
    step.dispatch({ type: "local_notice", level: "warn", text: forkCreateFailureText(error) });
    return false;
  } finally {
    step.dispatch({ type: "fork_creating", creating: false });
  }
}
