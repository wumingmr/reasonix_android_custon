# Harness-style execution migration

This document defines the current execution behavior introduced from the
`main-v2@986a6bc967` baseline, using the freshness and recovery principles from
the DeepSeek Harness reference `master@c291e7961a`. Reasonix keeps its Go
runtime, permission model, sandbox, Goal, Plan, checkpoints, and multi-agent
support. It does not adopt the Cordis plugin framework.

## Current behavior

- Structured file writers require a host-observed current version. Any
  successful text window is sufficient. Successful writes refresh it.
- Calls take effect in actual execution order. `read → edit → edit → bash` can
  complete in one provider batch. An ordinary failure does not cancel later
  independent calls.
- Large reads are bounded outputs. Pagination is optional and never prevents a
  command, network call, another file, or final completion.
- Started calls without a reliable result are durable `unknown` facts. They
  cause an advisory recovery message, never a tool ban or implicit replay.
- The model judges completion. `complete_step`, `review_report`, read-policy
  receipts, final-readiness proof, operation settlement, Auto Guard review, and
  recovery confirmation cards are retired.
- Consecutive identical calls receive parameter-free reminders at counts 3, 5,
  and 8. The calls still run.

Three limits are intentional. A window read is not a claim of whole-file
review. Bash and MCP calls are outside the file-observation policy. An unknown
external side effect has no host-enforced duplicate-execution guarantee.

File-operation failures use stable codes: `FS_NOT_OBSERVED` asks for a current
window read, `FS_STALE_VERSION` asks for a reread after an external change,
`FS_NOT_FOUND` reports an absent source, and `FS_ALREADY_EXISTS` reports a
protected create/move collision. These are local call failures and never create
a global pending operation.

## Compatibility

| Old data or API | Current handling |
| --- | --- |
| `source_token` argument | Parsed by compatible JSON decoders, ignored for authorization, absent from public schemas. |
| ReadCompletion, ReadPause, proof receipts | Preserved when decoding and saving historical sessions; never activate a gate. |
| `complete_step`, `review_report`, read receipt calls | Return one ordinary `tool_retired` result. |
| Auto Guard sidecar and configuration | Readable as history; not attached to the executor and not written by current config saves. |
| Tool recovery query | Returns immutable facts with `retired=true`. |
| Tool recovery action | Returns stable `tool_recovery_retired`; never replays an operation. |
| Unknown extension fields | Retained by the existing session compatibility container. |

The public tool set and descriptions changed. The first request after upgrade
therefore has one expected provider-prefix cache miss. Tool order and the new
prefix remain stable afterward. Desktop retains its service/shell contract
digest check; mixed incompatible shell and service builds are unsupported.

## Issue-sequence validation

| Reports | Previous trigger | Current assertion | Coverage |
| --- | --- | --- | --- |
| #9994, #9995, #10067, #10103 | An edit caused later bash or edits to remain evidence-blocked. | A successful write refreshes observation; bash is independent; three read/edit/bash/edit cycles complete. | Go integration and file-tool tests; Desktop displays normal cards. |
| #10053 | A bounded or pure read created unavoidable full-read debt. | One large-file window may be followed by bash and a final answer. | Agent harness test and bounded-read test. |
| #10085 | Reads in the same provider batch could not authorize its writer. | Results update observation synchronously in provider execution order. | Same-batch `read → edit → edit → bash` test. |
| #10153 | `outcome_unknown` installed a cross-session network/tool barrier. | Unknown is recorded once; subsequent diagnostic and identical calls use ordinary policy. | Recovery integration and retired endpoint tests. |

Local disk tests also cover unseen overwrite rejection, stale recovery after a
reread, same-size external replacement with restored mtime, permission changes,
aliases, competing writers, and no-overwrite creation. Encoding, buffer,
notebook, symbol, move, and multi-edit behavior remains owned by their existing
tool suites. Windows native identity has a Windows-only test and must be run on
a Windows host; cross-compilation alone is not acceptance evidence.

## Removed runtime systems

The runtime no longer contains full-read debt and continuation gates,
source-token authorization, operation prepared/applied/settled state changes,
proof-driven todo advancement, final-readiness recovery, repeated-call
rejection, evidence-gain progress/storm intervention, or an attachable Auto
Guard reviewer. Historical provider/session types remain only where decoding or
read-only display requires them.

The publication sequence is: resolve target, apply permission/sandbox/range
policy, lock, obtain current source and compare the observation, compute the
change, compare again, publish through the same route, update observation, and
return the actual result. ACP lacks conditional atomic write, and local checks
cannot stop an arbitrary external writer after the final check; neither route
is described as a universal CAS.
