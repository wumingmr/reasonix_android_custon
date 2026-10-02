# Session Operations and Runtime Decoupling

This document records the compatibility contract for target-bound local session
operations introduced in September 2026.

## Routing

Persistent session operations resolve an explicit `SessionSelector` in this
order:

1. canonical `SessionRef` (`hostId + sessionId`);
2. validated `sessionPath`;
3. `topicId` as a legacy/topic-only compatibility lookup.

An invalid higher-priority selector is an error and never falls through to a
lower-priority field. There is intentionally no bare `sessionId` selector:
canonical session IDs are qualified by `hostId`, while legacy sessions may not
have a canonical session ID at all. A `topicId` is not a session identity and
is accepted only as the lowest-priority compatibility address; if it resolves
to multiple sessions, the caller must provide a `SessionRef` or `sessionPath`.
Legacy identity is the validated canonical path plus the BranchMeta/file
generation observed by the operation. Runtime bindings are optional
projections; they are not proof that a durable session exists.

Persistent title, history, search, archive, restore, move, delete, fork, and
full-history copy operations can run without selecting or booting the target
conversation. Legacy move first adopts the source through the existing
migration journal; the retained legacy artifacts remain unchanged. Runtime
commands such as send, stop, and live model control still require an open,
ready binding.

Target history APIs cover bounded pages and windows, search, message location,
large-content capabilities, and bounded message-field reads. Every cursor or
content capability remains bound to the resolved durable identity and snapshot;
switching tabs cannot redirect the read.

Fork and full copy intentionally have different semantics. Fork accepts only a
verified completed-turn boundary. Full copy freezes the complete durable source,
publishes a new identity, and uses a caller operation ID plus a durable copy
receipt so a retry cannot create a second child. Neither operation navigates to
the child automatically.

## Title concurrency

Canonical session titles keep the existing `session/title` event payload.
`Projection.TitleSequence` is reconstructed from the sequence of the latest
title event and is used as the title-only CAS token. Normal message appends do
not create title conflicts. Every explicit title save, including a same-value
save, appends a title event.

Legacy BranchMeta adds one optional field:

```json
{"title_revision":"opaque random mutation token"}
```

Old sidecars without the field remain readable. The first title snapshot
initializes a token under the cross-process metadata lock. Manual and AI writes
replace it with a new random token. Transcript and listing projection writers
preserve the latest title and token, while the compatibility whole-record
`SaveBranchMeta` API continues to honor an explicitly supplied title.

An AI title result commits only if its title token, durable target identity, and
lifecycle generation still match. Cancellation is an optimization; CAS remains
the authority when a provider returns after cancellation.

### Mixed-version limit

Writers that implement this protocol detect A→B→A and same-value manual saves.
An older writer that removes `title_revision` causes a new AI result to be
rejected. No implementation can universally detect an old writer that changes
the title A→B→A while deliberately preserving an unknown revision token.

## Auxiliary providers

Cold AI title generation uses a provider-only handle with the target session and
workspace context. Config-backed models do not start extension runtimes.
Extension models start only the owning `plugin/<name>` package and package
dependencies declared through capability requirements. The handle does not
publish tools, MCP servers, UI actions, or prompt contributions into another
conversation and releases its sidecars when the operation ends.

The provider-visible title prompt and the maximum three authored user-message
inputs are unchanged.

## RPC and events

Mutation versions cross the Desktop RPC boundary as strings. Business errors
add `sessionCode`, `targetKey`, `operationId`, and `retryable` to JSON-RPC error
data. Unknown lower-level failures use a generic product message; local paths,
controller identities, lease details, provider bodies, and credentials are not
rendered in toasts.

Committed changes emit target-bound hints:

- `session_metadata_changed`
- `session_archived`
- `session_restored`
- `session_moved`
- `session_deleted`

The existing project-tree notifications remain during compatibility. Durable
storage is authoritative if an incremental event is missed or cannot be
ordered.
