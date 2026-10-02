import type { AppBindings } from "./bridge";
import { asArray } from "./array";
import type { QuestionAnswer } from "./types";

type ActiveTurnBindings = Pick<AppBindings, "ListTabs" | "SteerInboxItem" | "SteerInboxItemForTurn">;

export async function resolveActiveTurnId(binding: Pick<AppBindings, "ListTabs">, tabId: string, known?: string): Promise<string | undefined> {
  // Preserve the turn the user observed. Refreshing it could steer a successor
  // turn instead; the backend owns admission and durable follow-up fallback.
  if (known?.trim()) return known.trim();
  const authoritative = asArray(await binding.ListTabs()).find((tab) => tab.id === tabId)?.turnId;
  return authoritative;
}

type AskAnswerBindings = Pick<AppBindings, "ResolvePromptForTab" | "PendingPromptIdentitiesForTab">;

export async function resolvePromptForTab(
  binding: Pick<AppBindings, "ResolvePromptForTab" | "PendingPromptIdentitiesForTab">,
  tabId: string,
  promptId: string,
  kind: string,
  answer: Record<string, unknown>,
  knownTurnId?: string,
  knownRuntimeEpoch?: string,
): Promise<void> {
  const submit = await import("./exactPromptSubmit");
  return submit.resolvePromptForTab(binding, tabId, promptId, kind, answer, knownTurnId, knownRuntimeEpoch);
}

// Final frontend boundary before optimistic transcript state is created.
export function normalizeTurnSubmit(displayText: string, submitText: string) {
  const display = displayText.trim();
  const submit = submitText.trim();
  if (!submit) throw new Error("Message cannot be empty.");
  return { display, submit };
}

export async function answerPromptForActiveTurn(
  binding: AskAnswerBindings,
  tabId: string,
  promptId: string,
  answers: QuestionAnswer[],
  knownTurnId?: string,
  knownRuntimeEpoch?: string,
): Promise<void> {
  await resolvePromptForTab(binding, tabId, promptId, "ask", { questions: answers }, knownTurnId, knownRuntimeEpoch);
}

export async function steerInboxItemForActiveTurn(binding: ActiveTurnBindings, tabId: string, itemId: string, knownTurnId?: string) {
  if (typeof binding.SteerInboxItemForTurn !== "function") return binding.SteerInboxItem(tabId, itemId);
  const turnId = await resolveActiveTurnId(binding, tabId, knownTurnId);
  if (!turnId) throw new Error("active turn id is unavailable; refresh and try again");
  return binding.SteerInboxItemForTurn(tabId, turnId, itemId);
}
