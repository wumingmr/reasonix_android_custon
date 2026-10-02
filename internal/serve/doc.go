// Package serve exposes a control.Controller over HTTP: the typed event stream
// as Server-Sent Events, and the commands as small JSON POST endpoints. It is a
// second frontend alongside the chat TUI — proof that the controller is
// transport-agnostic, and the basis for a browser/desktop client. A server has
// one foreground session and may finish switched-away sessions in background.
//
// # Session ownership
//
// A session has exactly one writer at a time — the runtime holding its lease.
// When the machine hosting Serve is also where the user now sits (the remote
// desktop came "home"), the local Reasonix window can take a session over:
// Serve releases the lease and the local window acquires it. Serve keeps no
// controller authority for a mirrored session, but stays the rendezvous: the
// remote tab's SSE stream keeps rendering because the local writer pushes its
// frames through POST /external/frames, and the remote side drops to read-only
// until it reclaims speaking rights via POST /reclaim.
//
// Every transition is cooperative — nothing ever steals the OS-level lease
// file lock. Handoff releases what Serve holds (session_handoff.go); reclaim
// waits for the local writer to release what it holds (session_reclaim.go),
// and a dead writer releases implicitly when the kernel drops its file lock.
package serve
