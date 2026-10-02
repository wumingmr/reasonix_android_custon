import { addBreadcrumb } from "./breadcrumbs";
import { desktopHost, type RendererDiagnosticPayload } from "./desktopHost";

export type TranscriptDiagnosticStage =
  | "none"
  | "baseline_read"
  | "baseline_validate"
  | "snapshot_install"
  | "delta_read"
  | "delta_validate"
  | "delta_apply";

export type TranscriptDiagnosticReason =
  | "transport_rejected"
  | "protocol_version"
  | "snapshot_missing"
  | "history_not_ready"
  | "revision_regressed"
  | "revision_gap"
  | "business_gap"
  | "frame_cut_mismatch"
  | "sampling_identity_missing"
  | "sampling_gap"
  | "settlement_not_committed"
  | "settlement_identity_mismatch"
  | "resync_required"
  | "consumer_error"
  | "service_stopping"
  | "unknown";

export type TranscriptDiagnostic = RendererDiagnosticPayload & {
  kind: "transcript";
  event: "failure" | "summary" | "recovered" | "stopped";
  stage: TranscriptDiagnosticStage;
  reason: TranscriptDiagnosticReason;
  transport: "local" | "remote";
  errorType: "classified" | "error" | "string" | "object" | "unknown";
};

const active = new Map<number, string>();
let nextOwner = 1;
const crashSnapshotHost = globalThis as typeof globalThis & { __reasonixTranscriptDiagnostics?: string };

function formatDiagnostic(event: TranscriptDiagnostic): string {
  return `${event.event} stage=${event.stage} reason=${event.reason} type=${event.errorType} transport=${event.transport} revision=${event.revision} commit=${event.commit} attempts=${event.attempts} failures=${event.failures} duration_ms=${event.durationMs}`;
}

function syncCrashSnapshot(): void {
  crashSnapshotHost.__reasonixTranscriptDiagnostics = [...active.values()].join("\n");
}

export function newTranscriptDiagnosticOwner(): number {
  return nextOwner++;
}

export function publishTranscriptDiagnostic(owner: number, event: TranscriptDiagnostic, visible = true): void {
  const message = formatDiagnostic(event);
  if (event.event === "failure" || event.event === "summary") active.set(owner, message);
  else active.delete(owner);
  syncCrashSnapshot();
  if (!visible) return;
  addBreadcrumb("transcript.v2", message);
  try {
    void desktopHost().native.recordRendererDiagnostic(event).catch(() => undefined);
  } catch {
    // Diagnostics are optional; a broken host must not interrupt following.
  }
}

export function clearTranscriptDiagnostic(owner: number): void {
  active.delete(owner);
  syncCrashSnapshot();
}
