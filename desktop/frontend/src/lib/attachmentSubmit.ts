import type { AppBindings } from "./bridge";
import type { StructuredInvocationSubmit } from "./invocationDisplay";
import type { ComposerTarget } from "../generated/desktopContract.generated";
import type { WorkspaceReference } from "./composerDraftTypes";

export async function restoreExternalFolderReferences(app: AppBindings, target: ComposerTarget, refs: WorkspaceReference[]) {
  const external = refs.filter(ref => ref.isDir && ref.path.startsWith("__reasonix_external_folder/"));
  if (!external.length) return;
  const token = await captureAttachmentTarget(app, target, ["AttachDroppedForTarget"]);
  try {
    for (const ref of external) {
      if (!ref.displayPath) throw new Error("External folder source is unavailable; reattach the folder before sending");
      const restored = await app.AttachDroppedForTarget!(token, ref.displayPath);
      if (restored.kind !== "workspace" || !restored.isDir || restored.path !== ref.path) {
        throw new Error("External folder changed; reattach the folder before sending");
      }
    }
  } finally { await app.ReleaseAttachmentTarget?.(token); }
}

const pendingImageSubmissions = new Map<string, { fingerprint: string; id: string }>();

export function imageSubmissionIdentity(draft: string, fingerprint: string): string {
  const previous = pendingImageSubmissions.get(draft);
  if (previous?.fingerprint === fingerprint) return previous.id;
  const id = `image-${crypto.randomUUID()}`;
  pendingImageSubmissions.set(draft, { fingerprint, id });
  return id;
}

export function settleImageSubmission(draft: string, id?: string): void {
  if (pendingImageSubmissions.get(draft)?.id === id) pendingImageSubmissions.delete(draft);
}

export async function prepareImageSubmission(
  app: AppBindings,
  target: ComposerTarget,
  draftKey: string,
  fingerprint: string,
  attachments: Array<{ draftId?: string; clientAttachmentId?: string; recoveryPath?: string; displayName?: string }>,
  structured: StructuredInvocationSubmit | undefined,
  display: string,
  input: string,
) {
  const token = await captureImageTarget(app, target);
  const submissionId = imageSubmissionIdentity(draftKey, fingerprint);
  const restored = [];
  try {
    for (const [index, item] of attachments.entries()) {
      let draftId = item.draftId;
      if (!draftId && item.recoveryPath) {
        if (!app.AttachmentDataURLForTarget || !app.StageImageForTarget) throw new Error("unsupported: attachments-v2");
        const data = await app.AttachmentDataURLForTarget(token, item.recoveryPath);
        const staged = await app.StageImageForTarget(token, `${submissionId}:restore:${index}`, item.displayName || "image", "", data);
        draftId = staged.draftId;
      }
      if (!draftId) throw new Error("Image source is unavailable; reattach the image before sending");
      restored.push({ clientAttachmentId: item.clientAttachmentId || `image-${index + 1}`, draftId });
    }
  } catch (error) {
    await app.ReleaseAttachmentTarget?.(token).catch(() => {});
    throw error;
  }
  return {
    token,
    submissionId,
    structured: {
      display: structured?.display ?? display,
      input: structured?.input ?? input,
      invocations: structured?.invocations ?? [],
      attachmentTarget: token,
      attachmentSubmissionId: submissionId,
      attachments: restored,
    } satisfies StructuredInvocationSubmit,
  };
}

export function submitAttachmentTurn(app: AppBindings, submissionId: string, structured: StructuredInvocationSubmit, original: string, initialGoal?: { goal: string; toolApprovalMode?: string }) {
  if (!app.StartTurnForAttachmentTarget || !structured.attachmentTarget) throw new Error("unsupported: attachments-v2");
  return app.StartTurnForAttachmentTarget(structured.attachmentTarget, submissionId, {
    input: structured.input, display: structured.display, original,
    goal: initialGoal?.goal, toolApprovalMode: initialGoal?.toolApprovalMode,
    invocations: structured.invocations, attachments: structured.attachments ?? [],
  });
}

export function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

export async function captureImageTarget(app: AppBindings, target: ComposerTarget): Promise<string> {
  return captureAttachmentTarget(app, target, target.kind === "draft"
    ? ["StageImageForTarget", "AttachmentDataURLForTarget"]
    : ["StageImageForTarget", "ReadDraftImageForTarget"]);
}

export async function captureAttachmentTarget(
  app: AppBindings,
  target: ComposerTarget,
  required: ReadonlyArray<keyof AppBindings>,
): Promise<string> {
  if (!app.CaptureAttachmentTarget || required.some((name) => typeof app[name] !== "function")) {
    throw new Error("unsupported: attachments-v2");
  }
  const captured = await app.CaptureAttachmentTarget(target);
  if (!captured.capabilities.includes("attachments-v2")) throw new Error("unsupported: attachments-v2");
  return captured.token;
}

export async function stageImageFile(app: AppBindings, target: string, draftKey: string, file: File, persistentTarget?: ComposerTarget) {
  const dataURL = await readFileAsDataURL(file);
  const staged = await app.StageImageForTarget!(target, `${draftKey}:${file.name}:${file.lastModified}`, file.name, file.type, dataURL);
	const path = staged.draftId ? `draft:${staged.draftId}` : staged.path;
	if (!path) throw new Error("attachment staging returned no source");
	const previewUrl = staged.draftId
		? await app.ReadDraftImageForTarget!(target, staged.draftId)
		: await app.AttachmentDataURLForTarget!(target, path);
	const recoveryPath = persistentTarget && staged.draftId ? await app.SavePastedImageForComposerTarget(persistentTarget, dataURL) : undefined;
	return { path, previewUrl, displayName: file.name, draftId: staged.draftId || undefined, recoveryPath, file };
}
