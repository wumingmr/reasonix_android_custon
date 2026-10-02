import { setTimeout } from "node:timers/promises";

export function parseServiceReady(log) {
  const match = /desktop service ready: generation (\S+), pid (\d+)/.exec(log);
  return match ? { generation: match[1], pid: Number(match[2]), line: match[0] } : null;
}

// Playwright's waitForFunction polls synchronous truthiness: a Promise that
// resolves false is still truthy. Await host RPC results outside that API.
export async function waitForSmokeCondition(predicate, { timeout = 30000, interval = 25 } = {}) {
  const deadline = performance.now() + timeout;
  while (true) {
    if (await predicate()) return;
    if (performance.now() >= deadline) throw new Error("Timed out waiting for packaged smoke condition");
    await setTimeout(interval);
  }
}
