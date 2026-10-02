import { lazy, Suspense, type ComponentProps, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { AlarmClock, MessageSquare, Search, Settings, Trash2 } from "lucide-react";
import { Tooltip } from "../components/Tooltip";
import type { Translator } from "../lib/i18n";
import type { SettingsTab } from "../lib/types";
import logoWordmark from "../assets/logo-wordmark.svg";

const ProjectTree = lazy(() => import("../components/ProjectTree").then((module) => ({ default: module.ProjectTree })));

export type SidebarRegionProps = {
  className: string;
  collapsed: boolean;
  resize: {
    min: number;
    max: number;
    value: number;
    onPointerDown: (event: PointerEvent<HTMLButtonElement>) => void;
    onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) => void;
    onReset: () => void;
  };
  projectTree: ComponentProps<typeof ProjectTree>;
  t: Translator;
  onNewSession: () => void;
  onOpenPalette: () => void;
  paletteShortcut: string;
  onOpenTrash: () => void;
  onOpenAutomation: () => void;
  onOpenSettings: (tab: SettingsTab) => void;
};

/** Workbench sidebar: brand, quick new-session, project tree and utility nav. */
export function SidebarRegion(props: SidebarRegionProps) {
  const { t } = props;
  return (
    <>
      <aside className={props.className} aria-label={t("sidebar.navigation")}>
        <div className="sidebar__head" aria-hidden={props.collapsed}>
          <div className="sidebar__brand sidebar__brand--workbench">
            <img src={logoWordmark} alt="Reasonix" className="sidebar__brand-logo sidebar__brand-logo--workbench" draggable={false} />
          </div>
        </div>
        <div className="sidebar__quick-actions">
          <button className="sidebar__quick-action" type="button" onClick={props.onNewSession}>
            <MessageSquare size={18} aria-hidden="true" /><span>{t("topbar.newSession")}</span>
          </button>
        </div>
        <section className="sidebar__section sidebar__section--projects">
          <Suspense fallback={<div className="project-tree__empty project-tree__empty--subtle" role="status">{t("projectTree.loadingProjects")}</div>}>
            <ProjectTree {...props.projectTree} />
          </Suspense>
        </section>
        <nav className="sidebar__nav sidebar__nav--footer">
          <div className="sidebar__utility-row" aria-label={t("sidebar.utilityActions")}>
            <UtilityButton
              label={t("shortcuts.action.commandPalette")}
              tooltip={`${t("shortcuts.action.commandPalette")} ${props.paletteShortcut}`}
              icon={<Search size={16} />}
              onClick={props.onOpenPalette}
            />
            <UtilityButton label={t("sidebar.trash")} icon={<Trash2 size={16} />} onClick={props.onOpenTrash} />
            <UtilityButton label={t("heartbeat.scheduler")} icon={<AlarmClock size={16} />} onClick={props.onOpenAutomation} />
            <UtilityButton label={t("topbar.settings")} icon={<Settings size={16} />} onClick={() => props.onOpenSettings("general")} />
          </div>
        </nav>
      </aside>
      <button className="sidebar-resizer" type="button" role="separator" aria-orientation="vertical" aria-label={t("sidebar.resize")}
        aria-valuemin={props.resize.min} aria-valuemax={props.resize.max} aria-valuenow={props.resize.value}
        onPointerDown={props.resize.onPointerDown} onKeyDown={props.resize.onKeyDown} onDoubleClick={props.resize.onReset} />
    </>
  );
}

function UtilityButton({ icon, label, tooltip = label, onClick }: { icon: ReactNode; label: string; tooltip?: string; onClick: () => void }) {
  return <Tooltip label={tooltip} fill side="top"><button className="sidebar__utility-button" type="button" aria-label={label} onClick={onClick}>{icon}<span className="sr-only">{label}</span></button></Tooltip>;
}
