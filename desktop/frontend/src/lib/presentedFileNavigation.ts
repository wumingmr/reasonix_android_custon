/**
 * Compatibility seam for callers created with the first present-file UI.
 *
 * Navigation itself lives in the running instance's FileNavigationOwner, which
 * the runtime registers through `fileNavigationCommands`. Nothing here holds a
 * request, a listener or a cancellation controller of its own; these exports
 * only keep the original names bound to the current instance.
 */
import type { FileAction } from "./fileNavigationCommands";

export type { FileResourceRef, FileAccessContext, ResolvedFileResource } from "./fileResource";
export { resolveFileResourcePath } from "./fileResource";
export { openResource, performResourceAction } from "./fileNavigationCommands";
export type { FileNavigationOutcome } from "./fileNavigationOwner";

export type PresentedFileAction = FileAction;
