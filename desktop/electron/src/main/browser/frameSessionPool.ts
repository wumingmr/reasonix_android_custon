import type { DebuggerRelease } from "./debuggerLease.js";
import type { GuestDebugger } from "./guestView.js";

const key = {};
interface ChildSession { sessionId?: string; ready: Promise<string>; users: number; retire: boolean; detached: boolean }
export interface FrameSessionLease { ready: Promise<string>; release(failed: boolean): void }

// Target attachments belong to the root connection, not to one snapshot or
// ref lookup. Detaching between adjacent operations tears down the renderer's
// domain agents while the next operation may already be initializing them.
// Document contexts and remote object groups remain operation-scoped.
export class FrameSessionPool {
  private readonly sessions = new Map<string, ChildSession>();
  private closed = false;
  private readonly message = (_event: unknown, method: string, raw: unknown) => {
    if (method !== "Target.detachedFromTarget") return;
    const { sessionId } = raw as { sessionId?: string };
    for (const [target, child] of this.sessions) if (child.sessionId === sessionId) this.sessions.delete(target);
  };
  private constructor(private readonly debuggerAPI: GuestDebugger, private readonly connection: DebuggerRelease) {
    debuggerAPI.on("message", this.message);
  }
  static for(debuggerAPI: GuestDebugger, connection: DebuggerRelease): FrameSessionPool {
    return connection.shared(key, () => new FrameSessionPool(debuggerAPI, connection));
  }
  borrow(targetId: string): FrameSessionLease {
    if (this.closed) throw new Error("browser frame session pool is closed");
    const existing = this.sessions.get(targetId);
    if (existing) return this.lease(targetId, existing);
    const child: ChildSession = { ready: Promise.resolve(""), users: 0, retire: false, detached: false };
    this.sessions.set(targetId, child);
    child.ready = (async () => {
      const attached = await this.connection.send("Target.attachToTarget", { targetId, flatten: true }) as { sessionId: string };
      child.sessionId = attached.sessionId;
      if (this.closed || this.sessions.get(targetId) !== child) {
        throw new Error("browser frame session was invalidated during attachment");
      }
      for (const method of ["Page.enable", "Runtime.enable", "DOM.enable"]) {
        await this.connection.send(method, {}, attached.sessionId);
        if (this.closed || this.sessions.get(targetId) !== child) throw new Error("browser frame session was detached during initialization");
      }
      return attached.sessionId;
    })().catch(error => {
      if (this.sessions.get(targetId) === child) this.sessions.delete(targetId);
      this.detachChild(child);
      throw error;
    });
    return this.lease(targetId, child);
  }
  private lease(target: string, child: ChildSession): FrameSessionLease {
    child.users++;
    let released = false;
    return { ready: child.ready, release: failed => {
      if (released) return;
      released = true;
      child.users--; child.retire ||= failed;
      // Cancellation never detaches a session borrowed by another operation.
      // An abandoned attachment is removed now and its late result cleans itself.
      if (!child.users && child.retire) {
        if (this.sessions.get(target) === child) this.sessions.delete(target);
        this.detachChild(child);
      }
    } };
  }
  private detachChild(child: ChildSession): void {
    if (!child.sessionId || child.detached) return;
    child.detached = true;
    this.detach(child.sessionId);
  }
  private detach(sessionId: string): void {
    // Cleanup owns only this opaque session ID. A late attach response cannot
    // dispatch through, or clear resources belonging to, a replacement pool.
    try { if (this.debuggerAPI.isAttached()) void this.debuggerAPI.sendCommand("Target.detachFromTarget", { sessionId }).catch(() => {}); } catch { /* Native page already destroyed. */ }
  }
  dispose(transportAvailable: boolean): void {
    if (this.closed) return;
    this.closed = true;
    this.debuggerAPI.removeListener("message", this.message);
    if (transportAvailable && !this.connection.owned) for (const child of [...this.sessions.values()].reverse()) this.detachChild(child);
    this.sessions.clear();
  }
}
