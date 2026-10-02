export type InteractionKind = "ask" | "approval" | "plan" | "recovery" | "mcp";

export type InteractionTarget = Readonly<{
  tabId: string;
  sessionKey: string;
  hostId?: string;
  sessionId?: string;
  sessionGeneration?: number;
  promptId: string;
  turnId?: string;
  runtimeEpoch?: string;
  kind: InteractionKind;
  instanceKey: string;
  requestGeneration?: number;
  permissionRevision?: number;
}>;

export function interactionInstanceKey(target: Omit<InteractionTarget, "instanceKey">): string {
  return JSON.stringify([
    target.sessionKey,
    target.hostId ?? "",
    target.sessionId ?? "",
    target.sessionGeneration ?? 0,
    target.runtimeEpoch ?? "",
    target.turnId ?? "",
    target.kind,
    target.promptId,
    target.requestGeneration ?? 0,
    target.permissionRevision ?? 0,
  ]);
}

export function sameInteractionIdentity(
  prompt: { id: string; turnId?: string; runtimeEpoch?: string } | undefined,
  target: Pick<InteractionTarget, "promptId" | "turnId" | "runtimeEpoch">,
): boolean {
  return Boolean(prompt) && prompt?.id === target.promptId &&
    (target.turnId === undefined || prompt.turnId === target.turnId) &&
    (target.runtimeEpoch === undefined || prompt.runtimeEpoch === target.runtimeEpoch);
}
