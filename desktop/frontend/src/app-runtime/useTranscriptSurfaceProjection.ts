import { useLayoutEffect } from "react";
import { useCommittedCommand } from "../lib/useCommittedCommand";
import type { useNavigationSurface } from "../lib/useNavigationSurface";
import type { HistoryLoadOutcome, HistoryLoadTrigger, Item, State } from "../lib/useController";
import type { SessionAvailability } from "../lib/sessionAvailability";
import type { NavigateToTurn, TurnNavigationTarget } from "../lib/historyTurnNavigation";

type NavigationSurfaceApi = ReturnType<typeof useNavigationSurface>;

export type TranscriptSurfaceProjectionInput = {
  hydrating: boolean;
  hydrateHistoryLoaded: boolean | undefined;
  hydratePlaceholderItems: Item[] | undefined;
  hydratePlaceholderActive: boolean;
  items: Item[];
  guidanceConsumed?: State["guidanceConsumed"];
  remote: boolean;
  remoteItems: Item[];
  activeTabId: string | undefined;
  geometrySessionKey: string;
  transitioning: boolean;
  navigationDataReady: boolean;
  preserved: NavigationSurfaceApi["preserved"];
  controllerReady: boolean;
  availability: SessionAvailability;
  sessionActivity: boolean;
  imDetailActive: boolean;
  sessionHasContent: boolean;
  commitRendered: NavigationSurfaceApi["commitRendered"];
  commitPaint: NavigationSurfaceApi["commitPaint"];
  commitSingleSurface: (tabId: string) => void;
  ports: {
    navigateToTurn?(tabId: string, target: TurnNavigationTarget, current: () => boolean): ReturnType<NavigateToTurn>;
    loadOlderHistory(tabId: string, targetTurn: number | undefined, trigger: HistoryLoadTrigger): Promise<HistoryLoadOutcome>;
    loadNewerHistory(tabId: string, latest: boolean, current?: () => boolean): Promise<HistoryLoadOutcome>;
    commitThenSend(tabId: string, displayText: string, submitText?: string): Promise<void>;
  };
};

/**
 * Owns the transcript surface projection: hydration placeholders, the
 * empty-session hero gate, the committed-surface commit effect (only
 * committed presentation may become a retained source), the visible
 * source-retained surface selection, surface paint receipts, the latest
 * consumed guidance entry and the transcript prompt/load-older commands.
 */
export function useTranscriptSurfaceProjection(input: TranscriptSurfaceProjectionInput) {
  const { activeTabId, transitioning, ports } = input;
  const transcriptHydrating = input.hydrating && !input.hydrateHistoryLoaded;
  // Show the hero only after history hydration settles on a truly empty session.
  // Avoid flash while switching tabs: items may be empty while placeholders show.
  // Exclude IM/Bot detail: hero CSS collapses .main, which also hosts that panel.
  const emptyHero =
    input.availability.kind === "ready" &&
    !input.sessionActivity &&
    !transitioning &&
    !input.imDetailActive &&
    !input.sessionHasContent &&
    !transcriptHydrating &&
    !input.hydratePlaceholderActive;
  const transcriptItems = input.hydratePlaceholderActive ? input.hydratePlaceholderItems! : input.items;
  const handleLoadOlderHistory = useCommittedCommand((targetTurn?: number, trigger: HistoryLoadTrigger = "retry") => {
    return activeTabId ? ports.loadOlderHistory(activeTabId, targetTurn, trigger) : Promise.resolve("empty" as const);
  });
  const handleLoadNewerHistory = useCommittedCommand((latest = false, current?: () => boolean) => {
    return activeTabId ? ports.loadNewerHistory(activeTabId, latest, current) : Promise.resolve("empty" as const);
  });
  const handleNavigateToTurn = useCommittedCommand((target: TurnNavigationTarget, current: () => boolean) => {
    return activeTabId && !transitioning && ports.navigateToTurn ? ports.navigateToTurn(activeTabId, target, current) : Promise.resolve("cancelled" as const);
  });

  // Display items: backend history is authoritative after immediate commit.
  // rewindState only drives the undo banner, not optimistic truncation.
  const displayItems = transcriptItems;
  const committedSurfaceItems = input.remote ? input.remoteItems : displayItems;
  const committedGeometryKey = input.remote ? `tab:${activeTabId ?? "preview"}` : input.geometrySessionKey;
  // Only committed presentation can become a future retained source surface.
  // A suspended or abandoned render must never become navigation authority.
  const commitRendered = input.commitRendered;
  useLayoutEffect(() => {
    if (transitioning) return;
    commitRendered({
      tabId: activeTabId,
      items: committedSurfaceItems,
      geometrySessionKey: committedGeometryKey,
    });
  }, [activeTabId, commitRendered, committedSurfaceItems, transitioning, committedGeometryKey]);
  const visibleTranscriptSurface = transitioning && !input.navigationDataReady && input.preserved
    ? input.preserved
    : null;
  const visibleTranscriptItems = visibleTranscriptSurface?.items ?? displayItems;
  const visibleTranscriptTabId = visibleTranscriptSurface?.tabId ?? activeTabId;
  const visibleTranscriptGeometryKey = visibleTranscriptSurface?.geometrySessionKey ?? input.geometrySessionKey;
  const handleSurfacePaintReady = useCommittedCommand((token: string, outcome: "ready" | "degraded") => {
    const receipt = input.commitPaint(token, outcome);
    if (receipt) input.commitSingleSurface(receipt.targetTabId);
  });
  const latestGuidanceConsumed = input.guidanceConsumed ?? null;

  const handleTranscriptPrompt = useCommittedCommand((text: string, submitText = text) => {
    if (!activeTabId || !input.controllerReady) return;
    void ports.commitThenSend(activeTabId, text, submitText).catch((err) => {
      console.warn("Failed to submit transcript prompt", err);
    });
  });

  return {
    transcriptHydrating,
    emptyHero,
    visibleTranscriptItems,
    visibleTranscriptTabId,
    visibleTranscriptGeometryKey,
    handleLoadOlderHistory,
    handleLoadNewerHistory,
    handleNavigateToTurn,
    handleSurfacePaintReady,
    latestGuidanceConsumed,
    handleTranscriptPrompt,
  };
}
