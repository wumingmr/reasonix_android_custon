export type LocalSubmissionStatus = "sending" | "accepted" | "unknown" | "failed";

export interface LocalSubmission {
  submissionId: string;
  localId: string;
  text: string;
  submitText?: string;
  createdAt: number;
  sequence: number;
  anchorItemId?: string;
  placement?: "start" | "after" | "latest";
  status: LocalSubmissionStatus;
  messageId?: string;
  turnId?: string;
  checkpointTurn?: number;
  /** The first terminal event consumed this submission's checkpoint authority. */
  settled?: boolean;
}

export interface LocalSubmissionFields {
  localSubmissions: Record<string, LocalSubmission>;
  localSubmissionOrder: string[];
  localSubmissionSendRevision: number;
  visibleSubmissionHandoffs: Record<string, { submissionId: string }>;
}

export type CanonicalUserConfirmation = { messageId: string; submissionId?: string; turnId?: string };
type CanonicalUserIdentity = {
  kind: string;
  messageId?: string;
  submissionId?: string;
  turnId?: string;
};

export function canonicalUserConfirmations(items: readonly CanonicalUserIdentity[]): CanonicalUserConfirmation[] {
  return items.flatMap(item => item.kind === "user" && item.messageId ? [{ messageId: item.messageId, submissionId: item.submissionId, turnId: item.turnId }] : []);
}

/** Identity matching is shared by state settlement and defensive presentation. */
export function matchLocalSubmissions(submissions: readonly LocalSubmission[], confirmations: readonly CanonicalUserConfirmation[]) {
  const consumed = new Set<string>();
  const matches: Array<{ submissionId: string; messageId: string }> = [];
  for (const confirmation of confirmations) {
    const available = (local: LocalSubmission) => !consumed.has(local.submissionId);
    const local = submissions.find(local => available(local) && local.messageId === confirmation.messageId)
      ?? submissions.find(local => available(local) && !local.messageId && Boolean(confirmation.submissionId) && local.submissionId === confirmation.submissionId);
    if (!local) continue;
    consumed.add(local.submissionId);
    matches.push({ submissionId: local.submissionId, messageId: confirmation.messageId });
  }
  return matches;
}

export function pruneSubmissionHandoffs<T extends LocalSubmissionFields>(state: T, items: readonly CanonicalUserIdentity[]): T {
  const visible = new Set(canonicalUserConfirmations(items).map(item => item.messageId));
  const entries = Object.entries(state.visibleSubmissionHandoffs).filter(([id]) => visible.has(id));
  return entries.length === Object.keys(state.visibleSubmissionHandoffs).length ? state
    : { ...state, visibleSubmissionHandoffs: Object.fromEntries(entries) };
}

export function isUnknownSubmissionError(error: unknown): boolean {
  const outcome=(error as {data?:{submissionOutcome?:string}} | undefined)?.data?.submissionOutcome;
  if(outcome) return outcome === "unknown";
  return /timeout|timed out|network|connection|socket|channel.*closed|fetch failed|failed to fetch|\beof\b/i.test(error instanceof Error ? error.message : String(error));
}

export function orderedLocalSubmissions(state: LocalSubmissionFields): LocalSubmission[] {
  return state.localSubmissionOrder.flatMap((submissionId) => {
    const submission = state.localSubmissions[submissionId];
    return submission ? [submission] : [];
  });
}

export function beginLocalSubmission<T extends LocalSubmissionFields>(
  state: T,
  submission: Omit<LocalSubmission, "status">,
): T {
  return {
    ...state,
    localSubmissions: { ...state.localSubmissions, [submission.submissionId]: { ...submission, status: "sending" } },
    localSubmissionOrder: state.localSubmissionOrder.includes(submission.submissionId)
      ? state.localSubmissionOrder
      : [...state.localSubmissionOrder, submission.submissionId],
    localSubmissionSendRevision: state.localSubmissionSendRevision + 1,
  };
}

export function updateLocalSubmission<T extends LocalSubmissionFields>(
  state: T,
  submissionId: string | undefined,
  patch: Partial<LocalSubmission>,
): T {
  if (!submissionId) return state;
  const current = state.localSubmissions[submissionId];
  if (!current) return state;
  if (current.messageId && patch.messageId && current.messageId !== patch.messageId) return state;
  return {
    ...state,
    localSubmissions: { ...state.localSubmissions, [submissionId]: { ...current, ...patch, submissionId } },
  };
}

export function removeLocalSubmission<T extends LocalSubmissionFields>(state: T, submissionId: string | undefined): T {
  if (!submissionId || !state.localSubmissions[submissionId]) return state;
  const localSubmissions = { ...state.localSubmissions };
  delete localSubmissions[submissionId];
  return {
    ...state,
    localSubmissions,
    localSubmissionOrder: state.localSubmissionOrder.filter((candidate) => candidate !== submissionId),
  };
}

/** Retire each local echo once its durable user message is installed. */
export function settleLocalSubmissions<T extends LocalSubmissionFields>(
  state: T,
  items: readonly CanonicalUserIdentity[],
  confirmations: readonly CanonicalUserConfirmation[] = canonicalUserConfirmations(items),
): T {
  state = pruneSubmissionHandoffs(state, items);
  if (state.localSubmissionOrder.length === 0) return state;
  const remaining = { ...state.localSubmissions };
  const matches = matchLocalSubmissions(orderedLocalSubmissions(state), confirmations);
  const consumed = new Set(matches.map(match => match.submissionId));
  const visible = new Set(canonicalUserConfirmations(items).map(item => item.messageId));
  const handoffs = { ...state.visibleSubmissionHandoffs };
  for (const match of matches) {
    delete remaining[match.submissionId];
    if (visible.has(match.messageId) && !handoffs[match.messageId]) handoffs[match.messageId] = { submissionId: match.submissionId };
  }
  if (consumed.size === 0) return state;
  return {
    ...state,
    localSubmissions: remaining,
    visibleSubmissionHandoffs: handoffs,
    localSubmissionOrder: state.localSubmissionOrder.filter((submissionId) => !consumed.has(submissionId)),
  };
}

export type RebasedUserRecord = { submissionId?: string; text: string; submitText?: string };
type EchoLike = { kind: string; id: string; text?: string; submitText?: string };

function submittedText(entry: { text: string; submitText?: string }): string {
  return entry.submitText ?? entry.text;
}

/** User echoes that were already on screen when this submission was created. */
function preexistingEchoes(previousItems: readonly EchoLike[], local: LocalSubmission): EchoLike[] {
  if (!local.anchorItemId) return local.placement === "start" ? [] : [...previousItems];
  const anchor = previousItems.findIndex(item => item.id === local.anchorItemId);
  return anchor < 0 ? [...previousItems] : previousItems.slice(0, anchor + 1);
}

/**
 * A snapshot rebase (runtime rebuild, serve restart, model switch) may carry
 * user records without the message ids settleLocalSubmissions keys on, so an
 * echo the server already journaled would otherwise survive as a duplicate
 * turn stuck at "processing". Retire only echoes the rebased projection
 * provably owns: one whose submission id a durable record repeats, or — with
 * no turn in flight — the oldest echo whose text the newest durable user
 * record repeats. The text match is bounded by the echoes' own anchor: a
 * record that already existed when the echo was created can never absorb it,
 * so a lost re-send of an identical message stays visible. An active runtime
 * never absorbs by text because the trailing record may be an older sibling
 * of the in-flight submission.
 */
export function settleRebasedSubmissions<T extends LocalSubmissionFields>(
  state: T,
  previousItems: readonly EchoLike[],
  durable: readonly RebasedUserRecord[],
  idle: boolean,
): T {
  const pending = orderedLocalSubmissions(state).filter(local => local.status !== "failed");
  if (pending.length === 0) return state;
  const consumed = new Set<string>();
  const durableSubmissionIds = new Set(durable.map(record => record.submissionId).filter(Boolean));
  for (const local of pending) if (durableSubmissionIds.has(local.submissionId)) consumed.add(local.submissionId);
  const latest = durable[durable.length - 1];
  if (idle && latest) {
    const text = submittedText(latest);
    const durableCopies = durable.filter(record => submittedText(record) === text).length;
    const echo = pending.find(local => !consumed.has(local.submissionId) && submittedText(local) === text);
    if (echo) {
      const priorCopies = preexistingEchoes(previousItems, echo)
        .filter(item => item.kind === "user" && item.text !== undefined && submittedText({ text: item.text, submitText: item.submitText }) === text).length;
      if (durableCopies > priorCopies) consumed.add(echo.submissionId);
    }
  }
  if (consumed.size === 0) return state;
  const localSubmissions = { ...state.localSubmissions };
  for (const submissionId of consumed) delete localSubmissions[submissionId];
  return {
    ...state,
    localSubmissions,
    localSubmissionOrder: state.localSubmissionOrder.filter(submissionId => !consumed.has(submissionId)),
  };
}

export function checkpointLocalSubmission<T extends LocalSubmissionFields>(
  state: T,
  submissionId: string | undefined,
  checkpointTurn: number | undefined,
): T {
  if (!submissionId) return state;
  const current = state.localSubmissions[submissionId];
  if (!current || current.settled) return state;
  const validTurn = Number.isInteger(checkpointTurn) && checkpointTurn! >= 0;
  return updateLocalSubmission(state, submissionId, {
    checkpointTurn: validTurn ? checkpointTurn : current.checkpointTurn,
    settled: true,
  });
}
