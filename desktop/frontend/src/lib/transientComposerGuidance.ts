import type { AppBindings } from "./bridge";
import type { StructuredInvocationSubmit } from "./invocationDisplay";
import { pendingFollowups, type PendingFollowup } from "./pendingFollowup";
import { formatInboxError } from "./inboxError";
import type { Translator } from "./i18n";
import { TRANSIENT_GUIDANCE_RETRY_DELAYS_MS, isTransientInboxTargetError } from "./transientInboxTarget";

type RetryContext = {
  app: AppBindings;
  transientRetriesRef: { current: Map<string, { cancelled: boolean }> };
  pendingKeyRef: { current: string };
  submitPendingKey: string;
  submitDraftKey: string;
  submitTabId?: string;
  inboxSessionPath?: string;
  submittedDraft: string;
  queueOnly: boolean;
  followupDraftFingerprint: (key: string) => string;
  clearSubmittedDraft: (key: string) => void;
  setGuidanceRetryNonce: (update: (value: number) => number) => void;
  showToast: (message: string, kind: "info" | "warn") => void;
  t: Translator;
  locale: Parameters<typeof formatInboxError>[1];
};

export function createTransientGuidance(context: RetryContext) {
  const { app, transientRetriesRef, pendingKeyRef, submitPendingKey, submitDraftKey, submitTabId,
    inboxSessionPath, submittedDraft, queueOnly, followupDraftFingerprint, clearSubmittedDraft,
    setGuidanceRetryNonce, showToast, t, locale } = context;
  // Retry temporary route fences while preserving the exact pending request.
  // Exhausted requests remain visible for manual reconciliation.
  const retryTransientGuidanceEnqueue = (
    request: PendingFollowup,
    options: {
      pendingKey: string;
      submitDraftKey: string;
      submitTabId: string;
      sessionPath: string;
      submittedDraft: string;
      queueOnly: boolean;
      turnId?: string;
    },
  ) => {
    if (transientRetriesRef.current.has(request.key)) return;
    const token = { cancelled: false };
    transientRetriesRef.current.set(request.key, token);
    void (async () => {
      const { enqueueComposerGuidance } = await import("./inboxGuidanceSubmit");
      for (const delayMs of TRANSIENT_GUIDANCE_RETRY_DELAYS_MS) {
        await new Promise((resolve) => window.setTimeout(resolve, delayMs));
        if (token.cancelled || transientRetriesRef.current.get(request.key) !== token) return;
        // The user moved to another session: leave the pending request for
        // that session's own reconciliation instead of retrying a gone route.
        if (options.pendingKey !== pendingKeyRef.current) {
          transientRetriesRef.current.delete(request.key);
          return;
        }
        try {
          const target = app.CaptureInboxTarget
            ? await app.CaptureInboxTarget(options.submitTabId, options.sessionPath)
            : undefined;
          const receipt = await enqueueComposerGuidance(app, { ...request, target }, options.queueOnly, options.turnId);
          if (receipt?.error) throw new Error(receipt.error);
          if (!receipt?.itemId) throw new Error("Follow-up receipt unconfirmed");
          pendingFollowups.clear(options.pendingKey, request);
          if (followupDraftFingerprint(options.submitDraftKey) === options.submittedDraft) clearSubmittedDraft(options.submitDraftKey);
          setGuidanceRetryNonce((value) => value + 1);
          showToast(t("runtime.queued"), "info");
          transientRetriesRef.current.delete(request.key);
          return;
        } catch (error) {
          if (isTransientInboxTargetError(error)) continue;
          // A permanent refusal is actionable: surface it and drop the hold.
          pendingFollowups.clear(options.pendingKey, request);
          showToast(formatInboxError(error, locale), "warn");
          transientRetriesRef.current.delete(request.key);
          return;
        }
      }
      // The window outlasted the bound: keep the pending request so its
      // banner stays visible and a later send reconciles the receipt.
      if (transientRetriesRef.current.get(request.key) === token) transientRetriesRef.current.delete(request.key);
    })();
  };
  const holdTransientGuidance = (
    error: unknown,
    input: { display: string; submit: string; structured?: StructuredInvocationSubmit; turnId?: string },
  ): boolean => {
    if (!isTransientInboxTargetError(error)) return false;
    if (!submitPendingKey) return false;
    const existing = pendingFollowups.get(submitPendingKey);
    const request: PendingFollowup = existing ?? {
      key: `followup-${crypto.randomUUID()}`,
      tabId: submitTabId || "",
      display: input.display,
      submit: input.submit,
      structured: input.structured,
      draft: submittedDraft,
    };
    pendingFollowups.set(submitPendingKey, request);
    retryTransientGuidanceEnqueue(request, {
      pendingKey: submitPendingKey,
      submitDraftKey,
      submitTabId: submitTabId || "",
      sessionPath: inboxSessionPath || "",
      submittedDraft,
      queueOnly,
      turnId: input.turnId,
    });
    return true;
  };

  return holdTransientGuidance;
}

export async function captureStableInboxTarget(app: AppBindings, tabId: string, sessionPath: string) {
  if (!app.CaptureInboxTarget) return undefined;
  for (let attempt = 0; ; attempt++) {
    try { return await app.CaptureInboxTarget(tabId, sessionPath); }
    catch (error) {
      if (!isTransientInboxTargetError(error) || attempt >= TRANSIENT_GUIDANCE_RETRY_DELAYS_MS.length) throw error;
      await new Promise(resolve => window.setTimeout(resolve, TRANSIENT_GUIDANCE_RETRY_DELAYS_MS[attempt]));
    }
  }
}
