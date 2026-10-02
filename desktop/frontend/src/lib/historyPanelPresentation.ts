import { t, useT } from "./i18n";
import { sessionActivityTime } from "./session";
import type { HistoryMessage, SessionMeta } from "./types";
import { historyMessagesToItems, type Item } from "./useController";

export function historyDayLabel(ms: number): string {
  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((startOfDay(new Date()) - startOfDay(new Date(ms))) / 86_400_000);
  if (days <= 0) return t("history.today");
  if (days === 1) return t("history.yesterday");
  return new Date(ms).toLocaleDateString();
}

export function historyTimeLabel(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function historyDateBucket(ms: number): "today" | "yesterday" | "older" {
  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((startOfDay(new Date()) - startOfDay(new Date(ms))) / 86_400_000);
  if (days <= 0) return "today";
  if (days === 1) return "yesterday";
  return "older";
}

export function historySessionTime(s: SessionMeta, trash: boolean): number {
  return trash ? s.deletedAt || sessionActivityTime(s) : sessionActivityTime(s);
}

export function historySessionScope(s: SessionMeta): "project" | "global" {
  return s.scope === "project" ? "project" : "global";
}

export function isChannelHistorySession(s: SessionMeta): boolean {
  return s.kind === "channel" || s.sessionSource === "auto";
}

export function historySessionLocation(s: SessionMeta, tr: ReturnType<typeof useT>): string {
  if (isChannelHistorySession(s)) return [s.channelLabel || s.channel || tr("history.channel"), s.remoteId].filter(Boolean).join(" · ");
  if (s.workspaceRoot) {
    const parts = s.workspaceRoot.split(/[\\/]/).filter(Boolean);
    return parts[parts.length - 1] || s.workspaceRoot;
  }
  return historySessionScope(s) === "project" ? tr("history.filterProject") : tr("history.filterGlobal");
}

export function historySessionMetaLine(s: SessionMeta, tr: ReturnType<typeof useT>, trash = false): string {
  const time = historyTimeLabel(historySessionTime(s, trash));
  const suffix = trash && s.deletedAt ? ` · ${tr("history.deleted")}` : "";
  const prefix = isChannelHistorySession(s) ? `${tr("history.channelReadOnly")} · ` : "";
  const turns = s.turnsState === "unknown"
    ? tr("history.indexing")
    : tr(s.turns === 1 ? "history.turnOne" : "history.turnOther", { n: s.turns });
  return `${prefix}${turns} · ${time}${suffix}`;
}

export function historyPreviewItems(messages: HistoryMessage[]): Item[] {
  return historyMessagesToItems(messages, "hp").items;
}
