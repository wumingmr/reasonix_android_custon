import type { Meta, TabMeta } from "./types";
import { sameSessionIdentity, sessionIdentityStableKey, type SessionIdentity } from "./sessionIdentity";
import type { HydrateSurfacePolicy } from "./hydrateHistoryApply";

/** A passive runtime refresh may retain an already certified reading window.
 * Missing content metadata is not an instruction to replace it with the tail.
 * Navigation, generation changes, explicit retry and new content proof still
 * require the reader to establish a fresh window. */
export function coldHistoryRefreshProof(target: TabMeta, state: {
  meta?: Meta; hydrating?: boolean; hydrateError?: string;
  historyRevision?: number; historyDigest?: string;
} | undefined, preserve: boolean): { revision?: number; digest: string } | undefined {
  if (!preserve || !state || state.hydrating || state.hydrateError || !state.historyDigest
    || sessionIdentityStableKey(target) !== sessionIdentityStableKey(state.meta)) return undefined;
  if (target.sessionDigest && target.sessionDigest !== state.historyDigest) return undefined;
  if (target.sessionRevision !== undefined && target.sessionRevision > 0 && target.sessionRevision !== state.historyRevision) return undefined;
  return { revision: state.historyRevision, digest: state.historyDigest };
}

type ActiveTabHydrationTarget = SessionIdentity & {
  sessionRevision?: number;
  sessionDigest?: string;
};

export type ActiveTabHydrationLoadOptions = ActiveTabHydrationTarget & {
  preserveCachedHistory: boolean;
  surfacePolicy?: HydrateSurfacePolicy;
};

export function activeTabHydrationPlan(
  target: ActiveTabHydrationTarget,
  current: SessionIdentity | undefined,
  reset: boolean,
  requestedPolicy?: HydrateSurfacePolicy,
  requestedCache?: boolean,
): {
  sameSession: boolean;
  surfacePolicy: HydrateSurfacePolicy;
  loadOptions: ActiveTabHydrationLoadOptions;
} {
  const sameSession = sameSessionIdentity(target, current);
  const surfacePolicy = requestedPolicy ?? (sameSession ? "preserve-current" : "replace-surface");
  if (surfacePolicy === "replace-surface") {
    return {
      sameSession,
      surfacePolicy,
      loadOptions: {
        preserveCachedHistory: false,
        surfacePolicy,
        session: target.session,
        sessionPath: target.sessionPath,
        sessionRevision: target.sessionRevision,
        sessionDigest: target.sessionDigest,
        sessionGeneration: target.sessionGeneration,
      },
    };
  }
  return {
    sameSession,
    surfacePolicy,
    loadOptions: {
      preserveCachedHistory: sameSession && (requestedCache ?? !reset),
      session: target.session,
      sessionPath: target.sessionPath,
      sessionRevision: target.sessionRevision,
      sessionDigest: target.sessionDigest,
    },
  };
}

export function continueColdHistory(
  pending: Promise<"cached" | "loaded" | "miss" | "failed">,
  current: () => boolean,
  fallback: () => void | Promise<void>,
  follow: () => Promise<unknown>,
  ready: () => void | Promise<void>,
): void {
  void pending.then(result => {
    if (!current()) return;
    if (result === "miss" || result === "failed") return fallback();
    void follow().then(() => current() ? ready() : undefined).catch(() => {});
  }).catch(() => {});
}
