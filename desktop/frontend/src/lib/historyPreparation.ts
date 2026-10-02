// Preparation is a backend lifecycle state, not a transport/storage failure.
export class HistoryPreparingError extends Error {
  constructor() { super("Session history is preparing"); }
}

export type HistoryPreparationWait = () => Promise<void>;
export const waitForHistoryPreparation: HistoryPreparationWait = () => new Promise(resolve => setTimeout(resolve, 250));

export async function loadPreparedHistory<T>(
  load: () => Promise<T>, current: () => boolean,
  wait: HistoryPreparationWait = waitForHistoryPreparation,
): Promise<T | undefined> {
  while (current()) {
    try {
      const result = await load();
      return current() ? result : undefined;
    } catch (error) {
      if (!current()) return undefined;
      if (!(error instanceof HistoryPreparingError)) throw error;
    }
    await wait();
  }
  return undefined;
}
