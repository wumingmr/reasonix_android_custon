// Package cdp drives an external Chrome over the DevTools Protocol and
// presents it as a browser.Executor, so CLI, Serve, and headless sessions get
// the same browser_* tools the desktop shell serves from its own
// WebContentsViews. Nothing here is provider-visible: the tool schemas and
// their descriptions stay in internal/browser, and attaching this executor
// leaves the system-prompt prefix byte-identical.
//
// The contract's refusals are owned here because a raw Chrome has no ledger of
// its own. Every write reserves its model-minted operationId once and forever;
// a snapshot mints an opaque documentToken that the next navigation, page
// replacement, or user take-over retires, and a write carrying a retired token
// is refused as browser.ErrStaleReference rather than replayed against a page
// the model has not seen.
//
// Take-over detection is an approximation of the shell's guest preload. Refs
// and the listeners that watch for human input live in a per-document isolated
// world, so page scripts can neither read nor forge them, but CDP-dispatched
// input is indistinguishable from a hand at the keyboard once it reaches the
// DOM. The executor therefore marks a short window around each dispatch and
// counts trusted events outside it as the user's. A human click landing inside
// that window is missed; the failure mode is a stale snapshot, never a silent
// replay, because writes still carry single-use operationIds.
//
// Only tabs this executor opened are visible: an attached Chrome may hold the
// user's own logged-in tabs, and the agent never enumerates or drives them.
package cdp
