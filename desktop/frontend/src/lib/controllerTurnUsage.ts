import type { WireUsage, TurnUsage } from "./types";

// Failed-attempt estimates remain billable but cannot replace calibrated
// context occupancy. Context* counters describe only the latest attempt.
export function measuredContextPromptTokens(usage?: WireUsage): number | undefined {
  if (!usage || usage.estimated) return undefined;
  return (usage.contextPromptTokens ?? 0) > 0 ? usage.contextPromptTokens : usage.promptTokens ?? 0;
}

export function usageTotalTokens(usage?: WireUsage): number {
  if (!usage) return 0;
  if (usage.totalTokens > 0) return usage.totalTokens;
  const promptTokens = usage.promptTokens || usage.cacheHitTokens + usage.cacheMissTokens;
  return Math.max(0, promptTokens + usage.completionTokens);
}

export function mergeChatTurnUsage(current: TurnUsage | undefined, usage: WireUsage | undefined): TurnUsage | undefined {
  if (!usage) return current;
  const route = usage.costQuote?.modelRef?.trim();
  const routes = current?.routes ? [...current.routes] : [];
  if (route && !routes.includes(route)) routes.push(route);
  const hasCacheBuckets = usage.cacheHitTokens > 0 || usage.cacheMissTokens > 0;
  return {
    uncachedInputTokens: (current?.uncachedInputTokens ?? 0) + (hasCacheBuckets ? usage.cacheMissTokens : usage.promptTokens),
    outputTokens: (current?.outputTokens ?? 0) + usage.completionTokens,
    totalTokens: (current?.totalTokens ?? 0) + usageTotalTokens(usage),
    cacheReadTokens: (current?.cacheReadTokens ?? 0) + usage.cacheHitTokens,
    reasoningTokens: (current?.reasoningTokens ?? 0) + (usage.reasoningTokens ?? 0),
    routes: routes.length ? routes : undefined,
  };
}
