export type DraftLandingTarget = { scope: "global" | "project"; workspaceRoot: string };

type SurfaceTab = { scope?: string; workspaceRoot?: string; remote?: unknown };

/**
 * The draft a formal surface hands over to, when it goes away or when the user
 * asks it for a new session. The workspace root is the surface's actual
 * directory (UI preferences key on it); global draft requests drop it on the
 * wire. Remote tabs have no local draft workspace.
 */
export function draftLandingTargetForTab(tab?: SurfaceTab | null): DraftLandingTarget {
  if (!tab || tab.remote) return { scope: "global", workspaceRoot: "" };
  const workspaceRoot = tab.workspaceRoot || "";
  return { scope: tab.scope === "project" && workspaceRoot ? "project" : "global", workspaceRoot };
}
