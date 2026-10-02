import { useEffect } from "react";
import type { SessionSelector } from "../generated/desktopContract.generated";
import { useCommittedCommand } from "../lib/useCommittedCommand";
import { applyThemeScene } from "../lib/themePack";
import { useOverlayStore } from "../store/overlays";
import type { Translator } from "../lib/i18n";
import type { Item, LiveStream } from "../lib/useController";

export type SessionExportFormat = "markdown" | "json" | "pdf" | "image" | "diagnostic";

/**
 * Owns the session export commands (markdown/json/pdf/image file pickers and
 * writers), the export popover outside-click close and the theme scene that
 * switches between the empty home and the content task scene. Each command
 * captures the source identity and title of the render that published
 * it; resident items are used only for diagnostic counters; the renderer chunks stay lazy behind the file dialog.
 */
export function useSessionExportCommands(input: {
  tabId?: string;
  selector?: SessionSelector;
  remote: boolean;
  sessionTitle: string;
  items: readonly Item[];
  live: LiveStream | undefined;
  hasContent: boolean;
  t: Translator;
  showToast: (message: string, kind: "info" | "warn" | "error", options?: { durationMs?: number }) => void;
}) {
  const { tabId, remote, sessionTitle, items, live, hasContent, t, showToast } = input;
  const topicExportOpen = useOverlayStore((state) => state.topicExportOpen);
  const setTopicExportOpen = useOverlayStore((state) => state.setTopicExportOpen);

  // Theme pack scene: home when the session is empty, task once content exists.
  useEffect(() => {
    applyThemeScene(hasContent ? "task" : "home");
  }, [hasContent]);

  useEffect(() => {
    if (!topicExportOpen) return;
    const onDown = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!target?.closest(".topicbar__export")) setTopicExportOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [setTopicExportOpen, topicExportOpen]);

  const run = useCommittedCommand(async (format: SessionExportFormat | "clipboard") => (await import("../lib/sessionExportOperation")).runSessionExport({
    selector: input.selector ?? {}, tabId: tabId ?? "", format, title: sessionTitle, remote,
    residentItems: items.length, runningStream: Boolean(live), unresolvedTools: items.filter(item => item.kind === "tool" && item.resultMissing).length,
  }));
  const getSessionMarkdown = useCommittedCommand(async () => {
    try {
      const result = await run("clipboard");
      if (!result || result.cancelled) throw new DOMException("Export cancelled", "AbortError");
      return result.text ?? "";
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) showToast(t("topicBar.exportFailed", { error: error instanceof Error ? error.message : String(error) }), "error", { durationMs: 8000 });
      throw error;
    }
  });
  const exportSession = useCommittedCommand(async (format: SessionExportFormat) => {
    setTopicExportOpen(false);
    try {
      const result = await run(format);
      if (result && !result.cancelled) showToast(`${sessionTitle} · ${t("topicBar.exportSuccess", { count: result.files })}`, "info");
    } catch (err) {
      showToast(t("topicBar.exportFailed", { error: err instanceof Error ? err.message : String(err) }), "error", { durationMs: 8000 });
    }
  });

  return { getSessionMarkdown, exportSession };
}
