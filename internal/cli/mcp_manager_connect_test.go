package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/plugin"
)

// gatedMCPServer answers MCP over HTTP but holds initialize until release is
// closed, and reports on started when the handshake reaches it.
func gatedMCPServer(t *testing.T) (url string, started <-chan struct{}, release func()) {
	t.Helper()
	gate := make(chan struct{})
	reached := make(chan struct{})
	var reachedOnce, releaseOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := any(map[string]any{})
		switch req.Method {
		case "initialize":
			reachedOnce.Do(func() { close(reached) })
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			result = map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "docs", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "search", "description": "Search.", "inputSchema": map[string]any{"type": "object"}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	release = func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(srv.Close)
	t.Cleanup(release)
	return srv.URL, reached, release
}

func writeMCPWorkspace(t *testing.T, root, url string) {
	t.Helper()
	raw := minimalTestModelTOML
	if url != "" {
		raw += `
[[plugins]]
name = "docs"
type = "http"
url = "` + url + `"
auto_start = false
startup_timeout_seconds = 10
`
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newMCPConnectModel(t *testing.T, url string) (chatTUI, control.SessionAPI, string) {
	t.Helper()
	isolateCLIConfigHome(t)
	root := t.TempDir()
	writeMCPWorkspace(t, root, url)
	ctrl, err := setupProfile(context.Background(), "", 0, false, event.Discard, root)
	if err != nil {
		t.Fatalf("setupProfile: %v", err)
	}
	t.Cleanup(func() { ctrl.Close() })
	model := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	model.mcp = &mcpManager{stage: mcpStageDetail, name: "docs"}
	return model, ctrl, root
}

func runConnect(t *testing.T, cmd func() any) <-chan mcpConnectDoneMsg {
	t.Helper()
	out := make(chan mcpConnectDoneMsg, 1)
	go func() {
		done, _ := cmd().(mcpConnectDoneMsg)
		out <- done
	}()
	return out
}

func TestMCPManagerRetryDoesNotBlockTheUILoop(t *testing.T) {
	url, started, release := gatedMCPServer(t)
	model, ctrl, _ := newMCPConnectModel(t, url)
	view := mcpServerView{Name: "docs", Status: "failed"}

	begin := time.Now()
	next, cmd := model.applyMCPAction(view, mcpActionConnect)
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("Retry held the UI loop for %v while the server was unresponsive", elapsed)
	}
	if cmd == nil {
		t.Fatal("Retry returned no command to run the connect off the UI loop")
	}
	model = next.(chatTUI)
	result := runConnect(t, func() any { return cmd() })
	<-started

	model.mcp = nil
	model.openMCPManager("docs")
	if _, again := model.applyMCPAction(view, mcpActionConnect); again != nil {
		t.Fatal("reopening /mcp let a second Retry start another connect while one was in flight")
	}
	next, _ = model.applyMCPAction(view, mcpActionDisable)
	model = next.(chatTUI)
	if model.mcpDisabled["docs"] {
		t.Fatal("Disable ran while the connect was in flight")
	}
	next, _ = model.applyMCPAction(view, mcpActionRemove)
	model = next.(chatTUI)
	if model.mcp.stage == mcpStageConfirmRemove {
		t.Fatal("Remove opened its confirmation while the connect was in flight")
	}
	model.mcp.stage = mcpStageList

	release()
	done := <-result
	if done.server != "docs" || done.err != nil {
		t.Fatalf("connect result = %+v, want success for docs", done)
	}
	next, _ = model.Update(done)
	model = next.(chatTUI)
	if model.mcpConnecting["docs"] {
		t.Fatal("connect result did not clear the in-flight marker")
	}
	if model.mcp.stage != mcpStageList {
		t.Fatalf("connect result moved the manager to stage %v", model.mcp.stage)
	}
	if !mcpConnected(ctrl, "docs") {
		t.Fatal("server is not connected after a successful Retry")
	}
	next, _ = model.applyMCPAction(mcpServerView{Name: "docs", Status: "connected"}, mcpActionDisable)
	if model = next.(chatTUI); !model.mcpDisabled["docs"] || mcpConnected(ctrl, "docs") {
		t.Fatal("Disable after the connect finished did not take effect")
	}
}

func TestMCPConnectFinishingAfterRemovalIsDropped(t *testing.T) {
	url, started, release := gatedMCPServer(t)
	model, ctrl, root := newMCPConnectModel(t, url)

	next, cmd := model.applyMCPAction(mcpServerView{Name: "docs", Status: "failed"}, mcpActionConnect)
	model = next.(chatTUI)
	result := runConnect(t, func() any { return cmd() })
	<-started
	writeMCPWorkspace(t, root, "")
	release()

	done := <-result
	if done.err != nil {
		t.Fatalf("connect: %v", done.err)
	}
	next, _ = model.Update(done)
	model = next.(chatTUI)
	if mcpConnected(ctrl, "docs") {
		t.Fatal("a server removed from config while connecting stayed live")
	}
	if model.mcpConnecting["docs"] {
		t.Fatal("in-flight marker survived the dropped connect")
	}
}

func TestApplyMCPModeRecordsPluginConnectFailure(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("PATH", "")
	cfg := config.Default()
	cfg.Plugins = []config.PluginEntry{{Name: "broken", Command: "definitely-missing-reasonix-mcp", Tier: "background"}}
	if err := cfg.SaveTo("reasonix.toml"); err != nil {
		t.Fatalf("save config: %v", err)
	}

	m := newTestChatTUI()
	m.ctrl = newOwnedTestController(t, control.Options{Host: plugin.NewHost()})
	defer m.ctrl.Close()
	m.host = m.ctrl.Host()
	m.mcp = &mcpManager{
		stage: mcpStageMode,
		name:  "broken",
		snapshot: mcpSnapshot{configPath: "reasonix.toml", servers: []mcpServerView{{
			Name: "broken", Transport: "stdio", Status: "deferred", Configured: true, Tier: "background",
		}}},
	}

	next, cmd := m.applyMCPMode("background")
	if cmd == nil {
		t.Fatal("mode change started no connect")
	}
	m = next.(chatTUI)
	m.handleMCPConnectDone(cmd().(mcpConnectDoneMsg))

	failures := m.ctrl.Host().Failures()
	if len(failures) != 1 || failures[0].Name != "broken" {
		t.Fatalf("Host.Failures() = %+v, want broken failure", failures)
	}
	v, ok := m.mcp.selectedServer()
	if !ok {
		t.Fatal("selected server missing after refresh")
	}
	if v.Status != "failed" {
		t.Fatalf("server status = %q, want failed; server = %+v", v.Status, v)
	}
}
