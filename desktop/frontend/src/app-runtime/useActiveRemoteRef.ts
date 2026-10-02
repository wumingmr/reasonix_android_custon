import { useMemo } from "react";
import type { RemoteTabRefView, TabMeta } from "../lib/types";

/**
 * The active remote session reference handed to the project tree. The tree
 * keys full tree walks, ancestor expansion and its row projection on this
 * object, so a fresh object per render re-runs all of them on every
 * transcript delta. RemoteTabRef carries exactly hostId and workspace
 * (desktop/remote_projects.go), so rebuilding from those primitives plus the
 * bound session id keeps the identity stable across unrelated tab-meta
 * refreshes too, not merely across re-renders of the same meta object.
 */
export function useActiveRemoteRef(tab: TabMeta | undefined): RemoteTabRefView | undefined {
  const hostId = tab?.remote?.hostId;
  const workspace = tab?.remote?.workspace;
  const sessionId = tab?.sessionId;
  return useMemo(
    () => (hostId === undefined || workspace === undefined ? undefined : { hostId, workspace, sessionId }),
    [hostId, workspace, sessionId],
  );
}
