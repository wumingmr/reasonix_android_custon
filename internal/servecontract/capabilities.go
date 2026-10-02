// Package servecontract contains capability tokens shared by the Serve host
// and every remote client. Values live here so protocol negotiation cannot
// drift through independently maintained string literals.
package servecontract

// HistoryOutlineV1 exposes paged durable turn summaries independently of the live tail.
const HistoryOutlineV1 = "history-outline-v1"

const GoalLifecycleV2 = "goal-lifecycle-v2"

const TranscriptV2 = "transcript-v2"

const SubmissionIdentityV1 = "submission-identity-v1"

// TranscriptOutlineV1 announces the read-only turn-outline endpoint. A client
// that does not see this token keeps the loaded-turn rail instead of probing
// the route, so an older Serve never has to answer 404 to advertise itself.
const TranscriptOutlineV1 = "transcript-outline-v1"

// SessionForkTargetsV1 advertises a server that can list a session's completed
// turns (with the reason an unavailable one is refused) and create an
// independent child session from one of them without switching the parent.
const SessionForkTargetsV1 = "session-fork-targets-v1"

// SessionExportV1 provides identity-bound complete display snapshots.
const SessionExportV1 = "session-export-v1"

// InboxMutationsV1 provides session-fenced full-body edits and anchored moves.
const InboxMutationsV1 = "inbox-mutations-v1"
