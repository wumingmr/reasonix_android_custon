import type { AppBindings } from "./bridge";
import type { StructuredInvocationSubmit } from "./invocationDisplay";
import { resolveActiveTurnId } from "./inboxSubmit";
import { confirmFollowup, followupNotSubmitted, followupSessionKey, pendingFollowups, type PendingFollowup } from "./pendingFollowup";

type InboxEnqueueBindings = Pick<AppBindings, "EnqueueInboxFollowup" | "EnqueueInboxFollowupWithInvocations" | "EnqueueInboxSteer" | "EnqueueInboxSteerForTurn" | "EnqueueForAttachmentTarget">;

// Keep request construction with the lazy submission owner while callers
// capture the session target before crossing the module-loading boundary.
export function enqueueGuidanceForTarget(binding: AppBindings, target: PendingFollowup["target"], tabId: string, text: string, turnId?: string) {
  return enqueueTrackedGuidance(binding, {
    tabId, target, key: `guidance-${crypto.randomUUID()}`, display: text, submit: text, draft: text,
  }, turnId);
}

// Non-Composer callers share its unresolved-request owner so a lost receipt
// cannot cause a second POST when the user retries guidance.
export async function enqueueTrackedGuidance(binding: AppBindings, request: PendingFollowup, turnId?: string) {
  const { target } = request;
  const pendingKey = followupSessionKey(target?.sessionPath, target?.hostId, target?.workspace);
  const unresolved = pendingKey ? pendingFollowups.get(pendingKey) : undefined;
  if (unresolved) {
    const receipt = await confirmFollowup(binding, unresolved);
    pendingFollowups.clear(pendingKey, unresolved);
    if (unresolved.submit === request.submit) return receipt;
    // A different instruction still needs its own receipt; acknowledging the
    // previous request must never clear a newer draft as if it was delivered.
  }
  if (pendingKey) pendingFollowups.set(pendingKey, request);
  try {
    const receipt = await enqueueComposerGuidance(binding, request, false, turnId);
    if (receipt?.error) throw new Error(receipt.error);
    if (!receipt?.itemId) throw new Error("Follow-up receipt unconfirmed");
    if (pendingKey) pendingFollowups.clear(pendingKey, request);
    return receipt;
  } catch (error) {
    if (pendingKey && followupNotSubmitted(error)) pendingFollowups.clear(pendingKey, request);
    throw error;
  }
}

export async function enqueueComposerGuidance(binding: AppBindings, request: PendingFollowup, queueOnly: boolean, turnId?: string) {
  const { target, structured, tabId, display, submit, key } = request;
  // Structured invocations and image submissions require their own turn.
  if (queueOnly || structured) {
    if (target && !structured?.attachments?.length) {
      if (!binding.EnqueueInboxFollowupForTarget) throw new Error("reasonix_error:inbox_not_submitted — target submission unavailable");
      return binding.EnqueueInboxFollowupForTarget(target, display, submit, structured?.invocations ?? [], key);
    }
    return enqueueInboxGuidance(binding, tabId, display, submit, structured, { idempotency: key });
  }
  if (target && !structured) {
    // Discovery is read-only. If no turn can be established, preserve the input
    // as a follow-up using the original session target and idempotency key.
    const followup = () => {
      if (!binding.EnqueueInboxFollowupForTarget) throw new Error("reasonix_error:inbox_not_submitted — target submission unavailable");
      return binding.EnqueueInboxFollowupForTarget(target, display, submit, [], key);
    };
    const activeTurnId = await resolveActiveTurnId(binding, tabId, turnId).catch(() => undefined);
    if (!activeTurnId || !binding.InboxQueueForTarget) return followup();
    const result = await binding.InboxQueueForTarget(target, { kind: "enqueue_steer", text: submit, display, turnId: activeTurnId, idempotencyKey: key });
    // Only explicit unsupported guarantees no mutation. A transport failure or
    // absent receipt is uncertain and must use receipt recovery, never resend.
    if (result.outcome === "unavailable" && result.reason === "unsupported") return followup();
    if (result.outcome === "unavailable" && result.reason === "session_changed") {
      // Remote selection can change after the POST committed. Retain the
      // pending request for receipt recovery instead of declaring it unsent.
      throw new Error("Follow-up receipt unconfirmed — session_changed");
    }
    if (result.receipt?.error) throw new Error(result.receipt.error);
    if (!result.receipt?.itemId) throw new Error("Follow-up receipt unconfirmed");
    return result.receipt;
  }
  return enqueueInboxGuidanceForActiveTurn(binding, tabId, display, submit, structured, turnId, key);
}

export async function enqueueInboxGuidanceForActiveTurn(
  binding: InboxEnqueueBindings & Pick<AppBindings, "ListTabs">,
  tabId: string,
  display: string,
  submit: string,
  structured?: StructuredInvocationSubmit,
  knownTurnId?: string,
  idempotency?: string,
) {
  const turnId = !structured && typeof binding.EnqueueInboxSteerForTurn === "function"
    ? await resolveActiveTurnId(binding, tabId, knownTurnId)
    : knownTurnId;
  return enqueueInboxGuidance(binding, tabId, display, submit, structured, { steer: true, turnId, idempotency });
}

export function enqueueInboxGuidance(
  binding: InboxEnqueueBindings,
  tabId: string,
  display: string,
  submit: string,
  structured?: StructuredInvocationSubmit,
  opts?: { steer?: boolean; turnId?: string; idempotency?: string },
) {
	if (structured) {
		if (structured.attachments?.length) {
			if (!structured.attachmentTarget || !binding.EnqueueForAttachmentTarget) {
				return Promise.reject(new Error("unsupported: attachments-v2"));
			}
			return binding.EnqueueForAttachmentTarget(
				structured.attachmentTarget,
				structured.attachmentSubmissionId || opts?.idempotency || `image-${crypto.randomUUID()}`,
				structured.input.trim(),
				structured.display.trim() || display,
				structured.invocations,
				structured.attachments,
			);
		}
    return binding.EnqueueInboxFollowupWithInvocations(
      tabId,
      structured.display.trim() || display,
      structured.input.trim(),
      structured.invocations,
      opts?.idempotency ?? "",
    );
  }
  if (opts?.steer && typeof binding.EnqueueInboxSteer === "function") {
    if (typeof binding.EnqueueInboxSteerForTurn === "function") {
      if (!opts.turnId) return Promise.reject(new Error("active turn id is unavailable; refresh and try again"));
      return binding.EnqueueInboxSteerForTurn(tabId, opts.turnId, display, submit || display, opts.idempotency ?? "");
    }
    return binding.EnqueueInboxSteer(tabId, display, submit || display, opts.idempotency ?? "");
  }
  return binding.EnqueueInboxFollowup(tabId, display, submit || display, opts?.idempotency ?? "");
}
