import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { ArrowDown } from "lucide-react";
import { getTranscriptStore } from "../lib/transcriptStore";
import type { ControllerLiveStore, HistoryLoadOutcome, HistoryLoadTrigger, Item, LiveStream } from "../lib/useController";
import type { LocalSubmission } from "../lib/localSubmissionState";
import { forkTargetForAnswer, type ForkBlockReason, type ForkTargetSetView, type ForkTargetView } from "../lib/forkTargets";
import type { InvocationMetadataMap } from "../lib/invocationDisplay";
import type { WireCompletionSummary } from "../lib/types";
import { acquireMarkdownWorkerClient, releaseMarkdownWorkerClient } from "../lib/markdownWorkerClient";
import { ChatSource } from "../lib/chatViewSource";
import { ChatScrollController } from "../lib/chatScrollController";
import { ChatContentLoader } from "../lib/chatContentLoader";
import { ChatMountedOrder } from "../lib/chatMountedOrder";
import { ChatTurnJump } from "../lib/chatTurnJump";
import type { NavigateToTurn } from "../lib/historyTurnNavigation";
import { desktopHost } from "../lib/desktopHost";
import { findLoadedTurn, indexLoadedTurns } from "../lib/chatTurnRail";
import { signalOutline } from "../lib/transcriptOutlineSignals";
import { addBreadcrumb } from "../lib/breadcrumbs";
import { useT } from "../lib/i18n";
import { InvocationMetadataContext } from "./Message";
import { MarkdownImageTabContext } from "./MarkdownImageContext";
import { ChatFileScopeProvider } from "./ChatFileLinkContext";
import { ChatDetails, ChatNodeList, ChatRunning, type ChatActions } from "./ChatNodes";
import { Welcome } from "./Welcome";
import { SessionLoadingIndicator } from "./SessionLoadingIndicator";
import "./ChatTranscript.css";
const ChatTurnNavigator = lazy(() => import("./ChatTurnNavigator"));
export { NoticeCard } from "./TranscriptCards";

export type TranscriptProps = {
  onNavigateToTurn?: NavigateToTurn;
  items: Item[];
  localSubmissions?: readonly LocalSubmission[];
  localSubmissionSendRevision?: number;
  visibleSubmissionHandoffs?: Readonly<Record<string, { submissionId: string }>>;
  live?: LiveStream;
  liveStore?: ControllerLiveStore;
  tabId?: string;
  hostId?: string;
  geometrySessionKey?: string;
  footerHeight?: number;
  onPrompt: (displayText: string, submitText?: string) => void;
  onFork?: (target: ForkTargetView) => void;
  onOpenTurnChanges?: (summary: WireCompletionSummary, initialPath?: string) => void;
  /** Persisted fork boundaries of the shown session; undefined until the first read resolves. */
  forkTargets?: ForkTargetSetView;
  /** Non-null replaces every fork entry's own state, e.g. a surface that cannot create a child. */
  forkBlocked?: ForkBlockReason | null;
  running?: boolean;
  hydrating?: boolean;
  /** Shell surfaces own one shared loading indicator across empty/content states. */
  showLoadingFeedback?: boolean;
  hasOlderHistory?: boolean;
  hasNewerHistory?: boolean;
  historyStartTurn?: number;
  historyEndTurn?: number;
  /** Total turns the snapshot reports, used to keep the rail area while the
   * outline loads without showing it on a brand-new conversation. */
  totalTurns?: number;
  loadingOlderHistory?: boolean;
  olderHistoryError?: string;
  onLoadOlderHistory?: (targetTurn?: number, trigger?: HistoryLoadTrigger) => HistoryLoadOutcome | boolean | Promise<HistoryLoadOutcome | boolean>;
  loadingNewerHistory?: boolean;
  newerHistoryError?: string;
  onLoadNewerHistory?: (latest?: boolean, current?: () => boolean) => HistoryLoadOutcome | boolean | Promise<HistoryLoadOutcome | boolean>;
  turnStartAt?: number;
  invocationMetadata?: InvocationMetadataMap;
  surfaceCommitToken?: string;
  onSurfacePaintReady?: (token: string, outcome: "ready" | "degraded") => void;
};


/** Local and remote hosts share this natural-flow presentation adapter. */
export function Transcript(props: TranscriptProps) {
  const sessionKey = props.geometrySessionKey || `tab:${props.tabId ?? "preview"}`;
  return <ChatSession key={sessionKey} {...props} sessionKey={sessionKey} />;
}

function TranscriptConnection({ tabId }: { tabId?: string }) {
  const store = getTranscriptStore();
  const subscribe = useCallback((listener: () => void) => tabId ? store.subscribeState(tabId, listener) : () => {}, [store, tabId]);
  const snapshot = useCallback(() => tabId ? store.states.get(tabId)?.transcriptConnection : undefined, [store, tabId]);
  const status = useSyncExternalStore(subscribe, snapshot, snapshot);
  const t = useT();
  return status && status !== "connected" ? <p role="status">{t(status === "syncing" ? "chat.syncing" : "chat.disconnected")}</p> : null;
}

function ChatSession(props: TranscriptProps & { sessionKey: string }) {
  const { sessionKey, tabId, items, live, liveStore, running = false, hydrating = false,
    hasOlderHistory = false, hasNewerHistory = false, loadingOlderHistory = false, olderHistoryError,
    loadingNewerHistory = false, newerHistoryError, turnStartAt,
    onLoadOlderHistory, onLoadNewerHistory, onPrompt, onFork, onSurfacePaintReady, surfaceCommitToken } = props;
  const t = useT();
  const [source] = useState(() => new ChatSource(sessionKey));
  const [mounts] = useState(() => new ChatMountedOrder());
  const order = useSyncExternalStore(source.subscribeOrder, source.getOrderSnapshot, source.getOrderSnapshot);
  const [scroll] = useState(() => new ChatScrollController(sessionKey));
  const loader = useMemo(() => new ChatContentLoader(tabId), [tabId]);
  const scroller = useRef<HTMLDivElement>(null);
  const column = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLElement | null>(null);
  const lifetime = useRef(0);
  const [details, setDetails] = useState<string>();
  const closeDetails = useCallback(() => { setDetails(undefined); }, []);
  const openDetails = useCallback((key: string, element: HTMLElement) => { trigger.current = element; setDetails(key); }, []);
  const recover = useCallback((id: string) => onPrompt(t("notice.protocolRecoveryAction"), `/recover-context ${id}`), [onPrompt, t]);
  const actions = useMemo<ChatActions>(() => ({ openDetails, recover, openTurnChanges: props.onOpenTurnChanges,
    fork: onFork ? {
      targetFor: (answerKey) => forkTargetForAnswer(props.forkTargets, answerKey),
      loaded: props.forkTargets !== undefined,
      verifiable: props.forkTargets?.verifiable ?? false,
      blocked: props.forkBlocked ?? null,
      create: onFork,
    } : undefined }), [openDetails, recover, onFork, props.forkTargets, props.forkBlocked, props.onOpenTurnChanges]);
  useLayoutEffect(() => {
    source.update({ items, live: props.hasNewerHistory ? undefined : liveStore?.getSnapshot(tabId) ?? live, running, hydrating,
      localSubmissions: props.hasNewerHistory ? [] : props.localSubmissions,
      visibleSubmissionHandoffs: props.visibleSubmissionHandoffs,
      hasOlder: hasOlderHistory, loadingOlder: loadingOlderHistory, error: olderHistoryError,
      startedAt: turnStartAt, historyStartTurn: props.historyStartTurn });
  }, [source, items, props.localSubmissions, props.visibleSubmissionHandoffs, live, liveStore, tabId, running, hydrating, hasOlderHistory, loadingOlderHistory, olderHistoryError, turnStartAt, props.historyStartTurn, props.hasNewerHistory]);
  useEffect(() => liveStore?.subscribe(tabId, () => source.updateLive(props.hasNewerHistory ? undefined : liveStore.getSnapshot(tabId))), [source, liveStore, tabId, props.hasNewerHistory]);
  useLayoutEffect(() => {
    if (scroller.current && column.current) scroll.attach(scroller.current, column.current);
    return () => scroll.dispose();
  }, [scroll]);
  useEffect(() => {
    loader.activate();
    acquireMarkdownWorkerClient();
    return () => { lifetime.current++; source.dispose(); mounts.dispose(); loader.dispose(); releaseMarkdownWorkerClient(); };
  }, [source, mounts, loader]);
  useLayoutEffect(() => {
    if (!hydrating) scroll.ready();
    scroll.layout();
  }, [scroll, items, hydrating, props.footerHeight, details]);
  useEffect(() => {
    if (hydrating || !surfaceCommitToken || (items.length > 0 && order.length === 0)) return;
    let paint = 0;
    const frame = requestAnimationFrame(() => { paint = requestAnimationFrame(() => onSurfacePaintReady?.(surfaceCommitToken, "ready")); });
    return () => { cancelAnimationFrame(frame); cancelAnimationFrame(paint); };
  }, [hydrating, surfaceCommitToken, onSurfacePaintReady, items.length, order.length]);
  const submissionRevision = props.localSubmissionSendRevision ?? 0;
  const previousSubmissionRevision = useRef(submissionRevision);
  useLayoutEffect(() => {
    if (previousSubmissionRevision.current !== submissionRevision && running && !hydrating && !props.hasNewerHistory) scroll.toBottom();
    previousSubmissionRevision.current = submissionRevision;
  }, [submissionRevision, running, hydrating, scroll, props.hasNewerHistory]);
  const position = useSyncExternalStore(scroll.subscribe, scroll.getSnapshot, scroll.getSnapshot);
  const activeDetails = details && source.getNodeSnapshot(details)?.kind === "tool" ? details : undefined;
  const drawerWasOpen = useRef(false);
  useLayoutEffect(() => {
    if (!activeDetails && drawerWasOpen.current) {
      (trigger.current?.isConnected ? trigger.current : scroller.current)?.focus({ preventScroll: true });
      trigger.current = null;
    }
    drawerWasOpen.current = Boolean(activeDetails);
  }, [activeDetails]);
  const [pagingError, setPagingError] = useState(false);
  const [selectionBlocked, setSelectionBlocked] = useState(false);
  // Deduplicate paging buttons; target navigation uses the store request fence.
  const pagingPromise = useRef<Promise<HistoryLoadOutcome> | null>(null);
  const navigationIntent = useRef(0);
  const selectionInsideTranscript = () => {
    const selection = window.getSelection?.();
    return Boolean(selection && !selection.isCollapsed && scroller.current &&
      ((selection.anchorNode && scroller.current.contains(selection.anchorNode)) ||
        (selection.focusNode && scroller.current.contains(selection.focusNode))));
  };
  useEffect(() => {
    const clear = () => { if (!selectionInsideTranscript()) setSelectionBlocked(false); };
    document.addEventListener("selectionchange", clear);
    return () => document.removeEventListener("selectionchange", clear);
  }, []);
  const loadPage = (direction: "older" | "newer" | "latest", trigger: HistoryLoadTrigger = "viewport-user", current?: () => boolean): Promise<HistoryLoadOutcome> => {
    if (pagingPromise.current && direction !== "latest") return pagingPromise.current;
    const load = direction === "older" ? () => onLoadOlderHistory?.(undefined, trigger) : () => onLoadNewerHistory?.(direction === "latest", current);
    if (direction === "older" ? !onLoadOlderHistory : !onLoadNewerHistory) return Promise.resolve("empty");
    if (selectionInsideTranscript()) { setSelectionBlocked(true); return Promise.resolve("empty"); }
    const generation = lifetime.current;
    setPagingError(false);
    setSelectionBlocked(false);
    scroll.beforeChange();
    const run = (async (): Promise<HistoryLoadOutcome> => {
      try {
        // A host that still answers with a plain boolean is normalized here.
        const result = await load();
        if (result === true) return "loaded";
        if (result === false) return "empty";
        return result ?? "empty";
      } catch {
        if (generation === lifetime.current) setPagingError(true);
        return "empty";
      }
    })();
    pagingPromise.current = run;
    void run.finally(() => { if (pagingPromise.current === run) pagingPromise.current = null; });
    return run;
  };
  const loadOlder = (trigger: HistoryLoadTrigger = "viewport-user") => loadPage("older", trigger);
  // The jump outlives a single render, so it reads the live paging state
  // through refs rather than through the closure it was built with.
  const navigateRef = useRef(props.onNavigateToTurn); navigateRef.current = props.onNavigateToTurn;
  const selectionRef = useRef(selectionInsideTranscript); selectionRef.current = selectionInsideTranscript;
  const lifetimeRef = useRef(lifetime.current); lifetimeRef.current = lifetime.current;
  // Rebuild the mounted identity index only when the mount advances, then
  // resolve each target from it in constant time: scanning the mounted order
  // per outline entry is quadratic on long conversations.
  const jump = useMemo(() => new ChatTurnJump({
    mounts, scroll,
    navigate: async (target, current) => {
      if (selectionRef.current()) { setSelectionBlocked(true); return "cancelled"; }
      if (!current()) return "cancelled";
      return navigateRef.current?.(target, () => current() && !selectionRef.current()) ?? "unavailable";
    },
    resolveKey: (entry) => {
      const order = mounts.getSnapshot();
      const index = indexLoadedTurns(order, (key) => {
            const node = source.getNodeSnapshot(key);
            return node?.kind === "user" ? { id: node.item.id, messageId: node.item.messageId } : undefined;
      });
      return findLoadedTurn(index, { id: `m:${entry.messageId}`, messageId: entry.messageId, turn: 0, order: 0, prompt: "" });
    },
    // Refresh a stale cut once while retaining the original message identity.
    refresh: async () => { if (tabId) await (await import("../lib/transcriptOutlineStore")).getTranscriptOutlineStore().refresh(tabId); },
    isCurrent: () => lifetimeRef.current === lifetime.current,
  }), [mounts, scroll, source, tabId]);
  const jumpState = useSyncExternalStore(jump.subscribe, jump.getSnapshot, jump.getSnapshot);
  const returnToLatest = async () => {
    jump.cancel();
    const intent = ++navigationIntent.current;
    if (!hasNewerHistory) { scroll.toBottom(); return; }
    const generation = lifetime.current;
    let reading = false;
    const release = scroll.subscribeReaderIntent(() => { reading = true; });
    const current = () => !reading && intent === navigationIntent.current && generation === lifetime.current && !selectionRef.current();
    try {
      const result = await loadPage("latest", "retry", current);
      if (result === "loaded" && current()) scroll.toBottom();
    } finally { release(); }
  };
  const newerArmed = useRef(true);
  const autoNewer = useRef<() => void>(() => undefined);
  autoNewer.current = () => {
    if (!onLoadNewerHistory || selectionInsideTranscript()) return;
    newerArmed.current = false;
    const intent = ++navigationIntent.current;
    const generation = lifetime.current;
    let reading = false;
    const release = scroll.subscribeReaderIntent(() => { reading = true; });
    const current = () => !reading && intent === navigationIntent.current && generation === lifetime.current && !selectionRef.current();
    scroll.stopFollowing();
    void loadPage("newer", "auto-fill", current).finally(release);
  };
  useEffect(() => {
    if (!hasNewerHistory || !position.following) { newerArmed.current = true; return; }
    if (newerArmed.current && !hydrating && !loadingNewerHistory && !newerHistoryError) autoNewer.current();
  }, [hasNewerHistory, position.following, hydrating, loadingNewerHistory, newerHistoryError]);
  useEffect(() => () => jump.dispose(), [jump]);
  useEffect(() => desktopHost().native.onServiceState(state => {
    if (state.phase === "stopping" || state.phase === "exited") {
      navigationIntent.current++;
      jump.cancel(); if (tabId) signalOutline(tabId, "suspend");
    } else if (state.phase === "ready" && tabId) signalOutline(tabId);
  }), [jump, tabId]);
  useEffect(() => {
    if (jumpState.status !== "failed") return;
    addBreadcrumb("chat.jump", `turn jump failed: ${jumpState.reason ?? "unknown"}`);
  }, [jumpState.status, jumpState.reason]);
  return <InvocationMetadataContext.Provider value={props.invocationMetadata ?? {}}>
    <MarkdownImageTabContext.Provider value={tabId ?? ""}>
      <ChatFileScopeProvider scopeKey={source.sessionKey} tabId={tabId} hostId={props.hostId}>
      <section className="chat-transcript">
        <SessionLoadingIndicator active={hydrating && props.showLoadingFeedback !== false} identity={sessionKey} />
        <div className="chat-surface" inert={Boolean(activeDetails)}>
          <Suspense fallback={null}><ChatTurnNavigator source={source} scroll={scroll} mounts={mounts}
            tabId={tabId} hostId={props.hostId} knownTurns={props.totalTurns ?? 0}
            busyTurn={jumpState.status === "loading" ? jumpState.turn : null}
            failedTurn={jumpState.status === "failed" ? jumpState.turn : null}
            failedReason={jumpState.reason}
            // Every click takes the one transaction entry point, so a newer
            // selection always supersedes a pending jump instead of racing it.
            onNavigate={(target) => {
              navigationIntent.current++;
              if (target.anchor.kind === "loaded") jump.jumpTo(target.anchor.key);
              else void jump.jump({ turn: target.turn, resolve: async () => {
                if (!tabId) return undefined;
                const store = (await import("../lib/transcriptOutlineStore")).getTranscriptOutlineStore();
                const entry = target.anchor.kind === "unloaded" && target.anchor.messageId
                  ? { messageId: target.anchor.messageId } : await store.entry(tabId, target.ordinal);
                if (!entry) return undefined;
                const view = store.getView(tabId);
                return { messageId: entry.messageId, generation: view.generation, snapshotSequence: view.snapshotSequence };
              } });
            }}
            onRetryJump={() => { void jump.retry(); }}
            onCancelJump={() => jump.cancel()} /></Suspense>
          <div ref={scroller} id={`reasonix-chat-transcript-${tabId ?? "local"}`} className="transcript chat-flow-scroll" tabIndex={0} data-transcript-render-mode="full"
            data-transcript-hydrating={hydrating} data-scroll-mode={position.following ? "tail" : "reader"}>
            <div ref={column} className="chat-column">
              <TranscriptConnection tabId={tabId} />
              {(hasOlderHistory || hasNewerHistory) && <div className="chat-history-window" role="status">
                <span>{t("chat.historyRange", { start: Math.max(1, (props.historyStartTurn ?? 0) + 1), end: Math.max(1, props.historyEndTurn ?? props.totalTurns ?? 0), total: props.totalTurns ?? 0 })}</span>
              </div>}
              {hasOlderHistory && <button className="btn chat-older" disabled={loadingOlderHistory} onClick={() => void loadOlder()}>{t(loadingOlderHistory ? "chat.loading" : "chat.loadOlder")}</button>}
              {(olderHistoryError || pagingError) && <button className="btn" onClick={() => void loadOlder()}>{t("chat.loadFailed")}</button>}
              {selectionBlocked && <p className="chat-history-selection" role="status">{t("chat.historySelectionBlocked")}</p>}
              {!hydrating && items.length === 0 && !running && <Welcome onPrompt={onPrompt} />}
              <ChatNodeList key={source.sessionKey} source={source} mounts={mounts} loader={loader} scroll={scroll} actions={actions} tabId={tabId} hostId={props.hostId} />
              <ChatRunning source={source} />
              {hasNewerHistory && <div className="chat-history-newer">
                <button className="btn" disabled={loadingNewerHistory} onClick={() => void loadPage("newer")}>{t(loadingNewerHistory ? "chat.loading" : "chat.loadNewer")}</button>
                <button className="btn" disabled={loadingNewerHistory} onClick={() => void returnToLatest()}>{t("chat.toLatest")}</button>
              </div>}
              {newerHistoryError && <button className="btn" onClick={() => void loadPage("newer")}>{t("chat.loadFailed")}</button>}
            </div>
          </div>
          <button className="btn chat-to-bottom" hidden={position.following} aria-label={t("chat.toLatest")} onClick={() => void returnToLatest()}><ArrowDown size={18} /></button>
        </div>
        {activeDetails && <ChatDetails key={activeDetails} source={source} nodeKey={activeDetails} loader={loader} onClose={closeDetails} onNavigate={setDetails} />}
      </section>
      </ChatFileScopeProvider>
    </MarkdownImageTabContext.Provider>
  </InvocationMetadataContext.Provider>;
}
