# Responses tool-stream validation

Responses-compatible endpoints may omit individual tool-call completion events.
Reasonix can recover a complete `function_call` from the terminal response's
`output`, using `call_id` to avoid dispatching the same recovered call twice.

A clean completion must not silently lose a tool call:

- An announced call that remains unfinished at `response.completed` or `[DONE]`
  is a protocol error.
- A `function_call` in a completed response must have a call ID, name, string
  arguments, and an absent or `completed` status. A malformed call invalidates
  that sampling attempt, including any other speculative calls in it.
- Malformed unrelated output items remain independently skippable. String
  arguments are still interpreted by the existing tool-validation layer; the
  stream guard does not repair or parse their JSON contents.

The two causes have stable Go identities: `responses.ErrInvalidFunctionCall`
and `responses.ErrUnfinishedFunctionCall`; callers can use `errors.Is` through
wrapping rather than matching message text. This intentionally rejects a
no-argument tool whose terminal item omits `arguments` (the accepted test
control supplies the string `"{}"`)
and a terminal call still marked `in_progress` in `response.completed`.

These protocol errors are not automatic retry signals. The Agent keeps the
failed attempt out of committed model history and does not execute its client
function calls. Already completed work from earlier attempts is unaffected.
The returned error exposes its cause to the user-facing caller, not the model.
Errors contain no tool arguments or provider payloads.

Explicit `response.incomplete` output limits retain their truncation warning
and existing recovery behavior. Connection EOF before a terminal remains a
stream interruption. A text-only final answer, even one describing a future
step, remains a valid final; Reasonix does not infer tool calls from prose.

The regression suite uses local HTTP SSE fixtures through the real Responses
adapter and Agent. It establishes backend behavior, not that a particular
provider emitted those malformed streams. A provider-specific incident still
needs a sanitized event trace before its cause can be attributed.
