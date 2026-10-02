import { app } from "./bridge";
import type { ManualSessionCreationRequest, ManualSessionCreationView } from "../generated/desktopContract.generated";

/** Ephemeral presentation only; the host owns durable recovery and drafts. */
export type ManualCreationObservation = {
 request: ManualSessionCreationRequest;
 operation?: ManualSessionCreationView;
 pending: boolean;
 failed: boolean;
};

export async function createManualSession(request: ManualSessionCreationRequest, options?: {
 onSurfaceReady: (operation: ManualSessionCreationView) => Promise<void>;
 isObservationCurrent?: () => boolean;
 onProgress?: (operation: ManualSessionCreationView) => void;
 retry?: boolean;
}) {
 let operation;
 try { operation = await app.BeginManualSessionCreation(request); }
 catch (error) {
   // A lost response is resolved against the original persisted identity.
   try { operation = await app.GetManualSessionCreation(request.operationId); }
   catch { throw error; }
 }
 if (options?.retry && operation.phase !== "ready") operation = await app.RetryManualSessionCreation(request.operationId);
 let surfaceOpened = false;
 let lastProgress = "";
 while (true) {
   // Acknowledgement transfers creation to the host. Leaving this surface
   // stops only its observer, never the durable operation or its recovery.
   if (options?.isObservationCurrent?.() === false) return operation;
   // Elapsed milliseconds change on every read; repaint only meaningful state.
   const progress = JSON.stringify([operation.phase, operation.surfaceReady, operation.ref, operation.error,
     operation.progress?.status, operation.progress?.slow, operation.progress?.errorCode]);
   if (progress !== lastProgress) { lastProgress = progress; options?.onProgress?.(operation); }
   // The host publishes a formal session before constructing its runtime. Open
   // that identity once so typing can start; completion must never reselect it.
   // Older hosts only publish a usable surface at ready.
   if (!surfaceOpened && (operation.surfaceReady || operation.phase === "ready")) {
     surfaceOpened = true;
     await options?.onSurfaceReady(operation);
   }
   const status = operation.progress?.status;
   const recovering = status && ["queued", "running", "waiting_lock", "waiting_workspace", "retrying_storage"].includes(status);
   if (options?.isObservationCurrent?.() === false || operation.phase === "ready" || status === "blocked" || status === "stopped" || status === "stopping"
     || (!recovering && operation.phase !== "reserved" && operation.phase !== "starting")) return operation;
   await new Promise(resolve => setTimeout(resolve, 150));
   if (options?.isObservationCurrent?.() === false) return operation;
   operation = await app.GetManualSessionCreation(request.operationId);
 }
}
