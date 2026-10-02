package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// appendForkTurn commits one turn into a live exclusive session. A completed
// turn is the only forkable boundary; the open variant exists to be refused.
func appendForkTurn(t *testing.T, service *session.Service, ref session.SessionRef, turnID string, open bool) {
	t.Helper()
	runtime, ok := service.Runtime(ref)
	if !ok {
		t.Fatal("current session has no runtime")
	}
	reply, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "m1", Role: provider.RoleUser, Content: "one"}})
	events := []session.Event{
		{Kind: "turn/start"}, {Kind: "message/complete", Payload: reply},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}
	if open {
		events = []session.Event{{Kind: "turn/start"}}
	}
	if _, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: turnID, TurnID: turnID, Events: events}); err != nil {
		t.Fatal(err)
	}
}

func forkResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func forkCreateJSON(ref session.SessionRef, target session.ForkTarget, operationID string) string {
	boundary := target.BoundarySequence
	if boundary == 0 {
		boundary = 1
	}
	body, _ := json.Marshal(map[string]any{"sourceSessionId": ref.SessionID, "turnId": target.TurnID,
		"boundarySequence": boundary, "operationId": operationID})
	return string(body)
}

func postForkJSON(t *testing.T, baseURL string, ref session.SessionRef, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/fork-session", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(expectedSessionIDHeader, ref.SessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getForkTargets(t *testing.T, baseURL string, refs ...session.SessionRef) forkTargetsResponse {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/fork-targets", nil)
	if len(refs) > 0 {
		req.Header.Set(expectedSessionIDHeader, refs[0].SessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fork targets status = %d: %s", resp.StatusCode, forkResponseBody(t, resp))
	}
	var payload forkTargetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestForkTargetsRouteEncodesEmptyTargetSetAsArray(t *testing.T) {
	srv, _, _, ref := newExclusiveSessionServe(t)
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/fork-targets", nil)
	req.Header.Set(expectedSessionIDHeader, ref.SessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fork targets status = %d", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(forkResponseBody(t, resp)), &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw["targets"]); got != "[]" {
		t.Fatalf("empty targets encode as %s, want []", got)
	}
	// A null array decodes into a nil slice, so the decoded value is the check
	// that a client can always map or measure the list.
	payload := getForkTargets(t, server.URL, ref)
	if payload.Targets == nil || len(payload.Targets) != 0 || payload.Verifiable {
		t.Fatalf("empty target set = %+v", payload)
	}
}

func TestForkRoutesRequireAndEnforceSessionFence(t *testing.T) {
	srv, _, service, ref := newExclusiveSessionServe(t)
	appendForkTurn(t, service, ref, "turn-1", false)
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()

	get, _ := http.NewRequest(http.MethodGet, server.URL+"/fork-targets", nil)
	missing, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	var refusal forkErrorResponse
	if err := json.NewDecoder(missing.Body).Decode(&refusal); err != nil || missing.StatusCode != http.StatusBadRequest ||
		refusal.Code != "fork_unavailable" || refusal.Reason != session.ForkStaleSource {
		t.Fatalf("missing fence status=%d refusal=%+v err=%v", missing.StatusCode, refusal, err)
	}

	target := getForkTargets(t, server.URL, ref).Targets[0]
	staleRef := ref
	staleRef.SessionID = "another-session"
	stale := postForkJSON(t, server.URL, staleRef, forkCreateJSON(ref, target, "stale-fence"))
	defer stale.Body.Close()
	refusal = forkErrorResponse{}
	if err := json.NewDecoder(stale.Body).Decode(&refusal); err != nil || stale.StatusCode != http.StatusConflict ||
		refusal.Code != "fork_unavailable" || refusal.Reason != session.ForkStaleSource {
		t.Fatalf("stale fence status=%d refusal=%+v err=%v", stale.StatusCode, refusal, err)
	}
}

func TestForkRoutesReportLegacySessionWithoutTurnRecords(t *testing.T) {
	srv := newBrokerTestServer(t, boot.Options{})
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()

	payload := getForkTargets(t, server.URL)
	if payload.Targets == nil || len(payload.Targets) != 0 || payload.Verifiable {
		t.Fatalf("legacy target set = %+v", payload)
	}
	resp := postRuntimeJSON(t, server.URL+"/fork-session", `{"sourceSessionId":"legacy","turnId":"turn-1","boundarySequence":1,"operationId":"legacy-op"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("legacy fork session status = %d: %s", resp.StatusCode, forkResponseBody(t, resp))
	}
}

func TestForkTargetsRouteListsCompletedAndOpenTurns(t *testing.T) {
	srv, _, service, ref := newExclusiveSessionServe(t)
	appendForkTurn(t, service, ref, "turn-1", false)
	appendForkTurn(t, service, ref, "turn-2", true)
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()

	payload := getForkTargets(t, server.URL, ref)
	if !payload.Verifiable || len(payload.Targets) != 2 {
		t.Fatalf("target set = %+v", payload)
	}
	completed, open := payload.Targets[0], payload.Targets[1]
	if completed.TurnID != "turn-1" || completed.TurnNumber != 1 || !completed.Available || completed.Reason != "" {
		t.Fatalf("completed target = %+v", completed)
	}
	if open.TurnID != "turn-2" || open.TurnNumber != 2 || open.Available || open.Reason != session.ForkTurnOpen {
		t.Fatalf("open target = %+v", open)
	}
}

func TestForkSessionRouteRejectsMissingOrEmptyTurnID(t *testing.T) {
	srv, _, _, ref := newExclusiveSessionServe(t)
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()

	for _, body := range []string{`{}`, `{"turnId":""}`, `{"turnId":"   "}`, `{`, `{"turnId":"t1","name":`} {
		resp := postForkJSON(t, server.URL, ref, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q status = %d: %s", body, resp.StatusCode, forkResponseBody(t, resp))
		}
		resp.Body.Close()
	}
}

func TestForkSessionRouteCreatesChildWithoutSwitchingParent(t *testing.T) {
	srv, ctrl, service, ref := newExclusiveSessionServe(t)
	appendForkTurn(t, service, ref, "turn-1", false)
	parentPath := ctrl.SessionPath()
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()
	target := getForkTargets(t, server.URL, ref).Targets[0]

	resp := postForkJSON(t, server.URL, ref, forkCreateJSON(ref, target, "create-op"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fork session status = %d: %s", resp.StatusCode, forkResponseBody(t, resp))
	}
	var created forkSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.SessionID == "" || created.SessionID == ref.SessionID || created.TurnID != "turn-1" || created.TurnNumber != 1 {
		t.Fatalf("created child = %+v", created)
	}
	// The response never moves the serve session: the parent keeps its identity
	// and its path, and the X-Reasonix-Session-ID header still names the parent.
	if after, ok := ctrl.SessionRef(); !ok || after != ref || ctrl.SessionPath() != parentPath {
		t.Fatalf("parent identity = %+v (ok=%v path=%q)", after, ok, ctrl.SessionPath())
	}
	if got := resp.Header.Get(sessionIDHeader); got != "" {
		t.Fatalf("fork response session id header = %q, want none", got)
	}
	child, err := service.Query().Snapshot(t.Context(), session.SessionRef{HostID: created.HostID, SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	inherited := false
	for _, message := range child.Projection.Messages {
		if message.ID == "m1" {
			inherited = true
		}
	}
	if len(child.Projection.Turns) != 1 || !inherited {
		t.Fatalf("child projection = %+v", child.Projection)
	}

	retry := postForkJSON(t, server.URL, ref, forkCreateJSON(ref, target, "create-op"))
	defer retry.Body.Close()
	if retry.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d: %s", retry.StatusCode, forkResponseBody(t, retry))
	}
	var again forkSessionResponse
	if err := json.NewDecoder(retry.Body).Decode(&again); err != nil {
		t.Fatal(err)
	}
	if again.SessionID != created.SessionID {
		t.Fatalf("retry created %q, want the existing child %q", again.SessionID, created.SessionID)
	}
}

func TestForkSessionRouteReportsUnavailableTurnReason(t *testing.T) {
	srv, _, service, ref := newExclusiveSessionServe(t)
	appendForkTurn(t, service, ref, "turn-1", false)
	appendForkTurn(t, service, ref, "turn-2", true)
	server := httptest.NewServer(operatorHandler(srv))
	defer server.Close()
	targets := getForkTargets(t, server.URL, ref)

	resp := postForkJSON(t, server.URL, ref, forkCreateJSON(ref, targets.Targets[1], "open-op"))
	defer resp.Body.Close()
	body := forkResponseBody(t, resp)
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("refused fork status = %d, want a 4xx: %s", resp.StatusCode, body)
	}
	var refusal forkErrorResponse
	if err := json.Unmarshal([]byte(body), &refusal); err != nil || refusal.Code != "fork_unavailable" || refusal.Reason != session.ForkTurnOpen {
		t.Fatalf("refusal %q does not carry structured reason %q: %+v err=%v", body, session.ForkTurnOpen, refusal, err)
	}
}

func TestServerAdvertisesSessionForkTargetsOnlyForExclusiveSessions(t *testing.T) {
	service, err := session.NewService("serve", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown session service: %v", err)
		}
	})
	ctrl := control.New(control.Options{SessionService: service, ExclusiveSession: true})
	defer ctrl.Close()
	srv := New(ctrl, NewBroadcaster(), config.ServeConfig{AuthMode: "token", Token: "secret"})
	if !slices.Contains(srv.capabilities(), capabilityForkTargetsV1) {
		t.Fatalf("exclusive capabilities = %v", srv.capabilities())
	}
	if legacy := newBrokerTestServer(t, boot.Options{}); slices.Contains(legacy.capabilities(), capabilityForkTargetsV1) {
		t.Fatalf("legacy capabilities = %v", legacy.capabilities())
	}

	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	resp, err := http.Post(server.URL+"/auth/token", "application/json", strings.NewReader(`{"token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("handshake status = %d", resp.StatusCode)
	}
	advertised := strings.Split(resp.Header.Get(capabilitiesHeader), ",")
	if !slices.Contains(advertised, capabilityForkTargetsV1) {
		t.Fatalf("capabilities header = %v", advertised)
	}
}
