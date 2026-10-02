import type { GeneratedDesktopCommands } from "../generated/desktopContract.generated";

export type SessionUIBindings = Pick<GeneratedDesktopCommands,
  "BeginManualSessionCreation" | "GetManualSessionCreation" | "RetryManualSessionCreation" | "ListManualSessionCreations" |
  "GetSessionComposerState" | "SaveSessionComposerState" | "BeginSessionComposerSubmission" | "CompleteSessionComposerSubmission" | "ListSessionComposerConflicts">;

export function makeLazySessionUIMock(create: (scope: string, root: string, id: string) => Promise<void>): SessionUIBindings {
  let loaded: Promise<SessionUIBindings> | undefined;
  const names: (keyof SessionUIBindings)[] = ["BeginManualSessionCreation","GetManualSessionCreation","RetryManualSessionCreation","ListManualSessionCreations","GetSessionComposerState","SaveSessionComposerState","BeginSessionComposerSubmission","CompleteSessionComposerSubmission","ListSessionComposerConflicts"];
  return Object.fromEntries(names.map(name=>[name,async (...args:unknown[])=> {
    loaded ??= import("./sessionUIMock").then(module=>module.makeSessionUIMock(create));
    const binding=await loaded;
    return (binding[name] as (...args:unknown[])=>unknown).apply(binding,args);
  }])) as unknown as SessionUIBindings;
}
