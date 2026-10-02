# Dock file navigation

How the right dock opens a file, who owns the result, and why a click can no
longer start a render loop. Read this before changing
`WorkspaceDockRegion`, `WorkspacePanel`, `RemotePanel` or anything under
`lib/fileNavigation*`.

## Contract

Opening a file is a command, not a render.

```
file row / markdown link / verified answer reference / file tree / preview control
  → bound open command (FileNavigationOwner)
  → resolve the resource and re-check the command is still current
  → pick the target dock and preview position
  → commit a navigation record
  → the dock subscribes and renders what was committed
```

- **A render never navigates.** Panels read `owner.getSnapshot(key)` through
  `useSyncExternalStore`; nothing is published while React renders.
- **Resource identity, access context and navigation parameters are separate.**
  Preview identity is resource space + backend-canonical path. Session tab,
  source and optional `toolCallId` belong to `FileAccessContext`; `preview`,
  `source` and `reveal-tree` are parameters. Switching a parameter reuses the
  preview tab, while switching sessions can rebind its access without changing
  which file it represents.
- **Access context travels with the command.** Workspace, presented and
  host-verified answer references keep distinct readers. Reopening a path from
  another entry point uses that command's credentials, never an earlier
  presentation's. An answer reference is resolved and authorized again by the
  host for every read or direct action.
- **Records are keyed by dock tab.** A record holds the dock
  instance identity, its lifecycle generation, the entries with their access
  contexts, the last navigation intent, a display revision, a content revision
  and the lifetime signal.
- **Only an explicit command advances a revision.** An equivalent command keeps
  the entry objects and the entry list identical, so the preview does not
  restart a read that is still valid.

## Harness design → Reasonix implementation

| Harness design | Reasonix implementation |
| --- | --- |
| Command-driven navigation: opening is an event, rendering reads the result | `FileNavigationOwner` (`lib/fileNavigationOwner.ts`) commits records; `WorkspaceDockRegion` no longer builds a request during render; `useFileNavigationRecord` (`app-shell/useFileNavigation.ts`) only reads |
| Stable resource identity: a canonical file coordinate, not an object reference | `FileResourceRef` / `FileAccessContext` / `ResolvedFileResource` (`lib/fileResource.ts`); backend-resolved `identityPath` collapses relative/absolute aliases, while access context separately keeps workspace, presented and verified-reference readers distinct |
| Separate navigation parameters from identity | `FileNavigationParams` — `action` (`preview`/`source`/`reveal-tree`) and `view` (`files`/`changed`) never change what the resource is |
| Independent navigation instance per dock | One `FileNavigationOwner` per running app instance (`useFileNavigationRuntime`), one record per dock tab, `generation` per lifecycle |
| Command results are reported, not thrown | `FileNavigationOutcome` — `opened` / `cancelled` (superseded, closed, disposed) / `failed`; a cancelled command shows no error |
| Resource URL ownership on a cancelled browser preview | `openBrowserPreview` releases exactly one URL: the tab owns it once handed over, otherwise the command revokes it |
| No global cancellation | `fileNavigationLifetime.ts` is gone; a new command supersedes the record's pending one, and `retain`/`bindScope` end a record's lifetime |
| Tab restore without replay | The record survives a dock collapse; a remount reads the retained selection without re-applying the last intent or re-running the open command |

Not ported: split view, floating preview windows, layout undo history, the
Cordis plugin framework, and the `dsh-resource://` scheme (resource ids stay
internal to navigation).

## Lifecycle and cancellation

| Event | Effect |
| --- | --- |
| New command for the same dock | Aborts the record's pending operation; the older outcome is `cancelled` |
| Another command to a different dock | Nothing: panels never cancel each other |
| Dock tab closed or removed from the active workspace | `retain` drops the record and aborts its lifetime |
| Session changes inside the same project | `bindScope` keeps entries and selection, then replaces their access contexts with current-session workspace access |
| Project/remote-host resource space changes | `bindScope` rebuilds the record: empty entries, advanced generation, previous lifetime aborted |
| Dock collapsed (`workspacePanelOpen` false) | Nothing: the record is what re-expanding restores |
| Runtime unmounted | `dispose` aborts every record and every one-shot operation |
| Dock tab id reused after close | A new generation; remembered paths come back with workspace access only |

## Persistence

Only paths are persisted (`workspaceViewMemory`, the dock tab list). Lifecycle
generations, cancellation signals and access contexts live in memory and are
never written to `localStorage`. Storage written by older versions is read as
paths and revalidated against the current workspace, which is why a restored
preview never regains a presented tool scope.

No Go/Electron bridge payload, session log, tool schema, standing instruction or
provider request byte changes, so prompt caching is unaffected.

## Verification

| Area | Test |
| --- | --- |
| Navigation instance | `file-navigation-owner.test.ts` |
| Command ordering and cancellation | `file-navigation-races.test.ts` |
| Verified answer-reference reader and source toggle | `workspace-reference-reader.test.tsx` |
| Dock chain (local and remote) | `dock-file-navigation.test.tsx` |
| Preview tabs, cap, source mode, reveal, generations | `file-navigation-dock.test.tsx` |
| Render-loop defect, StrictMode, re-renders, remount | `file-navigation-lifecycle.test.tsx` |
| Remote reads, save isolation, disconnect | `remote-file-navigation-races.test.tsx` |
| Dock request delivery | `dock-navigation.test.ts`, `dock-view-requests.test.tsx` |
| Real DOM and Electron | `bench/dock-file-navigation.mjs` (`test:app-browser`, `test:dock-electron`) |
| Answer-reference click into the running owner | `bench/chat-file-reference.mjs` (`test:chat-file-browser`) |
