import type { AppBindings } from "./bridge";

export type PreviousDraftBindings = Pick<AppBindings,"OpenSessionDraft" | "OpenSessionDraftForTarget" | "RestoreSessionDraft" | "SaveSessionDraft" | "ListSessionDraftSummaries" | "DiscardSessionDraft" | "DismissSessionDraft" | "SetSessionDraftRestoreTarget" | "GetSessionDraft" | "GetDraftContext" | "GetSessionDraftState" | "ResumeDraftSubmission" | "BeginDraftSubmission" | "GetDraftSubmission" | "CancelDraftSubmission">;

export function makeLazyPreviousDraftMock(): PreviousDraftBindings {
  let loaded: Promise<PreviousDraftBindings> | undefined;
  const names: (keyof PreviousDraftBindings)[] = ["OpenSessionDraft","OpenSessionDraftForTarget","RestoreSessionDraft","SaveSessionDraft","ListSessionDraftSummaries","DiscardSessionDraft","DismissSessionDraft","SetSessionDraftRestoreTarget","GetSessionDraft","GetDraftContext","GetSessionDraftState","ResumeDraftSubmission","BeginDraftSubmission","GetDraftSubmission","CancelDraftSubmission"];
  return Object.fromEntries(names.map(name=>[name,async (...args:unknown[])=> {
    loaded ??= import("./previousDraftMock").then(module=>module.makePreviousDraftMock());
    const binding=await loaded;
    return (binding[name] as (...args:unknown[])=>unknown).apply(binding,args);
  }])) as unknown as PreviousDraftBindings;
}
