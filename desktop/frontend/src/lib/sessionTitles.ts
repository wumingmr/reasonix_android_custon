import type { TabMeta } from "./types";
import { t } from "./i18n";

export function defaultWorkspaceTitle(name?: string): string {
  const title = name?.trim();
  return title && title !== "Global" ? title : t("workspace.defaultName");
}

export function tabWorkspaceTitle(tab?: TabMeta): string {
  if (!tab) return defaultWorkspaceTitle();
  if (tab.scope === "project") return tab.workspaceName || tab.workspaceRoot || "Project";
  if (tab.scope === "global" && !tab.remote) return defaultWorkspaceTitle(tab.workspaceName);
  return tab.workspaceName || tab.workspaceRoot || "Global";
}

export function topicTitle(tab?: TabMeta): string {
  if (!tab) return defaultWorkspaceTitle();
  const workspaceTitle = tabWorkspaceTitle(tab);
  const topic = tab.topicTitle || (tab.scope === "global" ? workspaceTitle : "Untitled");
  return topic === workspaceTitle ? workspaceTitle : `${workspaceTitle} / ${topic}`;
}

export function topicDisplayTitle(tab?: TabMeta): string {
  if (!tab) return defaultWorkspaceTitle();
  return tab.topicTitle || (tab.scope === "global" ? tabWorkspaceTitle(tab) : "Untitled");
}

export function safeFilename(name: string): string {
  const cleaned = name.trim().replace(/[\\/:*?"<>|]+/g, "-").replace(/\s+/g, " ").slice(0, 80);
  return cleaned || "reasonix-session";
}
