import { lazy, Suspense, type ReactNode } from "react";
import { TopicbarActionsRegion } from "./TopicbarActionsRegion";
import { shouldMountExternalOpener } from "../components/ExternalOpener";
import { tabWorkspaceTitle, topicDisplayTitle, topicTitle } from "../lib/sessionTitles";
import { sidebarImScopeLabel, type SidebarImTopicSource, type SidebarImConnection } from "../app-runtime/sidebarImProjection";
import type { TopicbarView } from "./TopicbarRegion";
import type { TabMeta } from "../lib/types";
import type { Translator } from "../lib/i18n";
import type { SessionExportFormat } from "../app-runtime/useSessionExportCommands";

const TaskMonitorPanel = lazy(() => import("../components/TaskMonitorPanel").then((module) => ({ default: module.TaskMonitorPanel })));

/** Topicbar view projection: IM/bot detail identity, workspace label/subtitle,
 *  worktree merge entry and rename gating. Pure function of the committed tab
 *  and preferences; ownership stays in the caller. */
export function buildTopicbarView(input: {
  t: Translator;
  locale: string;
  activeTab: TabMeta | undefined;
  cwd: string | undefined;
  imDetail: SidebarImConnection | null;
  imTopicSources: Record<string, SidebarImTopicSource>;
  chromeHidden: boolean;
  windowsBrand: boolean;
  automationReturn: boolean;
  sidebar: { title: string; blocked: boolean; pressed: boolean; collapsed: boolean };
  rename: { editing: boolean; draft: string };
  draft?: { title: string; workspaceLabel: string };
}): TopicbarView {
  const { t, locale, activeTab, imDetail } = input;
  const topicbarTitle = input.draft?.title ?? (imDetail ? t("botDetail.title", { name: imDetail.title }) : topicDisplayTitle(activeTab));
  const topicbarWorkspaceLabel = input.draft?.workspaceLabel ?? (imDetail ? t("botDetail.subtitle") : activeTab ? tabWorkspaceTitle(activeTab) : "");
  const topicbarWorkspacePath = activeTab?.scope === "project" ? activeTab.workspaceRoot || input.cwd : "";
  const topicbarImSource = activeTab?.scope === "global" && activeTab.topicId ? input.imTopicSources[activeTab.topicId] : undefined;
  const topicbarImSourceLabel = imDetail
    ? imDetail.platformLabel
    : topicbarImSource ? t("msg.fromIm", { source: topicbarImSource.label }) : "";
  const topicbarImSourcePlatform = imDetail?.platform ?? topicbarImSource?.platform;
  const topicbarSubtitleVisible = Boolean(activeTab?.isolatedWorktree || topicbarImSourceLabel);
  const topicbarSubtitleTitle = imDetail
    ? [topicbarWorkspaceLabel, topicbarImSourceLabel, sidebarImScopeLabel(imDetail, t)].filter(Boolean).join(" · ")
    : [topicbarWorkspacePath || topicbarWorkspaceLabel, topicbarImSourceLabel].filter(Boolean).join(" · ");
  const topicbarCanRename = !input.draft && !imDetail && (Boolean(activeTab?.topicId) || Boolean(activeTab?.remote));
  return {
    automationReturn: input.automationReturn,
    automationReturnLabel: locale === "en" ? "Back to automation" : locale === "zh-TW" ? "返回自動化" : "返回自动化",
    chromeHidden: input.chromeHidden,
    brand: input.windowsBrand,
    sidebar: input.sidebar,
    title: { text: topicbarTitle, hover: !topicbarCanRename && imDetail ? topicbarTitle : topicTitle(activeTab),
      renameLabel: t("topicBar.renameSession"), editing: input.rename.editing, draft: input.rename.draft,
      canRename: topicbarCanRename, workspaceLabel: topicbarWorkspaceLabel },
    subtitle: { visible: topicbarSubtitleVisible, title: topicbarSubtitleTitle, worktreeTabId: activeTab?.isolatedWorktree ? activeTab.id : undefined,
      mergeLabel: t("worktree.mergeAction"), mergeTooltip: t("worktree.mergeButtonTooltip"),
      sourcePlatform: topicbarImSourcePlatform, sourceLabel: topicbarImSourceLabel },
  };
}

/** The topicbar actions stack: per-session actions, the dock toggle and the
 *  task-monitor popover. Pure prop-driven. */
export function TopicbarActionsStack(props: {
  t: Translator;
  activeTab: TabMeta | undefined;
  activeTabId: string | undefined;
  imDetailActive: boolean;
  dismissSignal: number;
  sessionHasContent: boolean;
  exportCommands: {
    getSessionMarkdown: () => Promise<string>;
    exportSession: (format: SessionExportFormat) => Promise<void>;
  };
  terminal: { toggle: () => void; enabled: boolean; open: boolean; prefetch: () => void };
  tasksOpen: false | "session" | "all";
  setTasksOpen: (update: (open: false | "session" | "all") => false | "session" | "all") => void;
  onCloseTasks: () => void;
  onOpenTaskSession: (tabID: string, taskID: string) => Promise<boolean>;
  dockToggle: ReactNode;
  launcherToggle?: ReactNode;
}) {
  const { t, activeTab, imDetailActive } = props;
  return (
    <div className="topicbar__actions">
      {props.launcherToggle}
      <TopicbarActionsRegion sessionIdentity={activeTab?.id}
        external={shouldMountExternalOpener(activeTab, imDetailActive) && activeTab
          ? { tabId: activeTab.id, dismissSignal: props.dismissSignal } : undefined}
        session={!imDetailActive ? {
          sessionHasContent: props.sessionHasContent,
          getSessionMarkdown: props.exportCommands.getSessionMarkdown,
          exportSession: (format) => void props.exportCommands.exportSession(format),
          toggleTerminal: props.terminal.toggle, terminalEnabled: props.terminal.enabled,
          terminalOpen: props.terminal.open, prefetchTerminal: props.terminal.prefetch,
          openSessionSummary: () => props.setTasksOpen((open) => open ? false : "session"), tasksOpen: Boolean(props.tasksOpen),
        } : undefined}
      />
      {props.dockToggle}
      {props.tasksOpen && (
        <div className="taskmonitor-popover" role="dialog" aria-label={t("summary.session")}>
          <Suspense fallback={null}>
            <TaskMonitorPanel
              key={`${activeTab?.id || props.activeTabId || "none"}:${activeTab?.workspaceRoot || "global"}:${activeTab?.sessionPath || ""}:${props.tasksOpen}`}
              tabID={activeTab?.id || props.activeTabId || ""}
              initialOpen initialScope={props.tasksOpen || "session"}
              popover
              summaryMode
              onClose={props.onCloseTasks}
              onOpenSession={props.onOpenTaskSession}
            />
          </Suspense>
        </div>
      )}
    </div>
  );
}
