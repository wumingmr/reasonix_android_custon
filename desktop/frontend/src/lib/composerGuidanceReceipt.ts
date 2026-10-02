export type GuidanceReceiptTracker = {
  start(draftKey: string): void;
  recordConsumed(draftKey: string, itemId: string): void;
  takeConsumed(draftKey: string, itemId: string): boolean;
  finish(draftKey: string): void;
};

export function createGuidanceReceiptTracker(): GuidanceReceiptTracker {
  const inFlight = new Map<string, number>();
  const consumedBeforeReceipt = new Map<string, Set<string>>();
  return {
    start(draftKey) {
      inFlight.set(draftKey, (inFlight.get(draftKey) ?? 0) + 1);
    },
    recordConsumed(draftKey, itemId) {
      if ((inFlight.get(draftKey) ?? 0) === 0) return;
      const consumed = consumedBeforeReceipt.get(draftKey) ?? new Set<string>();
      consumed.add(itemId);
      consumedBeforeReceipt.set(draftKey, consumed);
    },
    takeConsumed(draftKey, itemId) {
      return consumedBeforeReceipt.get(draftKey)?.delete(itemId) ?? false;
    },
    finish(draftKey) {
      const remaining = (inFlight.get(draftKey) ?? 1) - 1;
      if (remaining > 0) {
        inFlight.set(draftKey, remaining);
        return;
      }
      inFlight.delete(draftKey);
      consumedBeforeReceipt.delete(draftKey);
    },
  };
}
