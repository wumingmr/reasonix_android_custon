// Lightweight invalidation channel: Follow does not eagerly load the directory UI.
type Signal = "dirty" | "suspend";
const listeners = new Map<string, (signal: Signal) => void>();
export function observeOutline(tab: string, listener: (signal: Signal) => void): () => void {
  listeners.set(tab, listener);
  return () => { if (listeners.get(tab) === listener) listeners.delete(tab); };
}
export function signalOutline(tab: string, signal: Signal = "dirty"): void { listeners.get(tab)?.(signal); }
