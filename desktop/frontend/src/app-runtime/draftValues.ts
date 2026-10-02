import type { PersistentComposerDraft } from "../components/Composer";
import type { SessionDraftSettings } from "../generated/desktopContract.generated";

export const cloneDraftContent = (content: PersistentComposerDraft) => structuredClone(content);
export const cloneDraftSettings = (settings: SessionDraftSettings) => structuredClone(settings);

const EMPTY_CONTENT: PersistentComposerDraft = {
  text: "",
  invocations: [],
  attachments: [],
  workspaceRefs: [],
  pastedBlocks: [],
  openPastedLabels: [],
  sessionRefs: [],
  selectedTextRefs: [],
};

export function parseContent(raw: string): PersistentComposerDraft {
  try {
    const value = JSON.parse(raw || "{}") as Partial<PersistentComposerDraft>;
    return {
      text: typeof value.text === "string" ? value.text : "",
      invocations: Array.isArray(value.invocations) ? value.invocations : [],
      attachments: Array.isArray(value.attachments) ? value.attachments.map(({ previewUrl: _previewUrl, ...attachment }) => attachment) : [],
      workspaceRefs: Array.isArray(value.workspaceRefs) ? value.workspaceRefs : [],
      pastedBlocks: Array.isArray(value.pastedBlocks) ? value.pastedBlocks : [],
      openPastedLabels: Array.isArray(value.openPastedLabels) ? value.openPastedLabels : [],
      sessionRefs: Array.isArray(value.sessionRefs) ? value.sessionRefs : [],
      selectedTextRefs: Array.isArray(value.selectedTextRefs) ? value.selectedTextRefs : [],
    };
  } catch {
    return { ...EMPTY_CONTENT };
  }
}

export function contentJSON(content: PersistentComposerDraft): string {
  return JSON.stringify({
    ...content,
    attachments: content.attachments.map(({ previewUrl: _previewUrl, ...attachment }) => attachment),
  });
}
