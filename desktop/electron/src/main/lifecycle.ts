import { randomUUID } from "node:crypto";
import { errorText, type Logger } from "./log.js";
import type { ShutdownPhase } from "./service.js";

export type QuitPhase = "idle" | "preparing" | "saving" | "closing" | "failed" | "completed";

export interface LifecycleService {
  beforeClose(reason: string): Promise<boolean>;
  shutdown(
    reason?: "user_quit" | "update_restart" | "system_signal",
    onProgress?: (phase: ShutdownPhase) => void,
  ): Promise<void>;
  shutdownRequestIdentity?(): string;
}

export interface LifecycleApp {
  quit(): void;
  exit?(code: number): void;
  relaunch(args: string[], execPath?: string): void;
}

export interface QuitSequencerDeps {
  service: LifecycleService;
  app: LifecycleApp;
  flushRenderer?: () => Promise<void>;
  resumeRenderer?: () => Promise<void>;
  onWindowClosePrevented?: () => void;
  onPrepareFailed?: (message: string) => Promise<void> | void;
  confirmRecoveryDraftLoss?: (message: string) => Promise<boolean>;
  recoveryDraftTimeoutMs?: number;
  onShutdownFailed?: (message: string) => Promise<boolean>;
  onCloseAllowed(): void;
  cleanup?: Array<{ name: string; run(): void }>;
  schedule?: (run: () => void, milliseconds: number) => void;
  now?: () => number;
  log: Logger;
}

// Electron's before-quit fires on every app.quit(); this drives it through
// beforeClose (Go may veto) and shutdown exactly once, then lets it through.
export class QuitSequencer {
  private phase: QuitPhase = "idle";
  private approved = false;
  private relaunchArgs: string[] | null = null;
  private relaunchExecPath: string | undefined;
  private attempt = "";
  private reason: "user_quit" | "update_restart" | "system_signal" = "user_quit";
  private reasonClaimed = false;
  private preparing: Promise<void> | null = null;
  private finishing: Promise<void> | null = null;
  private quitRequested = false;
  private rendererFlushed = false;
  private recoveryRestart = false;
  private attemptStartedAt = 0;
  private draftSaveMs = 0;

  constructor(private readonly deps: QuitSequencerDeps) {}

  get currentPhase(): QuitPhase {
    return this.phase;
  }

  get isQuitting(): boolean {
    return (
      this.approved ||
      this.phase === "saving" ||
      this.phase === "closing" ||
      this.phase === "failed" ||
      this.phase === "completed"
    );
  }

  onBeforeQuit(): boolean {
    if (this.phase === "completed") return true;
    if (this.phase === "failed") {
      if (!this.finishing) this.phase = "saving";
      void this.startFinishing();
      return false;
    }
    if (this.phase === "preparing") {
      this.quitRequested = true;
      this.claimReason("user_quit");
      return false;
    }
    if (this.phase !== "idle") return false;
    this.claimReason("user_quit");
    this.quitRequested = true;
    this.beginAttempt();
    this.deps.log.info(`exit ${this.attempt}: ${this.approved ? "saving" : "preparing"}`);
    if (!this.approved) {
      this.phase = "preparing";
      this.startPreparing(() => this.ask());
      return false;
    }
    this.phase = "saving";
    void this.startFinishing();
    return false;
  }

  requestQuit(reason: "user_quit" | "system_signal" = "user_quit"): void {
    this.claimReason(reason);
    this.quitRequested = true;
    this.deps.app.quit();
  }

  requestWindowClose(): Promise<void> {
    if (this.phase === "completed") return Promise.resolve();
    if (this.phase === "failed") {
      if (!this.finishing) this.phase = "saving";
      return this.startFinishing();
    }
    if (this.isQuitting) return this.preparing ?? Promise.resolve();
    if (this.phase === "preparing") return this.preparing ?? Promise.resolve();
    if (this.phase !== "idle") return Promise.resolve();
    this.beginAttempt();
    this.phase = "preparing";
    this.deps.log.info(`exit ${this.attempt}: preparing window close`);
    return this.startPreparing(() => this.prepareWindowClose());
  }

  approve(): void {
    this.approved = true;
    this.quitRequested = true;
    this.deps.app.quit();
  }

  relaunch(args: string[], execPath?: string): void {
    this.relaunchArgs = args;
    this.relaunchExecPath = execPath;
    this.claimReason("update_restart");
    this.approve();
  }

  recoverRenderer(args: string[]): void {
    if (this.phase !== "idle") return;
    this.recoveryRestart = true;
    this.relaunch(args);
  }

  private async ask(): Promise<void> {
    let prevent = false;
    if (!(await this.flushRenderer("quit cancelled"))) return;
    try {
      prevent = await this.deps.service.beforeClose("quit");
    } catch (error) {
      this.deps.log.warn(`beforeClose(quit) failed, quitting anyway: ${errorText(error)}`);
    }
    if (prevent && !this.approved) {
      this.deps.log.info(`exit ${this.attempt}: cancelled`);
      this.quitRequested = false;
      await this.resumeRenderer();
      this.rendererFlushed = false;
      if (!this.quitRequested && !this.approved) this.resetTrigger();
      return;
    }
    this.approved = true;
  }

  private async prepareWindowClose(): Promise<void> {
    if (!(await this.flushRenderer("window close cancelled"))) return;
    let prevent = false;
    try {
      prevent = await this.deps.service.beforeClose("window");
    } catch (error) {
      this.deps.log.warn(`beforeClose(window) failed, quitting anyway: ${errorText(error)}`);
    }
    if (this.quitRequested || this.approved || !prevent) {
      this.approved = true;
      this.quitRequested = true;
      this.claimReason("user_quit");
      return;
    }
    await this.resumeRenderer();
    this.rendererFlushed = false;
    // Resuming editing crosses the renderer boundary. A quit received during
    // that await owns the next transition and must flush any resumed edits.
    if (this.quitRequested || this.approved) {
      this.approved = true;
      return;
    }
    this.deps.onWindowClosePrevented?.();
    this.deps.log.info(`exit ${this.attempt}: window hidden`);
    this.resetTrigger();
  }

  private async finish(): Promise<void> {
    if (!(await this.flushRenderer("shutdown cancelled"))) return;
    const serviceStartedAt = this.now();
    try {
      await this.deps.service.shutdown(this.reason, (phase) => {
        if (phase === "preparing" || phase === "saving" || phase === "closing") this.phase = phase;
      });
    } catch (error) {
      const message = errorText(error);
      this.deps.log.warn(
        `exit ${this.attempt}: shutdown failed request=${this.shutdownRequestIdentity()} reason=${this.reason} draft_ms=${this.draftSaveMs} service_ms=${this.now() - serviceStartedAt} total_ms=${this.totalMs()}: ${message}`,
      );
      this.phase = "failed";
      this.approved = true;
      let retry = false;
      try {
        retry = await this.deps.onShutdownFailed?.(message) === true;
      } catch (promptError) {
        this.deps.log.warn(`exit ${this.attempt}: shutdown failure prompt failed: ${errorText(promptError)}`);
      }
      if (retry) {
        this.phase = "saving";
        await this.finish();
      }
      return;
    }
    this.deps.log.info(
      `exit ${this.attempt}: shutdown complete request=${this.shutdownRequestIdentity()} reason=${this.reason} draft_ms=${this.draftSaveMs} service_ms=${this.now() - serviceStartedAt} total_ms=${this.totalMs()}`,
    );
    this.phase = "closing";
    for (const step of [{ name: "close permission", run: () => this.deps.onCloseAllowed() }, ...(this.deps.cleanup ?? [])]) {
      try {
        step.run();
        this.deps.log.info(`exit ${this.attempt}: cleanup ${step.name} complete`);
      } catch (error) {
        this.deps.log.warn(`exit ${this.attempt}: cleanup ${step.name} failed: ${errorText(error)}`);
      }
    }
    this.phase = "completed";
    this.deps.log.info(`exit ${this.attempt}: resources cleaned; requesting final shell exit`);
    if (this.deps.app.exit) {
      const schedule = this.deps.schedule ?? ((run, ms) => {
        setTimeout(run, ms).unref();
      });
      schedule(() => {
        this.deps.log.error("shell exit deadline exceeded after service shutdown");
        this.deps.app.exit?.(1);
      }, 5000);
    }
    try {
      if (this.relaunchArgs) this.deps.app.relaunch(this.relaunchArgs, this.relaunchExecPath);
    } catch (error) {
      this.deps.log.error(`relaunch failed: ${errorText(error)}`);
    } finally {
      this.deps.app.quit();
    }
  }

  private async resumeRenderer(): Promise<void> {
    try {
      await this.deps.resumeRenderer?.();
    } catch (error) {
      this.deps.log.warn(`exit ${this.attempt}: could not resume draft editing: ${errorText(error)}`);
    }
  }

  private startPreparing(run: () => Promise<void>): Promise<void> {
    if (this.preparing) return this.preparing;
    this.preparing = run().finally(() => {
      this.preparing = null;
      this.settlePreparation();
    });
    return this.preparing;
  }

  private startFinishing(): Promise<void> {
    if (this.finishing) return this.finishing;
    this.finishing = this.finish().finally(() => {
      this.finishing = null;
      this.settlePreparation();
    });
    return this.finishing;
  }

  private settlePreparation(): void {
    if (this.phase !== "preparing") return;
    // Publish idle only after the previous promise releases ownership. A new
    // transaction must never attach to a cancelled preparation's promise.
    this.phase = "idle";
    if (this.approved || this.quitRequested) this.deps.app.quit();
  }

  private async flushRenderer(cancelled: string): Promise<boolean> {
    if (this.rendererFlushed) return true;
    const startedAt = this.now();
    let timeout: ReturnType<typeof setTimeout> | undefined;
    try {
      const flush = this.deps.flushRenderer?.();
      if (this.recoveryRestart) {
        await Promise.race([flush, new Promise<never>((_, reject) => {
          timeout = setTimeout(() => reject(new Error("Draft save timed out during recovery")), this.deps.recoveryDraftTimeoutMs ?? 5000);
        })]);
      } else await flush;
      this.draftSaveMs = this.now() - startedAt;
      this.rendererFlushed = true;
      this.deps.log.info(`exit ${this.attempt}: draft saved draft_ms=${this.draftSaveMs}`);
      return true;
    } catch (error) {
      const message = errorText(error);
      const recoveryCancelled = this.recoveryRestart;
      if (this.recoveryRestart) {
        let proceed = false;
        try { proceed = await this.deps.confirmRecoveryDraftLoss?.(message) === true; }
        catch (promptError) { this.deps.log.warn(`recovery draft confirmation failed: ${errorText(promptError)}`); }
        if (proceed) {
          this.rendererFlushed = true;
          this.deps.log.warn("recovery restart: user accepted unsaved draft loss");
          return true;
        }
        this.recoveryRestart = false;
        this.relaunchArgs = null;
        this.relaunchExecPath = undefined;
      }
      this.deps.log.warn(`exit ${this.attempt}: draft flush failed; ${cancelled}: ${message}`);
      this.phase = "preparing";
      this.approved = false;
      this.quitRequested = false;
      this.rendererFlushed = false;
      if (recoveryCancelled) {
        // Queue barrier release even if the page is still hung; do not make
        // cancellation wait for that same unresponsive page a second time.
        void this.resumeRenderer();
      } else {
        try {
          await this.deps.onPrepareFailed?.(message);
        } catch (promptError) {
          this.deps.log.warn(`exit ${this.attempt}: draft failure prompt failed: ${errorText(promptError)}`);
        }
      }
      if (!this.quitRequested && !this.approved) this.resetTrigger();
      return false;
    } finally {
      clearTimeout(timeout);
    }
  }

  private claimReason(reason: "user_quit" | "update_restart" | "system_signal"): void {
    if (this.reasonClaimed) return;
    this.reason = reason;
    this.reasonClaimed = true;
  }

  private resetTrigger(): void {
    this.attempt = "";
    this.reason = "user_quit";
    this.reasonClaimed = false;
    this.quitRequested = false;
    this.attemptStartedAt = 0;
    this.draftSaveMs = 0;
  }

  private beginAttempt(): void {
    if (this.attempt) return;
    this.attempt = randomUUID();
    this.attemptStartedAt = this.now();
  }

  private now(): number {
    return this.deps.now?.() ?? Date.now();
  }

  private totalMs(): number {
    return this.attemptStartedAt > 0 ? Math.max(0, this.now() - this.attemptStartedAt) : 0;
  }

  private shutdownRequestIdentity(): string {
    return this.deps.service.shutdownRequestIdentity?.() || "none";
  }
}
