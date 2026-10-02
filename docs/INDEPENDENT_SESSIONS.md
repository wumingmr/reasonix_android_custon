# Independent session identity and repair acceptance

[简体中文](INDEPENDENT_SESSIONS.zh-CN.md)

Implementation baseline: `4c8cec3b9f69f979c34769eb197908b7f549c578`, plus the existing local working tree. Validation date: 2026-09-17. The record below captures implementation-stage local qualification. Delivery is isolated on base `81fe2f1fd`, excluding transcript PR #10438; the delivery PR records live CI and merge state.

## Product contract

An ordinary fork is an independent sibling session. The visible row, selection, unread state, title, menu, runtime, and notification must resolve to the same target. Sharing a TopicID does not make two sessions equal. Subagents and automatic recovery copies retain their dedicated semantics.

Existing SessionIDs and transcripts are retained. New forks retain `ParentSessionID`, inherit the parent's group, default to unpinned, and follow the parent only when manual ordering is enabled. An activity-sorted workspace stays activity-sorted.

This repair does not cover full-text navigation, transcript-window eviction, or the separate purge/log race. Existing working-tree changes in those areas are preserved.

## Identity and ownership

| Target | Stable identity | Request isolation |
| --- | --- | --- |
| Canonical session | HostID + SessionID | Existing generation/request version is additional to stable identity |
| Unadopted source | HostID + normalized SourceKey, including HeadID for multihead logs | Owner validates the exact source |
| Unsaved surface | HostID + TabID | Replaced through verified canonical/source aliases |
| TopicID-only compatibility call | Historical association | Multiple candidates produce `ambiguous_target` |

`sessionIdentityBaseKey` preserves the generation-bearing identity API used by history loading. `projectSessionIdentity` adapts existing refs and source identities for projections. Only owner-verified aliases merge identities; same-title/topic/path on a different host is insufficient.

Explicit invalid refs fail rather than falling back. Local resolvers reject remote refs. Remote single-session writes use the remote owner and negotiated exact identifiers; unsupported or ambiguous remote targets do not fall back to topic-wide writes. Remote organization reads do not start Serve.

Ticketed activation captures navigation intent before adoption and carries that same intent into the canonical open. A nested open cannot claim a new intent and overtake a newer click. Explicit source selection cannot attach an unrelated live sibling merely because its TopicID matches.

## Registry v3, organization, and compatibility

The workspace registry now owns `Organization`: revision, manual-order flag, explicit order, groups, migration version, and imported identities. `Workspace.SessionIDs` remains canonical membership; compatibility order is mirrored in the same transaction. Pins remain session presentation data.

`GetSessionOrganization` returns a snapshot. `UpdateSessionOrganization(workspace, expectedRevision, mutation)` applies semantic mutations such as moving a target before/after another target or changing group membership. Clients never submit the current visible page as the full workspace order. On a CAS conflict the latest snapshot is returned; the client replays the original mutation at most twice and then reloads with an error.

Migration imports old topic pins/groups/order only for not-yet-imported members. Explicit session choices win, including deliberately ungrouped members. Old topic positions expand into adjacent session intervals. Source adoption atomically replaces source identities in order and groups with its canonical ref through the existing migration journal. Adopting one head does not retire other heads of the same log.

The reader accepts v1/v2/v3. Before a v2 registry is upgraded, original bytes are retained in a `.v2.bak` backup; the existing v1 backup path is retained. Unknown fields survive round trips, including new nested organization records and remote project configuration. Old project files and organization sidecars remain import/compatibility material and are no longer authoritative after migration.

**Downgrade policy:** the supported previous reader/writer rejects v3, leaving its bytes unchanged. An older client is not supported for editing an upgraded workspace. Do not delete the registry to bypass this check or silently restore a backup over newer user choices. Transcript formats are not upgraded or removed by this change. The previous-writer smoke extracts the actual baseline implementation rather than simulating an old reader.

Fork persistence and publication are recoverable steps. The operation records child ID, parent ID, and pending publication; restart recovery attaches the same durable child and inherits group/order atomically. Repeating the operation does not reset a subsequently edited child title, pin, or group. Publication errors remain visible.

## Projection, lifecycle, pagination, and unread

- Directory and runtime projections share stable identity. Runtime overlays only runtime fields and cannot replace a durable ref or historical metadata. Empty runtime snapshots preserve durable rows. Source aliases also apply to pinned and incomplete pages.
- Successful archive receipts install an application-owned lifecycle fence, including verified aliases. Directory, runtime, and resident projections all apply it. Failed archives install no committed fence. Refresh start and component remount do not clear it. Only a newer active directory lifecycle proves restoration; runtime revisions cannot release it. Fences are conservatively retained until restoration or application exit; there is no absence-based retirement across unrelated owner revisions.
- Lists normalize/deduplicate, filter, sort, then freeze an immutable read result. Registry membership, lifecycle, presentation and organization share one projection, and canonical metadata is observed once. Continuations bind the frozen result, query and ordinal; title/result/organization writes do not invalidate them. Relevant lifecycle/source changes, expiry or eviction return `stale_cursor`. UI recovery stages and replaces the entire visible window once, preserving the prior display on failure. Background events coalesce without cancelling active reads. See [read snapshot implementation](READ_SNAPSHOT_PAGINATION.md) for legacy search, resources and acceptance gates.
- Unread v3 is one atomic local-storage object containing metric, baseline, record version, and verification/repair evidence. Normal reads only increase same-metric baselines. Only an imported unverified result baseline can be lowered once after a complete positive owner observation. Captured record versions and actual-read floors reject late repairs. Time-based source baselines are not interpreted as result sequences. Cross-window merges preserve newer real reads. Authority requests are deduplicated and limited to two concurrent requests; cold incomplete metadata defers repair.

## Seven-defect acceptance matrix

| Original defect | Repair and formal regression | Interface evidence |
| --- | --- | --- |
| History/top rename affects siblings | Exact selectors in command owners; exact canonical runtime title projection; delayed AI generation cannot rename a sibling created during generation. `independent-session-rename.test.tsx`, `TestAIRenameDoesNotRenameSiblingCreatedDuringGeneration` | Browser top/sidebar/history writes target B; native top rename preserves A history/title |
| Archived B revived by late runtime | Application-owned alias/lifecycle fences across all projections. `session-lifecycle-fences.test.ts`, `project-tree-archive-race.test.ts`, `independent-session-boundaries.test.ts` | Browser late directory/runtime + sidebar remount; native archive/restart/restore/restart |
| Legacy siblings share selection/unread | Source identity and metric-specific reads. `independent-session-boundaries.test.ts`, `session-read-activity.test.ts` | Browser B10→20 unread, reading A leaves B unread, reading B clears it; native A/B/A. Exact legacy source cases are mechanism tests |
| Adoption resets group or duplicates rows | Journal-atomic alias/group/order replacement; alias-aware first/incomplete-page merging. `TestOrganizationSourceJournalCommitPreservesUngroupedPlacement`, `TestIndependentLegacyHeadsAdoptOneWithoutHidingSibling`, `independent-session-boundaries.test.ts` | Browser B leaves group and remains ungrouped after remount; actual multihead adoption is Go fixture evidence |
| Frozen paging and obsolete cursor recovery | Sorting writes preserve a current snapshot; invalid lifecycle/expired/old-format cursors require a whole-window rebuild. `project_tree_organization_test.go`, `project_topic_group_filter_test.go`, `session_topic_snapshot_test.go` | Browser sends the obsolete old:5 cursor, receives stale_cursor, rebuilds the window and renders all seven rows without duplicates |
| Unread repair lowers a valid baseline | Owner verification, one-time imported repair, record CAS/read floor, storage-event merge. `session-read-activity.test.ts`, `session_activity_baseline_test.go` | Browser baseline20 remains20 after ready10/20 + remount; deterministic tests also cover contaminated100/10 and a read during repair |
| Fork fails to follow parent | Registry-authoritative order and recoverable publication. `TestOrganizationForkAttachmentInheritsGroupAndFollowsParent`, `TestOrganizationForkPreservesActivitySortWithoutEnablingManualOrder`, `TestIndependentForkResumesPublicationWithSameOperationWithoutResettingChoices` | Native real fork and independent continuation; interruption/restart/order are deterministic Go tests |

Additional formal coverage includes host isolation and passive remote reads (`remote_session_organization_test.go`), unknown remote config fields, v2 backup/unknown fields, organization CAS and failed transactions, multiple heads, existing canonical settings, native title binding/runtime identity, and superseded activation intent.

Two final boundary regressions are also covered: `runtime-notification-independent-session.test.ts` verifies that identical turn/prompt/session IDs on two hosts neither merge notifications nor select the wrong title; `TestBlankReuseWaitsForPendingCanonicalCreateInsteadOfCancellingIt` verifies that reusing an in-flight blank waits for its original canonical creation instead of cancelling it and leaving a second recoverable empty session. The native harness now checks that initial/pending identity before sending the first message.

## Reproduction commands

From the repository root unless a subshell changes directory:

```sh
go test ./...
(cd desktop && go test ./... -timeout 20m)
(cd desktop && go test -race ./internal/workspacestate -count=1)
(cd desktop && go run . -emit-contract frontend/src/generated)
go run ./tools/desktopinventory
git diff --check
(cd desktop/frontend && pnpm build && pnpm test:typecheck)
(cd desktop/frontend && pnpm test)
(cd desktop/frontend && pnpm test:remote && pnpm test:workspace)
(cd desktop/frontend && pnpm exec tsx --test src/__tests__/project-tree*.test.ts src/__tests__/session-identity*.test.ts src/__tests__/independent-session*.test.* src/__tests__/session-read-activity.test.ts src/__tests__/session-lifecycle-fences.test.ts)
(cd desktop/frontend && node bench/independent-sessions.mjs)
python3 desktop/packaging/registry-previous-writer-smoke.py
(cd desktop && go build -o build/bin/reasonix-desktop-service .)
(cd desktop/electron && pnpm build)
node desktop/packaging/independent-session-native-smoke.mjs
```

The Desktop module's complete suite takes several minutes; the explicit 20-minute process ceiling is not a per-case retry. A prior run exposed old expectations that intentionally attached a different same-topic live session. Those tests now verify exact adopted source mapping while preserving the live sibling. No test is skipped to obtain a pass.

## Evidence record and limits

| Layer | Local evidence |
| --- | --- |
| Root Go | Full suite passed; `reasonix-independent-root-final.log` |
| Desktop Go | Final full suite passed; main package took 301.282 seconds. `reasonix-independent-desktop-final.log` |
| Race | Registry, targeted navigation/AI-title/fork lifecycle, and in-flight blank creation reuse tests passed; `reasonix-independent-registry-race.log`, `reasonix-independent-lifecycle-race.log`, `reasonix-independent-blank-race.log` |
| Frontend | 50 focused session tests passed; notification/sound regressions passed; production/test typechecks and build passed. `pnpm test` pretest passed, then the discovery runner initially found an old organization-CAS fixture. After migrating it to the semantic API, all 394 discovery suites passed (`reasonix-frontend-discovery-final.log`). Build includes hooks, app layers, CSS and unchanged workspace bundle limits |
| Contract/inventory | Regenerated from current source; inventory freshness passed (`reasonix-independent-inventory-check.log`); `git diff --check` passed |
| Chromium | Actual ProjectTree, top bar, HistoryPanel, prompt card, command owners; stub only RPC boundary. Covers keyboard, drag/order/groups, approval/stop, archive races, stale pagination and unread. Evidence: `reasonix-independent-browser/result.json`, all four workbench/creation light/dark screenshots and `pagination-recovered.png` |
| Electron | Actual development shell, built production App renderer, real Go service and disposable loopback provider. Seven scenarios passed, including startup identity reuse, background completion and two restarts. `reasonix-independent-native/result.json`; `phase-*.json` records canonical identity/pending-create checkpoints |
| Previous writer | Actual baseline v3 rejection and byte preservation passed. `reasonix-independent-registry-compatibility.json` |
| Remote/workspace | Protocol/owner isolation fixtures and frontend `pnpm test:remote` / `pnpm test:workspace` passed; `reasonix-independent-remote-front.log`, `reasonix-independent-workspace-front.log`. No real SSH host disconnect/reconnect acceptance was performed |
| CI / packages | Not run. This is not cross-platform CI or signed production-package qualification |

Named evidence files are local run artifacts, not fixtures required by production. The scripts regenerate them. Native test data uses a disposable home and no external provider credentials. The native fixture creates a real fork, then seeds only the temporary registry's historical shared TopicID while the app is closed; it does not replace either transcript or SessionID.

Qualification found and retained two failures before the successful run: a cold unbundled Vite dev server missed the shell startup deadline (the final harness uses the finished renderer build without extending that deadline), and production-speed startup exposed cancellation of a pending blank create. The latter was fixed in the blank-session owner and has both controlled Go and native evidence. Cold dev-server startup itself is not qualified by this result.
