import type { KeyboardEvent } from "react";
import { sha256 } from "./attachDedup";
import type { PersistentComposerDraft } from "./composerDraftTypes";
import { replaceInvocationTextRange } from "./invocationDisplay";

export function cutUnchangedComposerText(
  current: PersistentComposerDraft,
  captured: Pick<PersistentComposerDraft, "text" | "invocations" | "pastedBlocks">,
  selection: { from: number; to: number; afterInvocationId?: string },
): PersistentComposerDraft | undefined {
  // An async clipboard write owns the captured content, never later edits.
  if (current.text !== captured.text
    || JSON.stringify(current.invocations) !== JSON.stringify(captured.invocations)
    || JSON.stringify(current.pastedBlocks) !== JSON.stringify(captured.pastedBlocks)) return;
  const next = replaceInvocationTextRange(current.text, current.invocations, selection.from, selection.to, "", selection.afterInvocationId);
  return { ...current, text: next.text, invocations: next.invocations };
}

function fileKey(file: File): string {
  return `${file.name}:${file.type}:${file.size}:${file.lastModified}`;
}

export function clipboardFiles(data: DataTransfer): File[] {
  const files = Array.from(data.files);
  const seen = new Set(files.map(fileKey));
  for (const item of Array.from(data.items)) {
    if (item.kind !== "file") continue;
    const file = item.getAsFile();
    if (!file) continue;
    const key = fileKey(file);
    if (seen.has(key)) continue;
    seen.add(key);
    files.push(file);
  }
  return files;
}

export function clipboardHasImageHint(data: DataTransfer): boolean {
  const imageType = (value: string) => {
    const type = value.toLowerCase();
    return type.startsWith("image/") || type.includes("png") || type.includes("jpeg") || type.includes("jpg") || type.includes("tiff");
  };
  return Array.from(data.items).some((item) => imageType(item.type)) || Array.from(data.types).some(imageType);
}

export function isPasteShortcut(e: KeyboardEvent<HTMLElement>): boolean {
  return e.key.toLowerCase() === "v" && (e.metaKey || e.ctrlKey) && !e.altKey;
}

export async function dataURLHash(dataUrl: string): Promise<string> {
  try {
    const res = await fetch(dataUrl);
    return sha256(await res.blob());
  } catch {
    return "";
  }
}
