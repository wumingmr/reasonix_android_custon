package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultSpecsInvariants(t *testing.T) {
	seen := map[string]string{}
	for lang, s := range DefaultSpecs() {
		if s.Command == "" || s.LanguageID == "" || len(s.Extensions) == 0 {
			t.Errorf("lang %q: incomplete spec %+v", lang, s)
		}
		for _, ext := range s.Extensions {
			if prev, dup := seen[ext]; dup {
				t.Errorf("extension %q claimed by both %q and %q", ext, prev, lang)
			}
			seen[ext] = lang
		}
		for _, fb := range s.Fallbacks {
			if fb == "" || fb == s.Command {
				t.Errorf("lang %q: bad fallback %q", lang, fb)
			}
		}
	}
	if seen[".go"] != "go" || seen[".rs"] != "rust" || seen[".cpp"] != "cpp" || seen[".cs"] != "csharp" {
		t.Errorf("unexpected routing: %v", seen)
	}
}

func TestExtensionRouting(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{
		"elixir": {Command: "no-such-elixir-ls-xyz", LanguageID: "elixir", Extensions: []string{".ex", ".exs"}, InstallHint: "mix archive.install"},
	})
	defer m.Close()

	if _, err := m.resolve("a.ex"); !errors.As(err, new(*notInstalledError)) {
		t.Fatalf("configured-but-missing language should yield notInstalledError, got %v", err)
	}
	_, err := m.resolve("a.go")
	if err == nil || !strings.Contains(err.Error(), "no language server") {
		t.Fatalf("unconfigured extension should report no server, got %v", err)
	}
}

func TestKotlinDefaultSpec(t *testing.T) {
	spec, ok := DefaultSpecs()["kotlin"]
	if !ok {
		t.Fatal("kotlin default spec missing")
	}
	if spec.Command != "kotlin-lsp" {
		t.Errorf("kotlin Command = %q, want the official PATH name kotlin-lsp", spec.Command)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "--stdio" {
		t.Errorf("kotlin Args = %v, want [--stdio] (client speaks stdio, server defaults to socket)", spec.Args)
	}
	hasFallback := false
	for _, fb := range spec.Fallbacks {
		if fb == "intellij-server" {
			hasFallback = true
		}
	}
	if !hasFallback {
		t.Errorf("kotlin Fallbacks = %v, want intellij-server fallback for the Windows zip layout", spec.Fallbacks)
	}
	for _, want := range []string{
		"macOS",
		"brew install JetBrains/utils/kotlin-lsp",
		"Linux",
		"kotlin-lsp.sh",
		"Windows",
		"intellij-server.exe",
	} {
		if !strings.Contains(spec.InstallHint, want) {
			t.Errorf("kotlin InstallHint = %q, want platform guidance containing %q", spec.InstallHint, want)
		}
	}
}

func TestResolveCommandFallback(t *testing.T) {
	binDir := t.TempDir()
	fake := func(name string) string {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	spec := ServerSpec{Command: "kotlin-lsp", Fallbacks: []string{"intellij-server"}, InstallHint: "hint"}
	t.Setenv("PATH", binDir) // hermetic: the real PATH may already have kotlin-lsp

	// Only the fallback on PATH → it is used.
	fallback := fake("intellij-server")
	bin, err := resolveCommand(spec)
	if err != nil {
		t.Fatalf("resolveCommand: %v", err)
	}
	if bin != fallback {
		t.Errorf("resolved %q, want fallback %q", bin, fallback)
	}

	// Both on PATH → the primary command wins.
	primary := fake("kotlin-lsp")
	bin, err = resolveCommand(spec)
	if err != nil {
		t.Fatalf("resolveCommand with both: %v", err)
	}
	if bin != primary {
		t.Errorf("resolved %q, want primary %q", bin, primary)
	}

	// Neither name on PATH surfaces the primary command in the install error.
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveCommand(spec); !errors.As(err, new(*notInstalledError)) {
		t.Fatalf("expected notInstalledError, got %v", err)
	}
}

func TestConnBidirectional(t *testing.T) {
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	// Close the writers at the end so both readLoop goroutines see EOF and exit
	// (in production the subprocess pipe EOFs on kill; here nothing else closes it).
	defer caW.Close()
	defer acW.Close()

	notif := make(chan string, 4)
	var client *conn
	client = newConn(caW, acR,
		func(method string, _ json.RawMessage) { notif <- method },
		func(id int64, _ string, _ json.RawMessage) { _ = client.reply(id, map[string]any{"ok": true}) })

	var server *conn
	server = newConn(acW, caR,
		func(string, json.RawMessage) {},
		func(id int64, method string, _ json.RawMessage) { _ = server.reply(id, map[string]any{"echo": method}) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := client.call(ctx, "ping", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("client call: %v", err)
	}
	if !strings.Contains(string(res), `"echo":"ping"`) {
		t.Fatalf("unexpected response: %s", res)
	}

	if err := server.notify("textDocument/publishDiagnostics", map[string]any{}); err != nil {
		t.Fatalf("server notify: %v", err)
	}
	select {
	case m := <-notif:
		if m != "textDocument/publishDiagnostics" {
			t.Fatalf("notify method = %q", m)
		}
	case <-ctx.Done():
		t.Fatal("notification not delivered")
	}

	sres, err := server.call(ctx, "workspace/configuration", nil)
	if err != nil {
		t.Fatalf("server→client call: %v", err)
	}
	if !strings.Contains(string(sres), `"ok":true`) {
		t.Fatalf("server→client reply: %s", sres)
	}
}

func TestReadFrame(t *testing.T) {
	in := "Content-Length: 17\r\nContent-Type: x\r\n\r\n" + `{"jsonrpc":"2.0"}` + "Content-Length: 2\r\n\r\n{}"
	r := bufio.NewReader(strings.NewReader(in))
	first, err := readFrame(r)
	if err != nil || string(first) != `{"jsonrpc":"2.0"}` {
		t.Fatalf("first frame = %q, err %v", first, err)
	}
	second, err := readFrame(r)
	if err != nil || string(second) != `{}` {
		t.Fatalf("second frame = %q, err %v", second, err)
	}
	if _, err := readFrame(r); err == nil {
		t.Fatal("expected EOF on third read")
	}
}

func TestURIRoundtrip(t *testing.T) {
	paths := []string{"/home/u/a b.go", "/x/y.rs"}
	if runtime.GOOS == "windows" {
		paths = []string{`C:\Users\u\a b.go`, `D:\x\y.rs`}
	}
	for _, p := range paths {
		uri := pathToURI(p)
		if !strings.HasPrefix(uri, "file://") {
			t.Errorf("%q → %q is not a file URI", p, uri)
		}
		if got, err := uriToPath(uri); err != nil || got != p {
			t.Errorf("roundtrip %q → %q, %v", p, got, err)
		}
	}
}

func TestWindowsFileURIConversions(t *testing.T) {
	tests := []struct {
		path string
		uri  string
	}{
		{`C:\Users\Test User\中文%20.go`, `file:///C:/Users/Test%20User/%E4%B8%AD%E6%96%87%2520.go`},
		{`\\server\share\Test User\中文%20.go`, `file://server/share/Test%20User/%E4%B8%AD%E6%96%87%2520.go`},
	}
	for _, tt := range tests {
		if got := pathToURIForOS(tt.path, "windows"); got != tt.uri {
			t.Errorf("pathToURIForOS(%q) = %q, want %q", tt.path, got, tt.uri)
		}
		if got, err := uriToPathForOS(tt.uri, "windows"); err != nil || got != tt.path {
			t.Errorf("uriToPathForOS(%q) = %q, %v; want %q", tt.uri, got, err, tt.path)
		}
	}
}

func TestURIToPathAuthorityAndValidation(t *testing.T) {
	if got, err := uriToPathForOS("file://localhost/tmp/a%20b%2520.go", "linux"); err != nil || got != "/tmp/a b%20.go" {
		t.Fatalf("localhost URI = %q, %v", got, err)
	}
	for _, uri := range []string{
		"https://server/share/a.go",
		"file://server/share/a.go",
		"file://server:123/share/a.go",
		"file:///tmp/a.go?mode=ro",
		"file:///tmp/a.go#fragment",
		"file:///tmp/%00.go",
		"%",
	} {
		if _, err := uriToPathForOS(uri, "linux"); err == nil {
			t.Errorf("uriToPathForOS(%q) unexpectedly succeeded", uri)
		}
	}
}

func TestFormatLocationsKeepsInvalidURIAndSkipsSnippet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "valid.go")
	if err := os.WriteFile(path, []byte("package valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{wsRoot: root}
	remote := "https://server/share/secret.go"
	got := m.formatLocations("definition", []Location{
		{URI: pathToURI(path), Range: Range{Start: Position{Line: 0}}},
		{URI: remote, Range: Range{Start: Position{Line: 6}}},
	})
	if !strings.Contains(got, "valid.go:1  package valid") {
		t.Fatalf("valid location lost snippet:\n%s", got)
	}
	if !strings.Contains(got, remote+":7") || strings.Contains(got, remote+":7  ") {
		t.Fatalf("invalid URI was treated as a local path:\n%s", got)
	}
}

func TestLocateEncoding(t *testing.T) {
	content := "package x\nαβ foo()\n" // line 2 has two 2-byte runes then a space
	u16, err := locate(content, 2, "foo", encodingUTF16)
	if err != nil {
		t.Fatal(err)
	}
	if u16.Line != 1 || u16.Character != 3 {
		t.Errorf("utf16 pos = %+v, want line 1 char 3", u16)
	}
	u8, err := locate(content, 2, "foo", encodingUTF8)
	if err != nil {
		t.Fatal(err)
	}
	if u8.Character != 5 {
		t.Errorf("utf8 char = %d, want 5", u8.Character)
	}
	if _, err := locate(content, 2, "missing", encodingUTF16); err == nil {
		t.Error("expected not-found error")
	}
}

func TestParseLocations(t *testing.T) {
	single := `{"uri":"file:///a","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":2}}}`
	if got := parseLocations(json.RawMessage(single)); len(got) != 1 || got[0].URI != "file:///a" {
		t.Errorf("single: %+v", got)
	}
	arr := `[{"uri":"file:///a","range":{}},{"uri":"file:///b","range":{}}]`
	if got := parseLocations(json.RawMessage(arr)); len(got) != 2 {
		t.Errorf("array: %+v", got)
	}
	link := `[{"targetUri":"file:///c","targetRange":{"start":{"line":2,"character":0},"end":{"line":2,"character":1}}}]`
	got := parseLocations(json.RawMessage(link))
	if len(got) != 1 || got[0].URI != "file:///c" || got[0].Range.Start.Line != 2 {
		t.Errorf("locationlink: %+v", got)
	}
	if parseLocations(json.RawMessage("null")) != nil {
		t.Error("null should yield nil")
	}
}

func TestParseHover(t *testing.T) {
	markup := `{"contents":{"kind":"markdown","value":"func F()"}}`
	if got := parseHover(json.RawMessage(markup)); got != "func F()" {
		t.Errorf("markup hover = %q", got)
	}
	marked := `{"contents":[{"language":"go","value":"func F()"},"docs"]}`
	if got := parseHover(json.RawMessage(marked)); got != "func F()\ndocs" {
		t.Errorf("marked array hover = %q", got)
	}
	if got := parseHover(json.RawMessage(`{"contents":""}`)); got != "" {
		t.Errorf("empty hover = %q", got)
	}
}
