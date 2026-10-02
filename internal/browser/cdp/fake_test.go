package cdp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeBrowser answers the DevTools subset this package speaks. Tests script it
// by replacing entries in handlers or by flipping the page-script state.
type fakeBrowser struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	ws       *websocket.Conn
	calls    []string
	args     map[string][]json.RawMessage
	handlers map[string]func(params json.RawMessage) (any, *protocolError)
	userSeq  int64
	tree     string
	refs     map[string]bool
	nextID   int
}

func newFakeBrowser(t *testing.T) *fakeBrowser {
	t.Helper()
	f := &fakeBrowser{
		t: t, args: map[string][]json.RawMessage{},
		handlers: map[string]func(json.RawMessage) (any, *protocolError){},
		tree:     "- button \"Save\" [ref=e1]",
		refs:     map[string]bool{"e1": true},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Browser":              "HeadlessChrome/fake",
			"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/devtools/browser/fake",
		})
	})
	mux.HandleFunc("/devtools/browser/fake", f.serveSocket)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBrowser) serveSocket(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.ws = ws
	f.mu.Unlock()
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		f.serve(ws, msg)
	}
}

func (f *fakeBrowser) serve(ws *websocket.Conn, msg message) {
	f.mu.Lock()
	f.calls = append(f.calls, msg.Method)
	f.args[msg.Method] = append(f.args[msg.Method], msg.Params)
	handler := f.handlers[msg.Method]
	f.mu.Unlock()

	var (
		result any
		fail   *protocolError
	)
	if handler != nil {
		result, fail = handler(msg.Params)
	} else {
		result, fail = f.builtin(msg)
	}
	reply := message{ID: msg.ID, SessionID: msg.SessionID}
	if fail != nil {
		reply.Error = fail
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			f.t.Errorf("fake: encode %s result: %v", msg.Method, err)
			return
		}
		reply.Result = raw
	}
	f.write(ws, reply)
	if msg.Method == "Page.navigate" || msg.Method == "Page.reload" || msg.Method == "Page.navigateToHistoryEntry" {
		f.emit(msg.SessionID, "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "frame-1", "url": "https://example.test/next"}})
		f.emit(msg.SessionID, "Page.frameStoppedLoading", map[string]any{"frameId": "frame-1"})
	}
}

// builtin answers the commands every test needs the same way.
func (f *fakeBrowser) builtin(msg message) (any, *protocolError) {
	switch msg.Method {
	case "Target.createTarget":
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("target-%d", f.nextID)
		f.mu.Unlock()
		return map[string]any{"targetId": id}, nil
	case "Target.attachToTarget":
		var in struct {
			TargetID string `json:"targetId"`
		}
		_ = json.Unmarshal(msg.Params, &in)
		return map[string]any{"sessionId": "session-" + in.TargetID}, nil
	case "Target.createBrowserContext":
		return map[string]any{"browserContextId": "context-1"}, nil
	case "Target.getTargetInfo":
		return map[string]any{"targetInfo": map[string]any{"url": "https://example.test/", "title": "Example"}}, nil
	case "Page.getFrameTree":
		return map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "frame-1", "url": "https://example.test/"}}}, nil
	case "Page.createIsolatedWorld":
		return map[string]any{"executionContextId": 7}, nil
	case "Page.getLayoutMetrics":
		return map[string]any{
			"cssContentSize":    map[string]any{"width": 800, "height": 2400},
			"cssVisualViewport": map[string]any{"clientWidth": 800, "clientHeight": 600, "pageX": 0, "pageY": 0},
		}, nil
	case "Page.getNavigationHistory":
		return map[string]any{"currentIndex": 1, "entries": []map[string]any{{"id": 10}, {"id": 11}}}, nil
	case "Page.captureScreenshot":
		return map[string]any{"data": fakePNG(f.t)}, nil
	case "Runtime.evaluate":
		return f.evaluate(msg.Params)
	}
	return map[string]any{}, nil
}

// evaluate stands in for the isolated world: it recognises each __rx call the
// executor makes and answers with the state the test set up.
func (f *fakeBrowser) evaluate(params json.RawMessage) (any, *protocolError) {
	var in struct {
		Expression    string `json:"expression"`
		ReturnByValue bool   `json:"returnByValue"`
	}
	_ = json.Unmarshal(params, &in)
	f.mu.Lock()
	defer f.mu.Unlock()
	expr := in.Expression
	switch {
	case strings.HasPrefix(expr, "__rx.state()"):
		return value(map[string]any{"userSeq": f.userSeq, "url": "https://example.test/", "title": "Example", "ready": "complete"}), nil
	case strings.HasPrefix(expr, "__rx.snapshot("):
		return value(map[string]any{
			"url": "https://example.test/", "title": "Example", "tree": f.tree,
			"refs": len(f.refs), "userSeq": f.userSeq, "truncated": false,
		}), nil
	case strings.HasPrefix(expr, "__rx.window("):
		return value(f.userSeq), nil
	case strings.HasPrefix(expr, "__rx.rect("):
		if !f.refs[refArg(expr)] {
			return value(nil), nil
		}
		return value(map[string]any{"x": 40, "y": 60, "width": 80, "height": 20, "tag": "button"}), nil
	case strings.HasPrefix(expr, "__rx.focus("):
		return value(map[string]any{"ok": f.refs[refArg(expr)], "reason": "the element refused focus"}), nil
	case strings.HasPrefix(expr, "__rx.select("):
		return value(map[string]any{"ok": true, "selected": []string{"one"}}), nil
	case strings.HasPrefix(expr, "__rx.element("):
		if !f.refs[refArg(expr)] {
			return map[string]any{"result": map[string]any{"type": "object", "subtype": "null"}}, nil
		}
		return map[string]any{"result": map[string]any{"type": "object", "objectId": "object-1"}}, nil
	}
	return map[string]any{"result": map[string]any{"type": "undefined"}}, nil
}

func value(v any) map[string]any {
	return map[string]any{"result": map[string]any{"type": "object", "value": v}}
}

// refArg pulls the quoted ref out of an __rx call such as __rx.rect("e1").
func refArg(expr string) string {
	_, rest, ok := strings.Cut(expr, `"`)
	if !ok {
		return ""
	}
	ref, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	return ref
}

func (f *fakeBrowser) write(ws *websocket.Conn, msg message) {
	data, err := json.Marshal(msg)
	if err != nil {
		f.t.Errorf("fake: encode reply: %v", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		return
	}
}

func (f *fakeBrowser) emit(session, method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		f.t.Errorf("fake: encode %s params: %v", method, err)
		return
	}
	f.mu.Lock()
	ws := f.ws
	f.mu.Unlock()
	if ws == nil {
		return
	}
	f.write(ws, message{Method: method, Params: raw, SessionID: session})
}

// setHandler overrides one command for the rest of the test.
func (f *fakeBrowser) setHandler(method string, fn func(json.RawMessage) (any, *protocolError)) {
	f.mu.Lock()
	f.handlers[method] = fn
	f.mu.Unlock()
}

func (f *fakeBrowser) takeOver() {
	f.mu.Lock()
	f.userSeq++
	f.mu.Unlock()
}

func (f *fakeBrowser) countCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == method {
			n++
		}
	}
	return n
}

func (f *fakeBrowser) lastArgs(method string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.args[method]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

func fakePNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fake png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// newTestExecutor attaches an executor to the fake browser with artifacts in
// the test's own directory.
func newTestExecutor(t *testing.T, f *fakeBrowser) *Executor {
	t.Helper()
	return newTestExecutorWithRoots(t, f)
}

// newTestExecutorWithRoots is newTestExecutor with the directories
// browser_upload may read from.
func newTestExecutorWithRoots(t *testing.T, f *fakeBrowser, uploadRoots ...string) *Executor {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exec, err := New(ctx, Options{
		Endpoint: f.srv.URL, ArtifactDir: t.TempDir(),
		NavigateTimeout: 5 * time.Second, UploadRoots: uploadRoots,
	})
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	t.Cleanup(exec.Shutdown)
	return exec
}
