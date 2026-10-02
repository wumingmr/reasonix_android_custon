import type { ComponentProps } from "react";
import type { WorkspacePanel } from "../components/WorkspacePanel";

export const requestKeys = ["changeRevealRequest", "verificationRevealRequest", "fileListRequest", "changeListRequest"] as const;
export type DockRequests = Pick<ComponentProps<typeof WorkspacePanel>, typeof requestKeys[number]> & { navigationSignal?: AbortSignal };
export const emptyDockRequests: DockRequests = Object.fromEntries(requestKeys.map(key => [key, null]));
type Occurrence = { controller: AbortController; revision: number; accepted: Partial<Record<typeof requestKeys[number], number>>; snapshot: DockRequests };

/** Committed navigation belongs to an open view, independently of its React body. */
export class DockNavigation {
  private scope = "";
  private view: string | null = null;
  private source: Partial<Record<typeof requestKeys[number], string>> = {};
  private occurrences = new Map<string, Occurrence>();
  private listeners = new Set<() => void>();
  private snapshot = emptyDockRequests;
  private attachment = 0;
  // StrictMode reconnects effects synchronously without ending the runtime.
  attach(): () => void {
    const attachment = ++this.attachment;
    return () => queueMicrotask(() => { if (this.attachment === attachment) this.dispose(); });
  }
  matches(scope: string, view: string | null): boolean { return this.scope === scope && this.view === view; }
  restoredView(scope: string, view: string | null): DockRequests {
    const occurrence = this.scope === scope && view ? this.occurrences.get(view) : undefined;
    return occurrence ? { ...emptyDockRequests, navigationSignal: occurrence.controller.signal } : emptyDockRequests;
  }
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  getSnapshot = () => this.snapshot;

  reconcile(openViews: readonly string[]): void {
    for (const [id, occurrence] of this.occurrences) {
      if (openViews.includes(id)) continue;
      occurrence.controller.abort();
      this.occurrences.delete(id);
      if (this.view === id) { this.view = null; this.publish(emptyDockRequests); }
    }
  }

  commit(scope: string, view: string | null, incoming: DockRequests, openViews?: readonly string[]): void {
    if (this.scope !== scope) { this.clear(); this.scope = scope; }
    if (openViews) this.reconcile(openViews);
    const switched = this.view !== view;
    this.view = view;
    if (!view) { this.publish(emptyDockRequests); return; }
    let occurrence = this.occurrences.get(view);
    if (!occurrence) {
      const controller = new AbortController();
      occurrence = { controller, revision: 0, accepted: {}, snapshot: { ...emptyDockRequests, navigationSignal: controller.signal } };
      this.occurrences.set(view, occurrence);
    }
    const identity = (key: typeof requestKeys[number]) => {
      const value = incoming[key];
      return value ? JSON.stringify([value.navigationSource ?? key, value.id, "tabId" in value ? value.tabId : null]) : undefined;
    };
    const changed = requestKeys.filter(key => this.source[key] !== identity(key));
    if (changed.length) {
      const next = { ...(switched ? emptyDockRequests : occurrence.snapshot), navigationSignal: occurrence.controller.signal };
      const target = occurrence;
      for (const key of changed) {
        const value = incoming[key];
        const revision = ++target.revision;
        const acceptNavigation = () => {
          if (value?.navigationCancellation?.aborted || target.controller.signal.aborted || target.accepted[key] === revision || this.view !== view || this.scope !== scope
            || target.snapshot[key]?.acceptNavigation !== acceptNavigation) return false;
          target.accepted[key] = revision;
          return true;
        };
        Object.assign(next, { [key]: value ? { ...value, acceptNavigation } : null });
        this.source[key] = identity(key);
      }
      occurrence.snapshot = next;
    } else if (switched) occurrence.snapshot = { ...emptyDockRequests, navigationSignal: occurrence.controller.signal };
    this.publish(occurrence.snapshot);
  }

  private publish(snapshot: DockRequests): void {
    if (this.snapshot === snapshot) return;
    this.snapshot = snapshot;
    for (const listener of this.listeners) listener();
  }

  private clear(): void {
    for (const occurrence of this.occurrences.values()) occurrence.controller.abort();
    this.occurrences.clear();
    this.view = null;
  }

  dispose(): void { this.clear(); this.publish(emptyDockRequests); }
}
