package serve

// SubmitBusyMessage identifies the legacy HTTP conflict requiring durable enqueue.
const SubmitBusyMessage = "session is busy"

// SubmitSessionChangedMessage identifies a rejected expected-session fence.
const SubmitSessionChangedMessage = "active session changed"
