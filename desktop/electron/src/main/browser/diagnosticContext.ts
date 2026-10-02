import { AsyncLocalStorage } from "node:async_hooks";

export interface BrowserRequestTrace {
  requestId: string; operationId?: string; tabId?: string; method: string;
  phase: string; elapsedMs: number; errorKind?: string; command?: string; target?: string;
}
const requests = new AsyncLocalStorage<{ emit(event: BrowserRequestTrace): void; request: Omit<BrowserRequestTrace, "phase" | "elapsedMs"> }>();
export function withBrowserDiagnosticRequest<T>(request: Omit<BrowserRequestTrace, "phase" | "elapsedMs">, emit: (event: BrowserRequestTrace) => void, work: () => T): T {
  return requests.run({ request, emit }, work);
}
export function traceBrowserCommand(command: string, target: string, phase: string, elapsedMs: number): void {
  const context = requests.getStore();
  context?.emit({ ...context.request, command, target, phase, elapsedMs });
}
