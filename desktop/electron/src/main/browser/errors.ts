import { RpcError } from "../rpc.js";

// Codes the Go BrowserExecutor maps onto its kernel sentinels; anything else
// is a transport failure and therefore an unknown outcome for a reserved write.
export const BROWSER_ERR_STALE_REFERENCE = -32010;
export const BROWSER_ERR_TAKEN_OVER = -32011;
export const BROWSER_ERR_NO_GRANT = -32012;

export type BrowserFailureKind = "page_not_ready" | "script_runtime_error" | "surface_unavailable" | "invalid_image" | "stale_document" | "capability_unsupported" | "cancelled";

export function browserFailure(kind: BrowserFailureKind, detail: string): RpcError {
  return new RpcError(-32013, `${kind}: ${detail}`, { kind });
}

export function staleReference(detail: string): RpcError {
  return new RpcError(BROWSER_ERR_STALE_REFERENCE, `stale reference: ${detail}`, { kind: "stale_document" });
}

export function takenOver(detail: string): RpcError {
  return new RpcError(BROWSER_ERR_TAKEN_OVER, `tab taken over by the user: ${detail}`, { kind: "taken_over" });
}

export function noGrant(detail: string): RpcError {
  return new RpcError(BROWSER_ERR_NO_GRANT, `no browser grant: ${detail}`);
}
