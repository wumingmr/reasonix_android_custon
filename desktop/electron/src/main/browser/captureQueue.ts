import { browserFailure } from "./errors.js";

// One temporary native host at a time. Cancellation removes queued work without
// ever borrowing the surface owned by the preceding request.
export class CaptureQueue {
  private tail: Promise<void> = Promise.resolve();

  async run<T>(signal: AbortSignal, work: () => Promise<T>): Promise<T> {
    const previous = this.tail;
    let release!: () => void;
    const slot = new Promise<void>((resolve) => { release = resolve; });
    this.tail = previous.then(() => slot);
    try {
      await abortable(previous, signal);
      signal.throwIfAborted();
      return await work();
    } finally { release(); }
  }
}

export function abortable<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const abort = () => reject(signal.reason?.code < 0 ? signal.reason : browserFailure("cancelled", "browser request cancelled or expired"));
    // The operation may already be running, even when its waiter is cancelled.
    // Always consume its eventual rejection before taking the early-abort path.
    promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
    if (signal.aborted) { abort(); return; }
    signal.addEventListener("abort", abort, { once: true });
  });
}
