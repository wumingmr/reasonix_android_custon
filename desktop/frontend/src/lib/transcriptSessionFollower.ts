import type { TranscriptSessionFollowerRuntime } from "./transcriptSessionFollowerRuntime";
import { desktopHost } from "./desktopHost";

/** Load the synchronization engine only when a session starts following. */
export class TranscriptSessionFollower {
  private runtime?: TranscriptSessionFollowerRuntime;
  private generation = 0;
  private releaseServiceState?: () => void;
  private readonly args: ConstructorParameters<typeof TranscriptSessionFollowerRuntime>;

  constructor(...args: ConstructorParameters<typeof TranscriptSessionFollowerRuntime>) {
    this.args = args;
  }

  get metrics() { return this.runtime?.metrics ?? { entries: 0, inlineBytes: 0 }; }

  async start(): Promise<void> {
    this.stop();
    const generation = ++this.generation;
    // Every local and remote owner observes the shell, including followers
    // created by late hydration callbacks after stopping was already published.
    const release = desktopHost().native.onServiceState(service => {
      if (generation === this.generation && (service.phase === "stopping" || service.phase === "exited")) {
        this.stop(false, "service_stopping");
      }
    });
    // The preload replays its current state synchronously during registration.
    if (generation !== this.generation) { release(); return; }
    this.releaseServiceState = release;
    const { TranscriptSessionFollowerRuntime } = await import("./transcriptSessionFollowerRuntime");
    if (generation !== this.generation) return;
    const runtime = new TranscriptSessionFollowerRuntime(...this.args);
    this.runtime = runtime;
    try { await runtime.start(); }
    catch (error) { if (generation === this.generation) throw error; }
  }

  stop(closeSubscription = true, reason?: "service_stopping"): void {
    this.generation++;
    this.releaseServiceState?.();
    this.releaseServiceState = undefined;
    this.runtime?.stop(closeSubscription, reason);
    this.runtime = undefined;
  }
}
