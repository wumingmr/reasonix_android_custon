import type { PresentedFile, WireEvent } from "./types";
import type { State } from "./useController";
import { t } from "./i18n";
import { RECOVERABLE_ERROR_EVENT } from "./globalCrashHandlers";

export interface AutoPresentedHTMLClaim {
  file: PresentedFile;
  toolCallId: string;
}

/** Runtime-only gate for the first HTML present result in each live turn. */
export class AutoPresentedHTMLGate {
  private readonly claimedTurnByTab = new Map<string, string>();

  observeTurnStart(tabId: string): void {
    this.claimedTurnByTab.delete(tabId);
  }

  claim(event: WireEvent, tabId: string, activeTabId: string | undefined, sessionGeneration: number, activeTurnId?: string, turnRevision = 0): AutoPresentedHTMLClaim | null {
    if (event.kind !== "tool_result" || event.tool?.err || !event.tool?.id || tabId !== activeTabId) return null;
    if (event.tool.name !== "present" && event.tool.resolvedName !== "present") return null;
    const file = event.tool.presentedFiles?.find((candidate) => /\.html?$/i.test(candidate.path));
    if (!file) return null;
    const turnKey = `${turnRevision}:${sessionGeneration}:${activeTurnId ?? event.turnId ?? "active"}`;
    if (this.claimedTurnByTab.get(tabId) === turnKey) return null;
    this.claimedTurnByTab.set(tabId, turnKey);
    return { file, toolCallId: event.tool.id };
  }
}

export async function openAutomaticPresentedHTML(
  gate: AutoPresentedHTMLGate,
  event: WireEvent,
  input: { tabId: string; activeTabId?: string; sessionGeneration: number; activeTurnId?: string; turnRevision: number },
): Promise<string | null> {
  const claim = gate.claim(event, input.tabId, input.activeTabId, input.sessionGeneration, input.activeTurnId, input.turnRevision);
  if (!claim) return null;
  const { openResource } = await import("./fileNavigationCommands");
  const outcome = await openResource({
    source: "presented", hostId: "local", tabId: input.tabId,
    toolCallId: claim.toolCallId, path: claim.file.path,
    sessionGeneration: input.sessionGeneration,
  }, { view: "preview" });
  return outcome.status === "failed" ? outcome.error.message : null;
}

const automaticGate = new AutoPresentedHTMLGate();

/** Keep the live-event controller small; all preview policy and failures stay in this lazy chunk. */
export default async function handleAutomaticPresentedHTML(
  event: WireEvent,
  tabId: string,
  activeTab: { current?: string },
  states: { current: Map<string, State> },
): Promise<void> {
  if (event.kind === "turn_done") {
    automaticGate.observeTurnStart(tabId);
    return;
  }
  const report = (error: unknown) => window.dispatchEvent(new CustomEvent(RECOVERABLE_ERROR_EVENT, { detail: {
    message: t("chat.fileActionFailed", { error: error instanceof Error ? error.message : String(error) }),
  } }));
  try {
    const activeTabId = activeTab.current;
    const state = states.current.get(tabId);
    if (!state?.meta || state.meta.remote) return;
    const error = await openAutomaticPresentedHTML(automaticGate, event, {
      tabId,
      activeTabId,
      sessionGeneration: state.meta.sessionGeneration ?? event.sessionGeneration ?? 0,
      activeTurnId: state.activeTurnId,
      turnRevision: 0,
    });
    if (error) report(error);
  } catch (error) {
    report(error);
  }
}
