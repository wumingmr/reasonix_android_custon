import { useCallback, useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import type { ChatSource } from "../lib/chatViewSource";
import type { ChatScrollController } from "../lib/chatScrollController";
import type { ChatMountedOrder } from "../lib/chatMountedOrder";
import { getTranscriptOutlineStore, localOutlineRead, remoteOutlineRead } from "../lib/transcriptOutlineStore";
import type { TurnJumpReason } from "../lib/chatTurnJump";
import { useT } from "../lib/i18n";
import { TurnNavigator, type TurnRailItem } from "./harness-chat/TurnNavigator";
import css from "./harness-chat/TurnNavigator.styles";
import "./harness-chat/TurnNavigator.css";

export default function ChatTurnNavigator({ source, scroll, mounts, tabId, hostId, onNavigate, onRetryJump, onCancelJump, busyTurn, failedTurn, failedReason, knownTurns = 0 }: {
  source: ChatSource; scroll: ChatScrollController; mounts: ChatMountedOrder; tabId?: string; hostId?: string;
  onNavigate: (item: TurnRailItem) => void; onRetryJump?: () => void; onCancelJump?: () => void;
  busyTurn?: string | null; failedTurn?: string | null; failedReason?: TurnJumpReason; knownTurns?: number;
}) {
  const t = useT();
  const store = getTranscriptOutlineStore();
  const subscribe = useCallback((notify: () => void) => tabId ? store.subscribe(tabId, notify) : () => {}, [store, tabId]);
  const read = useCallback(() => store.getView(tabId ?? ""), [store, tabId]);
  const outline = useSyncExternalStore(subscribe, read, read);
  const order = useSyncExternalStore(mounts.subscribe, mounts.getSnapshot, mounts.getSnapshot);
  const subscribeIdentities = useCallback((notify: () => void) => {
    const releases = order.filter(key => source.getNodeSnapshot(key)?.kind === "user").map(key => source.subscribeNode(key, notify));
    return () => { for (const release of releases) release(); };
  }, [order, source]);
  const readIdentities = useCallback(() => JSON.stringify(order.flatMap(key => {
    const node = source.getNodeSnapshot(key);
    return node?.kind === "user" ? [[key, node.item.messageId, node.item.historyTurn, node.item.checkpointTurn]] : [];
  })), [order, source]);
  const identities = useSyncExternalStore(subscribeIdentities, readIdentities, readIdentities);
  const position = useSyncExternalStore(scroll.subscribe, scroll.getSnapshot, scroll.getSnapshot);
  const initialTurns = useRef(knownTurns).current;
  useEffect(() => tabId ? store.activate(tabId, source.sessionKey, hostId && hostId !== "local" ? remoteOutlineRead : localOutlineRead, initialTurns) : undefined,
    [store, tabId, source, hostId, initialTurns]);
  const loaded = useMemo(() => {
    const identityRows = new Map(JSON.parse(identities).map((row: [string, string?, number?, number?]) => [row[0], row]));
    const result: TurnRailItem[] = [];
    for (const key of order) {
      const node = source.getNodeSnapshot(key);
      if (node?.kind === "user") {
        const row = identityRows.get(key) as [string, string?, number?, number?] | undefined;
        const previous = result[result.length - 1]?.ordinal ?? (node.item.submissionId ? knownTurns : 0);
        result.push({ turn: row?.[1] ? `m:${row[1]}` : key,
          ordinal: row?.[2] ?? row?.[3] ?? previous + 1, prompt: "", response: "", anchor: { kind: "loaded", key } });
      }
      else if (node?.kind === "assistant" && result.length) result[result.length - 1].answerKey = key;
    }
    return result;
  }, [order, source, identities, knownTurns]);
  const byTurn = useMemo(() => new Map(loaded.map(item => [item.ordinal, item])), [loaded]);
  const byId = useMemo(() => new Map(loaded.map(item => [item.turn, item])), [loaded]);
  const complete = Boolean(tabId) && outline.mode !== "unsupported";
  const count = complete ? Math.max(outline.totalTurns, knownTurns, ...loaded.map(item => item.ordinal), 0) : loaded.length;
  const getItem = useCallback((index: number): TurnRailItem => {
    // A legacy host only promises the order of its resident turns. Its
    // projected history numbers can overlap during submission handoff, so use
    // the resident order as the fallback rail position and React identity.
    if (!complete) return { ...loaded[index], ordinal: index + 1 };
    const ordinal = index + 1;
    const entry = outline.entries.get(ordinal);
    const mounted = entry ? byId.get(`m:${entry.messageId}`) : byTurn.get(ordinal);
    if (mounted) return { ...mounted, ordinal };
    return { turn: entry ? `m:${entry.messageId}` : `pending:${ordinal}`, ordinal,
      prompt: entry?.prompt ?? t("chat.loading"), response: entry?.answer ?? "",
      anchor: entry ? { kind: "unloaded", recordId: `m:${entry.messageId}`, messageId: entry.messageId } : { kind: "pending" }, unloaded: true };
  }, [complete, loaded, outline.entries, byId, byTurn, t]);
  const loadedIndex = loaded.findIndex(item => item.anchor.kind === "loaded" && item.anchor.key === position.activeKey);
  const activeIndex = complete && loadedIndex >= 0 ? loaded[loadedIndex].ordinal - 1 : loadedIndex;
  const range = useCallback((first: number, last: number) => {
    if (tabId && complete && outline.snapshotSequence !== undefined) void store.ensure(tabId, first + 1, last);
  }, [tabId, complete, store, outline.snapshotSequence]);
  const preview = useCallback((item: TurnRailItem) => <Preview source={source} item={item} />, [source]);
  const failed = outline.mode === "error";
  return <>
    <TurnNavigator items={loaded} totalCount={count} itemAt={getItem} activeIndex={activeIndex} onRange={range}
      activeTurn={position.activeKey || null} busyTurn={busyTurn ?? null} onNavigate={onNavigate} renderPreview={preview} t={t}
      loading={complete && outline.mode === "loading" && count > 1} failed={failed}
      onRetry={failed ? () => { if (tabId) void store.retry(tabId); } : failedTurn ? onRetryJump : undefined}
      jumpFailed={Boolean(failedTurn)} jumpReasonKey={failedReason === "snapshotExpired" ? "chat.turnNavigation.reasonExpired" : failedReason === "mountTimeout" ? "chat.turnNavigation.reasonMount" : "chat.turnNavigation.reasonUnavailable"}
      onCancelJump={busyTurn ? onCancelJump : undefined} />
    {outline.mode === "unsupported" && knownTurns > loaded.length && <span className="chat-turn-navigation-status" role="status">{t("chat.turnNavigation.upgrade")}</span>}
  </>;
}

function Preview({ source, item }: { source: ChatSource; item: TurnRailItem }) {
  const subscribe = useCallback((notify: () => void) => {
    const user = item.anchor.kind === "loaded" ? source.subscribeNode(item.anchor.key, notify) : undefined;
    const answer = item.answerKey ? source.subscribeNode(item.answerKey, notify) : undefined;
    return () => { user?.(); answer?.(); };
  }, [source, item]);
  const read = useCallback(() => {
    const user = item.anchor.kind === "loaded" ? source.getNodeSnapshot(item.anchor.key) : undefined;
    const answer = item.answerKey ? source.getNodeSnapshot(item.answerKey) : undefined;
    return JSON.stringify([user?.kind === "user" ? user.item.text.slice(0, 300) : item.prompt,
      answer?.kind === "assistant" ? answer.item.text.slice(0, 500) : item.response]);
  }, [source, item]);
  const [prompt, response] = JSON.parse(useSyncExternalStore(subscribe, read, read)) as string[];
  return <><div className={css.previewPrompt}>{prompt || item.ordinal}</div><div className={css.previewResponse}>{response}</div></>;
}
