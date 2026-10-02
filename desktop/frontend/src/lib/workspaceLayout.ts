export const SIDEBAR_AUTO_COLLAPSE_WIDTH = 1024;
export const DOCK_FULLSCREEN_WIDTH = 768;
export const DOCK_DEFAULT_RATIO = 0.45;
export const DOCK_MAX_RATIO = 0.7;

export function availableWorkspacePanelWidth({
  viewportWidth,
  sidebarCollapsed,
  sidebarWidth,
  chatMinWidth,
  resizerWidth,
}: {
  viewportWidth: number;
  sidebarCollapsed: boolean;
  sidebarWidth: number;
  chatMinWidth: number;
  resizerWidth: number;
}): number {
  return Math.max(0, Math.min(viewportWidth * DOCK_MAX_RATIO, viewportWidth - (sidebarCollapsed ? 0 : sidebarWidth) - chatMinWidth - resizerWidth));
}

export function resolveWorkspacePanelWidth({
  open,
  maximized,
  preferredWidth,
  minWidth,
  availableWidth,
}: {
  open: boolean;
  maximized: boolean;
  preferredWidth: number;
  minWidth: number;
  availableWidth: number;
}): number {
  if (!open || maximized) return preferredWidth;
  return Math.min(Math.max(minWidth, preferredWidth), Math.max(0, availableWidth));
}

export function resolveLiveWorkspacePanelWidth({
  viewportWidth,
  sidebarCollapsed,
  sidebarWidth,
  chatMinWidth,
  resizerWidth,
  open,
  maximized,
  preferredWidth,
  minWidth,
}: {
  viewportWidth: number;
  sidebarCollapsed: boolean;
  sidebarWidth: number;
  chatMinWidth: number;
  resizerWidth: number;
  open: boolean;
  maximized: boolean;
  preferredWidth: number;
  minWidth: number;
}): number {
  return resolveWorkspacePanelWidth({
    open,
    maximized,
    preferredWidth,
    minWidth,
    availableWidth: availableWorkspacePanelWidth({
      viewportWidth,
      sidebarCollapsed,
      sidebarWidth,
      chatMinWidth,
      resizerWidth,
    }),
  });
}

export function workspacePanelAriaMinWidth(minWidth: number, renderedWidth: number): number {
  return Math.min(minWidth, renderedWidth);
}

export function resolveWorkspacePanelPlacement({
  viewportWidth, sidebarCollapsed, sidebarWidth, chatMinWidth, resizerWidth,
  open, maximized, preferredWidth, minWidth, minRenderWidth, liveWidth,
}: {
  viewportWidth: number; sidebarCollapsed: boolean; sidebarWidth: number;
  chatMinWidth: number; resizerWidth: number; open: boolean; maximized: boolean;
  preferredWidth: number; minWidth: number; minRenderWidth: number; liveWidth?: number | null;
}) {
  const resolvedWidth = resolveLiveWorkspacePanelWidth({
    viewportWidth, sidebarCollapsed, sidebarWidth, chatMinWidth, resizerWidth,
    open, maximized, preferredWidth, minWidth,
  });
  const fullscreen = open && viewportWidth < DOCK_FULLSCREEN_WIDTH;
  const overlay = fullscreen || (open && !maximized && resolvedWidth < minRenderWidth);
  const storedWidth = maximized
    ? preferredWidth
    : overlay ? Math.min(preferredWidth, Math.max(minWidth, viewportWidth - 16)) : resolvedWidth;
  const renderWidth = fullscreen ? viewportWidth : liveWidth ?? storedWidth;
  const renderable = open && (maximized || overlay || renderWidth >= minRenderWidth);
  return { renderWidth, overlay, renderable, gridOpen: renderable && !maximized && !overlay };
}
