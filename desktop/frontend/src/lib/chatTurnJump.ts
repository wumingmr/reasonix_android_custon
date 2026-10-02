import type { ChatMountedOrder } from "./chatMountedOrder";
import type { ChatScrollController } from "./chatScrollController";
import type { NavigateToTurn, TurnNavigationTarget } from "./historyTurnNavigation";

export type TurnJumpReason = "turnUnavailable" | "snapshotExpired" | "mountTimeout";
export type TurnJumpState = { turn: string | null; status: "idle" | "loading" | "failed"; reason?: TurnJumpReason };
export type TurnJumpTarget = { turn: string; resolve: () => Promise<TurnNavigationTarget | undefined> };
export interface TurnJumpDeps {
  mounts: ChatMountedOrder; scroll: ChatScrollController;
  resolveKey: (target: TurnNavigationTarget) => string | undefined;
  navigate: NavigateToTurn; isCurrent: () => boolean;
  refresh: () => Promise<void>;
  clock?: { setTimeout: typeof setTimeout; clearTimeout: typeof clearTimeout };
}
const IDLE: TurnJumpState = Object.freeze({ turn: null, status: "idle" });

/** One intent owns metadata resolution, window replacement and DOM positioning. */
export class ChatTurnJump {
  private listeners = new Set<() => void>();
  private state = IDLE;
  private interaction = 0;
  private releaseReader?: () => void;
  private cancelMount?: () => void;
  private retryTarget?: TurnJumpTarget;
  constructor(private deps: TurnJumpDeps) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private publish(state: TurnJumpState) { this.state = state; for (const listener of [...this.listeners]) listener(); }
  cancel(): void { this.interaction++; this.releaseReader?.(); this.cancelMount?.(); this.publish(IDLE); }
  dispose(): void { this.cancel(); this.listeners.clear(); }
  retry(): Promise<void> { return this.retryTarget ? this.jump(this.retryTarget) : Promise.resolve(); }
  jumpTo(key: string): void { this.cancel(); this.deps.scroll.stopFollowing(); this.deps.scroll.jump(key); }
  async jump(target: TurnJumpTarget): Promise<void> {
    this.cancel();
    const interaction = this.interaction;
    const current = () => interaction === this.interaction && this.deps.isCurrent();
    this.retryTarget = target;
    this.deps.scroll.stopFollowing();
    this.releaseReader = this.deps.scroll.subscribeReaderIntent(() => this.cancel());
    this.publish({ turn: target.turn, status: "loading" });
    try {
      let resolved = await target.resolve();
      if (!current()) return;
      if (!resolved) throw new Error("turnUnavailable");
      // A retry keeps the selected message even if ordinals changed meanwhile.
      const original = resolved;
      this.retryTarget = { turn: target.turn, resolve: async () => original };
      let key = this.deps.resolveKey(resolved);
      if (!key) {
        let outcome = await this.deps.navigate(resolved, current);
        if (!current()) return;
        if (outcome === "stale") {
          // Preserve identity across a rewrite; never reinterpret the ordinal.
          await this.deps.refresh();
          if (!current()) return;
          resolved = { messageId: resolved.messageId };
          outcome = await this.deps.navigate(resolved, current);
        }
        if (!current()) return;
        if (outcome === "cancelled") { this.cancel(); return; }
        if (outcome !== "loaded") throw new Error(outcome === "stale" ? "snapshotExpired" : "turnUnavailable");
        key = await this.waitForMount(resolved, current);
      }
      if (!current()) return;
      if (!key) throw new Error("mountTimeout");
      this.deps.scroll.jump(key);
      this.releaseReader?.(); this.releaseReader = undefined;
      this.publish(IDLE);
    } catch (error) {
      if (!current()) return;
      this.releaseReader?.(); this.releaseReader = undefined;
      const message = error instanceof Error ? error.message : "turnUnavailable";
      this.publish({ turn: target.turn, status: "failed", reason: message === "snapshotExpired" || message === "mountTimeout" ? message : "turnUnavailable" });
    }
  }
  private waitForMount(target: TurnNavigationTarget, current: () => boolean): Promise<string | undefined> {
    const found = this.deps.resolveKey(target);
    if (found) return Promise.resolve(found);
    return new Promise(resolve => {
      const clock = this.deps.clock ?? { setTimeout, clearTimeout };
      let off = () => {};
      const finish = (key?: string) => { off(); clock.clearTimeout(timer); this.cancelMount = undefined; resolve(key); };
      const timer = clock.setTimeout(() => finish(), 5000);
      this.cancelMount = () => finish();
      off = this.deps.mounts.subscribe(() => {
        if (!current()) { finish(); return; }
        const key = this.deps.resolveKey(target); if (key) finish(key);
      });
    });
  }
}
