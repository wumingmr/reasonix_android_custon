import { app } from "./bridge";
import { useBrowserPanelStore, waitForBrowserHost } from "./browserPanelStore";
import { useActivityBarStore } from "../store/activityBar";
import { useLayoutStore } from "../store/layout";
import { useRemoteStore } from "../store/remote";
import {
  fileAccessContext,
  resolveFileResource,
  resolveFileResourcePath,
  type FileResourceRef,
  type ResolvedFileResource,
} from "./fileResource";
import {
  FileNavigationOwner,
  type FileNavigationOutcome,
  type FileNavigationParams,
} from "./fileNavigationOwner";
import { pathExtension } from "./filePaths";
import { bindFileBrowserPreview } from "./fileBrowserPreviewBindings";

/** Every action a file row or preview menu can ask for. */
export type FileAction =
  | "preview" | "source" | "reveal-tree"
  | "browser" | "open-native" | "reveal-native" | "save-copy";

const CANCELLED: FileNavigationOutcome = { status: "cancelled", reason: "superseded" };
const asError = (reason: unknown): Error => (reason instanceof Error ? reason : new Error(String(reason)));
const isNavigation = (action: FileAction): action is FileNavigationParams["action"] =>
  action === "preview" || action === "source" || action === "reveal-tree";

export const isHTMLResource = (ref: Pick<FileResourceRef, "hostId" | "path">): boolean =>
  ref.hostId === "local" && (pathExtension(ref.path) === "html" || pathExtension(ref.path) === "htm");

let previewOperationSequence = 0;
function previewOperationID(ref: FileResourceRef): string {
  previewOperationSequence += 1;
  return `file-preview-${Date.now().toString(36)}-${previewOperationSequence.toString(36)}-${ref.source}`;
}

/**
 * Bring the dock that presents this resource to the front and return its tab.
 * The dock opens before the navigation record is committed, so the panel that
 * mounts for it already has the record to read on its first render.
 */
export function revealFileResourceDock(ref: FileResourceRef): string {
  const layout = useLayoutStore.getState();
  if (ref.hostId !== "local") {
    const remote = useRemoteStore.getState();
    remote.openExplorer(ref.hostId);
    remote.setExplorerTab("files");
    layout.setRightDockMode("remote");
    layout.setWorkspacePanelOpen(true);
    return useActivityBarStore.getState().openEntry("remote", "Remote");
  }
  layout.setRightDockMode("files");
  layout.setWorkspacePanelOpen(true);
  return useActivityBarStore.getState().openEntry("file", "Files");
}

function revealBrowserDock(): string {
  const layout = useLayoutStore.getState();
  layout.setRightDockMode("browser");
  layout.setWorkspacePanelOpen(true);
  return useActivityBarStore.getState().openEntry("browser", "Browser");
}

export function createFileNavigationOwner(): FileNavigationOwner {
  return new FileNavigationOwner({ resolve: resolveFileResource, revealDock: revealFileResourceDock });
}

// The owner of the running app instance. The runtime registers the instance it
// holds; a standalone dock (a test or the browser fixture) gets its own, so the
// compatibility entry points below never fall back to module-level state.
let active: FileNavigationOwner | null = null;

export function setFileNavigationOwner(owner: FileNavigationOwner | null): void {
  active = owner;
}

/** The registered instance, or null. Reading never creates one. */
export function currentFileNavigationOwner(): FileNavigationOwner | null {
  return active;
}

export function fileNavigationOwner(): FileNavigationOwner {
  if (!active) active = createFileNavigationOwner();
  return active;
}

/** Release a preview URL this operation created but never handed to a browser tab. */
async function releaseBrowserPreview(url: string): Promise<void> {
  const store = useBrowserPanelStore.getState();
  const tab = store.tabs.find((candidate) => candidate.url === url);
  // A tab that took the URL owns it: closing revokes it through the store's own
  // rule. Only a URL no tab holds is revoked here, so it is revoked exactly once.
  if (tab) await store.close(tab.id);
  else await app.RevokeWorkspaceBrowserPreview(url).catch(() => undefined);
}

async function openBrowserPreview(ref: FileResourceRef, userInitiated = false): Promise<FileNavigationOutcome> {
  if (ref.hostId !== "local") {
    return { status: "failed", error: new Error("Remote file browser preview is unavailable; save a copy to this device first") };
  }
  const operation = fileNavigationOwner().beginOperation({
    sessionTabId: ref.tabId,
    dockTabId: revealBrowserDock(),
  });
  const finish = (outcome: FileNavigationOutcome): FileNavigationOutcome => {
    const owned = operation.owns();
    operation.finish();
    return owned ? outcome : CANCELLED;
  };
  const openFilePreview = (app as Partial<typeof app>).OpenFileBrowserPreviewForTab;
  if (typeof openFilePreview === "function") {
    try {
      if (!operation.owns()) return finish(CANCELLED);
      const result = await openFilePreview.call(app, ref.tabId, {
        source: ref.source,
        path: ref.path,
        toolCallId: ref.source === "presented" ? ref.toolCallId : undefined,
        operationId: previewOperationID(ref),
        expectedSessionGeneration: ref.sessionGeneration,
        userInitiated,
      });
      if (!operation.owns()) {
        await waitForBrowserHost().then((host) => host.close(result.tabId)).catch(() => undefined);
        return finish(CANCELLED);
      }
      await waitForBrowserHost();
      await useBrowserPanelStore.getState().refreshAndActivate(result.tabId);
      bindFileBrowserPreview(result.tabId, ref, result.url);
      if (result.error || result.status === "failed") {
        return finish({ status: "failed", error: new Error(result.error || "The built-in browser could not load this preview") });
      }
      return finish({ status: "opened", resource: resourceOf(ref) });
    } catch (error) {
      return finish({ status: "failed", error: asError(error) });
    }
  }

  if (ref.source === "reference") {
    return finish({ status: "failed", error: new Error("This desktop version cannot open answer references in the built-in browser") });
  }
  let url: string;
  try {
    url = ref.source === "presented"
      ? await app.CreatePresentedBrowserPreviewForTab(ref.tabId, ref.toolCallId, ref.path)
      : await app.CreateWorkspaceBrowserPreviewForTab(ref.tabId, ref.path);
  } catch (error) {
    return finish({ status: "failed", error: asError(error) });
  }
  // A URL this command created and no browser tab ever took is its own to
  // release, at every point where the operation may have lost its dock.
  const cancelledAfterCreation = async (): Promise<FileNavigationOutcome> => {
    await releaseBrowserPreview(url);
    return finish({ status: "cancelled", reason: "superseded" });
  };
  try {
    if (!operation.owns()) return await cancelledAfterCreation();
    await waitForBrowserHost();
    if (!operation.owns()) return await cancelledAfterCreation();
    const tab = await useBrowserPanelStore.getState().open(url, true, operation.signal, ref.tabId);
    bindFileBrowserPreview(tab.id, ref, url);
  } catch (error) {
    await releaseBrowserPreview(url);
    return finish({ status: "failed", error: asError(error) });
  }
  if (!operation.owns()) return await cancelledAfterCreation();
  return finish({ status: "opened", resource: resourceOf(ref) });
}

/** Refresh a bound file preview without returning a user-owned tab to Agent control. */
export function refreshBrowserResource(ref: FileResourceRef): Promise<FileNavigationOutcome> {
  return openBrowserPreview(ref, true);
}

/** A direct-action receipt; dock navigation resolves identity before storing it. */
const resourceOf = (ref: FileResourceRef): ResolvedFileResource => ({
  hostId: ref.hostId,
  path: ref.path,
  identityPath: ref.path.replace(/\\/g, "/"),
  requestedPath: ref.path,
  access: fileAccessContext(ref),
});

/**
 * Run one action for a caller-supplied file reference. Navigation actions
 * commit a record on the target dock; the others act on the resource directly.
 */
export async function performResourceAction(ref: FileResourceRef, action: FileAction): Promise<FileNavigationOutcome> {
  if (isNavigation(action)) {
    // The remote tree resolves a workspace path before it can reveal it, so the
    // resolution that guards this command lives in the owner's open command.
    return Promise.resolve(fileNavigationOwner().open({ ref, params: { action, view: "files" } }));
  }
  try {
    switch (action) {
      case "browser":
        return openBrowserPreview(ref);
      case "open-native":
        if (ref.hostId !== "local") return { status: "failed", error: new Error("This remote host does not expose a desktop opener") };
        if (ref.source === "reference") await app.OpenReferencePathForTab(ref.tabId, ref.path);
        else if (ref.source === "presented") await app.OpenPresentedPathForTab(ref.tabId, ref.toolCallId, ref.path);
        else await app.OpenWorkspacePathForTab(ref.tabId, ref.path);
        return { status: "opened", resource: resourceOf(ref) };
      case "reveal-native":
        if (ref.hostId !== "local") return { status: "failed", error: new Error("This remote host does not expose a desktop file manager") };
        if (ref.source === "reference") await app.RevealReferencePathForTab(ref.tabId, ref.path);
        else if (ref.source === "presented") await app.RevealPresentedPathForTab(ref.tabId, ref.toolCallId, ref.path);
        else await app.RevealWorkspacePathForTab(ref.tabId, ref.path);
        return { status: "opened", resource: resourceOf(ref) };
      case "save-copy":
        // The dialog completes against the path captured here; a later navigation
        // only takes away the right to report this receipt, never the write.
        if (ref.hostId !== "local") {
          if (ref.source === "reference") return { status: "failed", error: new Error("This remote host does not expose a file transfer for an answer reference") };
          if (ref.source === "presented") await app.SaveRemotePresentedFileAs(ref.tabId, ref.hostId, ref.toolCallId, ref.path);
          else await app.SaveRemoteFileAs(ref.hostId, await resolveFileResourcePath(ref));
        } else if (ref.source === "reference") await app.SaveReferencePathAsForTab(ref.tabId, ref.path);
        else if (ref.source === "presented") await app.SavePresentedPathAsForTab(ref.tabId, ref.toolCallId, ref.path);
        else await app.SaveWorkspacePathAsForTab(ref.tabId, ref.path);
        return { status: "opened", resource: resourceOf(ref) };
    }
  } catch (error) {
    return { status: "failed", error: asError(error) };
  }
}

export function openResource(
  ref: FileResourceRef,
  options: { view: "preview" | "source" | "browser" },
): Promise<FileNavigationOutcome> {
  return performResourceAction(ref, options.view === "preview" && isHTMLResource(ref) ? "browser" : options.view);
}
