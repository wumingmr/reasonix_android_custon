// The remote turn fork: the serve owns the child, while Desktop owns the
// durable operation id used to recover an unknown result. The renderer carries
// only the anchored target and acknowledges the operation after navigation.

import { useCallback, useEffect, useRef, type RefObject } from "react";
import { forkCreateFailureText, type ForkTargetSetView, type ForkTargetView } from "./forkTargets";
import { t } from "./i18n";
import { reducer, type State } from "./useController";
import type { ForkAnchorView, ForkCreationView } from "../generated/desktopContract.generated";

/** The bridge commands a remote turn fork needs, so a caller can supply its own. */
export interface RemoteForkBindings {
  ForkTargetsRemoteTab(tabID: string): Promise<ForkTargetSetView>;
  CreateForkRemoteTab(tabID: string, anchor: ForkAnchorView): Promise<ForkCreationView>;
  AcknowledgeForkOperation(tabID: string, operationID: string): Promise<void>;
}

/**
 * Reads a remote tab's fork boundaries, or undefined when this shell cannot ask.
 * The serve derives them from its own committed turn records, so the read is
 * valid while a turn runs, and an empty set means "no completed turn here" —
 * never "this serve cannot fork", which its advertised capability decides.
 */
export async function readRemoteForkTargets(bindings: RemoteForkBindings, tabId: string): Promise<ForkTargetSetView | undefined> {
  // A shell that predates the command reads no targets instead of failing the
  // transcript around it.
  if (typeof bindings.ForkTargetsRemoteTab !== "function") return undefined;
  const set = await bindings.ForkTargetsRemoteTab(tabId).catch(() => undefined);
  if (!set) return undefined;
  return { ...set, targets: Array.isArray(set.targets) ? set.targets : [], verifiable: Boolean(set.verifiable) };
}

/** What one remote fork request produced: the child's id, or the refusal to show the user. */
export type RemoteForkOutcome = { kind: "created"; sessionId: string; operationId: string } | { kind: "refused"; failure: string };

/** Creates or recovers one remote child through Desktop's durable operation. */
export async function requestRemoteForkTurn(
  bindings: RemoteForkBindings,
  tabId: string,
  target: ForkTargetView,
): Promise<RemoteForkOutcome> {
  try {
    const created = await bindings.CreateForkRemoteTab(tabId, target);
    if (created?.sessionId && created.operationId) return { kind: "created", sessionId: created.sessionId, operationId: created.operationId };
    // Structured reasons are localized here; ordinary diagnostics retain the
    // host's message.
    const detail = created?.error?.trim() ?? "";
    return { kind: "refused", failure: detail ? forkCreateFailureText(detail, created?.reason) : t("chat.branchFailed") };
  } catch (error) {
    return { kind: "refused", failure: forkCreateFailureText(error) };
  }
}

/** The anchored fork operations one remote session surface exposes. */
export interface RemoteForkTurnApi {
  /** Creates or recovers the child for one anchored operation. */
  forkTurn: (target: ForkTargetView) => Promise<{ sessionId: string; operationId: string } | undefined>;
  acknowledgeFork: (operationId: string) => Promise<void>;
  /**
   * The read the connection effect runs once hydration lands and once a turn
   * finishes. That effect owns its own install and uninstall per connection
   * generation, so it reaches the read through this ref rather than closing
   * over a callback identity it cannot depend on.
   */
  forkTargetsRefreshRef: RefObject<(() => Promise<void>) | null>;
}

/**
 * Owns one remote session's fork reads and creates. Each read is fenced by the
 * current session identity and a local sequence so a response from a replaced
 * connection cannot enter the transcript.
 */
export function useRemoteForkTurn(
  bindings: RemoteForkBindings,
  tabId: string | undefined,
  sessionPath: string | undefined,
  setTranscript: (update: (state: State) => State) => void,
  setPromptError: (error: string) => void,
): RemoteForkTurnApi {
  const session = sessionPath ?? "";
  const readSeqRef = useRef(0);
  const readIdentityRef = useRef("");
  readIdentityRef.current = `${tabId ?? ""}\0${session}`;
  const forkTargetsRefreshRef = useRef<(() => Promise<void>) | null>(null);
  const refreshForkTargets = useCallback(async (): Promise<void> => {
    if (!tabId) return;
    const readSeq = ++readSeqRef.current;
    const readIdentity = readIdentityRef.current;
    const targets = await readRemoteForkTargets(bindings, tabId);
    if (!targets || readSeqRef.current !== readSeq || readIdentityRef.current !== readIdentity) return;
    setTranscript((current) => reducer(current, { type: "fork_targets", targets }));
  }, [bindings, session, setTranscript, tabId]);
  useEffect(() => { forkTargetsRefreshRef.current = refreshForkTargets; }, [refreshForkTargets]);

  const forkTurn = useCallback(async (target: ForkTargetView): Promise<{ sessionId: string; operationId: string } | undefined> => {
    if (!tabId || !target?.turnId) return undefined;
    setPromptError("");
    const outcome = await requestRemoteForkTurn(bindings, tabId, target);
    if (outcome.kind === "created") return { sessionId: outcome.sessionId, operationId: outcome.operationId };
    setPromptError(outcome.failure);
    return undefined;
  }, [bindings, setPromptError, tabId]);

  const acknowledgeFork = useCallback(async (operationId: string) => {
    if (tabId && operationId) await bindings.AcknowledgeForkOperation(tabId, operationId);
  }, [bindings, tabId]);

  return { forkTurn, acknowledgeFork, forkTargetsRefreshRef };
}
