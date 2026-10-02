import type { ComposerDraft } from "./Composer";
import type { PersistentComposerDraft } from "../lib/composerDraftTypes";

export function emptyComposerDraft(): ComposerDraft {
  return {
    text: "",
    invocations: [],
    attachments: [],
    workspaceRefs: [],
    pastedBlocks: [],
    openPastedLabels: [],
    sessionRefs: [],
    selectedTextRefs: [],
    attachmentDedupKeys: {},
    nextPasteId: 1,
    historyIndex: -1,
    savedText: "",
    pendingGuidance: [],
    guidanceExpanded: false,
    guidanceSendingId: null,
    pendingPaste: 0,
    submitting: false,
  };
}
export function persistentComposerDraft(value: PersistentComposerDraft): ComposerDraft {
  return {
    ...emptyComposerDraft(),
    text: value.text ?? "",
    invocations: value.invocations ?? [],
    attachments: value.attachments ?? [],
    workspaceRefs: value.workspaceRefs ?? [],
    pastedBlocks: value.pastedBlocks ?? [],
    openPastedLabels: value.openPastedLabels ?? [],
    sessionRefs: value.sessionRefs ?? [],
    selectedTextRefs: value.selectedTextRefs ?? [],
  };
}

export function persistentSnapshot(draft: ComposerDraft): PersistentComposerDraft {
  return {
    text: draft.text,
    invocations: draft.invocations,
    attachments: draft.attachments,
    workspaceRefs: draft.workspaceRefs,
    pastedBlocks: draft.pastedBlocks,
    openPastedLabels: draft.openPastedLabels,
    sessionRefs: draft.sessionRefs,
    selectedTextRefs: draft.selectedTextRefs,
  };
}

export function composerDraftFingerprint(draft: ComposerDraft): string {
  return JSON.stringify([draft.text, draft.invocations, draft.attachments, draft.workspaceRefs,
    draft.sessionRefs, draft.selectedTextRefs, draft.pastedBlocks]);
}
