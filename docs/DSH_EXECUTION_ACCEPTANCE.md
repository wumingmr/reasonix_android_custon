# Harness migration acceptance report

Acceptance baseline: Reasonix `main-v2@986a6bc967`; behavioral reference: DeepSeek Harness `master@c291e7961a`. Validation date: 2026-09-13.

## Result

The harness-style execution model is the default and has no legacy behavior switch. Structured file mutations use host-owned live observations and version checks. Read coverage, proof receipts, operation settlement, Auto Guard, and unknown-effect recovery state no longer control tool admission or completion. Legacy fields and terminal states remain readable only for historical compatibility.

## Reported issue sequences

| Reports | Acceptance sequence | Result | Coverage |
| --- | --- | --- | --- |
| #9994, #9995, #10067, #10103 | Three `read → edit → bash → edit` cycles, including a Chinese path, CRLF, move, and delete | Passed; successful writes refresh observation and commands do not consume or freeze it | Agent integration, file tools, macOS, Windows 11 |
| #10053 | Read one window of a large file, run a command, and finish | Passed; no whole-file debt, forced continuation, or final gate | Agent integration and bounded-read tests |
| #10085 | Same-batch `read → edit → edit → bash` | Passed; observations update in actual execution order | Agent batch integration, macOS, Windows 11 |
| #10153 | Simulate a committed side effect whose result is lost, then inspect state and continue | Passed; the host records `unknown`, closes the turn as ordinary `interrupted`, requires no user decision, blocks no network or identical call, and performs no replay | Controller crash test, turn ledger, Desktop compatibility API |

## Freshness and concurrency

- Unobserved overwrite returns `FS_NOT_OBSERVED`; confirmed absence permits only a no-overwrite create.
- Any successful text window observes the current version, and each successful write advances it.
- Same-size rewrites, restored mtime, permission changes, replacements, and aliases invalidate stale observations.
- Competing writers from one version cannot silently lose an update; concurrent creates cannot overwrite the winner.
- Local publication retains temporary files, atomic replacement, permissions, and encoding. Buffer and disk targets have distinct identities.
- `read_file`, `write_file`, `edit_file`, `multi_edit`, `notebook_edit`, `delete_range`, `delete_symbol`, and `move_file` use the shared observation or post-commit state path.

## Recovery, completion, and compatibility

- Tool start is persisted before the body runs. Started calls settle on cancellation and calls that never start receive explicit results.
- Restart recovery pairs a missing result with one `unknown` result. Current runs emit neither `recovery_required` nor `requires_user_decision`; old values still decode and render read-only.
- Reconstructed context includes one bounded factual handoff and advisory state checks. Current file contents never synthesize success for the earlier call.
- `complete_step`, `review_report`, and read-policy receipts are absent from discovery. A legacy call receives an ordinary `tool_retired` result.
- Recovery action endpoints return `tool_recovery_retired` and cannot confirm, reject, inspect hidden arguments, or replay an operation.
- `todo_write` validates public fields, state values, hierarchy, and stable IDs. The model updates completion explicitly.
- Identical consecutive calls receive reminders only at counts 3, 5, and 8.

## Platform and product validation

The table below records the initial acceptance run at `3f7350f6e`. It did not
establish correctness of every publication interleaving. The PR review found
and corrected the additional defects listed in the next section; those findings
supersede the earlier unconditional freshness/publication claims.

| Environment | Validation | Result |
| --- | --- | --- |
| macOS | Full root Go tests and vet; independent Desktop and SDK Go modules; contract, golden, cache, and repository checks | Passed |
| Desktop frontend | Full frontend test suite and production build | Passed |
| Desktop shell | 206 shell tests and 57 Electron layout scenarios | Passed |
| Windows 11 VM | Native volume serial/file index/handle identity; same-size changes; real ACL `ChangeTime`; observation, concurrent create/edit, and harness scenarios | Passed |
| Desktop compatibility | Legacy recovery cards are read-only with no actions; current cards retain not-started/failed/interrupted/unknown facts; normal input remains available | Passed |

Windows validation ran natively in the local Parallels Windows 11 VM rather than through cross-compilation. The test copy and temporary artifacts were removed afterward.

## PR #10223 review corrections

Initial review baseline: head `3f7350f6e`, then-current merge-base `104792af2`.
The conflict integration was validated against `main-v2@4daa815be` (#10209).
The implementation follows DSH `fs-observation-policy` (session-owned observations,
presence-based write intent) and `fs-local/src/fsio.ts` (staging before no-overwrite
publication). Reasonix retains its Go execution, encoding, and buffer adapters.

| Defect in the reviewed head | Correction and regression evidence |
| --- | --- |
| Generic `read_file.Execute` bypassed observation registration | Delegate to the same bounded read implementation as `ExecuteRead`; preserve external-root display redaction |
| Deleting an observed file changed an overwrite into a blind create | Preserve the observed-present intent and return stale-version; normalize absent paths through existing symlink ancestors |
| Disk observation could authorize a buffer write | Commit existing sources only through the route that supplied the observed content |
| Same-content file replacement escaped checksum-only publication checks | Capture bytes and native metadata from one handle; compare identity, version, mode, and digest before writing |
| Linux `Ctim` was omitted because metadata matching recognized only `ctime` | Recognize both Unix field spellings; a platform-independent nanosecond regression supplements the native Linux stale-edit test |
| Creating directly at the destination exposed incomplete content; strict overwrite could copy on Windows EXDEV | Stage and fsync before atomic no-overwrite creation; use strict replacement without copy fallback |
| Replacing an inode changed the mutation-lock key | Hold both native identity and stable path locks in a globally sorted order; test a replacement while the first mutation holds its lock |
| Check-then-rename could overwrite a concurrent move destination | Use native no-replace rename on macOS/Linux/Windows; stage cross-device copies and publish with a no-overwrite link |
| Same-session runtime rebuild discarded observations | Clone live observations during controller lifecycle transfer; a real `git --version` call between consecutive edits remains usable |
| `complete_subtask` still applied host proof adjudication | Remove adjudication; keep an optional model report and label it separately from execution facts; plain final answers also finish |
| Retired guards left unused runtime functions and state | Delete unused shell-write classifiers, completion salvage, batch-result rewrites, review dumps, and budget/governor helpers; retain historical data fields; golangci-lint reports zero issues |

Regression owners: `internal/tool/builtin/harness_review_test.go`,
`internal/fileops/observation_review_test.go`, `internal/fileutil/atomicwrite_test.go`,
`internal/agent/harness_review_test.go`, and `internal/agent/complete_subtask_test.go`.
Native Windows reruns cover identity/ACL change detection, publication, moves,
observation isolation, real shell continuation, and optional subtask completion.
Retiring the old proof/read-policy schemas and adding the current delivery
projection change provider tool-prefix bytes once on upgrade. The regenerated
baseline and stable-extension cache guards pass, and subsequent boots are
stable. No live observation is serialized.

These changes do not supply universal external-process CAS, remote ACP CAS, or
exactly-once side effects. The review does not reclassify the initial frontend
and native-shell runs as new UI evidence, and does not claim a latency benchmark.
GitHub merge readiness still requires terminal checks on the pushed head.

## Removed and retained systems

Removed code includes whole-file read debt, frozen batch evidence, source-token authorization, anchor-range shadow evidence, operation prepared/applied/settled state, completion proof gates, the Auto Guard reviewer, recovery confirmation actions, repeated/no-progress rejection, the unused shell proof preflight, and legacy runtime switches that could reactivate those guards.

Goal, Plan approval, ordinary permissions, sandboxing, checkpoints, multi-agent execution, tool-call pairing, and factual execution display remain. Legacy provider/session fields, the `recovery_required` enum, and frontend recognition exist only to read old history; current execution does not write or activate them.

## Guarantee boundary

A window read means that version was observed; it is not whole-file review. Bash, MCP, and external programs do not grant file observations, and their file changes are detected by the next structured mutation. ACP has no conditional atomic-write API, and a local pre-publication check cannot constrain an external writer that ignores the process lock, so neither route promises universal cross-process CAS. Unknown external side effects have no host-level exactly-once guarantee.
