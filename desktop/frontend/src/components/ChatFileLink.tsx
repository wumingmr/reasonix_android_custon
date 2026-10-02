// A file the answer named, rendered as a first-class chat file reference.
//
// Left click goes to the right-hand preview and leaves the conversation where
// it is; the context menu keeps the actions that need a deliberate choice —
// source, reveal in the file tree, copy path, save a copy, or hand the file to
// the system. Every action re-verifies through the host, so a reference that
// has since been deleted reports itself instead of opening something else.

import { useCallback, useMemo, useState, type ReactNode } from "react";
import { Code2, Copy, ExternalLink, FileText, FolderSearch, FolderOpen, Save } from "lucide-react";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import { useT } from "../lib/i18n";
import { useToast } from "../lib/toast";
import { writeClipboardText } from "../lib/clipboard";
import { fileResourceCapabilities } from "../lib/fileResource";
import { openResource, performResourceAction, resolveFileResourcePath } from "../lib/presentedFileNavigation";
import type { ChatFileLink } from "./ChatFileLinkContext";

function errorText(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

/** The shared menu and click behavior behind every rendered file reference. */
export function useChatFileLinkActions(link: ChatFileLink) {
  const t = useT();
  const { showToast } = useToast();
  const [point, setPoint] = useState<ContextMenuPoint | null>(null);
  const close = useCallback(() => setPoint(null), []);
  const capabilities = useMemo(() => fileResourceCapabilities(link.ref, link.actions), [link.actions, link.ref]);

  const run = useCallback(async (action: "preview" | "source" | "reveal-tree" | "open-native" | "reveal-native" | "save-copy") => {
    try {
      const outcome = action === "preview" || action === "source"
        ? await openResource(link.ref, { view: action })
        : await performResourceAction(link.ref, action);
      if (outcome.status === "failed") throw outcome.error;
    } catch (reason) {
      showToast(t("chat.fileActionFailed", { error: errorText(reason) }), "error");
    }
  }, [link.ref, showToast, t]);

  const copyPath = useCallback(async () => {
    try {
      if (!await writeClipboardText(await resolveFileResourcePath(link.ref))) throw new Error(t("present.copyFailed"));
    } catch (reason) {
      showToast(t("chat.fileActionFailed", { error: errorText(reason) }), "error");
    }
  }, [link.ref, showToast, t]);

  const menuItems = useMemo<ContextMenuItem[]>(() => [
    { key: "preview", icon: <FileText size={13} />, label: t("present.open"), onSelect: () => { close(); void run("preview"); } },
    ...(capabilities.source ? [{ key: "source", icon: <Code2 size={13} />, label: t("present.source"), onSelect: () => { close(); void run("source"); } }] : []),
    ...(capabilities.revealTree ? [{ key: "reveal-tree", icon: <FolderSearch size={13} />, label: t("present.revealTree"), onSelect: () => { close(); void run("reveal-tree"); } }] : []),
    { type: "separator" as const, key: "path-separator" },
    ...(capabilities.copyPath ? [{ key: "copy-path", icon: <Copy size={13} />, label: t("present.copyPath"), onSelect: () => { close(); void copyPath(); } }] : []),
    ...(capabilities.saveCopy ? [{ key: "save-copy", icon: <Save size={13} />, label: t("present.saveCopy"), onSelect: () => { close(); void run("save-copy"); } }] : []),
    ...(capabilities.openNative ? [{ key: "open-native", icon: <ExternalLink size={13} />, label: t("present.openNative"), onSelect: () => { close(); void run("open-native"); } }] : []),
    ...(capabilities.revealNative ? [{ key: "reveal-native", icon: <FolderOpen size={13} />, label: t("present.revealNative"), onSelect: () => { close(); void run("reveal-native"); } }] : []),
  ], [capabilities, close, copyPath, run, t]);

  const onContextMenu = useCallback((event: { preventDefault(): void; stopPropagation(): void; clientX: number; clientY: number }) => {
    event.preventDefault();
    event.stopPropagation();
    setPoint(contextMenuPointFromEvent(event as never));
  }, []);

  const menu = <ContextMenu open={point !== null} point={point} items={menuItems} onClose={close} minWidth={220} ariaLabel={t("present.more")} />;
  return { open: run, onContextMenu, menu, capabilities };
}

export function ChatFileReferenceAnchor({ link, href, children }: { link: ChatFileLink; href: string; children: ReactNode }) {
  const { open, onContextMenu, menu } = useChatFileLinkActions(link);
  return <>
    <a
      className="md-rich-link md-rich-link--local"
      href={href}
      title={link.path}
      onClick={(event) => { event.preventDefault(); void open("preview"); }}
      onAuxClick={(event) => event.button === 1 && event.preventDefault()}
      onMouseDown={(event) => { if (event.button === 1) event.preventDefault(); }}
      onContextMenu={onContextMenu}
    >
      <ExternalLink aria-hidden="true" size={13} strokeWidth={2} />
      <span className="md-rich-link__label">{children}</span>
    </a>
    {menu}
  </>;
}

export function ChatFileReferenceCode({ link, children }: { link: ChatFileLink; children: ReactNode }) {
  const { open, onContextMenu, menu } = useChatFileLinkActions(link);
  return <>
    <button type="button" className="md-code md-code--presented-file" title={link.path}
      onClick={() => void open("preview")} onContextMenu={onContextMenu}>{children}</button>
    {menu}
  </>;
}
