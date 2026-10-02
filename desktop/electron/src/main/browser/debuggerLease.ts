import type { GuestDebugger } from "./guestView.js";
import { traceBrowserCommand } from "./diagnosticContext.js";

export const DEBUGGER_IDLE_MS = 1500;
interface SharedResource { dispose(transportAvailable: boolean): void }
interface Lease { users: number; pending: number; owned: boolean; resources: Map<object, SharedResource>; timer?: ReturnType<typeof setTimeout> }
export type DebuggerSender = (method: string, params?: unknown, sessionId?: string) => Promise<unknown>;
export type DebuggerRelease = (() => void) & { send: DebuggerSender; owned: boolean; shared<T extends SharedResource>(key: object, create: () => T): T };
const leases = new WeakMap<GuestDebugger, Lease>();

function idle(debuggerAPI: GuestDebugger, lease: Lease): void {
  if (leases.get(debuggerAPI) !== lease || lease.users || lease.pending) return;
  clearTimeout(lease.timer);
  // Immediate root detach/re-attach between observe and input can strand
  // Chromium's OOPIF isolated-world creation. Keep adjacent calls together.
  lease.timer = setTimeout(() => {
    if (leases.get(debuggerAPI) === lease && !lease.users && !lease.pending) disposeDebugger(debuggerAPI);
  }, DEBUGGER_IDLE_MS);
  lease.timer.unref?.();
}
export function acquireDebugger(debuggerAPI: GuestDebugger): DebuggerRelease {
  let lease = leases.get(debuggerAPI);
  if (lease && !debuggerAPI.isAttached()) { disposeDebugger(debuggerAPI); lease = undefined; }
  if (!lease) {
    const owned = !debuggerAPI.isAttached();
    if (owned) debuggerAPI.attach("1.3");
    lease = { users: 0, pending: 0, owned, resources: new Map() };
    leases.set(debuggerAPI, lease);
  }
  clearTimeout(lease.timer);
  lease.users++;
  let released = false;
  const release = () => {
    if (released) return;
    released = true;
    lease.users--;
    idle(debuggerAPI, lease);
  };
  return Object.assign(release, { owned: lease.owned, send: (method: string, params?: unknown, sessionId?: string) => sendDebuggerCommand(debuggerAPI, method, params, sessionId, lease), shared: <T extends SharedResource>(key: object, create: () => T): T => {
    if (leases.get(debuggerAPI) !== lease) throw new Error("browser debugger lease is unavailable");
    let resource = lease.resources.get(key);
    if (!resource) { resource = create(); lease.resources.set(key, resource); }
    return resource as T;
  } });
}

// Count the underlying command, not its deadline-limited waiter. Timing out a
// waiter must not detach a connection with a reply still in flight.
export async function sendDebuggerCommand(debuggerAPI: GuestDebugger, method: string, params?: unknown, sessionId?: string, expected?: Lease): Promise<unknown> {
  const lease = leases.get(debuggerAPI);
  if (!lease || expected && lease !== expected || !debuggerAPI.isAttached()) throw new Error("browser debugger lease is unavailable");
  clearTimeout(lease.timer);
  lease.pending++;
  const started = performance.now();
  const target = sessionId ? "child" : "root";
  traceBrowserCommand(method, target, "cdp_started", 0);
  try {
    const result = await debuggerAPI.sendCommand(method, params, sessionId);
    traceBrowserCommand(method, target, "cdp_completed", Math.round(performance.now() - started));
    return result;
  } catch (error) {
    traceBrowserCommand(method, target, "cdp_failed", Math.round(performance.now() - started));
    throw error;
  }
  finally { lease.pending--; idle(debuggerAPI, lease); }
}

// Run before native destruction/crash recovery, while the debugger is still
// accessible. Late completions must not touch a replacement lease.
export function disposeDebugger(debuggerAPI: GuestDebugger, detach = true): void {
  const lease = leases.get(debuggerAPI);
  if (!lease) return;
  leases.delete(debuggerAPI);
  clearTimeout(lease.timer);
  for (const resource of lease.resources.values()) resource.dispose(detach);
  lease.resources.clear();
  if (detach && lease.owned && debuggerAPI.isAttached()) debuggerAPI.detach();
}
