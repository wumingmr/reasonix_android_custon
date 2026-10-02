// Fork eligibility comes from the host's persisted turn records, never from
// checkpoints: a checkpoint knows a message count, while a turn record proves an
// atomic commit boundary. One transcript turn maps to its boundary through the
// assistant message identity, so live completion, history paging, and cold
// restore all resolve the same target.

import type { ForkAnchorView, ForkCreationView, ForkTargetSetView, ForkTargetView } from "../generated/desktopContract.generated";
import { t, type DictKey } from "./i18n";

export type { ForkTargetSetView, ForkTargetView };

/** The create-only fork commands the host binds per surface: local tabs and remote ones. */
export interface ForkTargetsBindings {
  ForkTargetsForTab(tabID: string): Promise<ForkTargetSetView>;
  CreateForkForTab(tabID: string, anchor: ForkAnchorView): Promise<ForkCreationView>;
  AcknowledgeForkOperation(tabID: string, operationID: string): Promise<void>;
}

/**
 * Why one transcript turn's fork entry offers no fork. Each state keeps its own
 * explanation, because the collapsed "unavailable" it replaces could not tell a
 * running turn from legacy history or from a surface that cannot create a child.
 */
export type ForkBlockReason = "loading" | "turn_open" | "unverifiable" | "active_authority" | "stale_source" | "read_only" | "unsupported" | "creating";

const FORK_REASON_KEYS: Record<ForkBlockReason, DictKey> = {
  loading: "chat.branchLoading",
  turn_open: "chat.branchTurnOpen",
  unverifiable: "chat.branchUnverifiable",
  active_authority: "chat.branchActiveAuthority",
  stale_source: "chat.branchStaleSource",
  read_only: "chat.branchReadOnly",
  unsupported: "chat.branchUnsupported",
  creating: "chat.branchCreating",
};

/** The locale key explaining one block reason, shared by the tooltip and its screen-reader text. */
export function forkReasonKey(reason: ForkBlockReason): DictKey {
  return FORK_REASON_KEYS[reason];
}

/**
 * The persisted message identity a transcript item key names, or undefined when
 * the key carries none. Assistant and user items are keyed `m:<messageId>`;
 * history entries without a message id keep a positional key that names no
 * durable message, so no boundary can be resolved from it.
 */
export function forkAnswerMessageId(itemKey: string | undefined): string | undefined {
  return itemKey?.startsWith("m:") ? itemKey.slice(2) : undefined;
}

/**
 * The persisted boundary for one turn's answer. Identity is the only match: an
 * array position or a page offset moves to another turn as soon as history is
 * prepended or a page is reloaded.
 */
export function forkTargetForAnswer(set: ForkTargetSetView | undefined, answerKey: string | undefined): ForkTargetView | undefined {
  const messageId = forkAnswerMessageId(answerKey);
  if (!messageId || !set) return undefined;
  return set.targets.find((target) => target.messageId === messageId);
}

/**
 * The refusal one target carries, or null when it may start a child. Each reason
 * the host names keeps its own explanation: a boundary it could not prove and
 * one it refused as unsafe are different facts, and only the first is an absent
 * boundary.
 */
export function forkTargetReason(target: ForkTargetView): ForkBlockReason | null {
  if (target.available) return null;
  if (target.reason === "turn_open") return "turn_open";
  return target.reason === "active_authority" ? "active_authority" : "unverifiable";
}

/**
 * The block reason for one tail node, or null when its target may fork. An
 * unmatched answer means the source proves boundaries but holds none for this
 * message: the open turn before its reply exists, or a turn that committed no
 * reply at all.
 */
export function forkBlockReason(input: {
  target: ForkTargetView | undefined;
  loaded: boolean;
  verifiable: boolean;
  blocked: ForkBlockReason | null;
  latest: boolean;
}): ForkBlockReason | null {
  if (input.blocked) return input.blocked;
  if (!input.loaded) return "loading";
  if (!input.verifiable) return "unverifiable";
  if (!input.target) return input.latest ? "turn_open" : "unverifiable";
  return forkTargetReason(input.target);
}

/** The notice text for a refused create-fork request. */
export function forkCreateFailureText(error: unknown, reason?: string): string {
  const detail = error instanceof Error ? error.message : String(error ?? "");
  const blockReason: ForkBlockReason | undefined = reason === "history_unverifiable" ? "unverifiable"
    : reason === "turn_open" || reason === "active_authority" || reason === "stale_source" || reason === "unsupported" ? reason : undefined;
  return blockReason ? t("chat.branchFailedDetail", { detail: t(forkReasonKey(blockReason)) }) : t("chat.branchFailedDetail", { detail });
}
