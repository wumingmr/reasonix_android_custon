import { lazy, Suspense, useState, type ComponentProps, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import type { DockNavigation } from "./dockNavigation";
import type { FileNavigationOwner } from "../lib/fileNavigationOwner";
import { fileNavigationOwner } from "../lib/fileNavigationCommands";
import type { Translator } from "../lib/i18n";
import type { RightDockMode } from "../store/layout";
import type { TabItem } from "../store/activityBar";
import { useActivityBarStore } from "../store/activityBar";
import { readWorkspaceTreeMemory, workspaceViewMemoryKey } from "../lib/workspaceViewMemory";
import { useDockViewRequests } from "./useDockViewRequests";

// The tab strip, its drag state machine and the add menu are a deferred
// surface: the dock is closed on most launches, so keep them out of the
// initial bundle.
const TabContainer = lazy(() => import("../components/TabContainer/TabContainer").then((module) => ({ default: module.TabContainer })));

const ContextPanel = lazy(() => import("../components/ContextPanel").then((module) => ({ default: module.ContextPanel })));
const RemotePanel = lazy(() => import("../components/RemotePanel").then((module) => ({ default: module.RemotePanel })));
const BrowserSurface = lazy(() => import("../components/BrowserPanelEntry"));
const WorkspacePanel = lazy(async () => {
  const [module] = await Promise.all([
    import("../components/WorkspacePanel"),
    import("../components/WorkspacePanelStability.css"),
  ]);
  return { default: module.WorkspacePanel };
});

export type WorkspaceDockRegionProps = {
  navigation?: DockNavigation;
  /** Navigation state each dock instance reads; the runtime passes the one it holds. */
  fileNavigation?: FileNavigationOwner;
  visible: boolean;
  overlay: boolean;
  mode: RightDockMode;
  showContext: boolean;
  t: Translator;
  /** Opens (or activates) the dock view a tab-picker entry stands for. */
  onPickEntry: (entryId: string) => void;
  remote: ComponentProps<typeof RemotePanel>;
  context: ComponentProps<typeof ContextPanel>;
  workspace: ComponentProps<typeof WorkspacePanel>;
  workspaceKey: string;
  workspaceRoot?: string;
  resizer?: {
    min: number;
    max: number;
    value: number;
    onPointerDown: (event: PointerEvent<HTMLButtonElement>) => void;
    onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) => void;
    onReset: () => void;
  };
};

/** The right-hand dock shared by every workspace tab. */
export function WorkspaceDockRegion(props: WorkspaceDockRegionProps) {
  const { visible, overlay, mode, showContext, t } = props;
  const firstFileTabId = useActivityBarStore(state => state.tabs.find(tab => tab.type === "file")?.id);
  const loadedRoot = useActivityBarStore(state => state.workspaceRoot);
  const activeTabId = useActivityBarStore(state => state.activeTabId);
  const tabs = useActivityBarStore(state => state.tabs);
  const { fileNavigation: fileNavigationProp } = props;
  // A standalone region keeps one instance for its whole life; the app shell
  // always passes the instance the runtime holds.
  const [fallbackFileNavigation] = useState(fileNavigationOwner);
  const fileNavigation = fileNavigationProp ?? fallbackFileNavigation;
  const projectReady = loadedRoot === (props.workspaceRoot ?? props.workspace.cwd ?? "");
  const requests = useDockViewRequests(`${props.workspaceKey}::${props.workspace.tabId ?? ""}`, visible && projectReady ? activeTabId : null, props.workspace, props.navigation, tabs.map(tab => tab.id));

  const renderTab = (tab: TabItem): ReactNode => {
    if (!projectReady) return null;
    if (firstFileTabId) readWorkspaceTreeMemory(workspaceViewMemoryKey(props.workspaceKey, firstFileTabId, true));
    switch (tab.type) {
      case "context":
        if (showContext) return <ContextPanel {...props.context} />;
        return <WorkspacePanel key={`${props.workspaceKey}::${tab.id}`} {...props.workspace} {...requests}
          dockTabId={tab.id} fileNavigation={fileNavigation}
          workspaceMemoryKey={workspaceViewMemoryKey(props.workspaceKey, tab.id)} workspaceMemoryVisitId={0} />;
      case "remote":
        return <RemotePanel key={`${props.workspaceKey}::${props.workspace.tabId}::${tab.id}`} {...props.remote} tabId={props.workspace.tabId} dockTabId={tab.id} fileNavigation={fileNavigation} navigationSignal={requests.navigationSignal} />;
      case "browser":
        return <BrowserSurface surface="panel" taskId={props.workspace.tabId} />;
      default:
        return (
          <WorkspacePanel
            key={`${props.workspaceKey}::${tab.id}`}
            {...props.workspace}
            {...requests}
            dockTabId={tab.id}
            fileNavigation={fileNavigation}
            workspaceMemoryKey={workspaceViewMemoryKey(props.workspaceKey, tab.id, tab.id === firstFileTabId)}
            workspaceMemoryVisitId={0}
            initialViewMode={tab.type === "changed" ? "changed" : "files"}
          />
        );
    }
  };

  return (
    <>
      {props.resizer && (
        <button
          className="workspace-panel-resizer" type="button" role="separator" aria-orientation="vertical"
          aria-label={t("rightDock.resize")} aria-valuemin={props.resizer.min}
          aria-valuemax={props.resizer.max} aria-valuenow={props.resizer.value}
          onPointerDown={props.resizer.onPointerDown} onKeyDown={props.resizer.onKeyDown}
          onDoubleClick={props.resizer.onReset}
        />
      )}
      {visible && (
        <aside className={["workbench-dock", `workbench-dock--${mode}`, overlay ? "workbench-dock--overlay" : ""].join(" ")} aria-label={t("rightDock.workbench")}>
          <div className="workbench-dock__panel">
            <Suspense fallback={null}>
              <TabContainer key={loadedRoot} renderTab={renderTab} onPickEntry={props.onPickEntry} onClosePanel={props.workspace.onClose} />
            </Suspense>
          </div>
        </aside>
      )}
    </>
  );
}
