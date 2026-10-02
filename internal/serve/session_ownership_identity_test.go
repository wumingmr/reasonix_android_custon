package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// retireExclusiveForeground releases every writer deterministically (the
// foreground plus any runtime a handoff rotated away from) so Windows temp-dir
// cleanup does not race the idle-retirement TTL.
func retireExclusiveForeground(t *testing.T, ctrl *control.Controller, service *session.Service) {
	t.Helper()
	ctrl.Close()
	if service != nil {
		_ = service.CloseAll(context.Background())
	}
}

// newIdentityLifecycleServe wraps the lifecycle test server with the frame
// tag an exclusive controller's production host would register, so identity
// transitions can assert how live frames get stamped.
func newIdentityLifecycleServe(t *testing.T, ctrl *control.Controller, ref session.SessionRef) *Server {
	t.Helper()
	bc := NewBroadcaster()
	lifecycle := newLifecycleTestServer(t, ctrl, bc, config.ServeConfig{})
	tag := newSessionTagSink(bc)
	tag.SetIdentity("", ref.SessionID)
	lifecycle.RegisterSessionTag(ctrl, tag)
	return lifecycle
}

// openIdentityWriter opens the final-format session's writer through a bare
// persistence handle, standing in for the local runtime that takes over. The
// returned session keeps the writer lock until Close.
func openIdentityWriter(t *testing.T, root string, ref session.SessionRef) *session.Session {
	t.Helper()
	handle, err := session.NewFilesystemPersistence(root).Open(ref.SessionID, session.ReadWrite)
	if err != nil {
		t.Fatalf("open identity writer: %v", err)
	}
	return handle
}

func identityRoot(t *testing.T, service *session.Service, ref session.SessionRef) string {
	t.Helper()
	dir, err := service.SessionDir(t.Context(), ref)
	if err != nil {
		t.Fatalf("resolve identity dir: %v", err)
	}
	return filepath.Dir(dir)
}

func serveBody(t *testing.T, method, url, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 0, 4<<10)
	buf := make([]byte, 4<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	return resp, string(raw)
}

// TestIdentityHandoffReleasesWriterAndOwnershipTracks proves the release half:
// /handoff on a final-format identity rotates the foreground, drops the writer
// lock before the grant is answered, and /ownership reports the external
// holder while the taker keeps the lock.
func TestIdentityHandoffReleasesWriterAndOwnershipTracks(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	route := "session-id:" + current.SessionID

	resp, raw := serveBody(t, http.MethodGet, ts.URL+"/ownership?session="+route, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ownership status = %d body %s", resp.StatusCode, raw)
	}
	var view ownershipView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if view.Holder != "serve" || view.Running {
		t.Fatalf("before handoff view = %+v, want serve holder idle", view)
	}
	defer retireExclusiveForeground(t, ctrl, service)

	resp, raw = serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"`+route+`","targetWriterId":"taker-writer","force":true,"mode":"wait","timeoutMs":2000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff status = %d body %s", resp.StatusCode, raw)
	}
	var grant mirrorGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		t.Fatal(err)
	}
	if grant.MirrorID == "" || grant.ReturnHandoffID == "" || grant.SourceWriterID == "" ||
		grant.TargetWriterID != "taker-writer" || grant.SessionPath != route {
		t.Fatalf("handoff grant = %+v", grant)
	}
	if ref, bound := ctrl.SessionRef(); bound {
		t.Fatalf("foreground still bound to %q after handoff; release must not allocate a replacement identity", ref.SessionID)
	}
	// The frame tag must follow the released foreground: a tag still pointing
	// at the handed-off identity misroutes every subsequent live frame.
	if tag := lifecycle.tagFor(ctrl); tag == nil {
		t.Fatal("frame tag missing after handoff release")
	} else if tag.path != "" || tag.sessionID != "" {
		t.Fatalf("frame tag after handoff = %+v, want no route until the next identity is allocated", tag)
	}
	if session.ProbeWriterHeld(filepath.Join(root, current.SessionID)) {
		t.Fatal("writer lock still held after handoff grant")
	}

	// The taker acquires the released writer; ownership flips to external.
	writer := openIdentityWriter(t, root, current)
	defer writer.Close(t.Context())
	resp, raw = serveBody(t, http.MethodGet, ts.URL+"/ownership?session="+route, "")
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || view.Holder != "external" || !view.TakenOver {
		t.Fatalf("after takeover view = %+v (status %d)", view, resp.StatusCode)
	}
}

// TestIdentityResumeMountsSpectatorWhenWriterHeld proves the attach contract:
// /resume for an identity another runtime writes answers 204 with the
// taken-over header instead of a hard failure, and /history serves the cold
// event log so the spectator can render.
func TestIdentityResumeMountsSpectatorWhenWriterHeld(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	ts := httptest.NewServer(operatorHandler(newLifecycleTestServer(t, ctrl, NewBroadcaster(), config.ServeConfig{})))
	defer ts.Close()
	route := "session-id:" + current.SessionID

	// Detach the foreground from the identity first so the resume path cannot
	// short-circuit onto the already-bound current session.
	if _, err := ctrl.BindFreshSession(t.Context(), "spectator-fresh"); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), current); err != nil {
		t.Fatalf("close rotated-out runtime: %v", err)
	}
	writer := openIdentityWriter(t, root, current)
	defer writer.Close(t.Context())

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/resume", `{"sessionId":"`+current.SessionID+`"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("spectator resume status = %d body %s", resp.StatusCode, raw)
	}
	if resp.Header.Get(sessionTakenOverHeader) == "" {
		t.Fatal("spectator resume omitted the taken-over header")
	}
	if resp.Header.Get(sessionIDHeader) != current.SessionID {
		t.Fatalf("spectator resume session header = %q", resp.Header.Get(sessionIDHeader))
	}

	resp, raw = serveBody(t, http.MethodGet, ts.URL+"/history?session="+route, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spectator history status = %d body %s", resp.StatusCode, raw)
	}

	resp, raw = serveBody(t, http.MethodGet, ts.URL+"/status?session="+route, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spectator status code = %d body %s", resp.StatusCode, raw)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatal(err)
	}
	if taken, _ := status["takenOver"].(bool); !taken {
		t.Fatalf("spectator status missing takenOver: %v", status)
	}
	retireExclusiveForeground(t, ctrl, service)
}

// TestIdentityAdoptRegistersWriterAfterServeRestart pins the canonical
// re-registration path used when a CLI survives the resident serve restarting.
func TestIdentityAdoptRegistersWriterAfterServeRestart(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	lifecycle := newLifecycleTestServer(t, ctrl, NewBroadcaster(), config.ServeConfig{})
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	defer retireExclusiveForeground(t, ctrl, service)
	route := "session-id:" + current.SessionID

	if _, err := ctrl.BindFreshSession(t.Context(), "adopt-fresh"); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), current); err != nil {
		t.Fatalf("close rotated-out runtime: %v", err)
	}
	writer := openIdentityWriter(t, root, current)
	defer writer.Close(t.Context())

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/adopt", `{"sessionPath":"`+route+`","writerId":"surviving-cli"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("identity adopt status = %d body %s", resp.StatusCode, raw)
	}
	var grant mirrorGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		t.Fatal(err)
	}
	if grant.SessionPath != route || grant.MirrorID == "" || grant.TargetWriterID != "surviving-cli" {
		t.Fatalf("identity adopt grant = %+v", grant)
	}
	if mirrored, ok := lifecycle.mirroredEntry(route); !ok || mirrored.targetWriterID != "surviving-cli" {
		t.Fatalf("identity mirror after adopt = %+v, present=%v", mirrored, ok)
	}
}

// TestIdentityReclaimReattachesForeground proves the return half: once the
// local writer drops the lock, /reclaim re-owns the identity for the serve and
// clears the mirror bookkeeping.
func TestIdentityReclaimReattachesForeground(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	route := "session-id:" + current.SessionID

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"`+route+`","targetWriterId":"taker-writer","force":true,"mode":"wait","timeoutMs":2000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff status = %d body %s", resp.StatusCode, raw)
	}
	writer := openIdentityWriter(t, root, current)
	if err := writer.Close(t.Context()); err != nil {
		t.Fatalf("taker release: %v", err)
	}

	resp, raw = serveBody(t, http.MethodPost, ts.URL+"/reclaim", `{"sessionPath":"`+route+`","mode":"wait","timeoutMs":5000}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reclaim status = %d body %s", resp.StatusCode, raw)
	}
	// After the reclaim the identity-selected status must answer ownership
	// explicitly false: clients apply present fields only, so an omitted
	// takenOver would pin the spectator banner forever.
	resp, raw = serveBody(t, http.MethodGet, ts.URL+"/status?session="+route, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-reclaim status code = %d body %s", resp.StatusCode, raw)
	}
	var reclaimed map[string]any
	if err := json.Unmarshal([]byte(raw), &reclaimed); err != nil {
		t.Fatal(err)
	}
	if taken, _ := reclaimed["takenOver"].(bool); taken {
		t.Fatalf("post-reclaim status still reports takenOver: %v", reclaimed)
	}
	if ref, bound := ctrl.SessionRef(); !bound || ref != current {
		t.Fatalf("foreground ref after reclaim = %+v (bound %v), want %+v", ref, bound, current)
	}
	// The frame tag must follow the re-owned identity: a stale tag stamps live
	// frames with another session and identity-routed subscribers drop them.
	if tag := lifecycle.tagFor(ctrl); tag == nil || tag.path != "" || tag.sessionID != current.SessionID {
		t.Fatalf("frame tag after reclaim = %+v, want identity %q", tag, current.SessionID)
	}
	if _, still := lifecycle.mirroredEntry(route); still {
		t.Fatal("mirror entry survived reclaim")
	}
	defer retireExclusiveForeground(t, ctrl, service)
	if !session.ProbeWriterHeld(filepath.Join(root, current.SessionID)) {
		t.Fatal("serve did not re-acquire the writer after reclaim")
	}
}

// TestIdentityHandoffRefusesForeignHolder pins the guard: a handoff for an
// identity this serve does not run is refused instead of granting a session
// the serve cannot release.
func TestIdentityHandoffRefusesForeignHolder(t *testing.T) {
	_, ctrl, service, _ := newExclusiveSessionServe(t)
	ts := httptest.NewServer(operatorHandler(newLifecycleTestServer(t, ctrl, NewBroadcaster(), config.ServeConfig{})))
	defer ts.Close()
	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"session-id:does-not-exist","targetWriterId":"taker","force":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown identity handoff status = %d body %s", resp.StatusCode, raw)
	}
	retireExclusiveForeground(t, ctrl, service)
}

// shortMirrorEndWait shrinks the farewell's release wait so the tests pin the
// protocol (which probe re-owns, how many probes the bound allows) rather than
// wall-clock time.
func shortMirrorEndWait(t *testing.T, polls int) {
	t.Helper()
	wait, poll := mirrorEndReleaseWait, mirrorEndReleasePoll
	mirrorEndReleasePoll = 2 * time.Millisecond
	mirrorEndReleaseWait = time.Duration(polls) * mirrorEndReleasePoll
	t.Cleanup(func() {
		mirrorEndReleaseWait, mirrorEndReleasePoll = wait, poll
		mirrorEndProbeHookForTest = nil
	})
}

// handoffIdentityForTest hands the fixture's identity to "taker-writer" and
// returns the grant.
func handoffIdentityForTest(t *testing.T, url, route string) mirrorGrant {
	t.Helper()
	resp, raw := serveBody(t, http.MethodPost, url+"/handoff", `{"sessionPath":"`+route+`","targetWriterId":"taker-writer","force":true,"mode":"wait","timeoutMs":2000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff status = %d body %s", resp.StatusCode, raw)
	}
	var grant mirrorGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		t.Fatal(err)
	}
	return grant
}

// TestIdentityMirrorEndAcceptsLiveWriter pins the farewell contract: the
// writer's return transaction sends mirror-end before process exit, so the
// writer lock may still be held past the release wait and the serve must accept
// (204) instead of 409ing a call its own protocol ordering requires. The
// outstanding return then belongs to the stale auto-reclaim, and the wait is
// bounded by the configured number of probes.
func TestIdentityMirrorEndAcceptsLiveWriter(t *testing.T) {
	const polls = 3
	shortMirrorEndWait(t, polls)
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	route := "session-id:" + current.SessionID
	grant := handoffIdentityForTest(t, ts.URL, route)
	writer := openIdentityWriter(t, root, current)
	defer writer.Close(t.Context())
	probes := 0
	mirrorEndProbeHookForTest = func(int) { probes++ }

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/mirror-end", `{"sessionPath":"`+route+`","mirrorId":"`+grant.MirrorID+`"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mirror-end with live writer status = %d body %s", resp.StatusCode, raw)
	}
	if ref, bound := ctrl.SessionRef(); bound && ref == current {
		t.Fatal("mirror-end re-owned the identity under a live writer")
	}
	if _, mirrored := lifecycle.mirroredEntry(route); !mirrored {
		t.Fatal("mirror entry dropped although the writer never released; the auto-reclaim has nothing to finish")
	}
	if probes < 1 || probes > polls+2 {
		t.Fatalf("farewell probed %d times, want between 1 and %d (bounded wait)", probes, polls+2)
	}
	retireExclusiveForeground(t, ctrl, service)
}

// The desktop releases its runtime right after sending mirror-end; the
// farewell must re-own the identity on the first probe that sees the lock
// free instead of answering 204 and leaving the remote side read-only until
// the 30 s stale auto-reclaim.
func TestIdentityMirrorEndReclaimsOnceWriterReleases(t *testing.T) {
	shortMirrorEndWait(t, 50)
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	route := "session-id:" + current.SessionID
	grant := handoffIdentityForTest(t, ts.URL, route)
	writer := openIdentityWriter(t, root, current)
	heldProbes := 0
	mirrorEndProbeHookForTest = func(attempt int) {
		heldProbes++
		if attempt == 0 {
			// The writer's teardown lands after the farewell was sent.
			if err := writer.Close(t.Context()); err != nil {
				t.Errorf("release taker writer: %v", err)
			}
		}
	}

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/mirror-end", `{"sessionPath":"`+route+`","mirrorId":"`+grant.MirrorID+`"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mirror-end status = %d body %s", resp.StatusCode, raw)
	}
	if heldProbes != 1 {
		t.Fatalf("farewell saw the lock held on %d probes, want exactly 1: the release must be picked up by the very next probe", heldProbes)
	}
	if ref, bound := ctrl.SessionRef(); !bound || ref != current {
		t.Fatalf("foreground after farewell = %+v (bound %v), want the reclaimed identity %+v", ref, bound, current)
	}
	if _, mirrored := lifecycle.mirroredEntry(route); mirrored {
		t.Fatal("mirror entry survived the farewell reclaim")
	}
	defer retireExclusiveForeground(t, ctrl, service)
	if !session.ProbeWriterHeld(filepath.Join(root, current.SessionID)) {
		t.Fatal("serve did not re-acquire the writer after the farewell")
	}
}

// An identity history read runs outside bindMu. A rotation or handoff landing
// between the read and the response must be reported as a changed runtime, as
// transcriptBoundRead already does, instead of answering the new route with
// the outgoing controller's transcript.
func TestHistoryIdentityRouteDetectsRuntimeChangeDuringRead(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	defer retireExclusiveForeground(t, ctrl, service)
	route := "session-id:" + current.SessionID

	historyIdentityReadHookForTest = func() {
		if _, err := ctrl.BindFreshSession(context.Background(), "rotated-mid-read"); err != nil {
			t.Errorf("rotate during read: %v", err)
		}
	}
	t.Cleanup(func() { historyIdentityReadHookForTest = nil })
	resp, raw := serveBody(t, http.MethodGet, ts.URL+"/history?session="+route, "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, "transcript runtime changed during read") {
		t.Fatalf("history across a mid-read rotation = %d %q, want 409 runtime changed", resp.StatusCode, raw)
	}
	historyIdentityReadHookForTest = nil
	if err := service.Close(t.Context(), current); err != nil {
		t.Fatalf("close rotated-out runtime: %v", err)
	}
	// With the rotation settled the route is served cold from the event log.
	resp, raw = serveBody(t, http.MethodGet, ts.URL+"/history?session="+route, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("history after rotation = %d body %s", resp.StatusCode, raw)
	}
}

// TestIdentityStatusAnswersFreeWriterWithRouteMatch pins the serve-restart
// recovery: a spectator identity whose writer exited and whose mirror entry
// was lost to the restart must get an explicit route-matching status with
// takenOver=false — the foreground snapshot names a different session and a
// pinned tab would discard it, leaving the banner stuck until re-attach.
func TestIdentityStatusAnswersFreeWriterWithRouteMatch(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	route := "session-id:" + current.SessionID

	// Move the foreground off the identity and free its writer, mimicking a
	// post-restart world where nothing holds the session.
	if _, err := ctrl.BindFreshSession(t.Context(), "elsewhere"); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), current); err != nil {
		t.Fatalf("release identity writer: %v", err)
	}

	resp, raw := serveBody(t, http.MethodGet, ts.URL+"/status?session="+route, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body %s", resp.StatusCode, raw)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatal(err)
	}
	if sid, _ := status["sessionId"].(string); sid != current.SessionID {
		t.Fatalf("status sessionId = %v, want the queried identity", status["sessionId"])
	}
	if taken, _ := status["takenOver"].(bool); taken {
		t.Fatalf("free-writer identity status still reports takenOver: %v", status)
	}
	retireExclusiveForeground(t, ctrl, service)
}

// countSessions returns the /sessions row count so identity lifecycle tests
// can pin what a transition persists.
func countSessions(t *testing.T, url string) int {
	t.Helper()
	resp, raw := serveBody(t, http.MethodGet, url+"/sessions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions status = %d body %s", resp.StatusCode, raw)
	}
	var rows []sessionListEntry
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// A handoff releases authority; it is not a conversation. The legacy keeper
// only unbinds, and the identity path must match: no replacement identity is
// created until the user actually starts one, so handoff/reclaim cycles do not
// litter /sessions with empty rows. /new on the released foreground still
// allocates on demand.
func TestIdentityHandoffDoesNotPersistReplacementSession(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	defer retireExclusiveForeground(t, ctrl, service)
	route := "session-id:" + current.SessionID

	before := countSessions(t, ts.URL)
	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"`+route+`","targetWriterId":"taker-writer","force":true,"mode":"wait","timeoutMs":2000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff status = %d body %s", resp.StatusCode, raw)
	}
	if after := countSessions(t, ts.URL); after != before {
		t.Fatalf("/sessions rows after handoff = %d, want %d (handoff persisted a replacement session)", after, before)
	}
	if _, bound := ctrl.SessionRef(); bound {
		t.Fatal("foreground is bound after handoff; nothing should be allocated until the next turn")
	}
	for _, msg := range ctrl.History() {
		if msg.Role != provider.RoleSystem {
			t.Fatalf("released foreground still carries the handed-off conversation: %+v", msg)
		}
	}
	// The released foreground stays usable: /new allocates exactly one fresh
	// identity on demand.
	resp, raw = serveBody(t, http.MethodPost, ts.URL+"/new", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("/new after handoff status = %d body %s", resp.StatusCode, raw)
	}
	fresh, bound := ctrl.SessionRef()
	if !bound || fresh == current {
		t.Fatalf("/new after handoff bound %+v (bound %v), want a fresh identity", fresh, bound)
	}
	if got := countSessions(t, ts.URL); got != before+1 {
		t.Fatalf("/sessions rows after /new = %d, want %d", got, before+1)
	}
}

// A turn admitted between the unlocked quiet probe and the locked release must
// be refused with the legacy path's busy-again error, not raced: otherwise the
// foreground is unbound mid-turn, the close fails as busy, and the caller gets
// a 500 with an orphaned running runtime.
func TestIdentityHandoffRefusesTurnAdmittedAfterQuietProbe(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServeWithOptions(t, func(opts *control.Options) {
		opts.Runner = blockingRunner{}
	})
	root := identityRoot(t, service, current)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	defer retireExclusiveForeground(t, ctrl, service)
	route := "session-id:" + current.SessionID

	handoffIdentityBeforeLockHookForTest = func() {
		// POST /chat was admitted right after the probe saw an idle foreground.
		ctrl.Submit("keep running")
		waitRunning(t, ctrl)
	}
	t.Cleanup(func() { handoffIdentityBeforeLockHookForTest = nil })
	defer func() {
		ctrl.Cancel()
		waitNotRunning(t, ctrl)
	}()

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"`+route+`","targetWriterId":"taker-writer","force":true,"mode":"wait","timeoutMs":2000}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, errHandoffBusyAgain.Error()) {
		t.Fatalf("handoff against a freshly admitted turn = %d %q, want 409 busy-again", resp.StatusCode, raw)
	}
	if ref, bound := ctrl.SessionRef(); !bound || ref != current {
		t.Fatalf("foreground binding after refused handoff = %+v (bound %v), want %+v", ref, bound, current)
	}
	if _, mirrored := lifecycle.mirroredEntry(route); mirrored {
		t.Fatal("refused handoff registered a mirror entry")
	}
	if !session.ProbeWriterHeld(filepath.Join(root, current.SessionID)) {
		t.Fatal("refused handoff dropped the writer lock")
	}
	if !ctrl.Running() {
		t.Fatal("refused handoff interrupted the admitted turn")
	}
}

// The controller can report idle while the runtime is still finalizing the
// turn's terminal commit; closing such a runtime is refused as busy. The
// handoff must report busy-again from the locked re-check and leave the
// binding intact, then succeed once the runtime settles.
func TestIdentityHandoffRefusesFinalizingRuntimeAndRecovers(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	defer retireExclusiveForeground(t, ctrl, service)
	route := "session-id:" + current.SessionID
	runtime, ok := service.Runtime(current)
	if !ok {
		t.Fatal("current runtime is not published")
	}
	generation := ctrl.ExecutionGeneration()
	handoffIdentityBeforeLockHookForTest = func() {
		runtime.NoteExecution(generation, session.RuntimeFinalizing, "terminal_commit")
	}
	t.Cleanup(func() { handoffIdentityBeforeLockHookForTest = nil })

	body := `{"sessionPath":"` + route + `","targetWriterId":"taker-writer","force":true,"mode":"wait","timeoutMs":2000}`
	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/handoff", body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, errHandoffBusyAgain.Error()) {
		t.Fatalf("handoff against a finalizing runtime = %d %q, want 409 busy-again", resp.StatusCode, raw)
	}
	if ref, bound := ctrl.SessionRef(); !bound || ref != current {
		t.Fatalf("foreground binding after refused handoff = %+v (bound %v), want %+v", ref, bound, current)
	}
	if _, mirrored := lifecycle.mirroredEntry(route); mirrored {
		t.Fatal("refused handoff registered a mirror entry")
	}

	handoffIdentityBeforeLockHookForTest = nil
	runtime.NoteExecution(generation, session.RuntimeIdle, "")
	resp, raw = serveBody(t, http.MethodPost, ts.URL+"/handoff", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff after the runtime settled = %d %q, want 200", resp.StatusCode, raw)
	}
	if _, bound := ctrl.SessionRef(); bound {
		t.Fatal("foreground still bound after the successful handoff")
	}
}

// registerDetachedIdentityHolderForTest parks ctrl in the background registry
// the way a detached legacy controller ends up there after upgrading to an
// identity mid-turn: keyed by its former transcript path while SessionRef
// reports the identity. The close-on-idle watcher is omitted because these
// tests assert ownership predicates, not idle retirement; done is pre-closed so
// takeDetached hands the entry over immediately.
func (s *Server) registerDetachedIdentityHolderForTest(key string, ctrl *control.Controller, tag *sessionTagSink) *detachedSession {
	done := make(chan struct{})
	close(done)
	d := &detachedSession{
		path: agent.CanonicalSessionPath(key), ctrl: ctrl, tag: tag,
		force: make(chan struct{}), reattach: make(chan struct{}), done: done,
	}
	s.detachedMu.Lock()
	s.detached[d.path] = d
	s.detachedMu.Unlock()
	return d
}

// A detached background session that runs an identity is still this serve's
// writer. /ownership must say so instead of "other", /adopt must refuse the
// claim instead of registering a mirror over our own writer, /status must not
// pin the tab read-only, and /handoff must release the detached holder the way
// the legacy detached handoff does.
func TestIdentityOwnershipCoversDetachedHolder(t *testing.T) {
	_, ctrl, service, current := newExclusiveSessionServe(t)
	root := identityRoot(t, service, current)
	lifecycle := newIdentityLifecycleServe(t, ctrl, current)
	ts := httptest.NewServer(operatorHandler(lifecycle))
	defer ts.Close()
	defer retireExclusiveForeground(t, ctrl, service)

	background, err := service.Create(t.Context(), session.CreateOptions{SessionID: "background"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), background.Ref()); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
	bg := control.New(control.Options{Runner: blockingRunner{}, Executor: exec, SessionDir: ctrl.SessionDir(), SessionService: service, ExclusiveSession: true})
	if _, err := bg.OpenSession(t.Context(), background.Ref()); err != nil {
		t.Fatal(err)
	}
	tag := newSessionTagSink(lifecycle.bc)
	tag.SetIdentity("", background.Ref().SessionID)
	lifecycle.RegisterSessionTag(bg, tag)
	bg.Submit("keep running")
	waitRunning(t, bg)
	lifecycle.registerDetachedIdentityHolderForTest(filepath.Join(ctrl.SessionDir(), "upgraded.jsonl"), bg, tag)
	closed := false
	defer func() {
		if !closed {
			bg.Cancel()
			waitNotRunning(t, bg)
			bg.Close()
		}
	}()
	route := "session-id:" + background.Ref().SessionID

	resp, raw := serveBody(t, http.MethodGet, ts.URL+"/ownership?session="+route, "")
	var view ownershipView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || view.Holder != "serve" || !view.Running {
		t.Fatalf("ownership of a detached identity = %+v (status %d), want serve holder running", view, resp.StatusCode)
	}
	resp, raw = serveBody(t, http.MethodPost, ts.URL+"/adopt", `{"sessionPath":"`+route+`","writerId":"impostor"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("adopt of our own detached identity = %d body %s, want 409", resp.StatusCode, raw)
	}
	if _, mirrored := lifecycle.mirroredEntry(route); mirrored {
		t.Fatal("adopt registered a mirror over this serve's own detached writer")
	}
	resp, raw = serveBody(t, http.MethodGet, ts.URL+"/status?session="+route, "")
	var status map[string]any
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatal(err)
	}
	if taken, _ := status["takenOver"].(bool); resp.StatusCode != http.StatusOK || taken {
		t.Fatalf("status of a detached identity reports takenOver: %v (status %d)", status, resp.StatusCode)
	}

	// The detached holder is handed off like a legacy detached session: the
	// turn is interrupted, the writer lock drops, the controller is retired.
	resp, raw = serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"`+route+`","targetWriterId":"taker-writer","force":true,"mode":"interrupt","timeoutMs":5000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff of a detached identity = %d body %s", resp.StatusCode, raw)
	}
	closed = true
	if lifecycle.detachedBusy(filepath.Join(ctrl.SessionDir(), "upgraded.jsonl")) {
		t.Fatal("handed-off detached holder is still registered")
	}
	if session.ProbeWriterHeld(filepath.Join(root, background.Ref().SessionID)) {
		t.Fatal("writer lock still held after handing off the detached identity")
	}
	if _, mirrored := lifecycle.mirroredEntry(route); !mirrored {
		t.Fatal("handoff of the detached identity did not register a mirror entry")
	}
	if ref, bound := ctrl.SessionRef(); !bound || ref != current {
		t.Fatalf("foreground changed while handing off a detached identity: %+v (bound %v)", ref, bound)
	}
}
