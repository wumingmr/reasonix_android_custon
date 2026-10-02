package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"reasonix/internal/control"
)

func newChatReferenceApp(t *testing.T, root string) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })
	tab := &WorkspaceTab{ID: "refs", WorkspaceRoot: root}
	app.tabs[tab.ID] = tab
	app.activeTabID = tab.ID
	return app
}

func writeChatReferenceFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resolveOne(t *testing.T, app *App, candidate string) ChatFileReference {
	t.Helper()
	result := app.ResolveChatFileReferencesForTab("refs", "turn-1", []ChatFileReferenceRequest{{Key: "k", Path: candidate}})
	if result.TurnKey != "turn-1" {
		t.Fatalf("turn key not echoed: %q", result.TurnKey)
	}
	if len(result.References) != 1 {
		t.Fatalf("resolve(%q) returned %d references, want 1", candidate, len(result.References))
	}
	return result.References[0]
}

func TestChatFileReferenceResolvesWorkspaceRelativeAndAbsolute(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "out/图 (1).svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)

	for _, candidate := range []string{
		"out/图 (1).svg",
		"./out/图 (1).svg",
		filepath.ToSlash(filepath.Join(root, "out/图 (1).svg")),
	} {
		got := resolveOne(t, app, candidate)
		if got.Status != "resolved" {
			t.Fatalf("resolve(%q) = %s/%s, want resolved", candidate, got.Status, got.Reason)
		}
		if got.DisplayPath != "out/图 (1).svg" {
			t.Fatalf("resolve(%q) display path = %q, want the workspace-relative spelling", candidate, got.DisplayPath)
		}
		if got.Kind != "image" {
			t.Fatalf("resolve(%q) kind = %q, want image", candidate, got.Kind)
		}
	}
}

func TestChatFileReferencePreservesRawFilenameCharacters(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "out/raw%20name.txt", "percent")
	writeChatReferenceFile(t, root, "out/raw name.txt", "space")
	if got := resolveOne(t, app, "out/raw%20name.txt"); got.Status != "resolved" || got.DisplayPath != "out/raw%20name.txt" {
		t.Fatalf("literal percent path = %+v", got)
	}
	if preview := app.ReadReferenceFileForTab("refs", "out/raw%20name.txt"); preview.Err != "" || preview.Body != "percent" {
		t.Fatalf("literal percent content = %+v", preview)
	}
	percentURL := localFileHref(filepath.Join(root, "out", "raw%20name.txt"))
	if got := resolveOne(t, app, percentURL); got.Status != "resolved" || got.DisplayPath != "out/raw%20name.txt" {
		t.Fatalf("encoded file URL = %+v", got)
	}
	if preview := app.ReadReferenceFileForTab("refs", percentURL); preview.Err != "" || preview.Body != "percent" {
		t.Fatalf("encoded file URL content = %+v", preview)
	}
	if runtime.GOOS == "windows" {
		return
	}
	writeChatReferenceFile(t, root, "out/raw?query#fragment.txt", "punctuation")
	if got := resolveOne(t, app, "out/raw?query#fragment.txt"); got.Status != "resolved" || got.DisplayPath != "out/raw?query#fragment.txt" {
		t.Fatalf("literal query/fragment path = %+v", got)
	}
}

func TestLocalPathSourceDecodesURLPathExactlyOnce(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"folder/raw%2520name.txt", filepath.FromSlash("folder/raw%20name.txt")},
		{"folder/raw%20name.txt", filepath.FromSlash("folder/raw name.txt")},
		{"folder/100%25.txt", filepath.FromSlash("folder/100%.txt")},
		{"folder/%E4%B8%AD%E6%96%87%23.txt", filepath.FromSlash("folder/中文#.txt")},
	}
	for _, tt := range tests {
		got, err := localPathSource(tt.source)
		if err != nil || got != tt.want {
			t.Fatalf("localPathSource(%q) = %q, %v; want %q", tt.source, got, err, tt.want)
		}
	}
	for _, source := range []string{"folder/%00.txt", "file:///tmp/%00.txt"} {
		if _, err := localPathSource(source); err == nil {
			t.Fatalf("localPathSource(%q) accepted NUL", source)
		}
	}
}

func TestChatFileReferenceOffersSourceForTextMedia(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "out/diagram.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	writeChatReferenceFile(t, root, "out/shot.png", "not really a png")
	writeChatReferenceFile(t, root, "out/notes.md", "# notes")

	svg := resolveOne(t, app, "out/diagram.svg")
	if !slices.Contains(svg.Actions, "source") {
		t.Fatalf("SVG is a text format and must offer the source view: %v", svg.Actions)
	}
	png := resolveOne(t, app, "out/shot.png")
	if slices.Contains(png.Actions, "source") {
		t.Fatalf("a raster image must not offer the source view: %v", png.Actions)
	}
	notes := resolveOne(t, app, "out/notes.md")
	if !slices.Contains(notes.Actions, "source") || notes.Kind != "" {
		t.Fatalf("plain text resolve = kind %q actions %v", notes.Kind, notes.Actions)
	}
	for _, action := range []string{"preview", "reveal-tree", "copy-path", "save-copy", "open-native", "reveal-native"} {
		if !slices.Contains(notes.Actions, action) {
			t.Fatalf("plain text is missing %q: %v", action, notes.Actions)
		}
	}
}

func TestChatFileReferenceRejectsEscapeAndNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, outside, "secret.txt", "secret")
	writeChatReferenceFile(t, root, "out/real.txt", "real")
	if err := os.MkdirAll(filepath.Join(root, "out", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "out", "real.txt"), filepath.Join(root, "out", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A link that leaves the workspace is refused for the escape, not for being
	// a link, so the reason stays the one the reader can act on.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "out", "escape-link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, test := range []struct {
		name      string
		candidate string
		status    string
	}{
		{"parent escape", "../secret.txt", "unavailable"},
		{"absolute escape", filepath.ToSlash(filepath.Join(outside, "secret.txt")), "unavailable"},
		{"symlink out of tree", "out/escape-link.txt", "unavailable"},
		{"directory", "out/dir", "unsupported"},
		{"missing file", "out/missing.txt", "unavailable"},
		{"empty", "   ", "unsupported"},
		{"nul byte", "out/\x00.txt", "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resolveOne(t, app, test.candidate)
			if got.Status != test.status {
				t.Fatalf("resolve(%q) = %s/%s, want %s", test.candidate, got.Status, got.Reason, test.status)
			}
			if got.DisplayPath != "" || len(got.Actions) != 0 {
				t.Fatalf("rejected candidate exposed a target: %+v", got)
			}
		})
	}
}

// A link that stays inside the workspace resolves to its target, and the
// reported path is the target: the panel must name the file it will show.
func TestChatFileReferenceFollowsInTreeSymlinkToItsTarget(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "out/real.txt", "real")
	if err := os.Symlink(filepath.Join(root, "out", "real.txt"), filepath.Join(root, "out", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := resolveOne(t, app, "out/link.txt")
	if got.Status != "resolved" || got.DisplayPath != "out/real.txt" {
		t.Fatalf("in-tree symlink resolve = %+v, want the target path", got)
	}
}

func TestChatFileReferenceRejectsSymlinkedDirectoryEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, outside, "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := resolveOne(t, app, "escape/secret.txt")
	if got.Status != "unavailable" || got.Reason != "outside-workspace" {
		t.Fatalf("a symlinked directory escaped the workspace: %+v", got)
	}
}

func TestChatFileReferenceAcceptsAuthorizedExternalFolder(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	external := t.TempDir()
	writeChatReferenceFile(t, external, "shared/dropped.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)

	ctrl := control.New(control.Options{SessionDir: t.TempDir(), SessionPath: filepath.Join(t.TempDir(), "s.jsonl"), Label: "refs", WorkspaceRoot: root})
	if _, _, err := ctrl.RegisterExternalFolderRef(external); err != nil {
		t.Fatalf("RegisterExternalFolderRef: %v", err)
	}
	app := NewApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })
	tab := &WorkspaceTab{ID: "refs", WorkspaceRoot: root, Ctrl: ctrl}
	app.tabs[tab.ID] = tab

	got := resolveOne(t, app, filepath.ToSlash(filepath.Join(external, "shared/dropped.svg")))
	if got.Status != "resolved" {
		t.Fatalf("an authorized external file was rejected: %+v", got)
	}
	realExternal, err := filepath.EvalSymlinks(external)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayPath != filepath.ToSlash(filepath.Join(realExternal, "shared/dropped.svg")) {
		t.Fatalf("external display path = %q", got.DisplayPath)
	}
	// The same file becomes unreachable once the session stops authorizing it.
	other := NewApp()
	t.Cleanup(func() { other.shutdown(context.Background()) })
	other.tabs[tab.ID] = &WorkspaceTab{ID: "refs", WorkspaceRoot: root}
	if again := resolveOne(t, other, filepath.ToSlash(filepath.Join(external, "shared/dropped.svg"))); again.Status != "unavailable" {
		t.Fatalf("resolution outlived its session authorization: %+v", again)
	}
}

func TestChatFileReferenceBatchLimitsAndShape(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "out/a.txt", "a")

	empty := app.ResolveChatFileReferencesForTab("refs", "t", nil)
	if empty.References == nil || len(empty.References) != 0 {
		t.Fatalf("empty batch must stay a non-nil empty array: %#v", empty.References)
	}

	long := strings.Repeat("a", chatFileReferenceMaxChars+1)
	got := resolveOne(t, app, long)
	if got.Status != "unsupported" || got.Reason != "too-long" {
		t.Fatalf("over-long candidate = %s/%s, want unsupported/too-long", got.Status, got.Reason)
	}

	candidates := make([]ChatFileReferenceRequest, 0, chatFileReferenceBatchLimit+10)
	for range chatFileReferenceBatchLimit + 10 {
		candidates = append(candidates, ChatFileReferenceRequest{Key: "k", Path: "out/a.txt"})
	}
	result := app.ResolveChatFileReferencesForTab("refs", "t", candidates)
	if len(result.References) != chatFileReferenceBatchLimit {
		t.Fatalf("batch of %d returned %d items, want %d", len(candidates), len(result.References), chatFileReferenceBatchLimit)
	}
}

func TestChatFileReferenceActionsRevalidateOnEveryCall(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	path := writeChatReferenceFile(t, root, "out/report.md", "body")

	if got := resolveOne(t, app, "out/report.md"); got.Status != "resolved" {
		t.Fatalf("setup: %+v", got)
	}
	if preview := app.ReadReferenceFileForTab("refs", "out/report.md"); preview.Err != "" || preview.Body != "body" {
		t.Fatalf("read = %+v", preview)
	}
	if source := app.ReadReferenceFileSourceForTab("refs", "out/report.md"); source.Err != "" || source.Body != "body" {
		t.Fatalf("source read = %+v", source)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := app.ResolveReferencePathForTab("refs", "out/report.md"); err != nil || resolved != realPath {
		t.Fatalf("resolve path = %q err %v, want %q", resolved, err, realPath)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if preview := app.ReadReferenceFileForTab("refs", "out/report.md"); preview.Err == "" {
		t.Fatal("a deleted reference still read successfully")
	}
	if _, err := app.ResolveReferencePathForTab("refs", "out/report.md"); err == nil {
		t.Fatal("a deleted reference still resolved a path")
	}
	if _, err := app.SaveReferencePathAsForTab("refs", "out/report.md"); err == nil {
		t.Fatal("a deleted reference still reached the save dialog")
	}
}

func TestChatFileReferenceHonorsCurrentReadPolicy(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "secret/token.txt", "secret")
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[sandbox]\nforbid_read = [\"secret\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolveOne(t, app, "secret/token.txt")
	if got.Status != "unavailable" || got.Reason != "blocked" {
		t.Fatalf("forbid_read did not block an answer reference: %+v", got)
	}
}

func TestChatFileReferenceRejectsUnknownSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	result := app.ResolveChatFileReferencesForTab("missing-tab", "t", []ChatFileReferenceRequest{{Key: "k", Path: "/etc/passwd"}})
	if len(result.References) != 1 || result.References[0].Status != "unavailable" || result.References[0].Reason != "unknown-session" {
		t.Fatalf("unknown session resolved a reference: %+v", result.References)
	}
}

// TestChatFileReferenceAcceptsWindowsSpellingsOnWindows keeps the drive/UNC
// matrix honest on the platform that owns those rules; every other host must
// refuse them rather than guess.
func TestChatFileReferenceAcceptsWindowsSpellingsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive and UNC paths are only meaningful on the host that owns them")
	}
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	writeChatReferenceFile(t, root, "out/drive.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	for _, candidate := range []string{
		filepath.Join(root, "out", "drive.svg"),
		strings.ToLower(filepath.Join(root, "out", "drive.svg")),
		"file:///" + strings.ReplaceAll(filepath.Join(root, "out", "drive.svg"), `\`, "/"),
	} {
		got := resolveOne(t, app, candidate)
		if got.Status != "resolved" {
			t.Fatalf("resolve(%q) = %s/%s, want resolved", candidate, got.Status, got.Reason)
		}
	}
}

func TestChatFileReferenceRejectsUnsupportedSchemes(t *testing.T) {
	root := t.TempDir()
	app := newChatReferenceApp(t, root)
	for _, candidate := range []string{
		"http://example.com/x.svg",
		"https://example.com/x.svg",
		"data:image/svg+xml;base64,PHN2Zy8+",
		"file://server/share/x.svg",
	} {
		got := resolveOne(t, app, candidate)
		if got.Status != "unsupported" && got.Status != "unavailable" {
			t.Fatalf("resolve(%q) = %s, want a refusal", candidate, got.Status)
		}
	}
}

func TestSanitizeMarkdownSVGStripsActiveContent(t *testing.T) {
	app := NewApp()
	view := app.SanitizeMarkdownSVG(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#f00"/></linearGradient></defs>
  <script>alert(1)</script>
  <rect width="10" height="10" fill="url(#g)" onload="alert(2)"/>
  <text x="1" y="1">hello</text>
  <foreignObject><body xmlns="http://www.w3.org/1999/xhtml">x</body></foreignObject>
  <image href="https://example.com/x.png"/>
</svg>`)
	if !view.OK {
		t.Fatalf("a valid SVG was refused: %+v", view)
	}
	for _, forbidden := range []string{"script", "onload", "foreignObject", "example.com", "javascript:"} {
		if strings.Contains(view.SVG, forbidden) {
			t.Fatalf("sanitized SVG kept %q: %s", forbidden, view.SVG)
		}
	}
	for _, kept := range []string{"linearGradient", "url(#g)", "<text", "hello"} {
		if !strings.Contains(view.SVG, kept) {
			t.Fatalf("sanitized SVG dropped %q: %s", kept, view.SVG)
		}
	}
}

func TestSanitizeMarkdownSVGEnforcesPreviewLimits(t *testing.T) {
	app := NewApp()
	// A model often omits xmlns; that stays valid. A different root or two
	// roots must not preview as one image.
	if view := app.SanitizeMarkdownSVG("<svg><rect/></svg>"); !view.OK {
		t.Fatalf("an SVG without a namespace was refused: %+v", view)
	}
	for _, body := range []string{
		`<html><body>x</body></html>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><g></svg>`,
		`not markup at all`,
	} {
		if view := app.SanitizeMarkdownSVG(body); view.OK {
			t.Fatalf("accepted invalid input %q: %+v", body, view)
		}
	}

	huge := `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat("<rect/>", markdownSVGPreviewMaxElements+1) + `</svg>`
	if view := app.SanitizeMarkdownSVG(huge); view.OK || view.Reason != "invalid" {
		t.Fatalf("an over-complex SVG was accepted: %+v", view)
	}

	deep := `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat("<g>", markdownSVGPreviewMaxDepth+1) + strings.Repeat("</g>", markdownSVGPreviewMaxDepth+1) + `</svg>`
	if view := app.SanitizeMarkdownSVG(deep); view.OK {
		t.Fatal("an over-nested SVG was accepted")
	}

	oversized := `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat(" ", markdownSVGPreviewMaxBytes) + `</svg>`
	if view := app.SanitizeMarkdownSVG(oversized); view.OK || view.Reason != "too-large" {
		t.Fatalf("an oversized SVG was accepted: %+v", view)
	}
}

// A sanitizer that ran slowly would stall the transcript; this only guards the
// pathological shape the element ceiling exists for.
func TestSanitizeMarkdownSVGStaysBounded(t *testing.T) {
	app := NewApp()
	body := `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat(`<rect width="1" height="1"/>`, 5000) + `</svg>`
	start := time.Now()
	if view := app.SanitizeMarkdownSVG(body); !view.OK {
		t.Fatalf("a 5000-element SVG was refused: %+v", view)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("sanitizing 5000 elements took %s", elapsed)
	}
}
