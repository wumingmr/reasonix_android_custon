package cli

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/session"
)

// resumeEntryIndex returns the 1-based /resume index of the row matching the
// predicate, or 0 when absent.
func resumeEntryIndex(entries []resumeEntry, match func(resumeEntry) bool) int {
	for i, entry := range entries {
		if match(entry) {
			return i + 1
		}
	}
	return 0
}

func assertMirrorLeft(t *testing.T, m *chatTUI, fake *fakeCanonicalServe, route string) {
	t.Helper()
	if binding, _, _, _ := m.takeover.snapshot(); binding != nil {
		t.Fatalf("mirror binding still active after leaving %q: %+v", route, binding)
	}
	if ends := fake.mirrorEnds(); len(ends) != 1 || ends[0] != route {
		t.Fatalf("mirror-end requests = %v, want exactly one for %q", ends, route)
	}
	if m.takeover.Returned() {
		t.Fatal("switching away from a mirror left the manager in returned state")
	}
}

// TestResumeCanonicalSessionReturnsActiveCanonicalMirror covers /resume to a
// final-format session while this CLI mirrors another canonical identity for
// the desktop: the mirror must end so the desktop tab regains its writer and
// stops receiving the next session's frames under the old mirror id.
func TestResumeCanonicalSessionReturnsActiveCanonicalMirror(t *testing.T) {
	route := cliCanonicalRoute("held")
	fake := newFakeCanonicalServe(t, route)
	withFakeCanonicalDiscovery(t, fake.base)
	m, ctrl, _, held := newCanonicalTakeoverTUI(t)
	m.runCanonicalTakeoverCommand(route)
	if ref, bound := ctrl.SessionRef(); !bound || ref != held {
		t.Fatalf("takeover did not attach: %+v bound=%v", ref, bound)
	}
	createCanonicalTestSession(t, session.RootForLegacyDir(ctrl.SessionDir()), "third", "third conversation")

	idx := resumeEntryIndex(resumeEntries(ctrl.SessionDir()), func(entry resumeEntry) bool {
		return entry.target.canonical() && entry.target.ref.SessionID == "third"
	})
	if idx == 0 {
		t.Fatal("third canonical session missing from /resume list")
	}
	m.runResumeCommand("/resume " + strconv.Itoa(idx))

	if ref, bound := ctrl.SessionRef(); !bound || ref.SessionID != "third" {
		t.Fatalf("controller after /resume = %+v bound=%v, want third", ref, bound)
	}
	assertMirrorLeft(t, m, fake, route)
}

// TestCanonicalTakeoverOfSecondIdentityReturnsFirstMirror covers /takeover of
// a second final-format identity while the first is still mirrored: Activate
// must not overwrite the live binding without ending the first mirror.
func TestCanonicalTakeoverOfSecondIdentityReturnsFirstMirror(t *testing.T) {
	routeA, routeB := cliCanonicalRoute("held"), cliCanonicalRoute("second")
	fake := newFakeCanonicalServeRoutes(t, routeA, routeB)
	withFakeCanonicalDiscovery(t, fake.base)
	m, ctrl, service, held := newCanonicalTakeoverTUI(t)
	second, err := service.Create(t.Context(), session.CreateOptions{SessionID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), second.Ref()); err != nil {
		t.Fatal(err)
	}
	m.runCanonicalTakeoverCommand(routeA)
	if ref, bound := ctrl.SessionRef(); !bound || ref != held {
		t.Fatalf("first takeover did not attach: %+v bound=%v", ref, bound)
	}

	m.runCanonicalTakeoverCommand(routeB)

	if ref, bound := ctrl.SessionRef(); !bound || ref != second.Ref() {
		t.Fatalf("controller after second takeover = %+v bound=%v, want second", ref, bound)
	}
	binding, _, _, _ := m.takeover.snapshot()
	if binding == nil || binding.path != routeB || !binding.canonical {
		t.Fatalf("mirror binding = %+v, want the second identity %q", binding, routeB)
	}
	if ends := fake.mirrorEnds(); len(ends) != 1 || ends[0] != routeA {
		t.Fatalf("mirror-end requests = %v, want exactly one for the first identity %q", ends, routeA)
	}
	if fake.handoffCount() != 2 {
		t.Fatalf("handoff requests = %d, want one per takeover", fake.handoffCount())
	}
}

// TestResumeLegacySessionReturnsActiveCanonicalMirror covers /resume to a
// legacy transcript while mirroring a canonical identity. The live keeper holds
// no lease after a canonical attach, so the detached source keeper carries no
// reverse reservation to publish; the switch must still end the mirror instead
// of failing with "no detached session lease held" or leaking it.
func TestResumeLegacySessionReturnsActiveCanonicalMirror(t *testing.T) {
	route := cliCanonicalRoute("held")
	fake := newFakeCanonicalServe(t, route)
	withFakeCanonicalDiscovery(t, fake.base)
	m, ctrl, _, held := newCanonicalTakeoverTUI(t)
	m.runCanonicalTakeoverCommand(route)
	if ref, bound := ctrl.SessionRef(); !bound || ref != held {
		t.Fatalf("takeover did not attach: %+v bound=%v", ref, bound)
	}
	legacy := saveQueryTestSession(t, ctrl.SessionDir(), "legacy-target.jsonl", "LEGACY-TARGET-PROMPT")

	idx := resumeEntryIndex(resumeEntries(ctrl.SessionDir()), func(entry resumeEntry) bool {
		return !entry.target.canonical() && entry.target.path == legacy
	})
	if idx == 0 {
		t.Fatal("legacy transcript missing from /resume list")
	}
	m.runResumeCommand("/resume " + strconv.Itoa(idx))

	ref, bound := ctrl.SessionRef()
	if bound || ctrl.SessionPath() != legacy || !ctrl.NativeLegacySession() {
		t.Fatalf("controller after /resume = %+v bound=%v, want the native legacy transcript", ref, bound)
	}
	loaded := false
	for _, msg := range ctrl.History() {
		loaded = loaded || msg.Content == "LEGACY-TARGET-PROMPT"
	}
	if !loaded {
		t.Fatal("history not loaded from the legacy target")
	}
	assertMirrorLeft(t, m, fake, route)
}

// TestResumeCanonicalSessionReturnsLegacyMirrorReservation covers the legacy
// half of the leave step: switching from a mirrored legacy transcript to a
// final-format session publishes the transcript's reverse reservation for the
// serve and ends the mirror before the controller publishes the new identity.
func TestResumeCanonicalSessionReturnsLegacyMirrorReservation(t *testing.T) {
	m, ctrl, service, _ := newCanonicalTakeoverTUI(t)
	legacy := saveQueryTestSession(t, ctrl.SessionDir(), "mirrored-legacy.jsonl", "mirrored legacy")
	if err := m.leases.Rebind(legacy); err != nil {
		t.Fatal(err)
	}
	fake := newFakeCanonicalServe(t, legacy)
	m.takeover.AttachController(ctrl)
	m.takeover.Activate(&cliTakeoverBinding{
		path: legacy, record: cliServeRecord{base: fake.base}, client: &http.Client{},
		grant: cliTakeoverGrant{MirrorID: "mirror-legacy", SourceWriterID: "serve-writer", ReturnHandoffID: "return-legacy"},
	})
	third, err := service.Create(t.Context(), session.CreateOptions{SessionID: "third"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), third.Ref()); err != nil {
		t.Fatal(err)
	}

	if err := m.commitCanonicalSessionSwitch(third.Ref()); err != nil {
		t.Fatalf("commitCanonicalSessionSwitch: %v", err)
	}

	if ref, bound := ctrl.SessionRef(); !bound || ref != third.Ref() {
		t.Fatalf("controller after switch = %+v bound=%v, want third", ref, bound)
	}
	assertMirrorLeft(t, m, fake, legacy)
	info, err := agent.LoadSessionLeaseInfo(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.HandoffTo != "serve-writer" || info.HandoffID != "return-legacy" {
		t.Fatalf("legacy reverse reservation = %+v, want the serve's return handoff", info)
	}
	if held := m.leases.HeldPath(); held != "" {
		t.Fatalf("keeper still holds %q after handing the legacy transcript back", held)
	}
}

// TestResumeCanonicalSessionHeldElsewhereKeepsMirror pins failure atomicity of
// the canonical switch: when the target's writer belongs to another runtime,
// the current mirror, controller binding, and lease stay exactly as they were.
func TestResumeCanonicalSessionHeldElsewhereKeepsMirror(t *testing.T) {
	route := cliCanonicalRoute("held")
	fake := newFakeCanonicalServe(t, route)
	withFakeCanonicalDiscovery(t, fake.base)
	m, ctrl, _, held := newCanonicalTakeoverTUI(t)
	m.runCanonicalTakeoverCommand(route)
	other, err := session.NewService("local", session.NewFilesystemPersistence(session.RootForLegacyDir(ctrl.SessionDir())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })
	busy, err := other.Create(t.Context(), session.CreateOptions{SessionID: "busy"})
	if err != nil {
		t.Fatal(err)
	}

	err = m.commitCanonicalSessionSwitch(session.SessionRef{HostID: busy.Ref().HostID, SessionID: "busy"})

	if !errors.Is(err, session.ErrWriterOwned) {
		t.Fatalf("switch to a held session returned %v, want ErrWriterOwned", err)
	}
	if ref, bound := ctrl.SessionRef(); !bound || ref != held {
		t.Fatalf("controller moved to %+v (bound %v) despite the refused switch", ref, bound)
	}
	binding, _, _, _ := m.takeover.snapshot()
	if binding == nil || binding.path != route {
		t.Fatalf("mirror binding = %+v after a refused switch, want %q still active", binding, route)
	}
	if ends := fake.mirrorEnds(); len(ends) != 0 {
		t.Fatalf("mirror-end requests = %v after a refused switch, want none", ends)
	}
}
