# File observation lifecycle

Reasonix protects structured file mutations with one live, host-owned observation of the current source version. It does not measure how much of a file the model read and it does not use receipts, `source_token`, or a whole-file reading debt.

## State and lifetime

Each live agent owns an in-memory table keyed by execution route and normalized file identity. A target is `unseen`, `absent`, or `present(version)`. The table survives ordinary user turns, compaction, and in-session model changes. It is cleared when a session is restored or replaced, forked, rewound, or moved to a different file execution environment. Child agents have independent tables.

Any successful text window records `present(version)`. A confirmed missing target records `absent`; permission failures, timeouts, and other read errors do not. A successful structured mutation records the committed version directly, so consecutive edits do not need another read or provider round. A commit whose new version cannot be established clears that observation while preserving the fact that the commit happened.

## Mutation contract

An edit of an unseen target returns `FS_NOT_OBSERVED`. Creating an unseen or confirmed-absent target uses a no-overwrite atomic create. Replacing a present target checks the observed version under the target mutation lock, computes the change from that source, checks again before publication, publishes atomically where the route supports it, then records the new version. A mismatch returns `FS_STALE_VERSION`; rereading any useful window restores eligibility.

Locks are shared by file tools in one host process. Multiple targets are locked in stable order. Symlinks, hard links, and case aliases use normalized path and native identity information. Disk versions include host file identity, size, nanosecond timestamps, change metadata, mode, and other available native fields. A bounded read uses one file handle and compares metadata before and after the window; it does not scan the rest of a large file to compute a hash.

Disk and unsaved-buffer versions are separate. Buffer commits never silently fall back to disk. ACP routes retain source digest rechecks and same-route submission because their current protocol has no atomic conditional write. Local publication checks cannot provide a universal filesystem CAS against an external process that ignores Reasonix locks after the final check.

`read_file`, `write_file`, `edit_file`, `multi_edit`, `notebook_edit`, `delete_symbol`, `delete_range`, and `move_file` use this contract. A `multi_edit` computes every edit against one original version and advances the observation once. A successful move marks the source absent and observes the destination. Bash, MCP, and external programs neither grant nor consume file observation; their changes are detected by the next structured mutation.

## Reading and completion

`read_file` always returns a bounded window with output and resource limits. `intent` and old cursors remain accepted for compatibility, but `intent=full` does not create a continuation requirement. Repeated reads return their actual window. A window read proves only that the model observed that version; it does not mean the model reviewed the whole file. Incomplete pagination never blocks bash, MCP, another file, or the model's final answer.

Old read envelopes, receipts, ReadPause, ReadCompletion, and `source_token` fields remain readable for historical display. They never reconstruct a live observation or reinstall an execution gate.
