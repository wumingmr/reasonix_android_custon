import { ErrorMessage } from "./ErrorMessage";
import { memo, useState } from "react";
import {
  ChevronDown, ChevronUp, Code2, ExternalLink, FileArchive, FileAudio,
  FileImage, FileText, FileVideo, FolderSearch, Globe, Save,
} from "lucide-react";
import type { PresentedFileView } from "../lib/chatViewSource";
import type { TurnFileView } from "../lib/turnFiles";
import type { WireCompletionSummary } from "../lib/types";
import { useT } from "../lib/i18n";
import {
  openResource, performResourceAction, resolveFileResourcePath,
  type FileResourceRef, type PresentedFileAction,
} from "../lib/presentedFileNavigation";
import { fileResourceCapabilities } from "../lib/fileResource";
import { writeClipboardText } from "../lib/clipboard";
import { ContextMenu, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import "./PresentedFiles.css";

const basename = (path: string) => path.replaceAll("\\", "/").split("/").filter(Boolean).pop() || path;
const extension = (path: string) => basename(path).split(".").pop()?.toLowerCase() ?? "";

function iconFor(path: string) {
  const ext = extension(path);
  if (["png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "svg"].includes(ext)) return FileImage;
  if (["mp3", "wav", "ogg", "m4a", "aac", "flac"].includes(ext)) return FileAudio;
  if (["mp4", "webm", "mov", "m4v", "ogv"].includes(ext)) return FileVideo;
  if (["zip", "tar", "gz", "7z", "rar"].includes(ext)) return FileArchive;
  if (["js", "jsx", "ts", "tsx", "go", "rs", "py", "java", "c", "cpp", "css", "html", "htm", "json", "csv", "md"].includes(ext)) return Code2;
  return FileText;
}

export const PresentedFiles = memo(function PresentedFiles({ files, tabId, hostId }: { files: readonly PresentedFileView[]; tabId?: string; hostId?: string }) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const shown = expanded ? files : files.slice(0, 4);
  if (!files.length) return null;
  return <section className="presented-files" aria-label={t("present.files")}>
    <div className="presented-files__grid">
      {shown.map(file => <FileEntry key={JSON.stringify([hostId, tabId, file.toolCallId, file.path])} description={file.description}
        refValue={{ source: "presented", hostId: hostId ?? "local", tabId: tabId ?? "", toolCallId: file.toolCallId, path: file.path }} />)}
    </div>
    {files.length > 4 && <button type="button" className="presented-files__toggle" onClick={() => setExpanded(value => !value)}>
      {expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
      {t(expanded ? "present.collapse" : "present.showAll", { count: files.length })}
    </button>}
  </section>;
});

export const ModifiedFiles = memo(function ModifiedFiles({ files, summary, onOpenReview }: {
  files: readonly TurnFileView[];
  summary?: WireCompletionSummary;
  tabId?: string;
  hostId?: string;
  onOpenReview?: (summary: WireCompletionSummary, initialPath?: string) => void;
}) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const [error, setError] = useState("");
  const recorded = summary?.receipt?.diff;
  // A complete empty receipt means all edits were undone. Never reconstruct
  // its net inventory from mutation calls or sum per-call preview diffs.
  const rows = recorded && recorded.coverage !== "unknown" && (recorded.files.length > 0 || recorded.coverage === "complete") ? recorded.files.map(file => ({
    path: file.path,
    added: file.added,
    removed: file.removed,
    binary: file.binary,
    uncounted: file.uncounted,
    modeOnly: file.modeOnly,
  })) : files.map(file => ({ path: file.path, added: 0, removed: 0, binary: false, modeOnly: false, uncounted: true }));
  const shown = expanded ? rows : rows.slice(0, 4);
  if (!rows.length) return null;
  const added = recorded?.added ?? rows.reduce((total, file) => total + (file.added ?? 0), 0);
  const removed = recorded?.removed ?? rows.reduce((total, file) => total + (file.removed ?? 0), 0);
  const countsUnknown = !recorded || recorded.coverage === "unknown" || rows.some(file => file.uncounted);
  const openReview = async (path: string) => {
    setError("");
    try {
      if (summary && onOpenReview) { onOpenReview(summary, path); return; }
      setError(t("completion.diffUnavailable"));
    } catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
  };
  return <section className="turn-files" aria-label={t("present.modifiedFiles")}>
    <button type="button" className="turn-files__head" onClick={() => void openReview(rows[0]!.path)}
      aria-label={t("present.openReview")}>
      <span className="turn-files__icon"><Code2 size={18} /></span>
      <span className="turn-files__summary">
        <strong>{t("present.editedCount", { count: rows.length })}</strong>
        <span>{countsUnknown ? t("present.linesUnknown") : <><span className="turn-files__added">+{added}</span> <span className="turn-files__removed">−{removed}</span></>}</span>
        {recorded?.coverage === "partial" && <small>{t("completion.partialStats")}</small>}
      </span>
    </button>
    <ul className="turn-files__list">
      {shown.map(file => <li key={file.path}>
        <button type="button" title={file.path} aria-label={t("present.openFileReview", { name: file.path })} onClick={() => void openReview(file.path)}>
          <span>{file.path}</span>
          <span>{file.binary ? t("present.binary") : file.modeOnly ? t("completion.modeOnly") : file.uncounted ? t("present.linesUnknown") : <>
            <span className="turn-files__added">+{file.added ?? 0}</span>{" "}<span className="turn-files__removed">−{file.removed ?? 0}</span>
          </>}</span>
        </button>
      </li>)}
    </ul>
    {rows.length > 4 && <button type="button" className="presented-files__toggle" onClick={() => setExpanded(value => !value)}>
      {expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
      {t(expanded ? "present.collapse" : "present.showAll", { count: rows.length })}
    </button>}
    {error && <p className="turn-files__error" role="status"><ErrorMessage error={error} /></p>}
  </section>;
});

function FileEntry({ refValue, description }: { refValue: FileResourceRef; description?: string }) {
  const t = useT();
  const [menu, setMenu] = useState<ContextMenuPoint | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const Icon = iconFor(refValue.path);
  const capabilities = fileResourceCapabilities(refValue);
  const run = async (action: PresentedFileAction) => {
    setMenu(null); setError(""); setBusy(true);
    try {
      const outcome = action === "preview" || action === "source" || action === "browser"
        ? await openResource(refValue, { view: action })
        : await performResourceAction(refValue, action);
      // A cancelled command reports nothing: it lost its dock rather than
      // failing, and the row must not claim an error the user never hit.
      if (outcome.status === "failed") setError(outcome.error.message);
    }
    catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setBusy(false); }
  };
  const copyPath = async () => {
    setMenu(null); setError(""); setBusy(true);
    try {
      const path = await resolveFileResourcePath(refValue);
      if (!await writeClipboardText(path)) throw new Error(t("present.copyFailed"));
    } catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setBusy(false); }
  };
  const menuItems: ContextMenuItem[] = [];
  const addAction = (action: PresentedFileAction, Icon: typeof FileText, label: string) => {
    menuItems.push({ key: action, icon: <Icon size={14} />, label, onSelect: () => void run(action) });
  };
  if (capabilities.browser) addAction("browser", Globe, t("present.browser"));
  if (capabilities.revealTree) addAction("reveal-tree", FolderSearch, t("present.revealTree"));
  if (capabilities.source) addAction("source", Code2, t("present.source"));
  if (capabilities.copyPath) menuItems.push({ key: "copy-path", icon: <FileText size={14} />, label: t("present.copyPath"), onSelect: () => void copyPath() });
  if (capabilities.openNative) addAction("open-native", ExternalLink, t("present.openNative"));
  if (capabilities.revealNative) addAction("reveal-native", FolderSearch, t("present.revealNative"));
  if (capabilities.saveCopy) addAction("save-copy", Save, t("present.saveCopy"));
  return <article className="presented-file" title={refValue.path} aria-busy={busy || undefined}>
    <button type="button" className="presented-file__main" disabled={busy} onClick={() => void run("preview")}>
      <span className="presented-file__icon"><Icon size={20} /></span>
      <span className="presented-file__copy"><strong>{basename(refValue.path)}</strong>{description && <small>{description}</small>}</span>
    </button>
    <div className="presented-file__split">
      <button type="button" className="presented-file__open" disabled={busy} onClick={() => void run("preview")}>{t(busy ? "chat.loading" : "present.open")}</button>
      <button type="button" className="presented-file__more" disabled={busy} aria-label={t("present.more")} aria-haspopup="menu" aria-expanded={menu !== null} onClick={event => {
        const rect = event.currentTarget.getBoundingClientRect();
        setMenu({ left: rect.right - 208, top: rect.bottom + 4, keyboardTarget: event.currentTarget });
      }}><ChevronDown size={13} /></button>
      <ContextMenu open={menu !== null} point={menu} items={menuItems} onClose={() => setMenu(null)} minWidth={208} ariaLabel={t("present.more")} />
    </div>
    {error && <p className="presented-file__error" role="status"><ErrorMessage error={error} /></p>}
  </article>;
}
