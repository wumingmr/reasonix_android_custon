import {
  sameAccessContext,
  type FileAccessContext,
  type FileResourceRef,
  type ResolvedFileResource,
} from "./fileResource";

/** Navigation parameters: how a resource is shown, never what identifies it. */
export type FileNavigationParams = Readonly<{
  action: "preview" | "source" | "reveal-tree";
  /** Dock surface the command targets: the file preview or the change list. */
  view: "files" | "changed";
}>;

export type FileNavigationAction = FileNavigationParams["action"];

/** What an open command produced, for the row or preview area that asked for it. */
export type FileNavigationOutcome =
  | Readonly<{ status: "opened"; resource: ResolvedFileResource }>
  | Readonly<{ status: "cancelled"; reason: "superseded" | "closed" | "disposed" | "unavailable" }>
  | Readonly<{ status: "failed"; error: Error }>;

/** One open preview: the confirmed resource plus the mode its tab renders in. */
export type FilePreviewEntry = Readonly<{ resource: ResolvedFileResource; source: boolean }>;

export type FileNavigationCommand = Readonly<{ ref: FileResourceRef; params: FileNavigationParams }>;

/**
 * A dock instance: the dock tab that shows a resource, and the session tab
 * whose scope authorizes reading it. The dock tab identifies the record — a
 * project keeps its previews when the session changes — while the session tab
 * only decides the credentials, so `bindScope` rebinds it.
 */
export type FileNavigationScope = Readonly<{ sessionTabId: string; dockTabId: string }>;

export const fileNavigationKey = (scope: FileNavigationScope): string => scope.dockTabId;

/** The last explicit navigation this record committed; a render never replays it. */
export type FileNavigationIntent = Readonly<{
  /** Advances per navigation command; a repeat of the same one keeps the value. */
  revision: number;
  resource: ResolvedFileResource;
  params: FileNavigationParams;
}>;

/**
 * What a dock currently shows, in two parts. `resource` names the space the
 * paths belong to (a project, a remote host): another one replaces the record.
 * `session` names the credentials the space is read with (a topic, a session
 * generation): another one keeps what is on screen but drops the access
 * contexts captured under it.
 */
export type FileNavigationScopeKey = Readonly<{ resource: string; session: string }>;

export type FileNavigationSnapshot = Readonly<{
  /** What this dock currently shows; null until a panel binds one. */
  scope: FileNavigationScopeKey | null;
  sessionTabId: string;
  dockTabId: string;
  /** Bumped whenever this dock's record is dropped and created again. */
  generation: number;
  /** Open preview entries, least recently used first. */
  entries: readonly FilePreviewEntry[];
  /** File-preview selection: the entry the preview area shows. */
  selected: FilePreviewEntry | null;
  /** Paths rendering as source, in entry order. */
  sourcePaths: readonly string[];
  /** Last explicit navigation command. */
  navigation: FileNavigationIntent | null;
  /** Display revision: bumped by every command that changed what is shown. */
  revision: number;
  /** Read-input revision: bumped only when the selected file's read inputs change. */
  contentRevision: number;
  /** Advances with every reveal-tree command so a repeat still expands the tree. */
  treeReveal: number;
  /** Aborted when this record's lifetime ends: dock closed, scope or runtime changed. */
  signal: AbortSignal;
}>;

export const FILE_PREVIEW_LIMIT = 5;

export type FileNavigationPorts = {
  /** Confirm a caller reference through the existing backend entry points. */
  resolve(ref: FileResourceRef): ResolvedFileResource | Promise<ResolvedFileResource>;
  /** Bring the dock that presents this resource forward; returns its dock tab id. */
  revealDock(ref: FileResourceRef): string;
};

export type FileNavigationRestore = Readonly<{
  paths: readonly string[];
  selectedPath: string | null;
  hostId: string;
}>;

/** A one-shot operation in a dock instance, e.g. creating a browser preview. */
export type FileNavigationOperation = Readonly<{
  signal: AbortSignal;
  /** True while no newer operation for the same dock has taken over. */
  owns(): boolean;
  finish(): void;
}>;

type Record = {
  snapshot: FileNavigationSnapshot;
  /** Lifetime of this dock instance's record; a reset aborts it. */
  lifetime: AbortController;
  /** Open command still resolving; a newer one supersedes it. */
  pending: { controller: AbortController; resource: string | null } | null;
};

const SUPERSEDED: FileNavigationOutcome = { status: "cancelled", reason: "superseded" };
/** The resource space a command moves its dock to, when the dock is host-scoped. */
const resourceOf = (ref: FileResourceRef): string | null => (ref.hostId === "local" ? null : ref.hostId);
const asError = (reason: unknown): Error => (reason instanceof Error ? reason : new Error(String(reason)));
const isPromise = <T>(value: T | Promise<T>): value is Promise<T> =>
  typeof (value as { then?: unknown } | null)?.then === "function";

/**
 * Navigation state for one running app instance, held outside React.
 *
 * Records are keyed by dock instance. A record carries the
 * resource identity, its access context, the navigation parameters and a
 * monotonic revision, so a panel reads a committed result instead of publishing
 * requests while it renders. Only an explicit command advances a revision, an
 * unchanged record returns the same snapshot reference, and a record whose dock
 * closed, scope changed or runtime went away is cancelled rather than replayed.
 */
export class FileNavigationOwner {
  private records = new Map<string, Record>();
  /** One-shot operations per dock instance, keyed like records. */
  private operations = new Map<string, AbortController>();
  /** Survives record deletion so a reused dock tab id never repeats a generation. */
  private generations = new Map<string, number>();
  /** One counter for the whole instance, so no two records repeat a revision. */
  private navigationRevision = 0;
  private listeners = new Set<() => void>();
  private disposed = false;
  private ports: FileNavigationPorts;

  constructor(ports: FileNavigationPorts) {
    this.ports = ports;
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  /** Stable per key: an unchanged record returns the same snapshot reference. */
  getSnapshot = (key: string): FileNavigationSnapshot | null => this.records.get(key)?.snapshot ?? null;

  /**
   * Bind what a dock shows. Another resource space starts a new lifetime;
   * another session in the same space keeps the previews and their positions
   * but drops the access context they were read with, because a presented tool
   * scope belongs to the session that captured it.
   */
  bindScope(scope: FileNavigationScope, key: FileNavigationScopeKey): void {
    const record = this.ensure(scope);
    const current = record.snapshot.scope;
    if (current?.resource === key.resource && current.session === key.session) return;
    if (current === null) {
      record.snapshot = { ...record.snapshot, scope: key };
      this.notify();
      return;
    }
    if (current.resource !== key.resource) {
      // A command that is moving this dock to another resource space is still
      // resolving: it owns the dock now, so the bind drops the previous space's
      // previews instead of cancelling the command that caused the switch.
      if (record.pending && record.pending.resource === key.resource) this.replaceResource(record, key);
      else this.reset(scope, key);
      return;
    }
    this.rebind(scope, record, key);
  }

  /**
   * Keep only the given dock keys, cancelling the rest. Called with the open
   * dock tabs of the active session, so a closed tab, another session or
   * another workspace ends its records instead of restoring stale access.
   */
  retain(openKeys: Iterable<string>): void {
    const keep = new Set(openKeys);
    let dropped = false;
    for (const [key, record] of Array.from(this.records)) {
      if (keep.has(key)) continue;
      this.drop(key, record);
      dropped = true;
    }
    for (const [key, operation] of Array.from(this.operations)) {
      if (keep.has(key)) continue;
      operation.abort();
      this.operations.delete(key);
    }
    if (dropped) this.notify();
  }

  /**
   * Begin a one-shot operation in a dock instance. A newer operation for the
   * same dock supersedes it, and the dock's lifetime ending cancels it; an
   * unrelated dock keeps running, so no panel cancels another's work.
   */
  beginOperation(scope: FileNavigationScope): FileNavigationOperation {
    const key = fileNavigationKey(scope);
    const previous = this.operations.get(key);
    previous?.abort();
    const controller = new AbortController();
    if (this.disposed) controller.abort();
    this.operations.set(key, controller);
    return {
      signal: controller.signal,
      owns: () => this.operations.get(key) === controller && !controller.signal.aborted,
      finish: () => { if (this.operations.get(key) === controller) this.operations.delete(key); },
    };
  }

  /**
   * Open a resource in the dock that presents it. Callers outside a dock (a
   * transcript row, a Markdown link) use this; a panel acting on its own
   * contents uses `openIn`, because it already is the target dock.
   */
  open(command: FileNavigationCommand): FileNavigationOutcome | Promise<FileNavigationOutcome> {
    if (this.disposed) return { status: "cancelled", reason: "disposed" };
    let scope: FileNavigationScope;
    try {
      scope = { sessionTabId: command.ref.tabId, dockTabId: this.ports.revealDock(command.ref) };
    } catch (error) {
      return { status: "failed", error: asError(error) };
    }
    return this.openIn(scope, command);
  }

  /** Open a resource in a dock instance the caller already identified. */
  openIn(
    scope: FileNavigationScope,
    command: FileNavigationCommand,
  ): FileNavigationOutcome | Promise<FileNavigationOutcome> {
    if (this.disposed) return { status: "cancelled", reason: "disposed" };
    const key = fileNavigationKey(scope);
    const record = this.ensure(scope);
    const operation = this.begin(record, resourceOf(command.ref));
    let resolved: ResolvedFileResource | Promise<ResolvedFileResource>;
    try {
      resolved = this.ports.resolve(command.ref);
    } catch (error) {
      return this.settle(key, record, operation, SUPERSEDED, (): FileNavigationOutcome =>
        ({ status: "failed", error: asError(error) }));
    }
    if (!isPromise(resolved)) {
      return this.settle(key, record, operation, SUPERSEDED, (): FileNavigationOutcome => {
        this.commitNavigation(record, resolved, command.params);
        return { status: "opened", resource: resolved };
      });
    }
    return resolved.then(
      (resource) => this.settle(key, record, operation, SUPERSEDED, (): FileNavigationOutcome => {
        this.commitNavigation(record, resource, command.params);
        return { status: "opened", resource };
      }),
      // A resolution that failed after its dock went away is a cancellation:
      // the row that asked is gone, and its failure has nowhere to be shown.
      (error) => this.settle(key, record, operation, SUPERSEDED, (): FileNavigationOutcome =>
        ({ status: "failed", error: asError(error) })),
    );
  }

  /** Activate an open entry; it keeps the access context of the command that opened it. */
  selectEntry(scope: FileNavigationScope, path: string): void {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    this.supersede(record);
    const entry = record?.snapshot.entries.find((candidate) => candidate.resource.path === path);
    if (!record || !entry) return;
    this.commitNavigation(record,entry.resource, {
      action: entry.source ? "source" : "preview",
      view: "files",
    });
  }

  /**
   * Select a path that carries no presentation of its own — a recent file or a
   * restored session. The read goes through the current workspace access, so a
   * path alone never re-grants the permissions of an earlier presentation.
   */
  selectPath(scope: FileNavigationScope, resource: FileResourceIdentityInput): FileNavigationOutcome | Promise<FileNavigationOutcome> | void {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    if (resource.hostId === "local") {
      return this.openIn(scope, {
        ref: { source: "workspace", hostId: resource.hostId, tabId: scope.sessionTabId, path: resource.path },
        params: { action: "preview", view: "files" },
      });
    }
    // Remote tree paths come from ListRemoteDir and are already host coordinates;
    // resolving them as derived artifacts would require a tool-call grant they do
    // not carry.
    this.supersede(record);
    this.commitNavigation(record, workspaceResource(resource, scope.sessionTabId), { action: "preview", view: "files" });
    return { status: "opened", resource: record.snapshot.selected!.resource };
  }

  /** Switch an open tab between its preview and its source, reusing the tab. */
  setSourceMode(scope: FileNavigationScope, path: string, source: boolean): void {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    this.supersede(record);
    const entry = record?.snapshot.entries.find((candidate) => candidate.resource.path === path);
    if (!record || !entry) return;
    this.commitNavigation(record, entry.resource, { action: source ? "source" : "preview", view: "files" });
  }

  /** Close one preview tab, selecting the neighbour the previous tab list implies. */
  closeEntry(scope: FileNavigationScope, path: string): void {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    this.supersede(record);
    const current = record.snapshot;
    const entries = current.entries.filter((entry) => entry.resource.path !== path);
    if (entries.length === current.entries.length) return;
    const selected = current.selected?.resource.path === path ? entries[entries.length - 1] ?? null : current.selected;
    this.commitState(record, { entries, selected });
  }

  /**
   * Drop every preview tab of this dock, as a scoped file or change list does.
   * The selection survives, so leaving the scope shows the file again without
   * a new command; `clearSelection` is what closes the preview itself.
   */
  clearEntries(scope: FileNavigationScope): void {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    this.supersede(record);
    this.commitState(record, { entries: [] });
  }

  clearSelection(scope: FileNavigationScope): void {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    this.supersede(record);
    this.commitState(record, { selected: null });
  }

  /**
   * Restore remembered paths for a dock no command has opened anything in yet.
   * Restored entries carry workspace access only; a record a command already
   * wrote to is left alone, so a restore never outranks a live navigation.
   */
  restore(scope: FileNavigationScope, state: FileNavigationRestore): void | Promise<void> {
    const record = this.records.get(fileNavigationKey(scope));
    if (!record) return;
    const current = record.snapshot;
    if (record.pending || current.entries.length > 0 || current.selected || !state.paths.length) return;
    const resources = state.paths.map((path): ResolvedFileResource | Promise<ResolvedFileResource> | null => {
      try {
        return this.ports.resolve({
          source: "workspace",
          hostId: state.hostId,
          tabId: scope.sessionTabId,
          path,
        });
      } catch {
        return null;
      }
    });
    const commit = (resolved: readonly (ResolvedFileResource | null)[]): void => {
      if (this.records.get(fileNavigationKey(scope)) !== record || record.snapshot !== current || record.pending) return;
      const entries = resolved
        .filter((resource): resource is ResolvedFileResource => resource !== null)
        .reduce((all, resource) => upsertEntry(all, resource, false).entries, [] as readonly FilePreviewEntry[]);
      if (!entries.length) return;
      this.commitState(record, {
        entries,
        selected: state.selectedPath
          ? entries.find((entry) => entry.resource.requestedPath === state.selectedPath) ?? null
          : null,
      });
    };
    if (!resources.some((resource) => resource !== null && isPromise(resource))) {
      commit(resources as (ResolvedFileResource | null)[]);
      return;
    }
    return Promise.all(resources.map((resource) => Promise.resolve(resource).catch(() => null))).then(commit);
  }

  dispose(): void {
    this.disposed = true;
    for (const [key, record] of Array.from(this.records)) this.drop(key, record);
    this.records.clear();
    for (const operation of Array.from(this.operations.values())) operation.abort();
    this.operations.clear();
    this.notify();
  }

  private ensure(scope: FileNavigationScope): Record {
    const key = fileNavigationKey(scope);
    const existing = this.records.get(key);
    if (existing) return existing;
    const lifetime = new AbortController();
    const record: Record = {
      lifetime,
      pending: null,
      snapshot: {
        scope: null,
        sessionTabId: scope.sessionTabId,
        dockTabId: scope.dockTabId,
        generation: this.generations.get(scope.dockTabId) ?? 0,
        entries: [],
        selected: null,
        sourcePaths: [],
        navigation: null,
        revision: 0,
        contentRevision: 0,
        treeReveal: 0,
        signal: lifetime.signal,
      },
    };
    this.records.set(key, record);
    return record;
  }

  /** Enter another resource space, keeping an operation that is moving the dock. */
  private replaceResource(record: Record, key: FileNavigationScopeKey): void {
    this.apply(record, (current) => ({
      ...current,
      scope: key,
      entries: [],
      selected: null,
      sourcePaths: [],
      revision: current.revision + 1,
      contentRevision: current.contentRevision + 1,
    }));
  }

  private reset(scope: FileNavigationScope, key: FileNavigationScopeKey): void {
    const recordKey = fileNavigationKey(scope);
    const previous = this.records.get(recordKey);
    if (previous) this.drop(recordKey, previous);
    const record = this.ensure(scope);
    record.snapshot = { ...record.snapshot, scope: key };
    this.notify();
  }

  /**
   * The same resource space under another session: the previews stay exactly
   * where the user left them, and every read is rebound to the current session
   * with workspace credentials, so a tool call from the previous session can
   * never authorize a read in this one.
   */
  private rebind(scope: FileNavigationScope, record: Record, key: FileNavigationScopeKey): void {
    const access: FileAccessContext = { source: "workspace", tabId: scope.sessionTabId };
    const downgrade = (entry: FilePreviewEntry): FilePreviewEntry =>
      ({ resource: { ...entry.resource, access }, source: entry.source });
    this.supersede(record);
    this.apply(record, (current) => ({
      ...current,
      scope: key,
      entries: current.entries.map(downgrade),
      selected: current.selected ? downgrade(current.selected) : null,
      revision: current.revision + 1,
      contentRevision: current.contentRevision + 1,
    }));
  }

  /**
   * A synchronous command takes the dock over: whatever open was still
   * resolving for it can no longer commit, so a click is never replaced by an
   * earlier command's late result.
   */
  private supersede(record: Record): void {
    record.pending?.controller.abort();
    record.pending = null;
  }

  private begin(record: Record, resource: string | null): AbortController {
    record.pending?.controller.abort();
    const operation = new AbortController();
    record.pending = { controller: operation, resource };
    return operation;
  }

  /** Commit only while this operation still owns the record; otherwise it lost. */
  private settle<T>(
    key: string,
    record: Record,
    operation: AbortController,
    lost: FileNavigationOutcome,
    commit?: () => T,
  ): T | FileNavigationOutcome {
    if (this.records.get(key) !== record || record.pending?.controller !== operation || operation.signal.aborted) return lost;
    record.pending = null;
    return commit ? commit() : lost;
  }

  private commitNavigation(
    record: Record,
    resource: ResolvedFileResource,
    params: FileNavigationParams,
  ): void {
    this.apply(record, (current) => {
      const { entries, selected } = upsertEntry(current.entries, resource, params.action === "source");
      const target = params.view === "files" ? selected : current.selected;
      // Every explicit command is delivered as its own navigation revision: a
      // repeat must still reveal the file when the dock moved on. Only the read
      // inputs decide whether the preview has to load its content again.
      this.navigationRevision += 1;
      return {
        ...current,
        entries,
        selected: target,
        sourcePaths: sourcePathsOf(entries),
        navigation: { revision: this.navigationRevision, resource, params },
        revision: current.revision + 1,
        contentRevision: current.contentRevision + (target !== current.selected ? 1 : 0),
        treeReveal: params.action === "reveal-tree" ? current.treeReveal + 1 : current.treeReveal,
      };
    });
  }

  private commitState(
    record: Record,
    patch: { entries?: readonly FilePreviewEntry[]; selected?: FilePreviewEntry | null },
  ): void {
    this.apply(record, (current) => {
      const entries = patch.entries ?? current.entries;
      const selected = patch.selected !== undefined ? patch.selected : current.selected;
      if (entries === current.entries && selected === current.selected) return null;
      return {
        ...current,
        entries,
        selected,
        sourcePaths: sourcePathsOf(entries),
        revision: current.revision + 1,
        contentRevision: current.contentRevision + (selected !== current.selected ? 1 : 0),
      };
    });
  }

  private apply(record: Record, reduce: (current: FileNavigationSnapshot) => FileNavigationSnapshot | null): void {
    const next = reduce(record.snapshot);
    if (!next) return;
    record.snapshot = next;
    this.notify();
  }

  private drop(key: string, record: Record): void {
    record.pending?.controller.abort();
    record.lifetime.abort();
    this.records.delete(key);
    this.generations.set(record.snapshot.dockTabId, record.snapshot.generation + 1);
  }

  private notify(): void {
    for (const listener of Array.from(this.listeners)) listener();
  }
}

export type FileResourceIdentityInput = Readonly<{ hostId: string; path: string }>;

function workspaceResource(resource: FileResourceIdentityInput, sessionTabId: string): ResolvedFileResource {
  const access: FileAccessContext = { source: "workspace", tabId: sessionTabId };
  return {
    hostId: resource.hostId,
    path: resource.path,
    identityPath: resource.path.replace(/\\/g, "/"),
    requestedPath: resource.path,
    access,
  };
}

function sameResource(left: ResolvedFileResource, right: ResolvedFileResource): boolean {
  return left.hostId === right.hostId
    && left.identityPath === right.identityPath
    && left.path === right.path
    && sameAccessContext(left.access, right.access);
}

const sourcePathsOf = (entries: readonly FilePreviewEntry[]): readonly string[] =>
  entries.filter((entry) => entry.source).map((entry) => entry.resource.path);

/**
 * Move an entry to the most-recent position. An entry whose resource and mode
 * are unchanged keeps its identity, and an unchanged list keeps its array
 * reference, so a repeated command leaves every derived value — including the
 * preview's read inputs — exactly as it was.
 */
function upsertEntry(
  entries: readonly FilePreviewEntry[],
  resource: ResolvedFileResource,
  source: boolean,
): { entries: readonly FilePreviewEntry[]; selected: FilePreviewEntry } {
  const existing = entries.find((candidate) => candidate.resource.identityPath === resource.identityPath);
  const entry = existing && existing.source === source && sameResource(existing.resource, resource)
    ? existing
    : { resource, source };
  const next = [...entries.filter((candidate) => candidate.resource.identityPath !== resource.identityPath), entry]
    .slice(-FILE_PREVIEW_LIMIT);
  const unchanged = next.length === entries.length && next.every((candidate, index) => candidate === entries[index]);
  return { entries: unchanged ? entries : next, selected: entry };
}
