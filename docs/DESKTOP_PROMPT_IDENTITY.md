# Desktop prompt identity

Desktop decision cards are owned by the controller that created them. Every
new prompt request carries the owning `hostId + sessionId`, session generation,
prompt id, turn id, runtime epoch and request kind. The kind identifies the
decision surface: `ask`, `approval`, `plan`, `recovery`, or `mcp`. The frontend
also derives an immutable request-instance key from this identity (plus approval
generation and permission revision where applicable) for component state and
Ask drafts.

The frontend submits the complete target through `ResolvePromptForSession`.
Desktop validates the tab's host, session and generation while fixing the
controller under the App lock, releases that lock, and then resolves the exact
prompt. The controller checks runtime epoch, active turn, prompt owner and
pending state before persisting `PromptAnswered` and waking the original waiter.
A stale binding, turn or runtime is rejected without routing the answer to a
replacement controller. Failed persistence restores only that request to its
pending state so the user can retry.

Prompt requests and lifecycle events expose `promptId`, `promptKind`, and
`turnId`. Desktop event envelopes carry the tab runtime epoch. Events without a
turn identity are marked `promptLegacy` and are accepted only by compatibility
paths.

Older host methods such as `AnswerQuestionForTab`, `ApproveTab`, and
`ResolveRecoveryTab` remain available for older clients. New frontend code uses
`ResolvePromptForSession` and does not silently downgrade to an unfenced method.
When a stale response is received, the card is removed from the active decision
surface and one tab-scoped prompt replay is requested; only a new pending
identity can re-arm a card.

Extension forms use the same rule with `SubmitExtensionFormExact`. The host
assigns a `formInstanceId` to every publication, pins the validated sidecar
client before releasing the hub lock, and applies completion only to that form
instance. Re-publishing the same plugin surface creates a different instance,
so an older completion cannot close or revive the replacement.

Remote Serve advertises `interaction-target-v1` and
`extension-form-instance-v1` independently. Without the relevant capability,
the current client keeps the session readable but disables that card or form
and asks the user to upgrade; it never sends an unsafe legacy mutation. These
identities are transport and UI ownership fields and require no persisted
session migration.
