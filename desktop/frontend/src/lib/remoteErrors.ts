import type { RemoteConnectionStatus } from "./types";

export type RemoteConnectionErrorKind =
  | "connection_failed"
  | "auth_failed"
  | "host_key_rejected"
  | "host_key_mismatch"
  | "degraded";

export type RemoteConnectionErrorSummaryKey = `remote.error.summary.${RemoteConnectionErrorKind}`;

export function remoteConnectionErrorKind(status?: RemoteConnectionStatus): RemoteConnectionErrorKind {
  if (status?.state === "degraded") return "degraded";
  return status?.errorDetails?.code ?? "connection_failed";
}

export function remoteConnectionErrorSummaryKey(status?: RemoteConnectionStatus): RemoteConnectionErrorSummaryKey {
  return `remote.error.summary.${remoteConnectionErrorKind(status)}`;
}

export function isRemoteHostKeyMismatch(status?: RemoteConnectionStatus): boolean {
  return remoteConnectionErrorKind(status) === "host_key_mismatch";
}

export function isRemoteTerminalFailure(status?: RemoteConnectionStatus): boolean {
  return status?.state === "stopped" && Boolean(status.error);
}

export function isRemoteDegradedWarning(status?: RemoteConnectionStatus): boolean {
  return status?.state === "degraded" && Boolean(status.error);
}

/**
 * A local runtime on the serve host owns the session. Mirrors the refusal
 * wordings desktop/remote_tab.go accepts in remoteSessionTakenOver plus the
 * transcript API's conflict surface ("remote transcript read failed (HTTP 409)"),
 * which is how a spectator's Follow request fails.
 */
export function isRemoteTakeoverError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error ?? "");
  return /\(HTTP 409\)/.test(message)
    || /taken over by a local Reasonix|writer is owned by another runtime|in use by another Reasonix process/.test(message);
}
