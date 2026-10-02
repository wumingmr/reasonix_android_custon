import { app } from "./bridge";
import type { SessionHistoryReadHandle } from "../generated/desktopContract.generated";

export type Binding = { promise: Promise<SessionHistoryReadHandle> };
const bindings = new Map<string, Binding>();

export function supportsHistoryRead(): boolean {
  return typeof app.BeginSessionHistoryReadForTab === "function"
    && typeof app.ReleaseSessionHistoryRead === "function"
    && typeof app.ReadSessionHistoryWindow === "function";
}

export function acquireHistoryRead(tabId: string): Binding {
  const previous = bindings.get(tabId);
  if (previous) return previous;
  const binding: Binding = { promise: Promise.resolve().then(() => app.BeginSessionHistoryReadForTab(tabId)) };
  bindings.set(tabId, binding);
  void binding.promise.catch(() => { if (bindings.get(tabId) === binding) bindings.delete(tabId); });
  return binding;
}

/** Retire the exact request scope, including a Begin reply arriving after navigation. */
export function releaseHistoryRead(tabId: string): void {
  const binding = bindings.get(tabId);
  if (!binding) return;
  bindings.delete(tabId);
  void binding.promise.then(handle => app.ReleaseSessionHistoryRead(handle.id)).catch(() => {});
}

export const currentHistoryReadBinding = (tabId: string): Binding | undefined => bindings.get(tabId);
