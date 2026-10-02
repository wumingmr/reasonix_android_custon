import { app } from "./bridge";
import { pathExtension } from "./filePaths";

/** Where a file reference came from: an agent presentation, the workspace, or verified answer text. */
export type FileResourceSource = "presented" | "workspace" | "reference";

type ResourceBase = { hostId: string; tabId: string; path: string; sessionGeneration?: number };

/** What a caller knows about a file: host, session, path, origin and tool call. */
export type FileResourceRef =
  | (ResourceBase & { source: "presented"; toolCallId: string })
  | (ResourceBase & { source: "workspace"; toolCallId?: string })
  // An answer-named path has no standing authorization. Every read and action
  // goes through the reference endpoints, which resolve it again for this tab.
  | (ResourceBase & { source: "reference" });

/**
 * The credentials a single read must present. Captured from the command that
 * asks for the read and never inherited by a later entry point: a workspace
 * reference carries no presented tool scope even when the same path was
 * presented earlier, so a path alone cannot re-grant an earlier presentation.
 */
export type FileAccessContext = Readonly<{
  source: FileResourceSource;
  /** Session tab whose scope authorizes the read. */
  tabId: string;
  /** Presented tool call; absent for workspace reads. */
  toolCallId?: string;
}>;

/** A file the backend entry point confirmed, with the context that reads it. */
export type ResolvedFileResource = Readonly<{
  hostId: string;
  /** Path the read entry points accept. */
  path: string;
  /** Stable backend-resolved coordinate used only for resource identity. */
  identityPath: string;
  /** Path as the caller supplied it, for display and tree reveal. */
  requestedPath: string;
  access: FileAccessContext;
}>;

export function fileAccessContext(ref: FileResourceRef): FileAccessContext {
  if (ref.source === "presented") return { source: "presented", tabId: ref.tabId, toolCallId: ref.toolCallId };
  return { source: ref.source, tabId: ref.tabId };
}

export const sameAccessContext = (left: FileAccessContext, right: FileAccessContext): boolean =>
  left.source === right.source && left.tabId === right.tabId && left.toolCallId === right.toolCallId;

/**
 * Resolve a caller spelling into the stable coordinate that identifies the
 * file. The original local spelling remains the read path because it may be an
 * external-folder token or a presented path whose access must be revalidated by
 * its scoped read entry point. Remote reads accept the resolved host coordinate.
 */
export async function resolveFileResource(ref: FileResourceRef): Promise<ResolvedFileResource> {
  const access = fileAccessContext(ref);
  const identityPath = await resolveFileResourcePath(ref);
  return {
    hostId: ref.hostId,
    path: ref.hostId === "local" ? ref.path : identityPath,
    identityPath,
    requestedPath: ref.path,
    access,
  };
}

/** Absolute path for copy-to-clipboard and save-a-copy, where display needs one. */
export function resolveFileResourcePath(ref: FileResourceRef): Promise<string> {
  if (ref.hostId !== "local") {
    if (ref.source === "reference") return Promise.resolve(ref.path);
    return ref.source === "presented"
      ? app.ResolveRemotePresentedPathForTab(ref.tabId, ref.hostId, ref.toolCallId, ref.path)
      : app.ResolveRemoteWorkspacePathForTab(ref.tabId, ref.hostId, ref.toolCallId ?? "", ref.path);
  }
  if (ref.source === "reference") return app.ResolveReferencePathForTab(ref.tabId, ref.path);
  return ref.source === "presented"
    ? app.ResolvePresentedPathForTab(ref.tabId, ref.toolCallId, ref.path)
    : app.ResolveWorkspacePathForTab(ref.tabId, ref.path);
}

const MEDIA = new Set(["html", "htm", "pdf", "png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "svg", "mp3", "wav", "ogg", "m4a", "aac", "mp4", "webm", "mov", "m4v", "ogv"]);
// SVG is an image to the previewer and a document to the editor, so both views
// are legitimate.
const BINARY = new Set(["png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "pdf", "mp3", "wav", "ogg", "m4a", "aac", "flac", "mp4", "webm", "mov", "m4v", "ogv", "zip", "tar", "gz", "7z", "rar", "doc", "docx", "xls", "xlsx", "ppt", "pptx"]);

export interface FileResourceCapabilities {
  preview: boolean;
  source: boolean;
  browser: boolean;
  revealTree: boolean;
  copyPath: boolean;
  openNative: boolean;
  revealNative: boolean;
  saveCopy: boolean;
}

/** Identity-only view of a file: capabilities depend on the host and the name. */
export type FileResourceIdentity = Readonly<{ hostId: string; path: string }>;

export const fileResourceIdentity = (resource: FileResourceIdentity): FileResourceIdentity =>
  ({ hostId: resource.hostId, path: resource.path });

/** Maps the host's verified action list onto the menu capability shape. */
export function capabilityActions(actions: readonly string[]): FileResourceCapabilities {
  const has = (action: string) => actions.includes(action);
  return {
    preview: has("preview"),
    source: has("source"),
    browser: has("browser"),
    revealTree: has("reveal-tree"),
    copyPath: has("copy-path"),
    openNative: has("open-native"),
    revealNative: has("reveal-native"),
    saveCopy: has("save-copy"),
  };
}

/** The host still revalidates every action; this snapshot only controls honest UI affordances. */
export function fileResourceCapabilities(resource: FileResourceIdentity, verified?: readonly string[]): FileResourceCapabilities {
  if (verified) return capabilityActions(verified);
  const remote = resource.hostId !== "local";
  const ext = pathExtension(resource.path);
  return {
    preview: true,
    source: !BINARY.has(ext),
    browser: !remote && MEDIA.has(ext),
    revealTree: true,
    copyPath: true,
    openNative: !remote,
    revealNative: !remote,
    saveCopy: true,
  };
}
