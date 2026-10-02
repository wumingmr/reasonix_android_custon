// A transient inbox-target fence: the remote tab is switching, reconnecting or
// re-aiming its route. The bridge marks it with a code and RPCErrorData
// {"transient": true}; the composer holds the message and retries it briefly
// instead of surfacing a failure the user cannot act on.

export const TRANSIENT_INBOX_TARGET_CODE = "inbox_target_transient";

// Bounded retry schedule for a held message: four attempts inside ~1.5s, the
// span a session switch or reconnect needs to settle.
export const TRANSIENT_GUIDANCE_RETRY_DELAYS_MS = [150, 350, 500, 500] as const;

export function isTransientInboxTargetError(error: unknown): boolean {
  if (error == null) return false;
  const data = typeof error === "object" ? (error as { data?: unknown }).data : undefined;
  if (data && typeof data === "object" && (data as { transient?: unknown }).transient === true) return true;
  const message = typeof error === "string"
    ? error
    : error instanceof Error
      ? error.message
      : String((error as { message?: unknown }).message ?? "");
  return message.includes(TRANSIENT_INBOX_TARGET_CODE);
}
