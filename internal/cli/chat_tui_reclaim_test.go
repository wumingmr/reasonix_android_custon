package cli

import (
	"net/http"
	"testing"
	"time"

	"reasonix/internal/event"
)

// TestLegacyMirrorReclaimRoutesTakeoverByPath proves a reclaim of a legacy
// path takeover is remembered as that path, not as the identity the exclusive
// engine imported it under, so "/takeover takes it back" goes through the
// serve's path handoff instead of posting session-id:<branch id> to a serve
// that holds a path lease. It also pins that the reclaim drops a stale picker.
func TestLegacyMirrorReclaimRoutesTakeoverByPath(t *testing.T) {
	m, ctrl, _, _ := newCanonicalTakeoverTUI(t)
	legacy := saveQueryTestSession(t, ctrl.SessionDir(), "mirrored-legacy.jsonl", "mirrored legacy")
	if err := m.leases.Rebind(legacy); err != nil {
		t.Fatal(err)
	}
	fake := newFakeCanonicalServe(t, legacy)
	withFakeCanonicalDiscovery(t, fake.base)
	m.takeover.AttachController(ctrl)
	m.takeover.Activate(&cliTakeoverBinding{
		path: legacy, record: cliServeRecord{base: fake.base}, client: &http.Client{},
		grant: cliTakeoverGrant{MirrorID: "mirror-legacy", SourceWriterID: "serve-writer", ReturnHandoffID: "return-legacy"},
	})
	yielded := make(chan struct{}, 1)
	m.takeover.SetYieldCallback(func() { yielded <- struct{}{} })
	fake.reclaim.Store(true)
	m.takeover.Emit(event.Event{Kind: event.Text, Text: "answer"})
	select {
	case <-yielded:
	case <-time.After(5 * time.Second):
		t.Fatal("reclaim did not yield the legacy mirror")
	}

	m.resumePick = &resumePicker{}
	next, cmd := m.Update(tuiSessionReclaimedMsg{})
	if cmd != nil {
		t.Fatalf("reclaim returned %T, want no command", cmd)
	}
	updated := next.(chatTUI)
	m = &updated
	if m.reclaimedTarget.canonical() || m.reclaimedTarget.path != legacy {
		t.Fatalf("reclaimed target = %+v, want the legacy path %q", m.reclaimedTarget, legacy)
	}
	if m.resumePick != nil {
		t.Fatal("reclaim left a stale resume picker overlay open")
	}

	// The identity route would reach the fake serve through the discovery
	// override; the path route must not post an identity handoff there.
	handoffs := fake.handoffCount()
	fake.reclaim.Store(false)
	m.runTakeoverCommand("/takeover")
	if got := fake.handoffCount(); got != handoffs {
		t.Fatalf("/takeover after a legacy reclaim posted %d identity handoff(s) for the imported id", got-handoffs)
	}
}

// TestTakeoverOfFreeLegacySessionAfterReclaimSkipsSnapshot covers /takeover
// of a legacy row while detached: the reclaimed runtime is already released,
// so the switch must not try to snapshot it, and a target nobody holds is
// resumed with the reclaim state cleared.
func TestTakeoverOfFreeLegacySessionAfterReclaimSkipsSnapshot(t *testing.T) {
	m, ctrl, _, _ := newCanonicalTakeoverTUI(t)
	legacy := saveQueryTestSession(t, ctrl.SessionDir(), "free-legacy.jsonl", "FREE-LEGACY-PROMPT")
	m.releaseCanonicalRuntime(ctrl)
	if _, bound := ctrl.SessionRef(); bound {
		t.Fatal("test setup: runtime still bound after release")
	}
	m.sessionReclaimed = true
	m.reclaimedTarget = cliResumeTarget{path: legacy}

	m.runTakeoverCommand("/takeover")

	if m.sessionReclaimed {
		t.Fatal("/takeover of the remembered legacy session left the TUI in reclaimed mode")
	}
	if ctrl.SessionPath() != legacy || !ctrl.NativeLegacySession() {
		t.Fatal("controller did not attach to the native legacy session")
	}
	loaded := false
	for _, msg := range ctrl.History() {
		loaded = loaded || msg.Content == "FREE-LEGACY-PROMPT"
	}
	if !loaded {
		t.Fatal("history not loaded from the legacy target")
	}
}
