import { ErrorMessage } from "./ErrorMessage";
import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Check, ChevronDown, Cloud, Folder, FolderOpen, GitBranch, GitGraph, MessageCircle, Plus, RefreshCw, Search, X } from "lucide-react";
import { asArray } from "../lib/array";
import { app, onProjectTreeChanged } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { defaultWorkspaceTitle } from "../lib/sessionTitles";
import { useBranchSwitcher } from "../lib/useBranchSwitcher";
import { useToast } from "../lib/toast";
import type { GitCommitView, ProjectNode } from "../lib/types";
import { useProjectCreation } from "./useProjectCreation";
import "./ComposerWorkspaceContextBar.css";

export type ComposerWorkspaceContext = {
  scope: "global" | "project";
  workspaceRoot: string;
  workspaceName?: string;
  defaultWorkspaceName?: string;
  gitBranch?: string;
  tabId?: string;
  scopeKey: string;
  remote?: boolean;
  onSwitchWorkspace: (path?: string) => Promise<unknown>;
  onWorkWithoutProject: () => Promise<unknown>;
  onRefreshProjects: () => Promise<unknown>;
};

function projectTitle(project: ProjectNode): string {
  const explicit = (project.label ?? "").trim();
  if (explicit) return explicit;
  const root = (project.root ?? "").replace(/[\\/]+$/, "");
  return root.split(/[\\/]/).filter(Boolean).pop() ?? root;
}

function currentWorkspaceTitle(context: ComposerWorkspaceContext, noProject: string): string {
  if (context.scope === "global" && !context.remote) return defaultWorkspaceTitle(context.defaultWorkspaceName);
  const explicit = (context.workspaceName ?? "").trim();
  if (explicit) return explicit;
  const root = context.workspaceRoot.replace(/[\\/]+$/, "");
  return root.split(/[\\/]/).filter(Boolean).pop() ?? noProject;
}

function shortCommit(hash: string): string {
  return hash.slice(0, 7);
}

function commitDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function ComposerGitGraphDialog({
  tabId,
  branch,
  restoreFocusRef,
  onClose,
}: {
  tabId: string;
  branch?: string;
  restoreFocusRef: RefObject<HTMLButtonElement | null>;
  onClose: () => void;
}) {
  const t = useT();
  const dialogRef = useRef<HTMLElement | null>(null);
  const [commits, setCommits] = useState<GitCommitView[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const generation = useRef(0);

  const load = useCallback(async () => {
    const request = ++generation.current;
    setLoading(true);
    setError("");
    try {
      const history = await app.WorkspaceGitHistory(tabId, "");
      if (generation.current === request) setCommits(asArray(history));
    } catch (reason) {
      if (generation.current === request) setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      if (generation.current === request) setLoading(false);
    }
  }, [tabId]);

  useEffect(() => {
    void load();
    const previousFocus = document.activeElement instanceof HTMLElement && document.activeElement !== document.body
      ? document.activeElement
      : restoreFocusRef.current;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled), [tabindex]:not([tabindex='-1'])") ?? []);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    requestAnimationFrame(() => dialogRef.current?.querySelector<HTMLElement>("button:not(:disabled)")?.focus());
    return () => {
      generation.current += 1;
      window.removeEventListener("keydown", onKeyDown);
      previousFocus?.focus();
    };
  }, [load, onClose, restoreFocusRef]);

  // The composer frame is an isolated stacking context, so an in-place
  // backdrop would paint under the workspace dock.
  return createPortal(
    <div className="modal-backdrop composer-git-graph-backdrop" data-app-overlay="" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose();
    }}>
      <section ref={dialogRef} className="composer-git-graph" role="dialog" aria-modal="true" aria-labelledby="composer-git-graph-title">
        <header className="composer-git-graph__header">
          <div>
            <h2 id="composer-git-graph-title"><GitGraph size={17} />{t("composer.workspace.gitGraph")}</h2>
            {branch ? <span title={branch}>{branch}</span> : null}
          </div>
          <div className="composer-git-graph__actions">
            <button type="button" aria-label={t("composer.workspace.refreshHistory")} title={t("composer.workspace.refreshHistory")} disabled={loading} onClick={() => void load()}>
              <RefreshCw size={15} className={loading ? "composer-git-graph__spin" : undefined} />
            </button>
            <button type="button" aria-label={t("common.close")} title={t("common.close")} onClick={onClose}><X size={16} /></button>
          </div>
        </header>
        <div className="composer-git-graph__columns" aria-hidden="true">
          <span>{t("composer.workspace.graph")}</span>
            <span>{t("subagents.description")}</span>
          <span>{t("composer.workspace.date")}</span>
            <span>{t("settings.themeLibrary.fieldAuthor")}</span>
          <span>{t("composer.workspace.commit")}</span>
        </div>
        <div className="composer-git-graph__body">
          {loading && commits.length === 0 ? <div className="composer-git-graph__note">{t("common.loading")}</div> : null}
          {error ? <div className="composer-git-graph__note composer-git-graph__note--error"><ErrorMessage error={error} /></div> : null}
          {!loading && !error && commits.length === 0 ? <div className="composer-git-graph__note">{t("composer.workspace.noHistory")}</div> : null}
          {commits.map((commit, index) => (
            <div className="composer-git-graph__row" key={commit.hash}>
              <span className="composer-git-graph__lane" aria-hidden="true"><i />{index < commits.length - 1 ? <b /> : null}</span>
              <span className="composer-git-graph__message" title={commit.message}>{commit.message}</span>
              <span title={commit.date}>{commitDate(commit.date)}</span>
              <span title={commit.author}>{commit.author}</span>
              <code title={commit.hash}>{shortCommit(commit.hash)}</code>
            </div>
          ))}
        </div>
      </section>
    </div>,
    document.body,
  );
}

export function ComposerWorkspaceContextBar({ context }: { context: ComposerWorkspaceContext }) {
  const t = useT();
  const { showToast } = useToast();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const projectSearchRef = useRef<HTMLInputElement | null>(null);
  const branchTriggerRef = useRef<HTMLButtonElement | null>(null);
  const projectRequest = useRef(0);
  const [projectMenuOpen, setProjectMenuOpen] = useState(false);
  const [projectQuery, setProjectQuery] = useState("");
  const [projects, setProjects] = useState<ProjectNode[]>([]);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [projectsError, setProjectsError] = useState("");
  const [switchingProject, setSwitchingProject] = useState("");
  const [branchCreateMode, setBranchCreateMode] = useState(false);
  const [gitGraphOpen, setGitGraphOpen] = useState(false);
  const closeGitGraph = useCallback(() => setGitGraphOpen(false), []);

  const branch = useBranchSwitcher({
    tabId: context.tabId ?? "",
    scopeKey: context.scopeKey,
    workspaceRoot: context.workspaceRoot,
    gitBranch: context.remote ? undefined : context.gitBranch,
    rootRef,
    onBranchChanged: () => {},
  });

  const loadProjects = useCallback(async () => {
    const request = ++projectRequest.current;
    setProjectsLoading(true);
    setProjectsError("");
    try {
      const snapshot = await app.GetProjectTreeSnapshot();
      if (projectRequest.current !== request) return;
      setProjects(asArray(snapshot.projects).filter((project) => project.kind === "project" && Boolean(project.root) && !project.remote));
    } catch (reason) {
      if (projectRequest.current === request) setProjectsError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      if (projectRequest.current === request) setProjectsLoading(false);
    }
  }, []);

  const creation = useProjectCreation({
    onAddProject: async (path) => { await context.onSwitchWorkspace(path); },
    onRefresh: async () => { await context.onRefreshProjects(); },
    showToast,
  });

  useEffect(() => onProjectTreeChanged(() => {
    projectRequest.current += 1;
    if (projectMenuOpen) void loadProjects();
  }), [loadProjects, projectMenuOpen]);

  useEffect(() => {
    if (!projectMenuOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && event.target instanceof Node && !rootRef.current.contains(event.target)) setProjectMenuOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setProjectMenuOpen(false);
    };
    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [projectMenuOpen]);

  useEffect(() => {
    setProjectMenuOpen(false);
    setProjectQuery("");
    setSwitchingProject("");
  }, [context.scope, context.workspaceRoot, context.remote]);

  const filteredProjects = useMemo(() => {
    const query = projectQuery.trim().toLowerCase();
    if (!query) return projects;
    return projects.filter((project) => [projectTitle(project), project.root ?? ""].some((value) => value.toLowerCase().includes(query)));
  }, [projectQuery, projects]);

  const openProjectMenu = () => {
    if (branch.branchMenuOpen) branch.toggleBranchMenu();
    const next = !projectMenuOpen;
    setProjectMenuOpen(next);
    setProjectQuery("");
    if (next) {
      void loadProjects();
      window.setTimeout(() => projectSearchRef.current?.focus(), 0);
    }
  };

  const switchProject = async (path: string) => {
    if (switchingProject) return;
    setSwitchingProject(path);
    setProjectMenuOpen(false);
    try {
      await context.onSwitchWorkspace(path);
    } catch (reason) {
      showToast(reason instanceof Error ? reason.message : String(reason), "error");
    } finally {
      setSwitchingProject("");
    }
  };

  const workWithoutProject = async () => {
    setProjectMenuOpen(false);
    try {
      await context.onWorkWithoutProject();
    } catch (reason) {
      showToast(reason instanceof Error ? reason.message : String(reason), "error");
    }
  };

  const workspaceTitle = currentWorkspaceTitle(context, t("workspace.defaultName"));
  const projectSelected = context.scope === "project" || context.remote;
  const workspaceTitleAttribute = projectSelected ? context.workspaceRoot || workspaceTitle : t("workspace.defaultHint");
  const branchAvailable = Boolean(context.tabId && context.workspaceRoot && branch.activeBranch && !context.remote);

  return (
    <>
      <div ref={rootRef} className="composer-workspace-bar" role="toolbar" aria-label={t("composer.workspace.context") }>
        <div className="composer-workspace-bar__project-wrap">
          {projectSelected ? (
            <button type="button" className="composer-workspace-bar__clear" aria-label={t("composer.workspace.workWithoutProject")} title={t("composer.workspace.workWithoutProject")} onClick={() => void workWithoutProject()}>
              <X size={14} />
            </button>
          ) : null}
          <button
            type="button"
            className={`composer-workspace-bar__choice${projectMenuOpen ? " composer-workspace-bar__choice--open" : ""}`}
            aria-haspopup="menu"
            aria-expanded={projectMenuOpen}
            aria-label={`${t("history.filterProject")}: ${workspaceTitle}`}
            title={workspaceTitleAttribute}
            onClick={openProjectMenu}
          >
            {context.remote ? <Cloud size={15} /> : <Folder size={15} />}
            <span>{workspaceTitle}</span>
            <ChevronDown size={14} />
          </button>
          {projectMenuOpen ? (
            <div className="composer-workspace-menu composer-workspace-menu--projects" role="menu" aria-label={t("composer.workspace.switchProject")}>
              <label className="composer-workspace-menu__search">
                <Search size={15} />
                <input
                  ref={projectSearchRef}
                  value={projectQuery}
                  aria-label={t("composer.workspace.search")}
                  placeholder={t("composer.workspace.search")}
                  onChange={(event) => setProjectQuery(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" && filteredProjects.length === 1 && filteredProjects[0].root) void switchProject(filteredProjects[0].root);
                  }}
                />
              </label>
              <div className="composer-workspace-menu__list">
                {projectsLoading && projects.length === 0 ? <div className="composer-workspace-menu__note">{t("common.loading")}</div> : null}
                {projectsError ? <div className="composer-workspace-menu__note composer-workspace-menu__note--error"><ErrorMessage error={projectsError} /></div> : null}
                {!projectsLoading && !projectsError && filteredProjects.length === 0 ? <div className="composer-workspace-menu__note">{t("palette.empty")}</div> : null}
                {filteredProjects.map((project) => {
                  const path = project.root ?? "";
                  const active = context.scope === "project" && !context.remote && path === context.workspaceRoot;
                  return (
                    <button key={project.key || path} type="button" role="menuitem" className={`composer-workspace-menu__item${active ? " composer-workspace-menu__item--active" : ""}`} disabled={Boolean(switchingProject)} title={path} onClick={() => {
                      if (active) setProjectMenuOpen(false);
                      else void switchProject(path);
                    }}>
                      <Folder size={16} />
                      <span>{projectTitle(project)}</span>
                      {switchingProject === path ? <i className="composer-workspace-menu__spinner" /> : active ? <Check size={15} /> : null}
                    </button>
                  );
                })}
              </div>
              <div className="composer-workspace-menu__actions">
                <button type="button" role="menuitem" disabled={creation.addingProject} onClick={() => { setProjectMenuOpen(false); void creation.handleAddProject(); }}><FolderOpen size={16} /><span>{t("composer.workspace.openFolder")}</span></button>
                <button type="button" role="menuitem" onClick={() => { setProjectMenuOpen(false); creation.openRemoteConnectFlow(); }}><Cloud size={16} /><span>{t("projectTree.remoteConnection")}</span></button>
                <button type="button" role="menuitem" onClick={() => void workWithoutProject()}><MessageCircle size={16} /><span>{t("composer.workspace.workWithoutProject")}</span>{!projectSelected ? <Check size={15} /> : null}</button>
              </div>
            </div>
          ) : null}
        </div>

        {branchAvailable ? (
          <div className="composer-workspace-bar__branch-wrap">
            <button
              ref={branchTriggerRef}
              type="button"
              className={`composer-workspace-bar__choice composer-workspace-bar__choice--branch${branch.branchMenuOpen ? " composer-workspace-bar__choice--open" : ""}`}
              aria-haspopup="menu"
              aria-expanded={branch.branchMenuOpen}
              aria-label={`${t("status.gitBranchTitle")}: ${branch.activeBranch}`}
              title={`${t("status.gitBranchTitle")}: ${branch.activeBranch}`}
              onClick={() => {
                setProjectMenuOpen(false);
                setBranchCreateMode(false);
                branch.toggleBranchMenu();
              }}
            >
              <GitBranch size={15} />
              <span>{branch.activeBranch}</span>
              <ChevronDown size={14} />
            </button>
            {branch.branchMenuOpen ? (
              <div className="composer-workspace-menu composer-workspace-menu--branches" role="menu" aria-label={t("rightDock.switchBranch")}>
                <label className="composer-workspace-menu__search">
                  <Search size={15} />
                  <input
                    ref={branch.branchSearchRef}
                    value={branch.branchQuery}
                    aria-label={branchCreateMode ? t("composer.workspace.newBranchPlaceholder") : t("rightDock.branchSearchPlaceholder")}
                    placeholder={branchCreateMode ? t("composer.workspace.newBranchPlaceholder") : t("rightDock.branchSearchPlaceholder")}
                    onChange={(event) => {
                      branch.setBranchQuery(event.target.value);
                      branch.setBranchSwitchErr("");
                    }}
                    onKeyDown={(event) => {
                      if (event.key !== "Enter") return;
                      if (branchCreateMode && branch.canCreate) void branch.createBranch(branch.trimmedQuery);
                      else if (branch.exactMatch) void branch.checkoutBranch(branch.trimmedQuery);
                    }}
                  />
                </label>
                <div className="composer-workspace-menu__section">{t("rightDock.branchSection")}</div>
                <div className="composer-workspace-menu__list composer-workspace-menu__list--branches">
                  {branch.branchesLoading ? <div className="composer-workspace-menu__note">{t("rightDock.branchMenuLoading")}</div> : null}
                  {!branch.branchesLoading && branch.branchesErr ? <div className="composer-workspace-menu__note composer-workspace-menu__note--error"><ErrorMessage error={branch.branchesErr} /></div> : null}
                  {!branch.branchesLoading && !branch.branchesErr && branch.filteredBranches.length === 0 ? <div className="composer-workspace-menu__note">{t("rightDock.branchNoMatch")}</div> : null}
                  {branch.filteredBranches.map((name) => (
                    <button key={name} type="button" role="menuitem" className={`composer-workspace-menu__item${name === branch.activeBranch ? " composer-workspace-menu__item--active" : ""}`} disabled={Boolean(branch.switchingBranch)} title={name} onClick={() => {
                      if (name === branch.activeBranch) branch.toggleBranchMenu();
                      else void branch.checkoutBranch(name);
                    }}>
                      <GitBranch size={15} />
                      <span>{name}</span>
                      {branch.switchingBranch === name ? <i className="composer-workspace-menu__spinner" /> : name === branch.activeBranch ? <Check size={15} /> : null}
                    </button>
                  ))}
                </div>
                {branch.branchSwitchErr ? <div className="composer-workspace-menu__note composer-workspace-menu__note--error"><ErrorMessage error={branch.branchSwitchErr} /></div> : null}
                <div className="composer-workspace-menu__actions">
                  <button type="button" role="menuitem" disabled={Boolean(branch.switchingBranch) || (branchCreateMode && !branch.canCreate)} onClick={() => {
                    if (branchCreateMode && branch.canCreate) void branch.createBranch(branch.trimmedQuery);
                    else {
                      setBranchCreateMode(true);
                      branch.setBranchQuery("");
                      window.setTimeout(() => branch.branchSearchRef.current?.focus(), 0);
                    }
                  }}><Plus size={16} /><span>{branchCreateMode && branch.trimmedQuery ? `${t("rightDock.branchCreate")} ${branch.trimmedQuery}` : t("rightDock.branchCreate")}</span></button>
                  <button type="button" role="menuitem" onClick={() => { if (branch.branchMenuOpen) branch.toggleBranchMenu(); setGitGraphOpen(true); }}><GitGraph size={16} /><span>{t("composer.workspace.gitGraph")}</span></button>
                </div>
              </div>
            ) : null}
          </div>
        ) : null}
      </div>
      {creation.remoteConnectFlow}
      {gitGraphOpen && context.tabId ? (
        <ComposerGitGraphDialog
          tabId={context.tabId}
          branch={branch.activeBranch}
          restoreFocusRef={branchTriggerRef}
          onClose={closeGitGraph}
        />
      ) : null}
    </>
  );
}

export default ComposerWorkspaceContextBar;
