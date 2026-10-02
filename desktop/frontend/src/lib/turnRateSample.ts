import type { State } from "./useController";

export function beginTurnModelActivity(s: State, now = Date.now()): State {
  return s.turnModelActiveAt && s.turnModelActiveAt > 0
    ? s
    : { ...s, turnModelActiveAt: now };
}

export function endTurnModelActivity(s: State, now = Date.now(), stashForUsage = false): State {
  if (!s.turnModelActiveAt || s.turnModelActiveAt <= 0) return s;
  const closedMs = Math.max(0, now - s.turnModelActiveAt);
  return { ...s, turnModelActiveAt: undefined, turnModelActiveMs: Math.max(0, s.turnModelActiveMs) + closedMs,
    pendingRequestModelMs: stashForUsage ? closedMs : s.pendingRequestModelMs };
}

/** Tool progress is cumulative, and its first restored reading is a baseline. */
export function sampleTurnArguments(s: State, argChars: number | undefined): State {
  const sample = s.turnRateSample;
  if (!sample || argChars === undefined || argChars <= 0) return s;
  return { ...s, turnRateSample: { ...sample, argChars,
    outputQuarters: sample.outputQuarters + (sample.argChars === undefined ? 0 : Math.max(0, argChars - sample.argChars)) } };
}
