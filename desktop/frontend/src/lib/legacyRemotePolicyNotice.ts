import type { DictKey } from "./i18n";

// Observe the remote's actual policy, without guessing a version or changing
// its settings. Notify at most once for each remote session.
export function createLegacyRemotePolicyNoticeTracker() {
  const warned = new Set<string>();
  return (sessionKey: string, qualityFloor?: string, stopCause?: string, qualityPause = false): DictKey | undefined => {
    const delivery = qualityFloor === "delivery";
    if (!delivery && stopCause !== "evaluator_unavailable" && !qualityPause) return;
    if (warned.has(sessionKey)) return;
    warned.add(sessionKey);
    return delivery ? "remote.legacyDeliveryPolicy" : "remote.legacyExecutionPolicy";
  };
}
