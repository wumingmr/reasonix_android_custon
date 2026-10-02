import type { WorkspaceSessionSummary } from "../generated/desktopContract.generated";

export function sessionMetadataPending(row: WorkspaceSessionSummary): boolean {
  return Boolean(row.metadataStatus && row.metadataStatus !== "ready" && row.metadataStatus !== "failed");
}

export function sessionIsBlank(row: WorkspaceSessionSummary): boolean {
  return row.blank && !sessionMetadataPending(row) && row.metadataStatus !== "failed";
}

export function retainPreparedSessionLabels(next: WorkspaceSessionSummary[], previous: WorkspaceSessionSummary[]): WorkspaceSessionSummary[] {
  const prior = new Map(previous.map(row => [row.ref.sessionId, row]));
  return next.map(row => {
    const old = prior.get(row.ref.sessionId);
    return sessionMetadataPending(row) && old
      ? { ...row, title: row.title || old.title, preview: row.preview || old.preview }
      : row;
  });
}
