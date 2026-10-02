// The chat body's link map.
//
// One answer can name the same file three ways: a `present` declaration, a
// native write or edit the turn already recorded, or a path the host verified
// from the answer text. All three become the same clickable reference, so a
// reader cannot tell which route produced it — and the answer text itself is
// never rewritten.

import { createContext, useContext, useEffect, useMemo, useRef, useSyncExternalStore, type ReactNode } from "react";
import type { PresentedFileView } from "../lib/chatViewSource";
import type { TurnFileView } from "../lib/turnFiles";
import { ChatFileReferenceStore, type ChatFileReferenceView } from "../lib/chatFileReferences";
import { chatFileCandidates, type ChatFileCandidate } from "../lib/chatFileCandidates";
import { fileIdentity, pathBasename } from "../lib/filePaths";
import type { FileResourceRef } from "../lib/presentedFileNavigation";

export type ChatFileLink = {
  /** Path handed to a host action: the verified display path when there is one. */
  path: string;
  ref: FileResourceRef;
  /** Host-verified actions, or undefined when only the extension is known. */
  actions?: readonly string[];
};

type TurnValue = { turnKey: string; factsVersion: number; links: ReadonlyMap<string, ChatFileLink> };

const ScopeContext = createContext<ChatFileReferenceStore | null>(null);
const TurnContext = createContext<TurnValue | null>(null);

/** Owns the session's reference store; it is disposed with the session. */
export function ChatFileScopeProvider({ scopeKey, tabId, hostId, children }: { scopeKey: string; tabId?: string; hostId?: string; children: ReactNode }) {
  const store = useMemo(() => new ChatFileReferenceStore(tabId ?? "", hostId ?? "local"), [scopeKey, tabId, hostId]);
  // Attach/detach rather than dispose directly: a StrictMode mount replay runs
  // the cleanup without a re-render, and an unconditional dispose would leave
  // the session with a dead store.
  useEffect(() => { store.attach(); return () => store.detach(); }, [store]);
  return <ScopeContext.Provider value={store}>{children}</ScopeContext.Provider>;
}

export function useChatFileStore(): ChatFileReferenceStore | null {
  return useContext(ScopeContext);
}

/**
 * Reports the candidates a committed block named. Markdown surfaces outside a
 * chat turn — the dock's `.md` preview, an extension card — have no turn
 * context and report nothing.
 */
export function useChatFileReporter(): ((candidates: readonly ChatFileCandidate[]) => void) | null {
  const store = useChatFileStore();
  const turn = useContext(TurnContext);
  const latest = useRef({ store, turn });
  latest.current = { store, turn };
  return useMemo(() => {
    if (!store || !turn) return null;
    return (candidates: readonly ChatFileCandidate[]) => {
      const { store: current, turn: owner } = latest.current;
      if (!current || !owner) return;
      current.report(owner.turnKey, owner.factsVersion, candidates);
    };
  }, [store, turn]);
}

/** Candidates are reported once per parse revision, not once per render. */
export function useChatFileCandidateReport(blocks: readonly { children: readonly unknown[] }[] | undefined, revision: number): void {
  const report = useChatFileReporter();
  const previous = useRef<{ revision: number; blocks: unknown; report: unknown }>(undefined);
  useEffect(() => {
    if (!report || !blocks) return;
    // The reporter identity changes when the turn's file facts change, so a
    // turn whose text has settled still re-reports once its facts arrive.
    if (previous.current?.revision === revision && previous.current.blocks === blocks && previous.current.report === report) return;
    previous.current = { revision, blocks, report };
    report(chatFileCandidates(blocks as never));
  }, [blocks, report, revision]);
}

export function ChatFileTurnProvider({ turnKey, factsVersion, presentedFiles, modifiedFiles, tabId, hostId, children }: {
  turnKey: string; factsVersion: number;
  presentedFiles: readonly PresentedFileView[]; modifiedFiles: readonly TurnFileView[];
  tabId?: string; hostId?: string; children: ReactNode;
}) {
  const store = useChatFileStore();
  const references = useChatFileReferences(store, turnKey);
  const value = useMemo<TurnValue>(() => ({
    turnKey,
    factsVersion,
    links: buildLinks({ presentedFiles, modifiedFiles, references, tabId: tabId ?? "", hostId: hostId ?? "local" }),
  }), [factsVersion, hostId, modifiedFiles, presentedFiles, references, tabId, turnKey]);
  return <TurnContext.Provider value={value}>{children}</TurnContext.Provider>;
}

const EMPTY_REFERENCES: ReadonlyMap<string, ChatFileReferenceView> = new Map();

function useChatFileReferences(store: ChatFileReferenceStore | null, turnKey: string): ReadonlyMap<string, ChatFileReferenceView> {
  const snapshot = useMemo(() => (() => store?.getTurnSnapshot(turnKey) ?? EMPTY_REFERENCES), [store, turnKey]);
  const subscribe = useMemo(() => (listener: () => void) => store?.subscribe(listener) ?? (() => {}), [store]);
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

function buildLinks({ presentedFiles, modifiedFiles, references, tabId, hostId }: {
  presentedFiles: readonly PresentedFileView[]; modifiedFiles: readonly TurnFileView[];
  references: ReadonlyMap<string, ChatFileReferenceView>; tabId: string; hostId: string;
}): ReadonlyMap<string, ChatFileLink> {
  // One entry per file. A presented delivery is registered first and owns the
  // entry's resource ref, because it carries the authorization the host
  // already trusts; a verified reference to the same file only adds spellings.
  type Entry = { paths: string[]; ref: FileResourceRef; actions?: readonly string[] };
  const entries: Entry[] = [];
  const byIdentity = new Map<string, Entry>();
  // `path` is the entry's primary spelling — the one actions are performed
  // with. `aliases` are extra spellings of the same file that the answer may
  // have used, and they do not create a second entry.
  const define = (path: string, ref: FileResourceRef, actions?: readonly string[], aliases: readonly string[] = []) => {
    const identity = fileIdentity(path);
    const existing = byIdentity.get(identity);
    if (existing) {
      for (const spelling of [path, ...aliases]) if (!existing.paths.includes(spelling)) existing.paths.push(spelling);
      return;
    }
    const entry: Entry = { paths: [path, ...aliases].filter((spelling, index, all) => all.indexOf(spelling) === index), ref, actions };
    byIdentity.set(identity, entry);
    entries.push(entry);
  };
  for (const file of presentedFiles) {
    define(file.path, { source: "presented", hostId, tabId, toolCallId: file.toolCallId, path: file.path });
  }
  for (const file of modifiedFiles) {
    define(file.path, { source: "workspace", hostId, tabId, toolCallId: file.toolCallId, path: file.path });
  }
  for (const reference of references.values()) {
    if (reference.status !== "resolved" || !reference.displayPath) continue;
    // The answer's own spelling is an alias: the host canonicalized the path,
    // but the reader must still be able to click the text they were shown.
    define(reference.displayPath, { source: "reference", hostId, tabId, path: reference.displayPath },
      reference.actions, [reference.path]);
  }

  const claims = new Map<string, number>();
  for (const entry of entries) claims.set(pathBasename(entry.paths[0]), (claims.get(pathBasename(entry.paths[0])) ?? 0) + 1);

  const links = new Map<string, ChatFileLink>();
  for (const entry of entries) {
    const link: ChatFileLink = { path: entry.paths[0], ref: entry.ref, actions: entry.actions };
    for (const path of entry.paths) if (!links.has(path)) links.set(path, link);
    // A basename alias is offered only when exactly one file claims the name:
    // an ambiguous short name stays ordinary text while full paths keep working.
    const name = pathBasename(entry.paths[0]);
    if (name && name !== entry.paths[0] && claims.get(name) === 1 && !links.has(name)) links.set(name, link);
  }
  return links;
}

export function useChatFileLink(text: string): ChatFileLink | undefined {
  const turn = useContext(TurnContext);
  return turn?.links.get(text.trim());
}
