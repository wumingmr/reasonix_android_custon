import { existsSync, readFileSync, renameSync, writeFileSync } from "node:fs";

export type GraphicsFault = { role: "GPU" | "renderer"; reason: string; exitCode: number };
const WINDOW_MS = 60_000;
const FAILURE_REASONS = new Set(["crashed", "abnormal-exit", "launch-failed", "oom", "integrity-failure", "memory-eviction"]);

/** Only explicit GPU failures survive a launch. An unclean exit is not GPU evidence. */
export class GraphicsFaultRecord {
  private writable = true;
  private revision = 0;
  private value: Record<string, unknown> = { version: 1 };
  constructor(private readonly path: string, private readonly build: string, private readonly now = Date.now) {
    try {
      if (!existsSync(path)) return;
      const value = JSON.parse(readFileSync(path, "utf8"));
      if (value?.version !== 1) { this.writable = false; return; }
      this.value = value;
    } catch { this.writable = false; }
  }
  pending(): boolean {
    const age = this.now() - Number(this.value.at);
    return this.value.pending === true && this.value.build === this.build && age >= 0 && age < 86_400_000;
  }
  record(fault: GraphicsFault): void {
    if (fault.role !== "GPU" || !FAILURE_REASONS.has(fault.reason)) return;
    this.revision++;
    this.save({ ...this.value, build: this.build, at: this.now(), pending: true, fault });
  }
  checkpoint(): number { return this.revision; }
  acknowledge(revision = this.revision): void {
    if (revision === this.revision) this.save({ ...this.value, pending: false });
  }
  private save(value: Record<string, unknown>): void {
    if (!this.writable) return;
    this.value = value;
    const temp = `${this.path}.tmp`;
    writeFileSync(temp, JSON.stringify(value) + "\n", { mode: 0o600 });
    renameSync(temp, this.path);
  }
}

export interface GraphicsRecoveryDeps {
  now?: () => number;
  stopping(): boolean;
  accelerationEnabled(): boolean;
  record(fault: GraphicsFault): void;
  acknowledge(): void;
  offer(gpu: boolean): Promise<void>;
  error(error: unknown): void;
}

/** One owner for reload budgets and prompts, independent of the renderer's health. */
export class GraphicsRecovery {
  private gpu: number[] = [];
  private renderers: number[] = [];
  private prompted = new Set<string>();
  private dialogActive = false;
  private deferredOffer: boolean | undefined;
  private unresponsiveAt: number | undefined;
  private lastFault: number | undefined;
  private readonly now: () => number;
  constructor(private readonly deps: GraphicsRecoveryDeps) { this.now = deps.now ?? Date.now; }
  get isPrompting(): boolean { return this.dialogActive; }

  fault(fault: GraphicsFault, canReload = true): boolean {
    if (this.deps.stopping()) return false;
    // A killed page still needs UI recovery, but killing a GPU process is not
    // evidence of a driver failure (task managers and normal shutdown do this).
    if (!FAILURE_REASONS.has(fault.reason) && !(fault.role === "renderer" && fault.reason === "killed")) return false;
    const now = this.now();
    this.lastFault = now;
    try { this.deps.record(fault); } catch (error) { this.deps.error(error); }
    if (fault.role === "GPU") {
      this.gpu = [...this.gpu.filter(at => now - at < WINDOW_MS), now].slice(-8);
      if (this.gpu.length >= 2) this.offer(true);
      return false;
    }
    this.renderers = [...this.renderers.filter(at => now - at < WINDOW_MS), now].slice(-8);
    // OOM and integrity errors are not repaired by repeatedly reloading the same page.
    if (!canReload || this.renderers.length >= 2 || fault.reason === "oom" || fault.reason === "integrity-failure" || fault.reason === "memory-eviction") {
      this.offer(this.gpu.some(at => now - at < WINDOW_MS));
      return false;
    }
    return true;
  }
  unresponsive(): void {
    this.unresponsiveAt ??= this.now();
    this.lastFault = this.now();
  }
  responsive(): void {
    if (this.unresponsiveAt !== undefined) this.lastFault = this.now();
    this.unresponsiveAt = undefined;
  }
  tick(): void {
    // Stalls are live state, unlike a recorded process failure. Re-check after
    // another dialog closes instead of queueing a possibly recovered stall.
    if (!this.dialogActive && this.unresponsiveAt !== undefined && this.now() - this.unresponsiveAt >= 15_000) {
      this.offer(this.gpu.some(at => this.now() - at < WINDOW_MS));
    }
  }
  healthy(): void {
    if (this.lastFault !== undefined && this.unresponsiveAt === undefined && !this.dialogActive && this.now() - this.lastFault >= WINDOW_MS) {
      this.lastFault = undefined;
      this.prompted.clear();
      this.gpu = [];
      this.renderers = [];
      try { this.deps.acknowledge(); } catch (error) { this.deps.error(error); }
    }
  }
  offer(gpu: boolean): void {
    if (this.deps.stopping()) return;
    if (this.dialogActive) {
      this.deferredOffer = this.deferredOffer === true || gpu;
      return;
    }
    const compatible = gpu && this.deps.accelerationEnabled();
    const key = compatible ? "gpu" : "renderer";
    if (this.prompted.has(key)) return;
    // Startup offers come from the previous launch and have no local fault time.
    this.lastFault ??= this.now();
    this.prompted.add(key);
    this.notice(() => this.deps.offer(compatible));
  }
  notice(show: () => Promise<void>): boolean {
    if (this.deps.stopping() || this.dialogActive) return false;
    this.dialogActive = true;
    void Promise.resolve().then(show).catch(this.deps.error).finally(() => {
      this.dialogActive = false;
      const pending = this.deferredOffer;
      this.deferredOffer = undefined;
      if (pending !== undefined) this.offer(pending);
    });
    return true;
  }
}
