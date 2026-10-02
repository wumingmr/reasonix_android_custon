import type { AppBindings } from "./bridge";
import type { SessionRef } from "./sessionRef";
import type { HistoryPage } from "./types";

export interface CanonicalProjectNodeFields {
  session?: SessionRef;
  canArchive?: boolean;
}

export interface SessionLifecycleBindings {
  ListHistoricalSessions?(): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
  GetHistoricalImportStatus?(): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
  ImportHistoricalSession?(id: string): Promise<import("../generated/desktopContract.generated").SessionRestoreResult>;
  PrepareSession?(selector: import("../generated/desktopContract.generated").SessionSelector): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  GetSessionPreparation?(operationId: string): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  CancelSessionPreparation?(operationId: string): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  CheckHistoricalSourceUpdate?(selector: import("../generated/desktopContract.generated").SessionSelector): Promise<import("../generated/desktopContract.generated").HistoricalSourceUpdateView>;
  PrepareHistoricalSourceVersion?(source: import("../generated/desktopContract.generated").SessionSourceRef, version: string): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  StartHistoricalImport?(ids: string[]): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
  ControlHistoricalImport?(action: string): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
	ApplySessionLifecycle(request: import("../generated/desktopContract.generated").SessionLifecycleRequest): Promise<import("../generated/desktopContract.generated").SessionLifecycleResult>;
	ListTrashEntries(query: string, cursor: string, limit: number): Promise<import("../generated/desktopContract.generated").TrashEntryPage>;
  ListRecoveryEntries(query: string, cursor: string, limit: number): Promise<import("../generated/desktopContract.generated").RecoveryEntryPage>;
  PreviewRecoveryEntry(id: string): Promise<HistoryPage>;
  RestoreRecoveryEntry(id: string, operationId: string): Promise<import("../generated/desktopContract.generated").SessionRestoreResult>;
  GetSessionUpgradeStatus(): Promise<import("../generated/desktopContract.generated").SessionUpgradeStatus>;
  PurgeCanonicalSession(ref: SessionRef): Promise<void>;
}

export type MockSessionLifecycleBindings = SessionLifecycleBindings & Pick<AppBindings, "InspectTopicRemoval" | "RemoveTopic">;

export function makeMockSessionLifecycleBindings(...args: Parameters<typeof import("./sessionLifecycleMock").makeMockSessionLifecycleBindings>): MockSessionLifecycleBindings {
  const bindings = import("./sessionLifecycleMock").then(module => module.makeMockSessionLifecycleBindings(...args));
  return Object.fromEntries(["PurgeCanonicalSession","ListTrashEntries","ApplySessionLifecycle","ListRecoveryEntries","PreviewRecoveryEntry","RestoreRecoveryEntry","GetSessionUpgradeStatus","InspectTopicRemoval","RemoveTopic"].map(name => [name, async function(this: AppBindings, ...values: unknown[]) {
    const owner = await bindings;
    return (owner[name as keyof typeof owner] as (...params: unknown[]) => unknown).apply(this, values);
  }])) as unknown as MockSessionLifecycleBindings;
}
