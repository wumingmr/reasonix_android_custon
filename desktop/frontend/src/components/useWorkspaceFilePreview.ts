import { useCallback, useEffect, useRef, useState } from "react";
import { app } from "../lib/bridge";
import {
  beginKeyedResourceRequest,
  emptyKeyedResource,
  rejectKeyedResourceRequest,
  resolveKeyedResourceRequest,
} from "../lib/keyedResource";
import type { FilePreview } from "../lib/types";
import type { FileResourceSource } from "../lib/fileResource";

export type WorkspaceFilePreviewInput = Readonly<{
  /** The dock is on screen; a hidden dock reads nothing. */
  open: boolean;
  /** The path the preview shows. */
  selectedPath: string | null;
  /** Presented tool call of the selected entry; a workspace entry passes none. */
  presentedToolCallId?: string;
  /** Selects the host reader that revalidates this entry's access. */
  accessSource: FileResourceSource;
  /** The selected entry renders as source. */
  source: boolean;
  /** Dock lifecycle generation; a new one re-reads the selection. */
  generation: number;
  workspaceScopeKey: string;
  workspaceTabId: string;
  /** Bumped by the workspace refresh owner when the content behind it changed. */
  contentRevision: number;
}>;

/**
 * The dock's file preview: which read its selection implies, the keyed resource
 * that holds the result, and the one command that re-reads it. The read inputs
 * come from the selected entry, never from a path-keyed cache, so a file
 * reopened from another entry point reads under that command's credentials and
 * an unchanged set of inputs never restarts a read that is still valid.
 */
export function useWorkspaceFilePreview(input: WorkspaceFilePreviewInput) {
  const { accessSource, contentRevision, generation, open, presentedToolCallId, selectedPath, source, workspaceScopeKey, workspaceTabId } = input;
  const [resource, setResource] = useState(() => emptyKeyedResource<FilePreview>());
  const [stale, setStale] = useState(false);
  // A read that lands after this dock is gone must not be committed: nothing
  // would render it. StrictMode's simulated unmount sets this back to mounted,
  // so a replayed mount keeps the read it already started.
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);
  const requestIdRef = useRef(0);
  const scopeRef = useRef(workspaceScopeKey);
  const generationRef = useRef(generation);
  scopeRef.current = workspaceScopeKey;
  generationRef.current = generation;
  const previewKey = selectedPath
    ? `${workspaceScopeKey}\u0000preview\u0000${source ? "source" : "preview"}\u0000${accessSource}\u0000${presentedToolCallId ?? ""}\u0000${selectedPath}`
    : null;
  const preview = previewKey && resource.key === previewKey ? resource.data : null;
  const loading = previewKey != null && resource.key === previewKey && resource.status === "refreshing";
  const error = previewKey && resource.key === previewKey ? resource.error : "";

  const refreshSelected = useCallback(() => {
    if (!selectedPath) return;
    setStale(false);
    const requestId = ++requestIdRef.current;
    const requestPath = selectedPath;
    const requestGeneration = generation;
    const requestKey = `${workspaceScopeKey}\u0000preview\u0000${source ? "source" : "preview"}\u0000${accessSource}\u0000${presentedToolCallId ?? ""}\u0000${requestPath}`;
    // Dock instance, session scope, resource identity and operation revision
    // must all still match before a result may be committed for this read.
    const current = () =>
      mountedRef.current
      && requestIdRef.current === requestId
      && scopeRef.current === workspaceScopeKey
      && generationRef.current === requestGeneration;
    setResource((state) => beginKeyedResourceRequest(state, requestKey, requestId, contentRevision));
    const read = accessSource === "reference"
      ? source
        ? app.ReadReferenceFileSourceForTab(workspaceTabId, requestPath)
        : app.ReadReferenceFileForTab(workspaceTabId, requestPath)
      : presentedToolCallId
      ? source
        ? app.ReadPresentedFileSourceForTab(workspaceTabId, presentedToolCallId, requestPath)
        : app.ReadPresentedFileForTab(workspaceTabId, presentedToolCallId, requestPath)
      : app.ReadFileForTab(workspaceTabId, requestPath);
    read
      .then((next) => {
        if (current()) setResource((state) => resolveKeyedResourceRequest(state, requestKey, requestId, next, contentRevision));
      })
      .catch((reason) => {
        if (current()) {
          setResource((state) => rejectKeyedResourceRequest(state, requestKey, requestId, String((reason as Error)?.message ?? reason)));
        }
      });
  }, [accessSource, contentRevision, generation, presentedToolCallId, selectedPath, source, workspaceScopeKey, workspaceTabId]);

  // The read starts once per set of read inputs. A StrictMode mount replay, an
  // effect reconnect or a re-render reconnect calls the effect again with the
  // very same callback, and must not issue a second read for it.
  const startedReadRef = useRef<typeof refreshSelected | null>(null);
  useEffect(() => {
    if (!open || !selectedPath) {
      startedReadRef.current = null;
      return;
    }
    if (startedReadRef.current === refreshSelected) return;
    startedReadRef.current = refreshSelected;
    refreshSelected();
  }, [open, refreshSelected, selectedPath]);

  const reset = useCallback(() => setResource(emptyKeyedResource()), []);
  return {
    previewKey,
    preview,
    loadingPreview: Boolean(loading),
    previewErr: error,
    presentedFileStale: stale,
    setPresentedFileStale: setStale,
    refreshSelected,
    resetPreview: reset,
  };
}
